// Package provisioning contains white-box tests for unexported helpers
// (usernameForAgent, mxidForAgent).  The tests intentionally use
// `package provisioning` rather than `package provisioning_test` so they
// can directly exercise internal sanitisation logic without exporting it.
// This deviates from the project's general _test-package convention, which
// is an accepted trade-off for internal unit tests.
package provisioning

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

// newTestProvisioner creates a Provisioner with a minimal config for unit
// testing helper methods.  It does NOT connect to a real homeserver.
func newTestProvisioner(t *testing.T, opts ...func(*Config)) *Provisioner {
	t.Helper()

	cfg := Config{
		Homeserver:       "https://matrix.example.com",
		AdminUserID:      "@admin:example.com",
		AdminAccessToken: "test-token",
		HomeserverType:   HomeserverSynapse,
		SharedSecret:     "test-secret",
	}
	for _, o := range opts {
		o(&cfg)
	}

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("newTestProvisioner: %v", err)
	}
	return p
}

// --- usernameForAgent tests ---

func TestUsernameForAgent_Simple(t *testing.T) {
	p := newTestProvisioner(t)
	got, err := p.usernameForAgent("mybot")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "mybot" {
		t.Errorf("usernameForAgent(\"mybot\"): got %q, want %q", got, "mybot")
	}
}

func TestUsernameForAgent_LowerCase(t *testing.T) {
	p := newTestProvisioner(t)
	got, err := p.usernameForAgent("MyBot")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "mybot" {
		t.Errorf("usernameForAgent(\"MyBot\"): got %q, want %q", got, "mybot")
	}
}

func TestUsernameForAgent_UnderscoreToHyphen(t *testing.T) {
	p := newTestProvisioner(t)
	got, err := p.usernameForAgent("my_bot_agent")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "my-bot-agent" {
		t.Errorf("usernameForAgent(\"my_bot_agent\"): got %q, want %q", got, "my-bot-agent")
	}
}

func TestUsernameForAgent_StripsInvalidChars(t *testing.T) {
	p := newTestProvisioner(t)
	got, err := p.usernameForAgent("hello world! @#$%")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	// After lower-casing:  "hello world! @#$%"
	// After underscore→hyphen: same (no underscores)
	// After stripping invalid: "helloworld"  (spaces and special chars removed)
	if got != "helloworld" {
		t.Errorf("usernameForAgent(\"hello world! @#$%%\"): got %q, want %q", got, "helloworld")
	}
}

func TestUsernameForAgent_PreservesValidChars(t *testing.T) {
	p := newTestProvisioner(t)
	// Dots, hyphens, slashes are valid localpart characters.
	got, err := p.usernameForAgent("agent.v2-beta/test")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "agent.v2-beta/test" {
		t.Errorf("usernameForAgent(\"agent.v2-beta/test\"): got %q, want %q", got, "agent.v2-beta/test")
	}
}

func TestUsernameForAgent_WithSuffix(t *testing.T) {
	p := newTestProvisioner(t, func(c *Config) {
		c.UsernameSuffix = "-agent"
	})
	got, err := p.usernameForAgent("mybot")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "mybot-agent" {
		t.Errorf("usernameForAgent with suffix: got %q, want %q", got, "mybot-agent")
	}
}

func TestUsernameForAgent_UpperCaseWithSuffix(t *testing.T) {
	p := newTestProvisioner(t, func(c *Config) {
		c.UsernameSuffix = "-agent"
	})
	got, err := p.usernameForAgent("MyBot")
	if err != nil {
		t.Fatalf("usernameForAgent: %v", err)
	}
	if got != "mybot-agent" {
		t.Errorf("got %q, want %q", got, "mybot-agent")
	}
}

func TestUsernameForAgent_AllInvalidCharsReturnsError(t *testing.T) {
	p := newTestProvisioner(t)
	// A name made entirely of characters outside [a-z0-9._\-/] should
	// produce an empty localpart after sanitisation, which must be an error.
	_, err := p.usernameForAgent("!!! @@@")
	if err == nil {
		t.Fatal("expected error for all-invalid-char agent name, got nil")
	}
}

// --- mxidForAgent tests ---

func TestMxidForAgent_Basic(t *testing.T) {
	p := newTestProvisioner(t)
	got, err := p.mxidForAgent("mybot")
	if err != nil {
		t.Fatalf("mxidForAgent: %v", err)
	}
	want := id.UserID("@mybot:example.com")
	if got != want {
		t.Errorf("mxidForAgent(\"mybot\"): got %q, want %q", got, want)
	}
}

func TestMxidForAgent_LowerCaseAndSuffix(t *testing.T) {
	p := newTestProvisioner(t, func(c *Config) {
		c.UsernameSuffix = "-bot"
	})
	got, err := p.mxidForAgent("MyAgent")
	if err != nil {
		t.Fatalf("mxidForAgent: %v", err)
	}
	want := id.UserID("@myagent-bot:example.com")
	if got != want {
		t.Errorf("mxidForAgent(\"MyAgent\"): got %q, want %q", got, want)
	}
}

func TestMxidForAgent_InvalidAdminUserID(t *testing.T) {
	cfg := Config{
		Homeserver:       "https://matrix.example.com",
		AdminUserID:      "bad-admin-id-no-colon",
		AdminAccessToken: "test-token",
		HomeserverType:   HomeserverGeneric,
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.mxidForAgent("mybot")
	if err == nil {
		t.Fatal("expected error for invalid AdminUserID without colon")
	}
}

// --- New() validation tests ---

func TestNew_MissingHomeserver(t *testing.T) {
	_, err := New(Config{
		AdminUserID:      "@admin:example.com",
		AdminAccessToken: "tok",
		SharedSecret:     "sec",
	})
	if err == nil {
		t.Fatal("expected error for missing Homeserver")
	}
}

func TestNew_MissingAdminUserID(t *testing.T) {
	_, err := New(Config{
		Homeserver:       "https://matrix.example.com",
		AdminAccessToken: "tok",
		SharedSecret:     "sec",
	})
	if err == nil {
		t.Fatal("expected error for missing AdminUserID")
	}
}

func TestNew_MissingAdminAccessToken(t *testing.T) {
	_, err := New(Config{
		Homeserver:  "https://matrix.example.com",
		AdminUserID: "@admin:example.com",
	})
	if err == nil {
		t.Fatal("expected error for missing AdminAccessToken")
	}
}

func TestNew_SynapseRequiresSharedSecret(t *testing.T) {
	_, err := New(Config{
		Homeserver:       "https://matrix.example.com",
		AdminUserID:      "@admin:example.com",
		AdminAccessToken: "tok",
		HomeserverType:   HomeserverSynapse,
		SharedSecret:     "",
	})
	if err == nil {
		t.Fatal("expected error for synapse type without shared secret")
	}
}

func TestNew_DefaultsToSynapse(t *testing.T) {
	// HomeserverType == "" should default to "synapse", which then requires SharedSecret.
	_, err := New(Config{
		Homeserver:       "https://matrix.example.com",
		AdminUserID:      "@admin:example.com",
		AdminAccessToken: "tok",
		SharedSecret:     "sec",
	})
	if err != nil {
		t.Fatalf("expected success with default homeserver type: %v", err)
	}
}

func TestNew_GenericDoesNotRequireSharedSecret(t *testing.T) {
	_, err := New(Config{
		Homeserver:       "https://matrix.example.com",
		AdminUserID:      "@admin:example.com",
		AdminAccessToken: "tok",
		HomeserverType:   HomeserverGeneric,
	})
	if err != nil {
		t.Fatalf("generic type should not require shared secret: %v", err)
	}
}
