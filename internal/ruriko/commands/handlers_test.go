package commands_test

// handler_test.go — unit tests for command handlers (issue #19).
//
// These tests exercise the handler logic using real (in-memory) store and
// secrets instances so that the data-layer integration is also verified.
// No Matrix client or Docker runtime is required.

import (
	"context"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// --- helpers ---------------------------------------------------------------

func newHandlerFixture(t *testing.T) (*commands.Handlers, *appstore.Store, *secrets.Store) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-handlers-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:   s,
		Secrets: sec,
	})
	return h, s, sec
}

// fakeEvent builds a minimal Matrix event for use in handler calls.
func fakeEvent(sender string) *event.Event {
	return &event.Event{
		Sender: id.UserID(sender),
		RoomID: id.RoomID("!test:example.com"),
	}
}

// parseCmd parses a /ruriko command string, failing the test on error.
func parseCmd(t *testing.T, text string) *commands.Command {
	t.Helper()
	r := commands.NewRouter("/ruriko")
	cmd, err := r.Parse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return cmd
}

// --- HandleHelp ------------------------------------------------------------

func TestHandleHelp_ContainsKeywords(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko help")

	resp, err := h.HandleHelp(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleHelp: %v", err)
	}
	for _, want := range []string{"agents", "secrets", "audit", "trace", "version"} {
		if !strings.Contains(resp, want) {
			t.Errorf("help response missing keyword %q", want)
		}
	}
}

// --- HandlePing ------------------------------------------------------------

func TestHandlePing_ReturnsPong(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko ping")

	resp, err := h.HandlePing(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandlePing: %v", err)
	}
	if !strings.Contains(resp, "Pong") {
		t.Errorf("expected 'Pong' in response, got %q", resp)
	}
	// Response must embed a trace ID.
	if !strings.Contains(resp, "trace:") {
		t.Errorf("expected trace ID in ping response, got %q", resp)
	}
}

// --- HandleAgentsList ------------------------------------------------------

func TestHandleAgentsList_Empty(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko agents list")

	resp, err := h.HandleAgentsList(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleAgentsList empty: %v", err)
	}
	if !strings.Contains(resp, "No agents") {
		t.Errorf("expected 'No agents' message, got %q", resp)
	}
}

func TestHandleAgentsList_WithAgent(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID:          "mybot",
		DisplayName: "My Bot",
		Template:    "cron",
		Status:      "stopped",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	cmd := parseCmd(t, "/ruriko agents list")
	resp, err := h.HandleAgentsList(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleAgentsList: %v", err)
	}
	if !strings.Contains(resp, "mybot") {
		t.Errorf("expected agent 'mybot' in list response, got %q", resp)
	}
}

// --- HandleAgentsShow ------------------------------------------------------

