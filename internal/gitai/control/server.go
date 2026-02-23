// Package control implements the Agent Control Protocol (ACP) HTTP server.
//
// Ruriko communicates with each Gitai agent over this interface to push
// configuration and secrets, check health, and request graceful restarts.
//
// Security hardening (Phase R2):
//   - Bearer-token authentication: set Handlers.Token to require
//     "Authorization: Bearer <token>" on every request.  When Token is empty
//     authentication is disabled (dev/test mode).
//   - Idempotency cache: mutating endpoints (/config/apply, /secrets/apply,
//     /process/restart, /tasks/cancel) record the X-Idempotency-Key header and
//     return the cached 200 response on replay within the TTL window.
//
// Endpoints:
//
//	GET  /health              → HealthResponse
//	GET  /status              → StatusResponse
//	POST /config/apply        → ConfigApplyRequest → 200 OK
//	POST /secrets/apply       → SecretsApplyRequest → 200 OK  [disabled by default, see R4.4]
//	POST /secrets/token       → SecretsTokenRequest → 200 OK (redeems via Kuze)
//	POST /process/restart     → 202 Accepted (triggers shutdown via restartFn)
//	POST /tasks/cancel        → 202 Accepted (cancels current in-flight task)
//	POST /events/{source}     → Event envelope → 202 Accepted (R12.1)
//
// Security hardening (Phase R4.4):
//   - POST /secrets/apply is disabled by default (Handlers.DirectSecretPushEnabled=false).
//     In production, secrets must flow via POST /secrets/token + Kuze redemption so that
//     plaintext values never traverse the ACP payload.  Set DirectSecretPushEnabled=true
//     only in dev or during migration.  A disabled endpoint returns 410 Gone.
//
// Event ingress (Phase R12.1 + R12.4):
//   - POST /events/{source} accepts normalised Event envelopes from gateway processes
//     (cron, external binaries) AND raw webhook deliveries from type:webhook gateways.
//   - Built-in gateways (cron) run on localhost and bypass bearer-token auth.
//   - External gateways must supply the ACP bearer token in Authorization: Bearer <token>.
//   - Webhook gateways (type:webhook) support either bearer or hmac-sha256 auth.
//     HMAC-SHA256 validates X-Hub-Signature-256 over the raw request body against the
//     secret named by config["hmacSecretRef"]; raw body is then wrapped into an Event.
//   - A fixed-window rate limiter (per-source + global) enforces MaxEventsPerMinute
//     from the active Gosuto Limits, returning 429 when exceeded.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/gateway"
)

// idempotencyTTL is how long the server caches responses by idempotency key.
const idempotencyTTL = 60 * time.Second

// maxEventBodyBytes caps the inbound event request body to prevent memory
// exhaustion from a misbehaving gateway process.
const maxEventBodyBytes = 1 * 1024 * 1024 // 1 MiB

// idempotencyEntry is a cached response for a single idempotency key.
type idempotencyEntry struct {
	status    int
	body      []byte
	expiresAt time.Time
}

// idempotencyCache is a simple in-memory store keyed by X-Idempotency-Key.
type idempotencyCache struct {
	mu      sync.Mutex
	entries map[string]idempotencyEntry
}

func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{entries: make(map[string]idempotencyEntry)}
}

// --- event rate limiter ---

// fixedWindow is a single fixed-window event counter.
type fixedWindow struct {
	count       int
	windowStart time.Time
}

// eventRateLimiter enforces per-source and global event ingress rate limits
// using a fixed 1-minute window. When maxPerMinute is 0 all events are allowed.
type eventRateLimiter struct {
	mu      sync.Mutex
	sources map[string]*fixedWindow
	global  fixedWindow
}

func newEventRateLimiter() *eventRateLimiter {
	return &eventRateLimiter{
		sources: make(map[string]*fixedWindow),
	}
}

