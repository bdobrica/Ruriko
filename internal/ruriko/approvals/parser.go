package approvals

import (
	"fmt"
	"strings"
)

// Decision holds the result of parsing an approve or deny message.
type Decision struct {
	// Approve is true for "approve", false for "deny".
	Approve bool
	// ApprovalID is the ID of the approval being acted on.
	ApprovalID string
	// Reason is the optional reason string (required for deny).
	Reason string
}

// ParseDecision parses a plain room message into an approval decision.
//
// Accepted formats (case-insensitive prefix):
//
//	approve <id>
//	approve <id> <reason text>
//	deny <id> reason="<text>"
//	deny <id> <reason text>
//
// Returns ErrNotADecision if the message does not start with "approve" or "deny".
// Returns an error if the message is malformed (e.g. deny without reason).
func ParseDecision(text string) (*Decision, error) {
	text = strings.TrimSpace(text)

	lower := strings.ToLower(text)
	var isApprove bool

	switch {
	case strings.HasPrefix(lower, "approve ") || lower == "approve":
		isApprove = true
	case strings.HasPrefix(lower, "deny ") || lower == "deny":
		isApprove = false
	default:
		return nil, ErrNotADecision
	}

	// Strip the verb.
	rest := strings.TrimSpace(text[len("approve"):])
	if !isApprove {
		rest = strings.TrimSpace(text[len("deny"):])
	}

	if rest == "" {
		return nil, fmt.Errorf("usage: %s <approval-id> [reason]", verb(isApprove))
	}

	parts := strings.Fields(rest)
	id := parts[0]

	// Remaining parts are the reason (may be in reason="..." form or plain text).
	var reason string
	if len(parts) > 1 {
		reason = parseReason(strings.Join(parts[1:], " "))
	}

	if !isApprove && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("deny requires a reason: deny <id> reason=\"<text>\" or deny <id> <text>")
	}

	return &Decision{
		Approve:    isApprove,
		ApprovalID: id,
		Reason:     reason,
	}, nil
}

// ErrNotADecision is returned when the message is not an approve/deny command.
var ErrNotADecision = fmt.Errorf("not an approval decision")

// verb returns the command verb string for error messages.
func verb(approve bool) string {
	if approve {
		return "approve"
	}
	return "deny"
}

// parseReason extracts the reason from either:
//   - `reason="<text>"` or `reason=<text>`
//   - plain trailing text
func parseReason(s string) string {
	s = strings.TrimSpace(s)

	// Support reason="..." or reason=...
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "reason=") {
		val := s[len("reason="):]
		val = strings.Trim(val, `"'`)
		return val
	}

	return s
}
