// Package acp provides an HTTP client for the Agent Control Protocol.
//
// Each Gitai agent exposes a small HTTP server on its control port.
// Ruriko uses this client to push config, push secrets, and check health.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
)

const defaultTimeout = 10 * time.Second

// Client is an ACP HTTP client for a single agent control endpoint.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new ACP client targeting the given base URL (e.g. "http://10.0.0.5:8765").
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status  string `json:"status"`
	AgentID string `json:"agent_id"`
}

// StatusResponse is returned by GET /status.
type StatusResponse struct {
	AgentID    string    `json:"agent_id"`
	Version    string    `json:"version"`
	GosutoHash string    `json:"gosuto_hash"`
	Uptime     float64   `json:"uptime_seconds"`
	StartedAt  time.Time `json:"started_at"`
	MCPs       []string  `json:"mcps"`
}

// ConfigApplyRequest is the body for POST /config/apply.
type ConfigApplyRequest struct {
	// YAML is the raw Gosuto configuration YAML to apply.
	YAML string `json:"yaml"`
	// Hash is the sha256 of the YAML content for verification.
	Hash string `json:"hash"`
}

// SecretsApplyRequest is the body for POST /secrets/apply.
type SecretsApplyRequest struct {
	// Secrets is a map of secret name → base64-encoded value.
	Secrets map[string]string `json:"secrets"`
}

// ErrorResponse is returned by the ACP server on errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Health calls GET /health and returns the response.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.get(ctx, "/health", &resp); err != nil {
		return nil, fmt.Errorf("health check: %w", err)
	}
	return &resp, nil
}

// Status calls GET /status and returns runtime information from the agent.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.get(ctx, "/status", &resp); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return &resp, nil
}

// ApplyConfig pushes a new Gosuto configuration to the agent.
func (c *Client) ApplyConfig(ctx context.Context, req ConfigApplyRequest) error {
	return c.post(ctx, "/config/apply", req, nil)
}

// ApplySecrets pushes a secrets bundle to the agent.
func (c *Client) ApplySecrets(ctx context.Context, req SecretsApplyRequest) error {
	return c.post(ctx, "/secrets/apply", req, nil)
}

// Restart requests the agent to gracefully restart its process.
func (c *Client) Restart(ctx context.Context) error {
	return c.post(ctx, "/process/restart", nil, nil)
}

// --- internal helpers ---

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	setTraceHeader(req, ctx)
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setTraceHeader(req, ctx)
	return c.do(req, out)
}

// setTraceHeader injects the trace ID from ctx into the X-Trace-ID request header.
func setTraceHeader(req *http.Request, ctx context.Context) {
	if traceID := trace.FromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}
}

func (c *Client) do(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if jsonErr := json.Unmarshal(bodyBytes, &errResp); jsonErr == nil && errResp.Error != "" {
			return fmt.Errorf("ACP %s %s → %d: %s", req.Method, req.URL.Path, resp.StatusCode, errResp.Error)
		}
		return fmt.Errorf("ACP %s %s → %d", req.Method, req.URL.Path, resp.StatusCode)
	}

	if out != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, out); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}
