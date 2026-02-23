package policy_test

import (
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
)

// staticProvider is a test helper that always returns the same config.
type staticProvider struct {
	cfg *gosutospec.Config
}

func (s *staticProvider) Config() *gosutospec.Config { return s.cfg }

func cfg(caps []gosutospec.Capability, senders, rooms []string) *gosutospec.Config {
	return &gosutospec.Config{
		Trust: gosutospec.Trust{
			AllowedSenders: senders,
			AllowedRooms:   rooms,
		},
		Capabilities: caps,
	}
}

func TestEvaluate_AllowExact(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "allow-fetch", MCP: "browser", Tool: "fetch", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("browser", "fetch", nil)
	if r.Decision != policy.DecisionAllow {
		t.Errorf("expected Allow, got %s (violation: %v)", r.Decision, r.Violation)
	}
	if r.MatchedRule != "allow-fetch" {
		t.Errorf("unexpected matched rule: %q", r.MatchedRule)
	}
}

func TestEvaluate_DenyExplicit(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "deny-rm", MCP: "shell", Tool: "rm", Allow: false},
	}, nil, nil)})

	r := e.Evaluate("shell", "rm", nil)
	if r.Decision != policy.DecisionDeny {
		t.Errorf("expected Deny, got %s", r.Decision)
	}
}

func TestEvaluate_RequireApproval(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "approve-deploy", MCP: "k8s", Tool: "apply", Allow: true, RequireApproval: true},
	}, nil, nil)})

	r := e.Evaluate("k8s", "apply", nil)
	if r.Decision != policy.DecisionRequireApproval {
		t.Errorf("expected RequireApproval, got %s", r.Decision)
	}
}

func TestEvaluate_WildcardMCP(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "allow-all", MCP: "*", Tool: "*", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("anything", "anytool", nil)
	if r.Decision != policy.DecisionAllow {
		t.Errorf("expected Allow, got %s", r.Decision)
	}
}

func TestEvaluate_DefaultDeny(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "allow-fetch", MCP: "browser", Tool: "fetch", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("browser", "unknown_tool", nil)
	if r.Decision != policy.DecisionDeny {
		t.Errorf("expected Deny (default), got %s", r.Decision)
	}
	if r.MatchedRule != "<default>" {
		t.Errorf("expected <default> rule, got %q", r.MatchedRule)
	}
}

func TestEvaluate_NilConfig(t *testing.T) {
	e := policy.New(&staticProvider{cfg: nil})
	r := e.Evaluate("any", "tool", nil)
	if r.Decision != policy.DecisionDeny {
		t.Errorf("expected Deny with nil config, got %s", r.Decision)
	}
}

func TestEvaluate_ConstraintURLPrefix(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{
			Name:        "allow-fetch-safe",
			MCP:         "browser",
			Tool:        "fetch",
			Allow:       true,
			Constraints: map[string]string{"url_prefix": "https://example.com"},
		},
	}, nil, nil)})

	// Should allow matching prefix.
	r := e.Evaluate("browser", "fetch", map[string]interface{}{"url": "https://example.com/path"})
	if r.Decision != policy.DecisionAllow {
		t.Errorf("expected Allow for matched prefix, got %s (violation: %v)", r.Decision, r.Violation)
	}

	// Should deny non-matching prefix.
	r2 := e.Evaluate("browser", "fetch", map[string]interface{}{"url": "https://evil.com/path"})
	if r2.Decision != policy.DecisionDeny {
		t.Errorf("expected Deny for mismatched prefix, got %s", r2.Decision)
	}
}

func TestIsSenderAllowed(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg(nil, []string{"@alice:matrix.org"}, nil)})

	if !e.IsSenderAllowed("@alice:matrix.org") {
		t.Error("expected alice to be allowed")
	}
	if e.IsSenderAllowed("@eve:matrix.org") {
		t.Error("expected eve to be denied")
	}
}

func TestIsRoomAllowed_Wildcard(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg(nil, nil, []string{"*"})})

	if !e.IsRoomAllowed("!any:room") {
		t.Error("expected wildcard to allow any room")
	}
}

