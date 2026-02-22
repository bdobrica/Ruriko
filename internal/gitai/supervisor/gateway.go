// gateway.go — external gateway subprocess lifecycle management.
//
// Built-in gateways (type: cron, type: webhook) are handled entirely within the
// Gitai process by gateway.Manager and control.Server respectively.  External
// gateways — those whose Gosuto spec includes a Command field — are separate
// binaries that run as supervised child processes, similar to MCP servers.
//
// Each external gateway process receives a standard set of injected environment
// variables so it knows where to deliver events:
//
//	GATEWAY_TARGET_URL   — http://127.0.0.1:{port}/events/{name}
//	                       The ACP endpoint the binary must POST event envelopes to.
//	GATEWAY_<KEY>        — one var per gateway config entry (key uppercased).
//
// The supervisor mirrors the Supervisor pattern: Reconcile() brings running
// processes in line with the active Gosuto spec, and Stop() tears everything
// down cleanly.  If autoRestart is true in the Gateway spec, the process is
// automatically restarted after a brief delay when it exits unexpectedly.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

const gracefulShutdownTimeout = 5 * time.Second

// ExternalGatewaySupervisor manages the lifecycle of external gateway processes.
// Only Gateway entries with a non-empty Command field are handled here; built-in
// gateway types ("cron", "webhook") are managed elsewhere and are silently ignored
// by Reconcile.
type ExternalGatewaySupervisor struct {
	mu           sync.RWMutex
	processes    map[string]*externalGatewayProcess
	specs        []gosutospec.Gateway
	secretEnv    map[string]string
	acpURL       string        // e.g. "http://127.0.0.1:8765"
	restartDelay time.Duration // how long to wait before restarting a crashed process
	ctx          context.Context
	cancel       context.CancelFunc
}

// externalGatewayProcess represents a single running external gateway.
type externalGatewayProcess struct {
	name   string
	spec   gosutospec.Gateway
	cancel context.CancelFunc // cancels this process's lifecycle context
	done   chan struct{}      // closed when the process goroutine exits
}

// NewExternalGatewaySupervisor creates an ExternalGatewaySupervisor whose
// managed processes will POST events to acpURL (e.g. "http://127.0.0.1:8765").
func NewExternalGatewaySupervisor(acpURL string) *ExternalGatewaySupervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &ExternalGatewaySupervisor{
		processes:    make(map[string]*externalGatewayProcess),
		secretEnv:    make(map[string]string),
		acpURL:       acpURL,
		restartDelay: restartDelay, // reuse the 5 s constant from supervisor.go
		ctx:          ctx,
		cancel:       cancel,
	}
}

// withRestartDelay sets the delay used between process restart attempts.
// Intended for tests that need fast restarts; not part of the public API.
func (s *ExternalGatewaySupervisor) withRestartDelay(d time.Duration) *ExternalGatewaySupervisor {
	s.restartDelay = d
	return s
}

// ApplySecrets updates the environment variables injected into newly-started
// gateway processes.  Existing processes are NOT restarted — call Reconcile
// after ApplySecrets if you want the running processes to pick up the changes.
func (s *ExternalGatewaySupervisor) ApplySecrets(env map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secretEnv = env
}

// Reconcile ensures exactly the external gateways described in gateways are
// running.  Built-in gateway types (type: cron, type: webhook) are silently
// skipped — they are managed by gateway.Manager and control.Server.
//
// Processes no longer in the spec, or whose config has changed, are stopped
// before the new version is started.
func (s *ExternalGatewaySupervisor) Reconcile(gateways []gosutospec.Gateway) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Index the wanted external gateways (those that specify a binary Command).
	wanted := make(map[string]gosutospec.Gateway, len(gateways))
	for _, gw := range gateways {
		if gw.Command != "" {
			wanted[gw.Name] = gw
		}
	}

	// Stop processes that are no longer wanted or whose config has changed.
	for name, proc := range s.processes {
		newSpec, ok := wanted[name]
		if !ok || externalGatewayChanged(proc.spec, newSpec) {
			slog.Info("supervisor/gateway: stopping external gateway", "name", name)
			proc.cancel()
			// Wait for the goroutine to confirm it has exited before mutating
			// the map.  The mutex is held here; the goroutine itself must never
			// attempt to acquire it (it doesn't).
			<-proc.done
			delete(s.processes, name)
		}
	}

	// Start gateways that are wanted but not currently running.
	for name, gw := range wanted {
		if _, running := s.processes[name]; !running {
			s.startLocked(gw)
		}
	}
	s.specs = gateways
}

// Stop cancels all external gateway processes and waits for them to exit.
func (s *ExternalGatewaySupervisor) Stop() {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, proc := range s.processes {
		slog.Info("supervisor/gateway: stopping external gateway on shutdown", "name", name)
		proc.cancel()
		<-proc.done
	}
	s.processes = make(map[string]*externalGatewayProcess)
}

// startLocked starts a single external gateway process goroutine.
// The caller must hold s.mu.
func (s *ExternalGatewaySupervisor) startLocked(gw gosutospec.Gateway) {
	procCtx, procCancel := context.WithCancel(s.ctx)
	proc := &externalGatewayProcess{
		name:   gw.Name,
		spec:   gw,
		cancel: procCancel,
		done:   make(chan struct{}),
	}
	s.processes[gw.Name] = proc
	slog.Info("supervisor/gateway: starting external gateway",
		"name", gw.Name, "command", gw.Command)
	go s.runGateway(procCtx, proc)
}

