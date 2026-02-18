// Package approvals manages the agent-side approval workflow.
//
// When the policy engine returns DecisionRequireApproval the agent:
//  1. Generates a unique approval ID.
//  2. Posts a human-readable approval request to the configured approvals room.
//  3. Polls the local store until the status is no longer pending or the TTL expires.
//
// An approval is considered resolved when an approver posts "approve <id>" or
// "deny <id> reason=..." in the approvals room. Ruriko's approval parser handles
// those messages on the human side; the agent just needs to detect the updated
// status in its local SQLite (written via the ACP /secrets/apply or directly by
// an extension — for now the agent polls its own store, which is updated by the
// Matrix event handler in app.go when it receives a decision message).
package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

const (
	defaultTTL   = 60 * time.Minute
	pollInterval = 3 * time.Second
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

	// Post to approvals room.
	msg := formatApprovalMessage(approvalID, action, target, paramsJSON, requestorMXID, expiresAt)
	if err := g.sender.SendText(approvalsRoom, msg); err != nil {
		// Non-fatal — the approval is stored; it just won't be visible.
		fmt.Printf("WARN: could not post approval request to room %s: %v\n", approvalsRoom, err)
	}

	// Poll until decided.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := g.db.GetApprovalStatus(approvalID)
			if err != nil {
				return fmt.Errorf("poll approval: %w", err)
			}
			switch status {
			case store.ApprovalApproved:
				return nil
			case store.ApprovalDenied:
				return fmt.Errorf("operation denied (approval %s)", approvalID)
			case store.ApprovalExpired:
				return fmt.Errorf("approval %s expired", approvalID)
			case store.ApprovalPending:
				// continue polling
			}
		}
	}
}

// RecordDecision updates an approval's status based on an incoming decision
// message (from an approver in the approvals room).
func (g *Gate) RecordDecision(approvalID string, status store.ApprovalStatus, decidedBy, reason string) error {
	return g.db.SetApprovalStatus(approvalID, status, decidedBy, reason)
}

// ParseDecision attempts to parse a Matrix message body as an approval decision.
// Returns ("", "", false) when the text is not a decision.
// Format: "approve <id>" or "deny <id> reason=...".
func ParseDecision(text string) (approvalID string, decision store.ApprovalStatus, reason string, ok bool) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "approve":
		return fields[1], store.ApprovalApproved, "", true
	case "deny":
		if len(fields) < 2 {
			return
		}
		id := fields[1]
		var r string
		for _, f := range fields[2:] {
			if after, found := strings.CutPrefix(f, "reason="); found {
				r = strings.Trim(after, `"`)
			}
		}
		return id, store.ApprovalDenied, r, true
	}
	return
}

func formatApprovalMessage(approvalID, action, target, paramsJSON, requestor string, expiresAt time.Time) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔐 Approval required — ID: `%s`\n", approvalID))
	sb.WriteString(fmt.Sprintf("Action: %s on %s\n", action, target))
	if paramsJSON != "" && paramsJSON != "null" {
		sb.WriteString(fmt.Sprintf("Params: %s\n", paramsJSON))
	}
	sb.WriteString(fmt.Sprintf("Requested by: %s\n", requestor))
	sb.WriteString(fmt.Sprintf("Expires: %s\n", expiresAt.UTC().Format(time.RFC3339)))
	sb.WriteString("\nReply with:\n")
	sb.WriteString(fmt.Sprintf("  `approve %s`\n", approvalID))
	sb.WriteString(fmt.Sprintf("  `deny %s reason=\"...\"`\n", approvalID))
	return sb.String()
}