// allow returns true when the event may proceed. It checks both the per-source
// window and the global window; both must have remaining capacity.
func (l *eventRateLimiter) allow(source string, maxPerMinute int) bool {
	if maxPerMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	// Refresh global window.
	if now.Sub(l.global.windowStart) >= time.Minute {
		l.global.count = 0
		l.global.windowStart = now
	}
	if l.global.count >= maxPerMinute {
		return false
	}

	// Refresh per-source window.
	src, ok := l.sources[source]
	if !ok {
		src = &fixedWindow{}
		l.sources[source] = src
	}
	if now.Sub(src.windowStart) >= time.Minute {
		src.count = 0
		src.windowStart = now
	}
	if src.count >= maxPerMinute {
		return false
	}

	// Both windows have capacity — consume one token from each.
	l.global.count++
	src.count++
	return true
}

// get returns the cached entry (ok=true) if the key exists and has not expired.
func (c *idempotencyCache) get(key string) (idempotencyEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return idempotencyEntry{}, false
	}
	return e, true
}

// set stores a response for the given key with the configured TTL.
func (c *idempotencyCache) set(key string, status int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = idempotencyEntry{
		status:    status,
		body:      body,
		expiresAt: time.Now().Add(idempotencyTTL),
	}
}

// ConfigApplyRequest mirrors the Ruriko ACP client's request body.
type ConfigApplyRequest struct {
	YAML string `json:"yaml"`
	Hash string `json:"hash"`
}

// SecretsApplyRequest mirrors the Ruriko ACP client's request body.
type SecretsApplyRequest struct {
	Secrets map[string]string `json:"secrets"`
}

// SecretLease is a single token-based secret lease delivered by Ruriko.
// The agent redeems RedemptionToken from KuzeURL to obtain the plaintext
// secret value; the raw secret never travels in the ACP payload.
type SecretLease struct {
	SecretRef       string `json:"secret_ref"`
	RedemptionToken string `json:"redemption_token"`
	KuzeURL         string `json:"kuze_url"`
}

// SecretsTokenRequest is the body for POST /secrets/token.
// The agent redeems each lease from Kuze rather than receiving plaintext.
type SecretsTokenRequest struct {
	Leases []SecretLease `json:"leases"`
}

// kuzeRedeemResponse mirrors the JSON returned by GET /kuze/redeem/<token>.
type kuzeRedeemResponse struct {
	SecretRef  string `json:"secret_ref"`
	SecretType string `json:"secret_type"`
	// Value is the base64-encoded plaintext secret value.
	Value string `json:"value"`
}

// maxRedeemResponseBytes caps the Kuze redemption response body to prevent
// memory exhaustion from a misbehaving (or compromised) Kuze endpoint.
const maxRedeemResponseBytes = 64 * 1024 // 64 KiB

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
	// MessagesOutbound is the total number of successful matrix.send_message
	// calls made by this agent since startup (R15.5 audit/observability).
	MessagesOutbound int64 `json:"messages_outbound,omitempty"`
}

