// Package acp provides an HTTP client for the Agent Control Protocol.
//
// Each Gitai agent exposes a small HTTP server on its control port.
// Ruriko uses this client to push config, push secrets, and check health.
//
// Security hardening (Phase R2):
//   - Bearer-token authentication: set Options.Token to send
//     "Authorization: Bearer <token>" on every request.
//   - Per-operation timeouts via context.WithTimeout (no shared client timeout).
//   - X-Request-ID on every request; X-Idempotency-Key on mutating operations.
//   - Response bodies are capped at 1 MiB to prevent memory exhaustion.
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

// Per-operation timeout constants (R2.3).
const (
	timeoutHealth  = 2 * time.Second
	timeoutStatus  = 3 * time.Second
	timeoutMutate  = 30 * time.Second // ApplyConfig, Restart, Cancel
	timeoutSecrets = 15 * time.Second // ApplySecrets
)

// maxResponseBytes caps the amount of body data read from ACP responses
// to prevent memory exhaustion from misbehaving agents (R2.4).
const maxResponseBytes = 1 << 20 // 1 MiB

// Options configures an ACP client.
type Options struct {
	// Token, when non-empty, is sent as a Bearer token in the Authorization
	// header on every request.  When empty the header is omitted (dev/test).
	Token string
}

// Client is an ACP HTTP client for a single agent control endpoint.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a new ACP client targeting the given base URL
// (e.g. "http://10.0.0.5:8765").  Zero or one Options value may be supplied.
func New(baseURL string, opts ...Options) *Client {
	var token string
	if len(opts) > 0 {
		token = opts[0].Token
	}
	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{}, // no global timeout — per-op contexts are used
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

// SecretLease is a single token-based secret lease issued to the agent.
// The agent redeems RedemptionToken from KuzeURL to obtain the plaintext value.
// Secrets never travel in this payload; only the token reference does.
type SecretLease struct {
	// SecretRef is the name of the secret this lease is for.
	SecretRef string `json:"secret_ref"`
	// RedemptionToken is the one-time Kuze token the agent presents on redemption.
	RedemptionToken string `json:"redemption_token"`
	// KuzeURL is the fully-qualified URL the agent calls to redeem the token.
	// Example: "https://ruriko.example.com/kuze/redeem/<token>"
	KuzeURL string `json:"kuze_url"`
}

// SecretsTokenRequest is the body for POST /secrets/token.
// It replaces SecretsApplyRequest in the token-based distribution model:
// secrets are pulled by the agent from Kuze rather than pushed as plaintext.
type SecretsTokenRequest struct {
	// Leases is the list of token leases; one entry per bound secret.
	Leases []SecretLease `json:"leases"`
}

// ErrorResponse is returned by the ACP server on errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Health calls GET /health and returns the response.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutHealth)
	defer cancel()
	var resp HealthResponse
	if err := c.get(ctx, "/health", &resp); err != nil {
		return nil, fmt.Errorf("health check: %w", err)
	}
	return &resp, nil
}

// Status calls GET /status and returns runtime information from the agent.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutStatus)
	defer cancel()
	var resp StatusResponse
	if err := c.get(ctx, "/status", &resp); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return &resp, nil
}

// ApplyConfig pushes a new Gosuto configuration to the agent.
func (c *Client) ApplyConfig(ctx context.Context, req ConfigApplyRequest) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutMutate)
	defer cancel()
	return c.post(ctx, "/config/apply", req, nil, true)
}

// ApplySecrets pushes a secrets bundle to the agent.
func (c *Client) ApplySecrets(ctx context.Context, req SecretsApplyRequest) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutSecrets)
	defer cancel()
	return c.post(ctx, "/secrets/apply", req, nil, true)
}

// ApplySecretsToken sends a token-based secret distribution request to the agent.
// The agent redeems each lease from Kuze to obtain the plaintext value; secrets
// never travel in the ACP payload.
func (c *Client) ApplySecretsToken(ctx context.Context, req SecretsTokenRequest) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutSecrets)
	defer cancel()
	return c.post(ctx, "/secrets/token", req, nil, true)
}

// Restart requests the agent to gracefully restart its process.
func (c *Client) Restart(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutMutate)
	defer cancel()
	return c.post(ctx, "/process/restart", nil, nil, true)
}

// Cancel requests the agent to cancel its current in-flight task.
func (c *Client) Cancel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutMutate)
	defer cancel()
	return c.post(ctx, "/tasks/cancel", nil, nil, true)
}

// --- internal helpers ---

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.setCommonHeaders(req, false)
	return c.do(req, out)
}

// post sends a POST request.  idempotent=true adds an X-Idempotency-Key header
// so the server can safely deduplicate retried calls within its TTL window.
func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}, idempotent bool) error {
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
	c.setCommonHeaders(req, idempotent)
	return c.do(req, out)
}

// setCommonHeaders attaches headers present on every outgoing ACP request:
//   - X-Trace-ID  (propagated from ctx when available)
//   - X-Request-ID (unique per call; aids server-side log correlation)
//   - X-Idempotency-Key (on mutating ops; equals X-Request-ID)
//   - Authorization: Bearer <token> (when a token is configured)
func (c *Client) setCommonHeaders(req *http.Request, addIdempotencyKey bool) {
	// Trace propagation.
	if traceID := trace.FromContext(req.Context()); traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}

	// Unique per-call request ID for server-side log correlation.
	reqID := trace.GenerateID()
	req.Header.Set("X-Request-ID", reqID)

	// Idempotency key for mutating operations (R2.2).
	if addIdempotencyKey {
		req.Header.Set("X-Idempotency-Key", reqID)
	}

	// Bearer token authentication (R2.1).
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) do(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	// Cap body reads to prevent memory exhaustion (R2.4).
	limited := io.LimitReader(resp.Body, maxResponseBytes)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if jsonErr := json.Unmarshal(bodyBytes, &errResp); jsonErr == nil && errResp.Error != "" {
			return fmt.Errorf("ACP %s %s → %d %s: %s",
				req.Method, req.URL.Path, resp.StatusCode, resp.Status, errResp.Error)
		}
		// Fallback: include a snippet of the raw body for diagnostics.
		snippet := string(bodyBytes)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet != "" {
			return fmt.Errorf("ACP %s %s → %d %s: %s",
				req.Method, req.URL.Path, resp.StatusCode, resp.Status, snippet)
		}
		return fmt.Errorf("ACP %s %s → %d %s",
			req.Method, req.URL.Path, resp.StatusCode, resp.Status)
	}

	if out != nil && len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, out); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}
