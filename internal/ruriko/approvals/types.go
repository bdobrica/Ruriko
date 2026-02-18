// Package approvals implements the approval workflow for gated Ruriko operations.
//
// When a sensitive command (agent delete, secret delete/rotate, gosuto set) is
// invoked, the request is held as a pending Approval in the database.  An
// authorised operator then sends `approve <id>` or `deny <id> reason="..."` in
// the admin/approvals room, which causes the operation to proceed or be
// cancelled.
package approvals

import (
	"time"
)

// Status represents the lifecycle state of an approval request.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

// DefaultTTL is the default time-to-live for a pending approval request.
const DefaultTTL = 24 * time.Hour

// Approval represents a pending (or resolved) approval request for a
// sensitive operation.
type Approval struct {
	// ID is a short, human-friendly random identifier (e.g. "a3f2b1").
	ID string

	// Action is the command key that triggered the approval (e.g. "agents.delete").
	Action string

	// Target is the primary subject of the action (e.g. agent ID, secret name).
	Target string

	// ParamsJSON is a JSON-encoded map of the original command's args and flags,
	// sufficient to re-execute the operation after approval.
	ParamsJSON string

	// RequestorMXID is the Matrix user ID who originally issued the command.
	RequestorMXID string

	// Status is the current lifecycle state.
	Status Status

	// CreatedAt is when the approval was created.
	CreatedAt time.Time

	// ExpiresAt is when the approval automatically expires if not actioned.
	ExpiresAt time.Time

	// ResolvedAt is set when the approval is approved, denied, or cancelled.
	ResolvedAt *time.Time

	// ResolvedByMXID is the Matrix user ID who resolved the approval (if any).
	ResolvedByMXID *string

	// ResolveReason is the optional reason given by the resolver.
	ResolveReason *string
}

// IsExpired returns true if the approval has passed its deadline and has not
// already been resolved.
func (a *Approval) IsExpired() bool {
	return a.Status == StatusPending && time.Now().After(a.ExpiresAt)
}

// Params is the deserialized form of ParamsJSON — the reconstructed command
// arguments and flags needed to re-execute a gated operation after approval.
type Params struct {
	// Args holds positional command arguments (same order as cmd.Args).
	Args []string `json:"args"`
	// Flags holds named --flag values (same as cmd.Flags).
	Flags map[string]string `json:"flags"`
}