// Handlers bundles the callbacks the server delegates to.
type Handlers struct {
	// AgentID is the agent's stable identifier.
	AgentID string
	// Version is the runtime version string.
	Version string
	// StartedAt is the time the binary started.
	StartedAt time.Time

	// Token, when non-empty, is the expected bearer token for all requests.
	// When empty, authentication is disabled (useful in local dev/test).
	Token string

	// DirectSecretPushEnabled controls whether the legacy POST /secrets/apply
	// endpoint (which carries raw secret values in the ACP request body) is
	// active.  Production deployments MUST leave this false (the default) so
	// that secrets only flow via Kuze token redemption (POST /secrets/token).
	// Setting this to true re-enables the old path for dev/migration use only.
	//
	// Feature flag: FEATURE_DIRECT_SECRET_PUSH (default: false / OFF)
	DirectSecretPushEnabled bool

	// GosutoHash returns the hash of the currently applied Gosuto config.
	GosutoHash func() string
	// MCPNames returns the names of running MCP servers.
	MCPNames func() []string
	// ApplyConfig validates and applies a new Gosuto YAML.
	ApplyConfig func(yaml, hash string) error
	// ApplySecrets updates the in-memory secret store.
	ApplySecrets func(secrets map[string]string) error
	// RequestRestart signals the application to perform a graceful restart.
	RequestRestart func()
	// RequestCancel signals the application to cancel the current in-flight task.
	// When nil the /tasks/cancel endpoint returns 503 Service Unavailable.
	RequestCancel func()

	// ActiveConfig returns the currently applied Gosuto config, or nil when no
	// config has been loaded. Used by POST /events/{source} to validate gateway
	// names and read MaxEventsPerMinute rate-limit settings.
	// When nil the event ingress endpoint accepts any source name (dev mode).
	ActiveConfig func() *gosutospec.Config

	// GetSecret looks up a secret value by its ref name from the agent's
	// in-memory secret store.  Used by the built-in webhook gateway to fetch
	// the HMAC secret for X-Hub-Signature-256 validation.
	// When nil and an hmac-sha256 webhook gateway receives a request, the
	// endpoint returns 503 Service Unavailable.
	GetSecret func(ref string) ([]byte, error)

	// HandleEvent is invoked with a fully validated inbound event envelope.
	// Implementations must be non-blocking (e.g. a channel send or goroutine
	// launch) so the HTTP response is returned promptly.
	// When nil, POST /events/{source} returns 503 Service Unavailable.
	HandleEvent func(ctx context.Context, evt *envelope.Event)

	// MessagesOutbound returns the total number of successful
	// matrix.send_message calls since agent startup (R15.5).
	// When nil, the field is omitted from the status response.
	MessagesOutbound func() int64
}

// Server is the ACP HTTP server.
type Server struct {
	addr         string
	handlers     Handlers
	server       *http.Server
	idemCache    *idempotencyCache
	httpClient   *http.Client // used by handleSecretsToken to call Kuze
	eventLimiter *eventRateLimiter
}

// New creates a new ACP Server listening on addr.
func New(addr string, h Handlers) *Server {
	s := &Server{
		addr:         addr,
		handlers:     h,
		idemCache:    newIdempotencyCache(),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		eventLimiter: newEventRateLimiter(),
	}

	// innerMux: ACP management endpoints — all protected by auth middleware.
	innerMux := http.NewServeMux()
	innerMux.HandleFunc("/health", s.handleHealth)
	innerMux.HandleFunc("/status", s.handleStatus)
	innerMux.HandleFunc("/config/apply", s.handleConfigApply)
	innerMux.HandleFunc("/secrets/apply", s.handleSecretsApply)
	innerMux.HandleFunc("/secrets/token", s.handleSecretsToken)
	innerMux.HandleFunc("/process/restart", s.handleRestart)
	innerMux.HandleFunc("/tasks/cancel", s.handleCancel)

	// outerMux: event ingress lives here with its own per-handler auth
	// (built-in gateways on localhost bypass bearer-token auth; external
	// gateways must present the ACP bearer token). Everything else falls
	// through to the auth-protected innerMux.
	//
	// Note: /events/{source} is registered without a method prefix so that
	// wrong-method requests reach the handler and receive a proper 405 rather
	// than falling through to the catch-all and getting a 404.
	outerMux := http.NewServeMux()
	outerMux.HandleFunc("/events/{source}", s.handleEventIngress)
	outerMux.Handle("/", s.authMiddleware(innerMux))

	s.server = &http.Server{
		Addr:         addr,
		Handler:      outerMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
}

// authMiddleware rejects requests that do not carry the correct bearer token.
// When Handlers.Token is empty, all requests are allowed (dev/test mode).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.handlers.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if auth[len("Bearer "):] != s.handlers.Token {
			writeError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start begins listening. It returns once the listener is bound so callers
// can immediately start sending requests.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("acp listen %s: %w", s.addr, err)
	}
	slog.Info("ACP server listening", "addr", ln.Addr().String())
	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("ACP server error", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		s.server.Shutdown(context.Background())
	}()
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.server.Shutdown(ctx)
}

