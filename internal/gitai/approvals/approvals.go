// Package approvals manages the agent-side approval workflow.
//
// Approval requests are persisted locally and surfaced to operators by Ruriko.
// Decision transport is unified through Ruriko -> ACP notification back to the
// agent (not direct Matrix decision parsing in the agent runtime).
//
// Deterministic decision semantics:
//   - approve => execute
//   - deny => refuse
//   - timeout/expiry => deny (fail-safe)
package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

const (
	defaultTTL   = 60 * time.Minute
	pollInterval = 200 * time.Millisecond
)

// Sender can send Matrix messages (subset of the matrix client interface).
type Sender interface {
	SendText(roomID, text string) error
}

// Gate manages pending approval requests for this agent.
type Gate struct {
	db     *store.Store
	sender Sender
}

// New creates a Gate using the provided store and Matrix sender.
func New(db *store.Store, sender Sender) *Gate {
	return &Gate{db: db, sender: sender}
}

// Request posts an approval request to approvalsRoom and blocks until the
// request is decided or the context is cancelled.
//
// Returns nil if approved; returns an error if denied, expired, or context done.
func (g *Gate) Request(
	ctx context.Context,
	approvalsRoom string,
	requestorMXID string,
	action string,
	target string,
	params map[string]interface{},
	ttl time.Duration,
) error {
	if ttl == 0 {
		ttl = defaultTTL
	}
	traceID := trace.FromContext(ctx)
	if traceID == "" {
		traceID = trace.GenerateID()
	}

	approvalID := "appr_" + traceID

	var paramsJSON string
	if len(params) > 0 {
		b, _ := json.Marshal(params)
		paramsJSON = string(b)
	}

	expiresAt := time.Now().Add(ttl)

	if err := g.db.SaveApproval(
		approvalID, traceID, approvalsRoom,
		action, target, paramsJSON,
		requestorMXID, expiresAt,
	); err != nil {
		return fmt.Errorf("save approval: %w", err)
	}

	// Poll until decided. Decision updates are expected to come from Ruriko via
	// ACP (control plane), not from direct Matrix commands in the agent.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(ttl)
	defer timeout.Stop()

	deny := func(reason string) error {
		_ = g.db.SetApprovalStatus(approvalID, store.ApprovalDenied, "ruriko", reason)
		return fmt.Errorf("operation denied (approval %s): %s", approvalID, reason)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return deny("timeout")
		case <-ticker.C:
			status, err := g.db.GetApprovalStatus(approvalID)
			if err != nil {
				return fmt.Errorf("poll approval: %w", err)
			}
			switch status {
			case store.ApprovalApproved:
				return nil
			case store.ApprovalDenied:
				return fmt.Errorf("operation denied (approval %s): denied", approvalID)
			case store.ApprovalExpired:
				return deny("timeout")
			case store.ApprovalPending:
				// continue polling
			default:
				return deny("invalid decision state")
			}
		}
	}
}

// RecordDecision updates an approval's status based on an incoming decision
// message (from an approver in the approvals room).
func (g *Gate) RecordDecision(approvalID string, status store.ApprovalStatus, decidedBy, reason string) error {
	return g.db.SetApprovalStatus(approvalID, status, decidedBy, reason)
}
