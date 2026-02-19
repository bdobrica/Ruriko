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
//	GET  /health          → HealthResponse
//	GET  /status          → StatusResponse
//	POST /config/apply    → ConfigApplyRequest → 200 OK
//	POST /secrets/apply   → SecretsApplyRequest → 200 OK
//	POST /process/restart → 202 Accepted (triggers shutdown via restartFn)
//	POST /tasks/cancel    → 202 Accepted (cancels current in-flight task)
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// idempotencyTTL is how long the server caches responses by idempotency key.
const idempotencyTTL = 60 * time.Second

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
}

// Server is the ACP HTTP server.
type Server struct {
	addr      string
	handlers  Handlers
	server    *http.Server
	idemCache *idempotencyCache
}

// New creates a new ACP Server listening on addr.
func New(addr string, h Handlers) *Server {
	s := &Server{
		addr:      addr,
		handlers:  h,
		idemCache: newIdempotencyCache(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/config/apply", s.handleConfigApply)
	mux.HandleFunc("/secrets/apply", s.handleSecretsApply)
	mux.HandleFunc("/process/restart", s.handleRestart)
	mux.HandleFunc("/tasks/cancel", s.handleCancel)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.authMiddleware(mux),
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
	writeJSON(w, http.StatusOK, StatusResponse{
		AgentID:    s.handlers.AgentID,
		Version:    s.handlers.Version,
		GosutoHash: hash,
		Uptime:     uptime,
		StartedAt:  s.handlers.StartedAt,
		MCPs:       mcps,
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