// --- handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:  "ok",
		AgentID: s.handlers.AgentID,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uptime := time.Since(s.handlers.StartedAt).Seconds()
	hash := ""
	if s.handlers.GosutoHash != nil {
		hash = s.handlers.GosutoHash()
	}
	var mcps []string
	if s.handlers.MCPNames != nil {
		mcps = s.handlers.MCPNames()
	}
	var msgsOut int64
	if s.handlers.MessagesOutbound != nil {
		msgsOut = s.handlers.MessagesOutbound()
	}
	writeJSON(w, http.StatusOK, StatusResponse{
		AgentID:          s.handlers.AgentID,
		Version:          s.handlers.Version,
		GosutoHash:       hash,
		Uptime:           uptime,
		StartedAt:        s.handlers.StartedAt,
		MCPs:             mcps,
		MessagesOutbound: msgsOut,
	})
}

func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		if cached, ok := s.idemCache.get(key); ok {
			slog.Debug("ACP: idempotent replay", "path", "/config/apply", "key", key)
			w.WriteHeader(cached.status)
			w.Write(cached.body)
			return
		}
	}

	var req ConfigApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if s.handlers.ApplyConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "config apply not available")
		return
	}
	if err := s.handlers.ApplyConfig(req.YAML, req.Hash); err != nil {
		slog.Error("ACP: config apply failed", "err", err)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	slog.Info("ACP: config applied", "hash", req.Hash[:min(12, len(req.Hash))])

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		s.idemCache.set(key, http.StatusOK, nil)
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSecretsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// R4.4: Direct secret push is disabled by default (production mode).
	// Secrets must be distributed via Kuze token redemption (POST /secrets/token).
	// Enable FEATURE_DIRECT_SECRET_PUSH only for dev or migration scenarios.
	if !s.handlers.DirectSecretPushEnabled {
		writeError(w, http.StatusGone,
			"direct secret push is disabled; use POST /secrets/token with Kuze token redemption")
		return
	}

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		if cached, ok := s.idemCache.get(key); ok {
			slog.Debug("ACP: idempotent replay", "path", "/secrets/apply", "key", key)
			w.WriteHeader(cached.status)
			w.Write(cached.body)
			return
		}
	}

	var req SecretsApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if s.handlers.ApplySecrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets apply not available")
		return
	}
	if err := s.handlers.ApplySecrets(req.Secrets); err != nil {
		slog.Error("ACP: secrets apply failed", "err", err)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	slog.Info("ACP: secrets applied", "count", len(req.Secrets))

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		s.idemCache.set(key, http.StatusOK, nil)
	}
	w.WriteHeader(http.StatusOK)
}

// handleSecretsToken handles POST /secrets/token.
//
// Ruriko sends a list of {secret_ref, redemption_token, kuze_url} leases.
// For each lease the agent queries the Kuze redemption URL, presenting its
// identity via X-Agent-ID. The decoded values are passed to ApplySecrets so
// the rest of the runtime sees no change; only the delivery path differs.
//
// Security properties:
//   - Raw secret values never appear in the ACP request body.
//   - Each token is single-use and short-lived (AgentTTL ≈ 60 s).
//   - The agent identity is verified by Kuze on each redemption.
func (s *Server) handleSecretsToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		if cached, ok := s.idemCache.get(key); ok {
			slog.Debug("ACP: idempotent replay", "path", "/secrets/token", "key", key)
			w.WriteHeader(cached.status)
			w.Write(cached.body)
			return
		}
	}

	var req SecretsTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	if len(req.Leases) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	if s.handlers.ApplySecrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets apply not available")
		return
	}

	// Redeem each Kuze token to fetch the plaintext secret value.
	redeemed := make(map[string]string, len(req.Leases))
	var failedRefs []string

	for _, lease := range req.Leases {
		val, err := s.redeemLease(r.Context(), lease)
		if err != nil {
			slog.Warn("ACP: failed to redeem secret lease",
				"ref", lease.SecretRef, "err", err)
			failedRefs = append(failedRefs, lease.SecretRef)
			continue
		}
		redeemed[lease.SecretRef] = val
	}

	if len(redeemed) == 0 {
		slog.Error("ACP: all secret token redemptions failed", "refs", failedRefs)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("all %d secret redemption(s) failed", len(req.Leases)))
		return
	}

	if err := s.handlers.ApplySecrets(redeemed); err != nil {
		slog.Error("ACP: secrets token apply failed", "err", err)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	slog.Info("ACP: secrets applied via Kuze token redemption",
		"applied", len(redeemed), "failed", len(failedRefs))

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		s.idemCache.set(key, http.StatusOK, nil)
	}
	w.WriteHeader(http.StatusOK)
}

