// Package runtime contains the agent state reconciler.
package runtime

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ReconcilerConfig configures the reconciliation loop.
type ReconcilerConfig struct {
	// Interval is how often to poll container state. Defaults to 30s.
	Interval time.Duration
	// AlertFunc is called when an unexpected state change is detected.
	// If nil, issues are only logged.
	AlertFunc func(agentID, message string)
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

	log.Printf("[reconciler] starting, interval=%s", r.cfg.Interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("[reconciler] stopping")
			return
		case <-ticker.C:
			if err := r.Reconcile(ctx); err != nil {
				log.Printf("[reconciler] error: %v", err)
			}
		}
	}
}

// Reconcile runs a single reconciliation pass.
// It lists all managed containers, compares with the DB, and updates status.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	// Get all agents from the DB
	agents, err := r.store.ListAgents()
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	if len(agents) == 0 {
		return nil
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

	for _, agent := range agents {
		// Skip agents that are known to be not running
		if agent.Status == "stopped" || agent.Status == "deleted" {
			continue
		}

		handle, found := handleMap[agent.ID]
		if !found {
			// Agent should be running but no container found
			if agent.Status == "running" {
				log.Printf("[reconciler] agent %s: container missing, marking error", agent.ID)
				r.store.UpdateAgentStatus(agent.ID, "error")
				r.alert(agent.ID, "container missing; expected running")
			}
			continue
		}

		status, err := r.runtime.Status(ctx, handle)
		if err != nil {
			log.Printf("[reconciler] agent %s: status error: %v", agent.ID, err)
			continue
		}

		newStatus := containerStateToAgentStatus(status.State)
		if newStatus != agent.Status {
			log.Printf("[reconciler] agent %s: status %s → %s", agent.ID, agent.Status, newStatus)
			r.store.UpdateAgentStatus(agent.ID, newStatus)

			// Alert on unexpected transitions
			if newStatus == "error" || (agent.Status == "running" && newStatus != "running") {
				r.alert(agent.ID, fmt.Sprintf("unexpected status change: %s → %s (exit_code=%d)",
					agent.Status, newStatus, status.ExitCode))
			}
		}

		// Always update last_seen for running agents
		if status.State == StateRunning {
			r.store.UpdateAgentLastSeen(agent.ID)
		}
	}

	return nil
}

func (r *Reconciler) alert(agentID, message string) {
	if r.cfg.AlertFunc != nil {
		r.cfg.AlertFunc(agentID, message)
	} else {
		log.Printf("[reconciler] ALERT agent=%s: %s", agentID, message)
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
