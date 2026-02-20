// Package runtime contains the agent state reconciler.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ACPStatusChecker is the subset of the ACP client used during reconciliation.
// It is defined here so that tests can provide lightweight mocks without
// importing the full acp package.
//
// *acp.Client satisfies this interface directly.
type ACPStatusChecker interface {
	Health(ctx context.Context) (*acp.HealthResponse, error)
	Status(ctx context.Context) (*acp.StatusResponse, error)
}

// ACPClientFactory constructs an ACPStatusChecker for the given agent control
// URL and bearer token.  Pass acp.NewACPChecker as the production factory.
// If nil, ACP health checks and drift detection are skipped.
type ACPClientFactory func(controlURL, token string) ACPStatusChecker

// NewACPChecker is the production ACPClientFactory — it wraps acp.New.
func NewACPChecker(controlURL, token string) ACPStatusChecker {
	return acp.New(controlURL, acp.Options{Token: token})
}

// ReconcilerConfig configures the reconciliation loop.
type ReconcilerConfig struct {
	// Interval is how often to poll container state. Defaults to 30s.
	Interval time.Duration
	// AlertFunc is called when an unexpected state change is detected.
	// If nil, issues are only logged.
	AlertFunc func(agentID, message string)

	// ACPClientFactory, when non-nil, is used to create ACP clients for
	// enabled agents whose provisioning_state is "healthy".  Used to
	// refresh actual_gosuto_hash and last_health_check.
	// If nil, ACP queries are skipped entirely.
	ACPClientFactory ACPClientFactory

	// HealthStaleThreshold is the maximum acceptable age of a successful
	// ACP /health response before the reconciler raises an alert.
	// Zero (default) disables staleness alerting.
	HealthStaleThreshold time.Duration
}

// Reconciler periodically syncs container state into the agents table.
type Reconciler struct {
	runtime Runtime
	store   *store.Store
	cfg     ReconcilerConfig
}

// NewReconciler creates a new Reconciler.
func NewReconciler(rt Runtime, s *store.Store, cfg ReconcilerConfig) *Reconciler {
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	return &Reconciler{runtime: rt, store: s, cfg: cfg}
}

// Run starts the reconciliation loop. Blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	slog.Info("[reconciler] starting", "interval", r.cfg.Interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("[reconciler] stopping")
			return
		case <-ticker.C:
			traceID := trace.GenerateID()
			reconcileCtx := trace.WithTraceID(ctx, traceID)
			if err := r.Reconcile(reconcileCtx); err != nil {
				slog.Error("[reconciler] reconcile error", "err", err, "trace_id", traceID)
			}
		}
	}
}

// Reconcile runs a single reconciliation pass.
// It lists all managed containers, compares with the DB, and updates status.
// It also detects orphan containers — containers labelled as ruriko-managed
// that have no corresponding record in the database.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	// Get all agents from the DB
	agents, err := r.store.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	// Get all managed containers from the runtime
	handles, err := r.runtime.List(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	// Build a map: agentID → handle
	handleMap := make(map[string]AgentHandle, len(handles))
	for _, h := range handles {
		handleMap[h.AgentID] = h
	}

	// Build a set of known agent IDs for orphan detection below.
	knownAgentIDs := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		knownAgentIDs[a.ID] = struct{}{}
	}

	for _, agent := range agents {
		// Skip agents that are known to be not running
		if agent.Status == "stopped" || agent.Status == "deleted" {
			continue
		}

		handle, found := handleMap[agent.ID]
		if !found {
			// Agent should be running but no container found
			if agent.Status == "running" {
				slog.Warn("[reconciler] container missing, marking error", "agent", agent.ID, "trace_id", trace.FromContext(ctx))
				r.store.UpdateAgentStatus(ctx, agent.ID, "error")
				r.alert(agent.ID, "container missing; expected running")
			}
			continue
		}

		status, err := r.runtime.Status(ctx, handle)
		if err != nil {
			slog.Warn("[reconciler] status error", "agent", agent.ID, "err", err, "trace_id", trace.FromContext(ctx))
			continue
		}

		newStatus := containerStateToAgentStatus(status.State)
		if newStatus != agent.Status {
			slog.Info("[reconciler] status change", "agent", agent.ID, "from", agent.Status, "to", newStatus, "trace_id", trace.FromContext(ctx))
			r.store.UpdateAgentStatus(ctx, agent.ID, newStatus)

			// Alert on unexpected transitions
			if newStatus == "error" || (agent.Status == "running" && newStatus != "running") {
				r.alert(agent.ID, fmt.Sprintf("unexpected status change: %s → %s (exit_code=%d)",
					agent.Status, newStatus, status.ExitCode))
			}
		}

		// Always update last_seen for running agents
		if status.State == StateRunning {
			r.store.UpdateAgentLastSeen(ctx, agent.ID)
		}

		// R5.3: ACP health + drift detection for healthy, enabled agents.
		if r.cfg.ACPClientFactory != nil &&
			agent.Enabled &&
			agent.ProvisioningState == "healthy" &&
			agent.ControlURL.Valid && agent.ControlURL.String != "" {

			r.reconcileACP(ctx, agent)
		}
	}

	// Detect orphan containers: ruriko-managed containers with no DB record.
	for agentID := range handleMap {
		if _, inDB := knownAgentIDs[agentID]; !inDB {
			slog.Warn("[reconciler] orphan container detected", "agent", agentID)
			r.alert(agentID, "orphan container: no matching agent record in database")
		}
	}

	return nil
}

