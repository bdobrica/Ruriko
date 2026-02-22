// Package webhook implements the Ruriko-side webhook reverse proxy (R13.1).
//
// Inbound webhook deliveries arrive at Ruriko's HTTP server:
//
//	POST /webhooks/{agent}/{source}
//
// Ruriko validates the request, authenticates the caller (bearer token or
// HMAC-SHA256), rate-limits per agent, then forwards the raw body to the
// target agent's ACP event endpoint:
//
//	POST {agent.control_url}/events/{source}
//
// The forwarded request carries the agent's own ACP bearer token so the Gitai
// server can authenticate it. The agent's response status is propagated back
// to the webhook sender.
//
// Authentication modes (configured per gateway in Gosuto):
//   - "bearer" (default): Authorization: Bearer header must match the agent's ACP token.
//   - "hmac-sha256": X-Hub-Signature-256 header is validated against the
//     request body using the key stored at config["hmacSecretRef"] in the
//     Ruriko secret store.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// DefaultRateLimit is the default maximum number of inbound webhook deliveries
// per agent per minute when no explicit limit is configured.
const DefaultRateLimit = 60

// maxBodyBytes caps inbound webhook request bodies to prevent memory
// exhaustion from oversized payloads.
const maxBodyBytes = 1 * 1024 * 1024 // 1 MiB

// agentStore is the minimal interface the Proxy needs from the Store.
type agentStore interface {
	GetAgent(ctx context.Context, id string) (*store.Agent, error)
	GetLatestGosutoVersion(ctx context.Context, agentID string) (*store.GosutoVersion, error)
}

// secretsGetter is the minimal interface the Proxy needs from the secrets store.
type secretsGetter interface {
	Get(ctx context.Context, name string) ([]byte, error)
}

// Proxy handles POST /webhooks/{agent}/{source} by authenticating,
// rate-limiting, and forwarding inbound webhook deliveries to the
// corresponding Gitai agent's ACP /events/{source} endpoint.
type Proxy struct {
	store      agentStore
	secrets    secretsGetter
	limiter    *rateLimiter
	httpClient *http.Client
}

// Config holds options for creating a Proxy.
type Config struct {
	// RateLimit is the maximum number of webhook deliveries allowed per agent
	// per minute. Defaults to DefaultRateLimit (60) when zero or negative.
	RateLimit int
}

