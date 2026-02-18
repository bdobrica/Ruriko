// Package audit provides the audit room notification subsystem.
//
// When configured with a Matrix room ID (MATRIX_AUDIT_ROOM), Ruriko posts
// concise human-readable summaries of major control-plane events to that
// room so operators can monitor activity without tailing the SQLite audit log.
//
// Supported event types (AuditEvent.Kind):
//   - KindAgentCreated, KindAgentStarted, KindAgentStopped, KindAgentRespawned,
//     KindAgentDeleted, KindAgentDisabled
//   - KindApprovalRequested, KindApprovalApproved, KindApprovalDenied
//   - KindSecretsRotated, KindSecretsPushed
//   - KindError
//
// All events include the originating trace ID so operators can quickly look
// up the full audit log entry with /ruriko trace <id>.
package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
)

// Kind is a machine-readable event category.
type Kind string

const (
	KindAgentCreated      Kind = "agent.created"
	KindAgentStarted      Kind = "agent.started"
	KindAgentStopped      Kind = "agent.stopped"
	KindAgentRespawned    Kind = "agent.respawned"
	KindAgentDeleted      Kind = "agent.deleted"
	KindAgentDisabled     Kind = "agent.disabled"
	KindApprovalRequested Kind = "approval.requested"
	KindApprovalApproved  Kind = "approval.approved"
	KindApprovalDenied    Kind = "approval.denied"
	KindSecretsRotated    Kind = "secrets.rotated"
	KindSecretsPushed     Kind = "secrets.pushed"
	KindError             Kind = "error"
)

// Event carries the data that the audit notifier formats and sends.
type Event struct {
	// Kind identifies the type of event.
	Kind Kind
	// Actor is the Matrix user ID that triggered the event.
	Actor string
	// Target is the primary resource affected (agent name, secret name, …).
	Target string
	// Message is a human-friendly description of what happened.
	Message string
	// TraceID ties the notification back to the SQLite audit record.
	// When empty the value is taken from the context.
	TraceID string
	// Timestamp defaults to time.Now() when zero.
	Timestamp time.Time
}

// Notifier sends audit room notifications for major control-plane events.
type Notifier interface {
	// Notify posts an audit event. Implementations MUST NOT block the caller
	// for longer than a short timeout; send failures should be logged, not
	// propagated.
	Notify(ctx context.Context, evt Event)
}

// Sender is the subset of the Matrix client needed by MatrixNotifier.
// Defined as an interface so the notifier can be unit-tested independently.
type Sender interface {
	SendNotice(roomID, message string) error
}

// MatrixNotifier posts formatted notices to a Matrix audit room.
type MatrixNotifier struct {
	sender Sender
	roomID string
}

// NewMatrixNotifier creates a MatrixNotifier that posts to roomID via sender.
func NewMatrixNotifier(sender Sender, roomID string) *MatrixNotifier {
	return &MatrixNotifier{sender: sender, roomID: roomID}
}

// Notify formats evt as a human-readable notice and posts it to the audit room.
// Errors are logged at WARN level; the caller is never blocked.
func (n *MatrixNotifier) Notify(ctx context.Context, evt Event) {
	if n.roomID == "" {
		return
	}

	tid := evt.TraceID
	if tid == "" {
		tid = trace.FromContext(ctx)
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	icon := kindIcon(evt.Kind)
	msg := fmt.Sprintf("%s [%s] %s", icon, evt.Kind, evt.Message)
	if evt.Target != "" {
		msg = fmt.Sprintf("%s %s → %s", icon, evt.Target, evt.Message)
	}
	if tid != "" {
		msg = fmt.Sprintf("%s\n  trace: %s", msg, tid)
	}
	if evt.Actor != "" {
		msg = fmt.Sprintf("%s\n  actor: %s", msg, evt.Actor)
	}

	if err := n.sender.SendNotice(n.roomID, msg); err != nil {
		slog.Warn("audit notifier: failed to send room notice",
			"room", n.roomID, "kind", evt.Kind, "err", err)
	} else {
		slog.Debug("audit notifier: sent notice", "room", n.roomID, "kind", evt.Kind)
	}
}

// Noop is a no-op Notifier used when audit room notifications are disabled.
type Noop struct{}

// Notify does nothing.
func (Noop) Notify(_ context.Context, _ Event) {}

// kindIcon returns a Unicode icon for the event kind.
func kindIcon(k Kind) string {
	switch k {
	case KindAgentCreated:
		return "🟢"
	case KindAgentStarted:
		return "▶️"
	case KindAgentStopped:
		return "⏹️"
	case KindAgentRespawned:
		return "🔄"
	case KindAgentDeleted:
		return "🗑️"
	case KindAgentDisabled:
		return "🚫"
	case KindApprovalRequested:
		return "🔔"
	case KindApprovalApproved:
		return "✅"
	case KindApprovalDenied:
		return "❌"
	case KindSecretsRotated:
		return "🔑"
	case KindSecretsPushed:
		return "📤"
	case KindError:
		return "🚨"
	default:
		return "ℹ️"
	}
}
