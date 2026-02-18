package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// DispatchFunc is a callback used by the approval workflow to re-execute a
// gated handler after an approval decision.  app.go wires this to
// Router.Dispatch.
type DispatchFunc func(ctx context.Context, action string, cmd *Command, evt *event.Event) (string, error)

// HandleApprovalsList lists approval requests, optionally filtered by status.
//
// Usage: /ruriko approvals list [--status pending|approved|denied|expired|cancelled]
func (h *Handlers) HandleApprovalsList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	if h.approvals == nil {
		return "", fmt.Errorf("approval workflow is not configured")
	}

	// Expire stale entries before listing.
	if _, err := h.approvals.CheckExpiry(ctx); err != nil {
		slog.Warn("failed to expire stale approvals", "err", err)
	}

	statusFilter := cmd.GetFlag("status", "")

	list, err := h.approvals.Store().List(ctx, statusFilter)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "approvals.list", "", "error", nil, err.Error())
		return "", fmt.Errorf("failed to list approvals: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "approvals.list", "", "success",
		store.AuditPayload{"count": len(list), "status_filter": statusFilter}, ""); err != nil {
		slog.Warn("audit write failed", "op", "approvals.list", "err", err)
	}

	if len(list) == 0 {
		return fmt.Sprintf("No approvals found.\n\n(trace: %s)", traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Approvals** (%d)\n\n", len(list)))
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("%-14s %-12s %-20s %-12s %s\n", "ID", "STATUS", "ACTION", "TARGET", "EXPIRES/RESOLVED"))
	sb.WriteString(strings.Repeat("-", 78) + "\n")
	for _, a := range list {
		timeField := "exp:" + a.ExpiresAt.Format("01-02 15:04")
		if a.ResolvedAt != nil {
			timeField = "res:" + a.ResolvedAt.Format("01-02 15:04")
		}
		sb.WriteString(fmt.Sprintf("%-14s %-12s %-20s %-12s %s\n",
			a.ID, string(a.Status), a.Action, truncateTarget(a.Target, 12), timeField))
	}
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))
	return sb.String(), nil
}

// HandleApprovalsShow displays details of a single approval request.
//
// Usage: /ruriko approvals show <id>
func (h *Handlers) HandleApprovalsShow(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	if h.approvals == nil {
		return "", fmt.Errorf("approval workflow is not configured")
	}

	id, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko approvals show <id>")
	}

	// Expire stale entries before fetching.
	if _, expErr := h.approvals.CheckExpiry(ctx); expErr != nil {
		slog.Warn("failed to expire stale approvals", "err", expErr)
	}

	a, err := h.approvals.Store().Get(ctx, id)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "approvals.show", id, "error", nil, err.Error())
		return "", fmt.Errorf("approval not found: %s", id)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "approvals.show", id, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "approvals.show", "id", id, "err", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Approval: %s**\n\n", a.ID))
	sb.WriteString(fmt.Sprintf("Status:    %s\n", a.Status))
	sb.WriteString(fmt.Sprintf("Action:    %s\n", a.Action))
	sb.WriteString(fmt.Sprintf("Target:    %s\n", a.Target))
	sb.WriteString(fmt.Sprintf("Requestor: %s\n", a.RequestorMXID))
	sb.WriteString(fmt.Sprintf("Created:   %s\n", a.CreatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Expires:   %s\n", a.ExpiresAt.Format(time.RFC3339)))
	if a.ResolvedAt != nil {
		sb.WriteString(fmt.Sprintf("Resolved:  %s\n", a.ResolvedAt.Format(time.RFC3339)))
	}
	if a.ResolvedByMXID != nil {
		sb.WriteString(fmt.Sprintf("Resolved by: %s\n", *a.ResolvedByMXID))
	}
	if a.ResolveReason != nil && *a.ResolveReason != "" {
		sb.WriteString(fmt.Sprintf("Reason:    %s\n", *a.ResolveReason))
	}

	if a.IsExpired() {
		sb.WriteString("\n⚠️  This approval has expired.\n")
	} else if a.Status == approvals.StatusPending {
		sb.WriteString(fmt.Sprintf("\n✅ To approve: `approve %s`\n", a.ID))
		sb.WriteString(fmt.Sprintf("❌ To deny:    `deny %s reason=\"<text>\"`\n", a.ID))
	}

	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))
	return sb.String(), nil
}

