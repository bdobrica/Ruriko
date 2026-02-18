package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Client communicates with a single MCP server process over stdin/stdout using
// JSON-RPC 2.0 (newline-delimited).
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	nextID atomic.Int64

	pending map[int64]chan *Response
	pendMu  sync.Mutex
}

// NewClient starts the MCP process described by command+args+env and performs
// the initial MCP handshake. The Client is ready to call ListTools / CallTool
// once New returns without error.
func NewClient(ctx context.Context, name, command string, args []string, env []string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start mcp process: %w", err)
	}

	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int64]chan *Response),
	}

	// Start reader goroutine.
	go c.readLoop(stdout)

	// MCP handshake: initialize
	var initResult InitializeResult
	if err := c.call(ctx, "initialize", InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    ClientCaps{},
		ClientInfo:      ClientInfo{Name: "gitai", Version: "1"},
	}, &initResult); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}

	// Send initialized notification (no response expected).
	notif, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	c.mu.Lock()
	fmt.Fprintf(c.stdin, "%s\n", notif)
	c.mu.Unlock()

	slog.Info("mcp server ready",
		"name", name,
		"server", initResult.ServerInfo.Name,
		"version", initResult.ServerInfo.Version,
	)
	return c, nil
}

// ListTools returns the list of tools exposed by this MCP server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result ListToolsResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// CallTool invokes a named tool with the given arguments.
func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*CallToolResult, error) {
	var result CallToolResult
	if err := c.call(ctx, "tools/call", CallToolParams{Name: toolName, Arguments: args}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Close shuts down the MCP process.
func (c *Client) Close() error {
	c.stdin.Close()
	return c.cmd.Wait()
}

// --- internal ---

func (c *Client) call(ctx context.Context, method string, params, result interface{}) error {
	id := c.nextID.Add(1)
	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan *Response, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	c.mu.Lock()
	_, err = fmt.Fprintf(c.stdin, "%s\n", data)
	c.mu.Unlock()
	if err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return fmt.Errorf("write request: %w", err)
	}

	select {
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil {
			return nil
		}
		// Re-marshal the result interface{} to get the typed value.
		b, err := json.Marshal(resp.Result)
		if err != nil {
			return fmt.Errorf("re-marshal result: %w", err)
		}
		return json.Unmarshal(b, result)
	}
}

func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB per line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			slog.Warn("mcp: failed to parse response", "name", c.name, "err", err)
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendMu.Unlock()
		if ok {
			ch <- &resp
		}
	}
	// Drain pending requests on EOF.
	c.pendMu.Lock()
	for id, ch := range c.pending {
		ch <- &Response{ID: id, Error: &ResponseError{Code: -32000, Message: "MCP process closed"}}
	}
	c.pending = make(map[int64]chan *Response)
	c.pendMu.Unlock()
}