func (r *Reconciler) alert(agentID, message string) {
	if r.cfg.AlertFunc != nil {
		r.cfg.AlertFunc(agentID, message)
	} else {
		slog.Warn("[reconciler] ALERT", "agent", agentID, "message", message)
	}
}

func containerStateToAgentStatus(state ContainerState) string {
	switch state {
	case StateRunning:
		return "running"
	case StateStopped, StateExited, StateCreated:
		return "stopped"
	case StateUnknown, StateRemoving:
		return "error"
	default:
		return "error"
	}
}

// reconcileACP performs ACP-level drift detection and health checks for a
// single healthy, enabled agent.  It is called for every agent whose
// provisioning_state is "healthy" and whose control URL is known.
//
// On each reconcile pass it:
//  1. Calls ACP GET /health → updates last_health_check on success; alerts on
//     failure.  Optionally alerts when last_health_check is stale.
//  2. Calls ACP GET /status → updates actual_gosuto_hash in the DB.  If the
//     actual hash differs from desired_gosuto_hash, emits a drift alert.
func (r *Reconciler) reconcileACP(ctx context.Context, agent *store.Agent) {
	checker := r.cfg.ACPClientFactory(agent.ControlURL.String, agent.ACPToken.String)

	// --- health check ---------------------------------------------------
	if _, err := checker.Health(ctx); err != nil {
		slog.Warn("[reconciler] ACP health check failed",
			"agent", agent.ID, "err", err, "trace_id", trace.FromContext(ctx))
		r.alert(agent.ID, fmt.Sprintf("ACP health check failed: %v", err))
	} else {
		if err := r.store.UpdateAgentHealthCheck(ctx, agent.ID); err != nil {
			slog.Warn("[reconciler] failed to update last_health_check",
				"agent", agent.ID, "err", err)
		}
	}

	// Staleness check: alert if the last successful health check is too old.
	if r.cfg.HealthStaleThreshold > 0 && agent.LastHealthCheck.Valid {
		age := time.Since(agent.LastHealthCheck.Time)
		if age > r.cfg.HealthStaleThreshold {
			r.alert(agent.ID, fmt.Sprintf("health check stale: last seen %s ago (threshold: %s)",
				age.Round(time.Second), r.cfg.HealthStaleThreshold))
		}
	}

	// --- status / drift check -------------------------------------------
	statusResp, err := checker.Status(ctx)
	if err != nil {
		slog.Warn("[reconciler] ACP status check failed",
			"agent", agent.ID, "err", err, "trace_id", trace.FromContext(ctx))
		// Non-fatal: we'll retry on the next pass.
		return
	}

	if statusResp.GosutoHash != "" {
		if err := r.store.SetAgentActualGosutoHash(ctx, agent.ID, statusResp.GosutoHash); err != nil {
			slog.Warn("[reconciler] failed to update actual_gosuto_hash",
				"agent", agent.ID, "err", err)
		}

		// Drift: desired is known and differs from what the agent is running.
		if agent.DesiredGosutoHash.Valid && agent.DesiredGosutoHash.String != "" &&
			statusResp.GosutoHash != agent.DesiredGosutoHash.String {
			r.alert(agent.ID, fmt.Sprintf(
				"Gosuto config drift detected: desired=%s…, actual=%s…",
				truncate(agent.DesiredGosutoHash.String, 8),
				truncate(statusResp.GosutoHash, 8),
			))
		}
	}
}

// truncate returns the first n characters of s, or s itself if it is shorter.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