// New creates a new Proxy.
func New(st agentStore, sec secretsGetter, cfg Config) *Proxy {
	limit := cfg.RateLimit
	if limit <= 0 {
		limit = DefaultRateLimit
	}
	return &Proxy{
		store:      st,
		secrets:    sec,
		limiter:    newRateLimiter(limit, time.Minute),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// RouteRegistrar is satisfied by *http.ServeMux and by app.HealthServer's
// Handle method, allowing the Proxy to register its routes without importing
// the app package directly.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
}

// RegisterRoutes mounts the webhook proxy handler on the given registrar.
// It registers the prefix /webhooks/ so all requests below that path are
// handled by this proxy.
func (p *Proxy) RegisterRoutes(r RouteRegistrar) {
	r.Handle("/webhooks/", http.HandlerFunc(p.handleWebhook))
}

// handleWebhook is the HTTP handler for POST /webhooks/{agent}/{source}.
func (p *Proxy) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse /webhooks/{agent}/{source}
	path := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "invalid path: expected /webhooks/{agent}/{source}", http.StatusNotFound)
		return
	}
	agentID, source := parts[0], parts[1]

	ctx := r.Context()

	// Validate the agent exists, is enabled, and has an ACP control URL.
	agent, err := p.store.GetAgent(ctx, agentID)
	if err != nil {
		slog.Info("webhook: agent not found", "agent", agentID, "source", source)
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if !agent.Enabled {
		slog.Info("webhook: agent is disabled", "agent", agentID, "source", source)
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if !agent.ControlURL.Valid || agent.ControlURL.String == "" {
		slog.Warn("webhook: agent has no control URL", "agent", agentID, "source", source)
		http.Error(w, "agent not available", http.StatusServiceUnavailable)
		return
	}

	// Per-agent rate limiting.
	if !p.limiter.Allow(agentID) {
		slog.Info("webhook: rate limit exceeded", "agent", agentID, "source", source)
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Validate that the agent's active Gosuto config contains the named
	// gateway with type: webhook.
	gw, err := p.findWebhookGateway(ctx, agentID, source)
	if err != nil {
		slog.Info("webhook: gateway lookup failed",
			"agent", agentID, "source", source, "err", err)
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}

	// Read the request body before auth so HMAC can validate it.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		slog.Warn("webhook: failed to read request body", "agent", agentID, "source", source, "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Authenticate the inbound request per the gateway's authType.
	authType := gw.Config["authType"]
	if authType == "" {
		authType = "bearer"
	}
	switch authType {
	case "bearer":
		if err := p.validateBearer(r, agent); err != nil {
			slog.Info("webhook: bearer auth failed",
				"agent", agentID, "source", source, "err", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	case "hmac-sha256":
		if err := p.validateHMAC(ctx, r, body, gw); err != nil {
			slog.Info("webhook: HMAC auth failed",
				"agent", agentID, "source", source, "err", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	default:
		slog.Error("webhook: unsupported authType in gateway config",
			"agent", agentID, "source", source, "authType", authType)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Forward the body to the agent's ACP /events/{source} endpoint.
	acpURL := strings.TrimRight(agent.ControlURL.String, "/") + "/events/" + source
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	acpToken := ""
	if agent.ACPToken.Valid {
		acpToken = agent.ACPToken.String
	}

	status, err := p.forward(ctx, acpURL, acpToken, body, contentType)
	if err != nil {
		slog.Error("webhook: forward to agent failed",
			"agent", agentID, "source", source, "acp_url", acpURL, "err", err)
		http.Error(w, "failed to forward to agent", http.StatusBadGateway)
		return
	}

	slog.Info("webhook: forwarded",
		"agent", agentID, "source", source, "acp_url", acpURL, "acp_status", status)

	// Propagate the agent's response status to the webhook sender.
	w.WriteHeader(status)
}

// findWebhookGateway loads the agent's latest Gosuto version, parses it, and
// returns the gateway named source if it has type "webhook". Returns an error
// if the agent has no Gosuto config, the source is absent, or it is not a
// webhook gateway.
func (p *Proxy) findWebhookGateway(ctx context.Context, agentID, source string) (*gosutospec.Gateway, error) {
	gv, err := p.store.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("get gosuto version: %w", err)
	}

	cfg, err := gosutospec.Parse([]byte(gv.YAMLBlob))
	if err != nil {
		return nil, fmt.Errorf("parse gosuto: %w", err)
	}

	for i := range cfg.Gateways {
		gw := &cfg.Gateways[i]
		if gw.Name == source {
			if gw.Type != "webhook" {
				return nil, fmt.Errorf("gateway %q has type %q, not %q", source, gw.Type, "webhook")
			}
			return gw, nil
		}
	}
	return nil, fmt.Errorf("gateway %q not configured", source)
}

// validateBearer checks that the Authorization: Bearer header on r matches
// the agent's stored ACP token. When the agent has no token (dev mode) the
// validation always passes.
func (p *Proxy) validateBearer(r *http.Request, agent *store.Agent) error {
	if !agent.ACPToken.Valid || agent.ACPToken.String == "" {
		// Dev/test mode: authentication is disabled for this agent.
		return nil
	}

	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return fmt.Errorf("missing or malformed Authorization header")
	}
	token := strings.TrimPrefix(auth, prefix)
	if token != agent.ACPToken.String {
		return fmt.Errorf("invalid bearer token")
	}
	return nil
}

// validateHMAC checks that the X-Hub-Signature-256 header on r is a valid
// HMAC-SHA256 of body, using the key stored at gw.Config["hmacSecretRef"].
func (p *Proxy) validateHMAC(ctx context.Context, r *http.Request, body []byte, gw *gosutospec.Gateway) error {
	secretRef, ok := gw.Config["hmacSecretRef"]
	if !ok || secretRef == "" {
		return fmt.Errorf("gateway %q is missing hmacSecretRef in config", gw.Name)
	}

	secretVal, err := p.secrets.Get(ctx, secretRef)
	if err != nil {
		return fmt.Errorf("fetch hmac secret %q: %w", secretRef, err)
	}

	sigHdr := r.Header.Get("X-Hub-Signature-256")
	if sigHdr == "" {
		return fmt.Errorf("missing X-Hub-Signature-256 header")
	}
	const prefix = "sha256="
	if !strings.HasPrefix(sigHdr, prefix) {
		return fmt.Errorf("X-Hub-Signature-256 must start with %q", prefix)
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(sigHdr, prefix))
	if err != nil {
		return fmt.Errorf("invalid hex in X-Hub-Signature-256: %w", err)
	}

	mac := hmac.New(sha256.New, secretVal)
	mac.Write(body)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, provided) {
		return fmt.Errorf("HMAC signature mismatch")
	}
	return nil
}

// forward sends body to acpURL as a POST request carrying the agent's bearer
// token, and returns the HTTP response status code. The response body is
// drained and discarded.
func (p *Proxy) forward(ctx context.Context, acpURL, token string, body []byte, contentType string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, acpURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build forward request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("forward http: %w", err)
	}
	defer resp.Body.Close()
	// Drain the response body so the underlying TCP connection can be reused.
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes)) //nolint:errcheck

	return resp.StatusCode, nil
}
