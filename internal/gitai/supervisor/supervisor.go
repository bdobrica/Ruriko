// Package supervisor manages the lifecycle of MCP server sub-processes.
// It starts each server described in the active Gosuto config, restarts them
// on unexpected exit when auto_restart is set, and provides a lookup from
// server name to a live mcp.Client.
package supervisor

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/mcp"
)

const restartDelay = 5 * time.Second

// Supervisor manages a set of MCP server processes.
type Supervisor struct {
	mu        sync.RWMutex
	clients   map[string]*mcp.Client
	specs     []gosutospec.MCPServer
	secretEnv map[string]string // env vars injected into all MCP processes
	ctx       context.Context
	cancel    context.CancelFunc
}

// New creates a Supervisor with no servers running yet.
func New() *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		clients:   make(map[string]*mcp.Client),
		secretEnv: make(map[string]string),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// ApplySecrets updates the environment injected into newly-started MCP processes.
// Existing processes are NOT restarted automatically — call Reload to pick up changes.
func (s *Supervisor) ApplySecrets(env map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretEnv = env
}

// Reconcile ensures that exactly the servers in specs are running.
// Servers no longer in the new spec are stopped; new ones are started.
func (s *Supervisor) Reconcile(specs []gosutospec.MCPServer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Index new specs by name.
	wanted := make(map[string]gosutospec.MCPServer, len(specs))
	for _, sp := range specs {
		wanted[sp.Name] = sp
	}

	// Stop servers not in new spec.
	for name, client := range s.clients {
		if _, ok := wanted[name]; !ok {
			slog.Info("supervisor: stopping mcp server", "name", name)
			client.Close()
			delete(s.clients, name)
		}
	}

	// Start new or changed servers.
	for name, sp := range wanted {
		if _, running := s.clients[name]; !running {
			s.startLocked(sp)
		}
	}
	s.specs = specs
}

// Get returns the live mcp.Client for the named server, or nil.
func (s *Supervisor) Get(name string) *mcp.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[name]
}

// Names returns all currently running MCP server names.
func (s *Supervisor) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.clients))
	for k := range s.clients {
		out = append(out, k)
	}
	return out
}

// Stop shuts down all managed MCP processes.
func (s *Supervisor) Stop() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, c := range s.clients {
		slog.Info("supervisor: stopping mcp server", "name", name)
		c.Close()
	}
	s.clients = make(map[string]*mcp.Client)
}

// startLocked starts a single MCP server and, if auto_restart is enabled,
// watches for unexpected exit and restarts it. Must be called with s.mu held.
func (s *Supervisor) startLocked(sp gosutospec.MCPServer) {
	env := s.buildEnv(sp)
	client, err := mcp.NewClient(s.ctx, sp.Name, sp.Command, sp.Args, env)
	if err != nil {
		slog.Error("supervisor: failed to start mcp server", "name", sp.Name, "err", err)
		if sp.AutoRestart {
			go s.watchAndRestart(sp)
		}
		return
	}
	s.clients[sp.Name] = client
	if sp.AutoRestart {
		go s.watchAndRestart(sp)
	}
}

// watchAndRestart waits for a process to exit, then restarts it after restartDelay.
func (s *Supervisor) watchAndRestart(sp gosutospec.MCPServer) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(restartDelay):
		}

		s.mu.RLock()
		_, still := s.clients[sp.Name]
		s.mu.RUnlock()
		if still {
			// Process is still alive from the supervisor's perspective; nothing to do.
			continue
		}

		slog.Info("supervisor: restarting mcp server", "name", sp.Name)
		env := s.buildEnvLocked(sp)
		client, err := mcp.NewClient(s.ctx, sp.Name, sp.Command, sp.Args, env)
		if err != nil {
			slog.Error("supervisor: restart failed", "name", sp.Name, "err", err)
			continue
		}
		s.mu.Lock()
		s.clients[sp.Name] = client
		s.mu.Unlock()
	}
}

// buildEnv merges the system environment, static MCP spec env, and injected secrets.
func (s *Supervisor) buildEnv(sp gosutospec.MCPServer) []string {
	s.mu.RLock()
	secretEnv := s.secretEnv
	s.mu.RUnlock()
	return buildEnv(sp, secretEnv)
}

func (s *Supervisor) buildEnvLocked(sp gosutospec.MCPServer) []string {
	return buildEnv(sp, s.secretEnv)
}

func buildEnv(sp gosutospec.MCPServer, secretEnv map[string]string) []string {
	base := os.Environ()
	extra := make([]string, 0, len(sp.Env)+len(secretEnv))
	for k, v := range secretEnv {
		extra = append(extra, k+"="+v)
	}
	for k, v := range sp.Env {
		extra = append(extra, k+"="+v)
	}
	return append(base, extra...)
}
