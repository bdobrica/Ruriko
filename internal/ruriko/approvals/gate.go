package approvals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// GatedActions is the set of command handler keys that require approval before
// execution. Handlers check this before proceeding.
var GatedActions = map[string]bool{
	"agents.delete":   true,
	"secrets.delete":  true,
	"secrets.rotate":  true,
	"gosuto.set":      true,
	"gosuto.rollback": true,
}

// IsGated returns true when the given action requires an approval.
func IsGated(action string) bool {
	return GatedActions[action]
}

// Gate manages the creation of approval requests for gated operations.
type Gate struct {
	store *Store
	ttl   time.Duration
}

// NewGate creates a Gate backed by the given approval Store.
// ttl controls how long a pending approval remains valid; pass 0 to use DefaultTTL.
func NewGate(store *Store, ttl time.Duration) *Gate {
	if ttl == 0 {
		ttl = DefaultTTL
	}
	return &Gate{store: store, ttl: ttl}
}

// Store returns the underlying approvals Store.
func (g *Gate) Store() *Store {
	return g.store
}

// Request creates a new pending approval for a gated operation and returns the
// Approval record with its ID.  The caller should tell the user the ID so they
// can approve or deny it later.
func (g *Gate) Request(ctx context.Context, action, target string, args []string, flags map[string]string, requestorMXID string) (*Approval, error) {
	params := Params{
		Args:  args,
		Flags: flags,
	}
	if params.Flags == nil {
		params.Flags = make(map[string]string)
	}

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize approval params: %w", err)
	}

	return g.store.Create(ctx, action, target, string(paramsBytes), requestorMXID, g.ttl)
}

// DecodeParams deserializes an Approval's ParamsJSON back into a Params struct.
func DecodeParams(paramsJSON string) (*Params, error) {
	var p Params
	if err := json.Unmarshal([]byte(paramsJSON), &p); err != nil {
		return nil, fmt.Errorf("failed to decode approval params: %w", err)
	}
	if p.Flags == nil {
		p.Flags = make(map[string]string)
	}
	return &p, nil
}

// CheckExpiry atomically marks stale approvals as expired and returns the count.
// This should be called periodically (e.g. from the reconciler or on each command).
func (g *Gate) CheckExpiry(ctx context.Context) (int64, error) {
	return g.store.ExpireStale(ctx)
}
