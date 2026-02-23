// Package policy provides the Gitai policy engine.
//
// The engine evaluates whether a proposed tool invocation is permitted,
// requires approval, or is denied, based on the active Gosuto configuration.
// Evaluation is purely deterministic -- no LLM involvement.
package policy

import (
	"fmt"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// Decision is the outcome of policy evaluation.
type Decision int

const (
	// DecisionAllow means the tool call is permitted immediately.
	DecisionAllow Decision = iota
	// DecisionRequireApproval means the call is permitted but needs human sign-off first.
	DecisionRequireApproval
	// DecisionDeny means the call is not permitted.
	DecisionDeny
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionRequireApproval:
		return "require_approval"
	case DecisionDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// Violation describes why a call was denied or constrained.
type Violation struct {
	Rule       string
	Constraint string
	Message    string
}

func (v Violation) Error() string {
	if v.Constraint != "" {
		return fmt.Sprintf("[%s] constraint %q: %s", v.Rule, v.Constraint, v.Message)
	}
	return fmt.Sprintf("[%s] %s", v.Rule, v.Message)
}

// Result is the full output of a policy evaluation.
type Result struct {
	Decision    Decision
	MatchedRule string
	Violation   *Violation
}

// Engine evaluates policy against the currently loaded Gosuto config.
type Engine struct {
	loader ConfigProvider
}

// ConfigProvider is any type that can return the current Gosuto config.
type ConfigProvider interface {
	Config() *gosutospec.Config
}

// New returns a new Engine backed by the provided config provider.
func New(provider ConfigProvider) *Engine {
	return &Engine{loader: provider}
}

// Evaluate checks whether calling tool on mcpServer with the given args is
// permitted by the current Gosuto policy.
//
// Rules are first-match-wins. The default is DENY.
func (e *Engine) Evaluate(mcpServer, tool string, args map[string]interface{}) Result {
	cfg := e.loader.Config()
	if cfg == nil {
		return Result{
			Decision:    DecisionDeny,
			MatchedRule: "<no config>",
			Violation: &Violation{
				Rule:    "<no config>",
				Message: "no Gosuto configuration loaded",
			},
		}
	}

	for _, cap := range cfg.Capabilities {
		if !matchesGlob(cap.MCP, mcpServer) {
			continue
		}
		if !matchesGlob(cap.Tool, tool) {
			continue
		}

		// Rule matched. Check constraints first.
		if v := checkConstraints(cap, args); v != nil {
			return Result{
				Decision:    DecisionDeny,
				MatchedRule: cap.Name,
				Violation:   v,
			}
		}

		if !cap.Allow {
			return Result{
				Decision:    DecisionDeny,
				MatchedRule: cap.Name,
				Violation: &Violation{
					Rule:    cap.Name,
					Message: "capability rule denies this tool call",
				},
			}
		}

		if cap.RequireApproval {
			return Result{
				Decision:    DecisionRequireApproval,
				MatchedRule: cap.Name,
			}
		}

		return Result{
			Decision:    DecisionAllow,
			MatchedRule: cap.Name,
		}
	}

	// No rule matched -- default deny.
	return Result{
		Decision:    DecisionDeny,
		MatchedRule: "<default>",
		Violation: &Violation{
			Rule:    "<default>",
			Message: fmt.Sprintf("no capability rule matches mcp=%q tool=%q; default deny", mcpServer, tool),
		},
	}
}

// IsSenderAllowed returns true if the given Matrix user ID is allowed to
// interact with this agent according to Gosuto trust settings.
func (e *Engine) IsSenderAllowed(senderMXID string) bool {
	cfg := e.loader.Config()
	if cfg == nil {
		return false
	}
	return matchesAny(cfg.Trust.AllowedSenders, senderMXID)
}

// IsRoomAllowed returns true if the given Matrix room ID is in the allowed list.
func (e *Engine) IsRoomAllowed(roomID string) bool {
	cfg := e.loader.Config()
	if cfg == nil {
		return false
	}
	return matchesAny(cfg.Trust.AllowedRooms, roomID)
}

// IsMessagingConfigured returns true when the active Gosuto config declares at
// least one allowed messaging target.  When this returns false the built-in
// matrix.send_message tool must be treated as unavailable: it is excluded from
// the tool list sent to the LLM and any stray call attempts are denied by the
// default-deny policy.
func (e *Engine) IsMessagingConfigured() bool {
	cfg := e.loader.Config()
	if cfg == nil {
		return false
	}
	return len(cfg.Messaging.AllowedTargets) > 0
}

// checkConstraints validates args against the capability's constraint map.
// Returns non-nil only when a constraint is violated.
func checkConstraints(cap gosutospec.Capability, args map[string]interface{}) *Violation {
	for key, expected := range cap.Constraints {
		switch key {
		case "url_prefix":
			if u, ok := args["url"].(string); ok {
				if len(u) < len(expected) || u[:len(expected)] != expected {
					return &Violation{
						Rule:       cap.Name,
						Constraint: key,
						Message:    fmt.Sprintf("url %q does not start with %q", u, expected),
					}
				}
			}
		default:
			if actual, ok := args[key]; ok {
				if fmt.Sprintf("%v", actual) != expected {
					return &Violation{
						Rule:       cap.Name,
						Constraint: key,
						Message:    fmt.Sprintf("arg %q = %v, expected %q", key, actual, expected),
					}
				}
			}
		}
	}
	return nil
}

// matchesGlob returns true when pattern is "*" or equals value exactly.
func matchesGlob(pattern, value string) bool {
	return pattern == "*" || pattern == value
}

// matchesAny returns true when "*" is in the list or value appears in the list.
func matchesAny(list []string, value string) bool {
	for _, s := range list {
		if s == "*" || s == value {
			return true
		}
	}
	return false
}
