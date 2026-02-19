package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/bdobrica/Ruriko/common/version"
)

// HealthServer exposes /health, /status, and any additionally registered
// HTTP endpoints (e.g. Kuze routes).
// It is optional; Ruriko runs without it when HTTPAddr is empty.
type HealthServer struct {
	addr      string
	store     statusProvider
	startedAt time.Time
	server    *http.Server
	mux       *http.ServeMux
}

// statusProvider is the minimal interface the health server needs from Store.
type statusProvider interface {
	AgentCount(ctx context.Context) (int, error)
}

// healthResponse is returned by GET /health.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// statusResponse is returned by GET /status.
type statusResponse struct {
	Status     string    `json:"status"`
	Version    string    `json:"version"`
	Commit     string    `json:"commit"`
	BuildTime  string    `json:"build_time"`
	StartedAt  time.Time `json:"started_at"`
	UptimeSecs float64   `json:"uptime_seconds"`
	AgentCount int       `json:"agent_count"`
}

// NewHealthServer creates and configures the HTTP server (does not start it).
func NewHealthServer(addr string, sp statusProvider) *HealthServer {
	mux := http.NewServeMux()
	hs := &HealthServer{
		addr:      addr,
		store:     sp,
		startedAt: time.Now(),
		mux:       mux,
	}
	mux.HandleFunc("/health", hs.handleHealth)
	mux.HandleFunc("/status", hs.handleStatus)
	return hs
}

// ServeHTTP implements http.Handler so the server can be tested without a
// live network listener (e.g. with httptest.NewRecorder).
func (h *HealthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// Handle registers a handler for the given URL pattern, delegating to the
// underlying ServeMux.  Call this before Start to add extra routes (e.g.
// Kuze endpoints).
func (h *HealthServer) Handle(pattern string, handler http.Handler) {
	h.mux.Handle(pattern, handler)
}

// Start begins listening in the background. Blocks until the listener is
// established so the caller knows the port is open before returning.
func (h *HealthServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return fmt.Errorf("health server: listen %s: %w", h.addr, err)
	}

	h.server = &http.Server{
		Handler:      h,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("health server listening", "addr", ln.Addr().String())
		if err := h.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("health server stopped", "err", err)
		}
	}()

	// Shutdown when ctx is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("health server shutdown error", "err", err)
		}
	}()

	return nil
}

// Stop shuts down the HTTP server.
func (h *HealthServer) Stop() {
	if h.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.server.Shutdown(ctx); err != nil {
		slog.Warn("health server shutdown error", "err", err)
	}
}

// handleHealth responds with a simple ok JSON payload.
func (h *HealthServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:  "ok",
		Version: version.Version,
		Commit:  version.GitCommit,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStatus responds with runtime statistics.
func (h *HealthServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	agentCount := 0
	if h.store != nil {
		if n, err := h.store.AgentCount(r.Context()); err == nil {
			agentCount = n
		}
	}

	uptime := time.Since(h.startedAt).Seconds()
	resp := statusResponse{
		Status:     "ok",
		Version:    version.Version,
		Commit:     version.GitCommit,
		BuildTime:  version.BuildTime,
		StartedAt:  h.startedAt,
		UptimeSecs: uptime,
		AgentCount: agentCount,
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJSON serialises v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("health: failed to encode JSON response", "err", err)
	}
}
