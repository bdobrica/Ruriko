// Package mcp provides types for the Model Context Protocol (MCP) JSON-RPC 2.0
// transport, and a client that communicates with a running MCP server process
// over stdin/stdout.
package mcp

// --- JSON-RPC 2.0 wire types ---

// Request is an outbound JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// Response is an inbound JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Result  interface{}    `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
}

// ResponseError is the error field in a JSON-RPC 2.0 response.
type ResponseError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *ResponseError) Error() string { return e.Message }

// --- MCP method types ---

// InitializeParams is sent by the client as the first call.
type InitializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    ClientCaps `json:"capabilities"`
	ClientInfo      ClientInfo `json:"clientInfo"`
}

// ClientCaps describes client-side MCP capabilities.
type ClientCaps struct{}

// ClientInfo describes the connecting client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server's response to initialize.
type InitializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      ServerInfo `json:"serverInfo"`
	Capabilities    ServerCaps `json:"capabilities"`
}

// ServerInfo holds the MCP server's name and version.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCaps describes server-side MCP capabilities.
type ServerCaps struct {
	Tools *struct{} `json:"tools,omitempty"`
}

// ListToolsResult is returned by tools/list.
type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

// Tool describes a single callable MCP tool.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"inputSchema,omitempty"`
}

// CallToolParams is sent to invoke a tool.
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// CallToolResult holds the tool's output.
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ContentItem is a single piece of content returned by a tool.
type ContentItem struct {
	Type string `json:"type"` // "text", "image", etc.
	Text string `json:"text,omitempty"`
	Data string `json:"data,omitempty"` // base64 for images
	MIME string `json:"mimeType,omitempty"`
}