func TestHandleAgentsShow_NotFound(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko agents show noexist")

	_, err := h.HandleAgentsShow(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
}

func TestHandleAgentsShow_Found(t *testing.T) {
	h, s, _ := newHandlerFixture(t)
	ctx := context.Background()

	if err := s.CreateAgent(ctx, &appstore.Agent{
		ID:          "researchbot",
		DisplayName: "Research Bot",
		Template:    "research-agent",
		Status:      "running",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	cmd := parseCmd(t, "/ruriko agents show researchbot")
	resp, err := h.HandleAgentsShow(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleAgentsShow: %v", err)
	}
	if !strings.Contains(resp, "researchbot") {
		t.Errorf("expected agent ID in show response, got %q", resp)
	}
	if !strings.Contains(resp, "research-agent") {
		t.Errorf("expected template in show response, got %q", resp)
	}
}

// --- HandleAgentsCreate (no runtime) ---------------------------------------

func TestHandleAgentsCreate_MissingFlags(t *testing.T) {
	h, _, _ := newHandlerFixture(t)

	cases := []string{
		"/ruriko agents create",
		"/ruriko agents create --name mybot",
		"/ruriko agents create --name mybot --template cron",
	}
	for _, input := range cases {
		cmd := parseCmd(t, input)
		_, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@alice:example.com"))
		if err == nil {
			t.Errorf("expected error for %q, got nil", input)
		}
	}
}

func TestHandleAgentsCreate_InvalidID(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko agents create --name UPPER --template cron --image img:latest")

	_, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for invalid agent ID, got nil")
	}
}

func TestHandleAgentsCreate_NoRuntime(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko agents create --name mybot --template cron --image img:latest")

	resp, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleAgentsCreate (no runtime): %v", err)
	}
	if !strings.Contains(resp, "mybot") {
		t.Errorf("expected agent ID in create response, got %q", resp)
	}
}

func TestHandleAgentsCreate_Duplicate(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	ctx := context.Background()
	cmd := parseCmd(t, "/ruriko agents create --name mybot --template cron --image img:latest")

	if _, err := h.HandleAgentsCreate(ctx, cmd, fakeEvent("@alice:example.com")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := h.HandleAgentsCreate(ctx, cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error on duplicate create, got nil")
	}
}

// --- HandleSecretsList -----------------------------------------------------

func TestHandleSecretsList_Empty(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko secrets list")

	resp, err := h.HandleSecretsList(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleSecretsList empty: %v", err)
	}
	if !strings.Contains(resp, "No secrets") {
		t.Errorf("expected 'No secrets' message, got %q", resp)
	}
}

func TestHandleSecretsList_WithSecret(t *testing.T) {
	h, _, sec := newHandlerFixture(t)
	ctx := context.Background()

	if err := sec.Set(ctx, "api-key-1", secrets.TypeAPIKey, []byte("sk-test")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	cmd := parseCmd(t, "/ruriko secrets list")
	resp, err := h.HandleSecretsList(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleSecretsList: %v", err)
	}
	if !strings.Contains(resp, "api-key-1") {
		t.Errorf("expected secret name in list response, got %q", resp)
	}
}

// --- HandleSecretsSet ------------------------------------------------------

func TestHandleSecretsSet_MissingFlags(t *testing.T) {
	h, _, _ := newHandlerFixture(t)

	cases := []string{
		"/ruriko secrets set mykey",
	}
	for _, input := range cases {
		cmd := parseCmd(t, input)
		_, err := h.HandleSecretsSet(context.Background(), cmd, fakeEvent("@alice:example.com"))
		if err == nil {
			t.Errorf("expected error for %q, got nil", input)
		}
	}
}

func TestHandleSecretsSet_RequiresKuze(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko secrets set mykey --type api_key")

	_, err := h.HandleSecretsSet(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error when Kuze is not configured, got nil")
	}
	if !strings.Contains(err.Error(), "requires Kuze") {
		t.Fatalf("expected Kuze requirement error, got %v", err)
	}
}

func TestHandleSecretsSet_InvalidType(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko secrets set mykey --type bad_type")

	_, err := h.HandleSecretsSet(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
}

func TestHandleSecretsSet_DoesNotAcceptInlineValue(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko secrets set mykey --type api_key --value c2stdGVzdA==")

	_, err := h.HandleSecretsSet(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected refusal when --value is provided, got nil")
	}
	if !strings.Contains(err.Error(), "requires Kuze") {
		t.Fatalf("expected Kuze requirement error, got %v", err)
	}
}

// --- HandleSecretsInfo -----------------------------------------------------

func TestHandleSecretsInfo_NotFound(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko secrets info noexist")

	_, err := h.HandleSecretsInfo(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected error for unknown secret, got nil")
	}
}

func TestHandleSecretsInfo_Found(t *testing.T) {
	h, _, sec := newHandlerFixture(t)
	ctx := context.Background()

	if err := sec.Set(ctx, "tok", secrets.TypeMatrixToken, []byte("mxtoken")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	cmd := parseCmd(t, "/ruriko secrets info tok")
	resp, err := h.HandleSecretsInfo(ctx, cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleSecretsInfo: %v", err)
	}
	if !strings.Contains(resp, "tok") {
		t.Errorf("expected secret name in info response, got %q", resp)
	}
	if !strings.Contains(resp, "matrix_token") {
		t.Errorf("expected type in info response, got %q", resp)
	}
}

// --- HandleAuditTail -------------------------------------------------------

func TestHandleAuditTail_Empty(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko audit tail")

	resp, err := h.HandleAuditTail(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err != nil {
		t.Fatalf("HandleAuditTail: %v", err)
	}
	// Should return the header even when the audit log is empty.
	if !strings.Contains(resp, "Audit Entries") {
		t.Errorf("expected audit header in response, got %q", resp)
	}
}