// redeemLease calls the Kuze redemption URL for a single lease, presenting
// the agent's identity via X-Agent-ID. Returns the base64-encoded secret
// value on success (ready to pass directly into ApplySecrets).
func (s *Server) redeemLease(ctx context.Context, lease SecretLease) (string, error) {
	if lease.KuzeURL == "" {
		return "", fmt.Errorf("empty kuze_url for secret %q", lease.SecretRef)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lease.KuzeURL, nil)
	if err != nil {
		return "", fmt.Errorf("build redeem request: %w", err)
	}
	req.Header.Set("X-Agent-ID", s.handlers.AgentID)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("kuze GET %q: %w", lease.KuzeURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRedeemResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read kuze response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return "", fmt.Errorf("kuze returned %d for %q: %s", resp.StatusCode, lease.SecretRef, snippet)
	}

	var kr kuzeRedeemResponse
	if err := json.Unmarshal(body, &kr); err != nil {
		return "", fmt.Errorf("decode kuze response for %q: %w", lease.SecretRef, err)
	}
	if kr.Value == "" {
		return "", fmt.Errorf("kuze returned empty value for %q", lease.SecretRef)
	}

	return kr.Value, nil
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		if cached, ok := s.idemCache.get(key); ok {
			slog.Debug("ACP: idempotent replay", "path", "/process/restart", "key", key)
			w.WriteHeader(cached.status)
			w.Write(cached.body)
			return
		}
	}

	slog.Info("ACP: restart requested")
	if s.handlers.RequestRestart != nil {
		go s.handlers.RequestRestart()
	}

	body := []byte(`{"status":"restarting"}`)
	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		s.idemCache.set(key, http.StatusAccepted, body)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		if cached, ok := s.idemCache.get(key); ok {
			slog.Debug("ACP: idempotent replay", "path", "/tasks/cancel", "key", key)
			w.WriteHeader(cached.status)
			w.Write(cached.body)
			return
		}
	}

	if s.handlers.RequestCancel == nil {
		writeError(w, http.StatusServiceUnavailable, "task cancel not available")
		return
	}
	slog.Info("ACP: task cancel requested")
	go s.handlers.RequestCancel()

	body := []byte(`{"status":"cancelling"}`)
	if key := r.Header.Get("X-Idempotency-Key"); key != "" {
		s.idemCache.set(key, http.StatusAccepted, body)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// handleEventIngress handles POST /events/{source} (R12.1 + R12.4).
//
// It dispatches to one of two sub-handlers based on the gateway type
// registered in the active Gosuto config:
//
//   - Non-webhook gateways (cron, external processes): the request body must
//     be a pre-formed Event envelope (JSON).  Auth is the standard ACP bearer
//     token with a localhost bypass for in-process gateways.
//
//   - Webhook gateways (type: webhook): the request body is a raw HTTP
//     webhook POST payload.  Auth is either bearer token (default) or
//     HMAC-SHA256 (X-Hub-Signature-256 header).  The raw body is wrapped in
//     an Event envelope by handleWebhookEvent.
//
// Auth rules apply before body parsing:
//   - Built-in gateways (cron etc.) run within the same host and connect from
//     127.0.0.1 / ::1. Localhost connections bypass bearer-token auth.
//   - External gateway processes must supply the ACP bearer token.
//   - Webhook HMAC auth bypasses the bearer check and validates the signature
//     over the raw body instead.
//   - When Handlers.Token is empty (dev/test) all connections are accepted.
func (s *Server) handleEventIngress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	source := r.PathValue("source")
	if source == "" {
		writeError(w, http.StatusBadRequest, "missing event source in path")
		return
	}

	// Validate source against the active Gosuto gateway list and read rate
	// limit from config. When ActiveConfig is nil (dev mode) we skip name
	// validation and apply no rate limit. Capture the gateway config so that
	// we can detect webhook-type gateways and read their auth settings.
	var maxEventsPerMinute int
	var foundGW *gosutospec.Gateway
	if s.handlers.ActiveConfig != nil {
		cfg := s.handlers.ActiveConfig()
		if cfg != nil {
			for i := range cfg.Gateways {
				if cfg.Gateways[i].Name == source {
					gw := cfg.Gateways[i] // copy to avoid loop-var alias
					foundGW = &gw
					break
				}
			}
			if foundGW == nil {
				slog.Warn("event dropped", "source", source, "reason", "unknown_source")
				writeError(w, http.StatusNotFound,
					fmt.Sprintf("unknown gateway source %q", source))
				return
			}
			maxEventsPerMinute = cfg.Limits.MaxEventsPerMinute
		}
	}

	// Route webhook gateways to the dedicated sub-handler which performs
	// raw-body auth and wraps the payload in an Event envelope.
	if foundGW != nil && foundGW.Type == "webhook" {
		s.handleWebhookEvent(w, r, source, foundGW, maxEventsPerMinute)
		return
	}

	// ── Non-webhook path (cron, external gateway processes) ──────────────────

	// Auth: localhost (built-in gateways) bypasses bearer-token check.
	if s.handlers.Token != "" && !isLocalhost(r) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if auth[len("Bearer "):] != s.handlers.Token {
			writeError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
	}

	// Decode and validate the event envelope (body capped at 1 MiB).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body: "+err.Error())
		return
	}
	var evt envelope.Event
	if err := json.Unmarshal(body, &evt); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := evt.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event envelope: "+err.Error())
		return
	}
	// Ensure the envelope's declared source matches the URL path parameter so
	// a gateway cannot impersonate a different source.
	if evt.Source != source {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("envelope source %q does not match URL source %q", evt.Source, source))
		return
	}

	// Rate limiting: token-bucket per source + global (maxEventsPerMinute).
	if !s.eventLimiter.allow(source, maxEventsPerMinute) {
		slog.Warn("event dropped", "source", source, "reason", "rate_limit", "limit", maxEventsPerMinute)
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("rate limit exceeded for gateway %q (%d events/min)", source, maxEventsPerMinute))
		return
	}

	// Dispatch to app handler.
	if s.handlers.HandleEvent == nil {
		writeError(w, http.StatusServiceUnavailable, "event handling not available")
		return
	}
	s.handlers.HandleEvent(r.Context(), &evt)
	// "event received" — source, type, timestamp (payload content never logged at INFO).
	slog.Info("event received", "source", source, "type", evt.Type, "ts", evt.TS)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// handleWebhookEvent handles inbound webhook deliveries for gateways with