func TestDecisionString(t *testing.T) {
	tests := []struct {
		d    policy.Decision
		want string
	}{
		{policy.DecisionAllow, "allow"},
		{policy.DecisionRequireApproval, "require_approval"},
		{policy.DecisionDeny, "deny"},
	}
	for _, tt := range tests {
		if got := tt.d.String(); got != tt.want {
			t.Errorf("Decision(%d).String() = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- R15.3: built-in tool policy gate tests ---

// TestEvaluate_BuiltinTool_Allow verifies that a capability rule of the form
// (mcp: builtin, tool: matrix.send_message, allow: true) permits the call.
func TestEvaluate_BuiltinTool_Allow(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "allow-matrix-send", MCP: "builtin", Tool: "matrix.send_message", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("builtin", "matrix.send_message", nil)
	if r.Decision != policy.DecisionAllow {
		t.Errorf("expected Allow, got %s (violation: %v)", r.Decision, r.Violation)
	}
	if r.MatchedRule != "allow-matrix-send" {
		t.Errorf("unexpected matched rule: %q", r.MatchedRule)
	}
}

// TestEvaluate_BuiltinTool_DefaultDeny verifies that matrix.send_message is
// denied when no capability rule matches the (builtin, matrix.send_message)
// tuple — the engine's default-deny applies to built-in tools just like MCPs.
func TestEvaluate_BuiltinTool_DefaultDeny(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		// Rule exists for a different MCP server — matrix.send_message gets no match.
		{Name: "allow-browser-fetch", MCP: "browser", Tool: "fetch", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("builtin", "matrix.send_message", nil)
	if r.Decision != policy.DecisionDeny {
		t.Errorf("expected Deny (default), got %s", r.Decision)
	}
	if r.MatchedRule != "<default>" {
		t.Errorf("expected <default> rule, got %q", r.MatchedRule)
	}
}

// TestEvaluate_BuiltinTool_RequireApproval verifies that approval-gated
// messaging targets produce DecisionRequireApproval via the engine.
func TestEvaluate_BuiltinTool_RequireApproval(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "approve-matrix-send", MCP: "builtin", Tool: "matrix.send_message", Allow: true, RequireApproval: true},
	}, nil, nil)})

	r := e.Evaluate("builtin", "matrix.send_message", map[string]interface{}{"target": "user", "message": "hello"})
	if r.Decision != policy.DecisionRequireApproval {
		t.Errorf("expected RequireApproval, got %s", r.Decision)
	}
	if r.MatchedRule != "approve-matrix-send" {
		t.Errorf("unexpected matched rule: %q", r.MatchedRule)
	}
}

// TestEvaluate_BuiltinTool_WildcardAllowsMatrixSend verifies that a wildcard
// capability rule (mcp: builtin, tool: *) grants access to matrix.send_message.
func TestEvaluate_BuiltinTool_WildcardAllowsMatrixSend(t *testing.T) {
	e := policy.New(&staticProvider{cfg: cfg([]gosutospec.Capability{
		{Name: "allow-all-builtins", MCP: "builtin", Tool: "*", Allow: true},
	}, nil, nil)})

	r := e.Evaluate("builtin", "matrix.send_message", nil)
	if r.Decision != policy.DecisionAllow {
		t.Errorf("expected Allow from wildcard rule, got %s", r.Decision)
	}
}

// --- R15.3: IsMessagingConfigured tests ---

// TestIsMessagingConfigured_NoTargets verifies that an agent with no messaging
// section reports messaging as not configured.
func TestIsMessagingConfigured_NoTargets(t *testing.T) {
	e := policy.New(&staticProvider{cfg: &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Trust:      gosutospec.Trust{AllowedRooms: []string{"*"}, AllowedSenders: []string{"*"}},
		// No Messaging section.
	}})

	if e.IsMessagingConfigured() {
		t.Error("IsMessagingConfigured() = true, want false when no AllowedTargets")
	}
}

// TestIsMessagingConfigured_EmptyTargets verifies that an explicitly empty
// AllowedTargets slice also reports messaging as not configured.
func TestIsMessagingConfigured_EmptyTargets(t *testing.T) {
	e := policy.New(&staticProvider{cfg: &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Trust:      gosutospec.Trust{AllowedRooms: []string{"*"}, AllowedSenders: []string{"*"}},
		Messaging:  gosutospec.Messaging{AllowedTargets: []gosutospec.MessagingTarget{}},
	}})

	if e.IsMessagingConfigured() {
		t.Error("IsMessagingConfigured() = true, want false for empty AllowedTargets")
	}
}

// TestIsMessagingConfigured_WithTargets verifies that an agent with at least
// one configured target reports messaging as available.
func TestIsMessagingConfigured_WithTargets(t *testing.T) {
	e := policy.New(&staticProvider{cfg: &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Trust:      gosutospec.Trust{AllowedRooms: []string{"*"}, AllowedSenders: []string{"*"}},
		Messaging: gosutospec.Messaging{
			AllowedTargets: []gosutospec.MessagingTarget{
				{RoomID: "!kairo-admin:localhost", Alias: "kairo"},
			},
		},
	}})

	if !e.IsMessagingConfigured() {
		t.Error("IsMessagingConfigured() = false, want true when AllowedTargets is populated")
	}
}

// TestIsMessagingConfigured_NilConfig verifies that a nil config produces false.
func TestIsMessagingConfigured_NilConfig(t *testing.T) {
	e := policy.New(&staticProvider{cfg: nil})
	if e.IsMessagingConfigured() {
		t.Error("IsMessagingConfigured() = true with nil config, want false")
	}
}