// runGateway is the process lifecycle loop for a single external gateway.
// It starts the binary, waits for it to exit, and — if autoRestart is true —
// restarts it after restartDelay.  It terminates when its context is cancelled.
func (s *ExternalGatewaySupervisor) runGateway(ctx context.Context, proc *externalGatewayProcess) {
	defer close(proc.done)

	for {
		env := s.buildEnv(proc.spec)
		cmd := exec.Command(proc.spec.Command, proc.spec.Args...)
		cmd.Env = env
		// Route gateway stdout/stderr to the agent's own stderr so that
		// structured log collectors pick up gateway output alongside agent logs.
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			slog.Error("supervisor/gateway: failed to start external gateway",
				"name", proc.name, "command", proc.spec.Command, "err", err)
			if !proc.spec.AutoRestart {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.restartDelay):
				continue
			}
		}

		slog.Debug("supervisor/gateway: external gateway process started",
			"name", proc.name, "pid", cmd.Process.Pid)

		// Wait for the process to exit in a separate goroutine so we can also
		// select on context cancellation (for clean shutdown).
		exitCh := make(chan error, 1)
		go func() { exitCh <- cmd.Wait() }()

		select {
		case <-ctx.Done():
			// Context cancelled — shut down the gateway process gracefully.
			if cmd.Process != nil {
				slog.Debug("supervisor/gateway: sending SIGTERM to external gateway",
					"name", proc.name, "pid", cmd.Process.Pid)
				_ = cmd.Process.Signal(syscall.SIGTERM)
				// Give the process a window to exit cleanly, then force-kill.
				select {
				case <-exitCh:
					// Exited cleanly.
				case <-time.After(gracefulShutdownTimeout):
					slog.Warn("supervisor/gateway: graceful shutdown timed out; sending SIGKILL",
						"name", proc.name, "pid", cmd.Process.Pid)
					_ = cmd.Process.Kill()
					<-exitCh
				}
			}
			slog.Info("supervisor/gateway: external gateway stopped",
				"name", proc.name)
			return

		case err := <-exitCh:
			if err != nil {
				slog.Warn("supervisor/gateway: external gateway exited unexpectedly",
					"name", proc.name, "err", err)
			} else {
				slog.Info("supervisor/gateway: external gateway exited cleanly",
					"name", proc.name)
			}
			if !proc.spec.AutoRestart {
				return
			}
			slog.Info("supervisor/gateway: restarting external gateway",
				"name", proc.name)
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.restartDelay):
				continue
			}
		}
	}
}

// buildEnv constructs the environment slice for an external gateway process by
// merging (in increasing priority order):
//
//  1. os.Environ()        — inherited process environment
//  2. secretEnv           — Ruriko-managed secrets (same as MCP processes)
//  3. spec.Env            — static env vars from the Gosuto spec
//  4. GATEWAY_TARGET_URL  — ACP /events/{name} endpoint for this gateway
//  5. GATEWAY_<KEY>       — one var per config entry (key uppercased)
func (s *ExternalGatewaySupervisor) buildEnv(gw gosutospec.Gateway) []string {
	s.mu.RLock()
	secretEnv := s.secretEnv
	s.mu.RUnlock()
	return buildGatewayEnv(gw, s.acpURL, secretEnv)
}

// buildGatewayEnv is the pure-function core of buildEnv, separated for
// testability without needing a fully-wired supervisor instance.
func buildGatewayEnv(gw gosutospec.Gateway, acpURL string, secretEnv map[string]string) []string {
	base := os.Environ()
	extra := make([]string, 0, len(secretEnv)+len(gw.Env)+len(gw.Config)+1)

	for k, v := range secretEnv {
		extra = append(extra, k+"="+v)
	}
	for k, v := range gw.Env {
		extra = append(extra, k+"="+v)
	}
	// Inject the ACP event ingress URL so the gateway knows where to POST events.
	extra = append(extra, fmt.Sprintf("GATEWAY_TARGET_URL=%s/events/%s", acpURL, gw.Name))
	// Inject GATEWAY_<KEY>=value for each config entry (key uppercased).
	for k, v := range gw.Config {
		extra = append(extra, fmt.Sprintf("GATEWAY_%s=%s", strings.ToUpper(k), v))
	}
	return append(base, extra...)
}

// ────────────────────────────────────────────────────────────────────────────
// Helper functions
// ────────────────────────────────────────────────────────────────────────────

// externalGatewayChanged reports whether the config-relevant parts of an external
// gateway spec have changed, requiring the process to be stopped and restarted.
func externalGatewayChanged(old, newSpec gosutospec.Gateway) bool {
	if old.Command != newSpec.Command {
		return true
	}
	if !stringSliceEqual(old.Args, newSpec.Args) {
		return true
	}
	if !stringMapEqual(old.Env, newSpec.Env) {
		return true
	}
	if !stringMapEqual(old.Config, newSpec.Config) {
		return true
	}
	if old.AutoRestart != newSpec.AutoRestart {
		return true
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