// HandleApprovalDecision processes a plain `approve <id>` or
// `deny <id> reason="..."` message from an admin room.
//
// This is NOT a /ruriko-prefixed command.  It is called directly from
// app.handleMessage after trying the regular router.
func (h *Handlers) HandleApprovalDecision(ctx context.Context, text string, evt *event.Event) (string, error) {
	decision, err := approvals.ParseDecision(text)
	if err != nil {
		if errors.Is(err, approvals.ErrNotADecision) {
			return "", approvals.ErrNotADecision
		}
		return "", err
	}

	if h.approvals == nil {
		return "", fmt.Errorf("approval workflow is not configured")
	}

	traceID := trace.GenerateID()
	senderMXID := evt.Sender.String()

	// Expire stale entries first.
	if _, expErr := h.approvals.CheckExpiry(ctx); expErr != nil {
		slog.Warn("failed to expire stale approvals", "err", expErr)
	}

	// Fetch the approval to check its current state.
	approval, err := h.approvals.Store().Get(ctx, decision.ApprovalID)
	if err != nil {
		return "", fmt.Errorf("approval not found: %s", decision.ApprovalID)
	}

	if approval.Status != approvals.StatusPending {
		return fmt.Sprintf("⚠️  Approval **%s** is already **%s** and cannot be changed.\n\n(trace: %s)",
			decision.ApprovalID, approval.Status, traceID), nil
	}

	// Prevent self-approval: the requestor must not approve their own request.
	if decision.Approve && approval.RequestorMXID == senderMXID {
		return "", fmt.Errorf("cannot approve your own request; another operator must approve")
	}

	if approval.IsExpired() {
		// Already expired — resolve and report.
		_ = h.approvals.Store().Cancel(ctx, decision.ApprovalID, senderMXID, "expired before decision")
		return fmt.Sprintf("⚠️  Approval **%s** has expired.\n\n(trace: %s)", decision.ApprovalID, traceID), nil
	}

	action := "approval.deny"
	if decision.Approve {
		action = "approval.approve"
	}

	if decision.Approve {
		if err := h.approvals.Store().Approve(ctx, decision.ApprovalID, senderMXID, decision.Reason); err != nil {
			h.store.WriteAudit(ctx, traceID, senderMXID, action, decision.ApprovalID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to approve: %w", err)
		}

		h.store.WriteAudit(ctx, traceID, senderMXID, action, decision.ApprovalID, "success",
			store.AuditPayload{"original_action": approval.Action, "target": approval.Target}, "")

		// Re-execute the approved operation via the dispatch callback.
		result, execErr := h.executeApproved(ctx, approval, evt, traceID)
		if execErr != nil {
			return fmt.Sprintf("✅ Approved by **%s** — but execution failed: %s\n\n(trace: %s)",
				senderMXID, execErr, traceID), nil
		}

		return fmt.Sprintf("✅ Approved by **%s**.\n\n%s", senderMXID, result), nil
	}

	// Deny path.
	if err := h.approvals.Store().Deny(ctx, decision.ApprovalID, senderMXID, decision.Reason); err != nil {
		h.store.WriteAudit(ctx, traceID, senderMXID, action, decision.ApprovalID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to deny: %w", err)
	}

	h.store.WriteAudit(ctx, traceID, senderMXID, action, decision.ApprovalID, "success",
		store.AuditPayload{"original_action": approval.Action, "target": approval.Target, "reason": decision.Reason}, "")

	reasonStr := ""
	if decision.Reason != "" {
		reasonStr = fmt.Sprintf(" Reason: %s.", decision.Reason)
	}

	return fmt.Sprintf("❌ Denied by **%s**.%s\n\n(trace: %s)", senderMXID, reasonStr, traceID), nil
}

// executeApproved reconstructs the original Command from an approved Approval
// and re-executes it via the registered dispatch function.
func (h *Handlers) executeApproved(ctx context.Context, approval *approvals.Approval, approverEvt *event.Event, traceID string) (string, error) {
	if h.dispatch == nil {
		return "", fmt.Errorf("dispatch function not configured")
	}

	params, err := approvals.DecodeParams(approval.ParamsJSON)
	if err != nil {
		return "", fmt.Errorf("failed to decode approval params: %w", err)
	}

	// Reconstruct a Command with the _approved flag set so the handler
	// skips the gate check on re-execution.
	flags := params.Flags
	if flags == nil {
		flags = make(map[string]string)
	}
	flags["_approved"] = "true"
	flags["_approval_id"] = approval.ID
	flags["_trace_id"] = traceID

	cmd := &Command{
		Args:  params.Args,
		Flags: flags,
	}

	// Parse name and subcommand from action key (e.g. "agents.delete" → name="agents", sub="delete").
	parts := strings.SplitN(approval.Action, ".", 2)
	cmd.Name = parts[0]
	if len(parts) == 2 {
		cmd.Subcommand = parts[1]
	}

	// Execute on behalf of the original requestor so audit logs show the
	// right actor.  We shallow-copy the approver's event and override the
	// Sender field with the original requestor's MXID.
	requestorEvt := *approverEvt
	requestorEvt.Sender = id.UserID(approval.RequestorMXID)

	return h.dispatch(ctx, approval.Action, cmd, &requestorEvt)
}

// requestApprovalIfNeeded checks whether the action requires approval, and if
// so creates a pending approval and returns (msg, true, nil).  If approval is
// not needed (or already granted via _approved flag), it returns ("", false, nil).
func (h *Handlers) requestApprovalIfNeeded(
	ctx context.Context,
	action, target string,
	cmd *Command,
	evt *event.Event,
) (msg string, needsApproval bool, err error) {
	// Already approved — skip the gate.
	if cmd.GetFlag("_approved", "") == "true" {
		return "", false, nil
	}

	if h.approvals == nil || !approvals.IsGated(action) {
		return "", false, nil
	}

	traceID := trace.GenerateID()

	tracedCtx := trace.WithTraceID(ctx, traceID)
	ap, err := h.approvals.Request(tracedCtx, action, target, cmd.Args, cmd.Flags, evt.Sender.String())
	if err != nil {
		return "", true, fmt.Errorf("failed to create approval request: %w", err)
	}

	h.store.WriteAudit(ctx, traceID, evt.Sender.String(), action+".approval_requested", target, "pending",
		store.AuditPayload{"approval_id": ap.ID}, "")

	msg = fmt.Sprintf(
		"⏳ **Approval required** for **%s** on **%s**.\n\n"+
			"Approval ID: `%s`\n"+
			"Requestor:   %s\n"+
			"Expires:     %s\n\n"+
			"Reply with:\n"+
			"• `approve %s` — to proceed\n"+
			"• `deny %s reason=\"<text>\"` — to cancel\n\n"+
			"(trace: %s)",
		action, target,
		ap.ID,
		evt.Sender.String(),
		ap.ExpiresAt.Format(time.RFC3339),
		ap.ID, ap.ID,
		traceID,
	)

	return msg, true, nil
}

// truncateTarget shortens target strings for table display.
func truncateTarget(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
