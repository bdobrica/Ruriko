// Package control implements the Agent Control Protocol (ACP) HTTP server.
//
// Ruriko communicates with each Gitai agent over this interface to push
// configuration and secrets, check health, and request graceful restarts.
//
// Endpoints:
//
//	GET  /health         → HealthResponse
//	GET  /status         → StatusResponse
//	POST /config/apply   → ConfigApplyRequest → 200 OK
//	POST /secrets/apply  → SecretsApplyRequest → 200 OK
//	POST /process/restart → 202 Accepted (triggers shutdown via restartFn)
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

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
}

// Server is the ACP HTTP server.
type Server struct {
	addr     string
	handlers Handlers
	server   *http.Server
}

// New creates a new ACP Server listening on addr.
func New(addr string, h Handlers) *Server {
	s := &Server{addr: addr, handlers: h}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/config/apply", s.handleConfigApply)
	mux.HandleFunc("/secrets/apply", s.handleSecretsApply)
	mux.HandleFunc("/process/restart", s.handleRestart)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s
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
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSecretsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
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
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slog.Info("ACP: restart requested")
	if s.handlers.RequestRestart != nil {
		go s.handlers.RequestRestart()
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