// type: webhook (R12.4).
//
// Unlike the standard event ingress which expects a pre-formed Event envelope,
// this handler accepts a raw HTTP POST body from an external webhook sender
// (GitHub, Stripe, custom services, etc.) and wraps it in an Event envelope
// using gateway.WrapRawWebhookBody.
//
// Auth:
//   - authType "bearer" (default): ACP bearer token, localhost-bypass applies.
//   - authType "hmac-sha256": validates X-Hub-Signature-256 over the raw body
//     against the secret named by config["hmacSecretRef"] in the agent's
//     secret store.  Bearer auth is deliberately skipped so caller does not
//     need the ACP token — only the HMAC shared secret.
func (s *Server) handleWebhookEvent(
	w http.ResponseWriter,
	r *http.Request,
	source string,
	gwCfg *gosutospec.Gateway,
	maxEventsPerMinute int,
) {
	// Read the raw body first — HMAC validation must be computed over the
	// exact bytes received (including whitespace, key ordering, etc.).
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxEventBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read webhook body: "+err.Error())
		return
	}

	// Determine auth type from gateway config. Default is "bearer".
	authType := gwCfg.Config["authType"]
	if authType == "" {
		authType = "bearer"
	}

	switch authType {
	case "bearer":
		// Same localhost-bypass bearer check as non-webhook gateways.
		if s.handlers.Token != "" && !isLocalhost(r) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			if auth[len("Bearer "):] != s.handlers.Token {
				writeError(w, http.StatusUnauthorized, "invalid bearer token")
				return
			}
		}

	case "hmac-sha256":
		// Validate the X-Hub-Signature-256 header against the raw body.
		// Bearer token is NOT required for HMAC-authenticated webhooks.
		sigHeader := r.Header.Get("X-Hub-Signature-256")
		if sigHeader == "" {
			writeError(w, http.StatusUnauthorized, "missing X-Hub-Signature-256 header")
			return
		}
		hmacRef := gwCfg.Config["hmacSecretRef"]
		if hmacRef == "" {
			// validateGateway should have caught this, but be defensive.
			writeError(w, http.StatusInternalServerError,
				"webhook gateway misconfigured: hmacSecretRef is empty")
			return
		}
		if s.handlers.GetSecret == nil {
			writeError(w, http.StatusServiceUnavailable,
				"secret lookup not available; cannot validate HMAC signature")
			return
		}
		hmacSecret, err := s.handlers.GetSecret(hmacRef)
		if err != nil {
			slog.Error("webhook: HMAC secret not found",
				"source", source, "ref", hmacRef, "err", err)
			// Do not leak whether the secret is absent or wrong — both look
			// like an auth failure to the external caller.
			writeError(w, http.StatusUnauthorized, "HMAC secret not available")
			return
		}
		if !gateway.ValidateHMACSHA256(hmacSecret, rawBody, sigHeader) {
			slog.Warn("webhook: invalid HMAC signature", "source", source)
			writeError(w, http.StatusUnauthorized, "invalid HMAC signature")
			return
		}

	default:
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("unsupported webhook authType %q", authType))
		return
	}

	// Rate limiting.
	if !s.eventLimiter.allow(source, maxEventsPerMinute) {
		slog.Warn("event dropped", "source", source, "reason", "rate_limit", "limit", maxEventsPerMinute)
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("rate limit exceeded for gateway %q (%d events/min)", source, maxEventsPerMinute))
		return
	}

	// Wrap the raw body into a normalised Event envelope.
	evt := gateway.WrapRawWebhookBody(source, rawBody)

	// Dispatch to app handler.
	if s.handlers.HandleEvent == nil {
		writeError(w, http.StatusServiceUnavailable, "event handling not available")
		return
	}
	s.handlers.HandleEvent(r.Context(), evt)
	// "event received" — source, type, timestamp (payload content never logged at INFO).
	slog.Info("event received", "source", source, "type", evt.Type, "ts", evt.TS)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// isLocalhost reports whether the request originates from the loopback
// interface (127.0.0.1 or ::1). Used to allow built-in gateway processes
// (which run in-process and connect from localhost) to bypass bearer-token
// authentication on the event ingress endpoint.
func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// TestHandler exposes the server's HTTP handler for use in httptest.NewServer.
// This is only intended for tests.
func (s *Server) TestHandler() http.Handler {
	return s.server.Handler
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
