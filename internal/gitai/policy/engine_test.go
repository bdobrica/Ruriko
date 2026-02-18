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
