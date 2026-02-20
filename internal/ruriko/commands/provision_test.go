package commands
package commands_test

// provision_test.go — integration tests for the R5.2 automated provisioning
// pipeline (HandleAgentsCreate → runProvisioningPipeline).
//
// The tests use:
//  - A real (in-memory SQLite) store instance
//  - A stubbed runtime.Runtime that immediately returns "running"
//  - An httptest server acting as the Gitai ACP endpoint
//  - A testing/fstest.MapFS carrying a minimal Gosuto template
//  - A channel-backed RoomSender to capture breadcrumb notices

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// --- stubs ------------------------------------------------------------------

// stubRuntime implements runtime.Runtime. Spawn always returns a handle that
// points to the provided controlURL; all other methods are no-ops.
type stubRuntime struct {
	controlURL string
}

func (s *stubRuntime) Spawn(_ context.Context, spec runtime.AgentSpec) (runtime.AgentHandle, error) {
	return runtime.AgentHandle{
		AgentID:     spec.ID,
		ContainerID: "stubcontainer123",
		ControlURL:  s.controlURL,
	}, nil
}
func (s *stubRuntime) Stop(_ context.Context, _ runtime.AgentHandle) error     { return nil }
func (s *stubRuntime) Start(_ context.Context, _ runtime.AgentHandle) error    { return nil }
func (s *stubRuntime) Restart(_ context.Context, _ runtime.AgentHandle) error  { return nil }
func (s *stubRuntime) Remove(_ context.Context, _ runtime.AgentHandle) error   { return nil }
func (s *stubRuntime) List(_ context.Context) ([]runtime.AgentHandle, error)   { return nil, nil }
func (s *stubRuntime) Status(_ context.Context, _ runtime.AgentHandle) (runtime.RuntimeStatus, error) {
	return runtime.RuntimeStatus{State: runtime.StateRunning}, nil
}

// capturingSender records all SendNotice calls.
type capturingSender struct {
	mu   sync.Mutex
	msgs []string
}

func (c *capturingSender) SendNotice(roomID, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, message)
	return nil
}

func (c *capturingSender) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]string, len(c.msgs))
	copy(cp, c.msgs)
	return cp
}

// --- mock ACP server --------------------------------------------------------

// mockACPServer provides minimal /health, /config/apply, and /status
// endpoints. It captures the hash from /config/apply so that /status can
// return it, simulating a real Gitai agent.
type mockACPServer struct {
	mu          sync.Mutex
	appliedHash string
	srv         *httptest.Server
}

func newMockACPServer() *mockACPServer {
	m := &mockACPServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "agent_id": "testbot"})
	})

	mux.HandleFunc("/config/apply", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			YAML string `json:"yaml"`
			Hash string `json:"hash"`
		}
		_ = json.Unmarshal(body, &req)
		m.mu.Lock()
		m.appliedHash = req.Hash
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		h := m.appliedHash
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"agent_id":       "testbot",
			"version":        "dev",
			"gosuto_hash":    h,
			"uptime_seconds": 1.0,
			"started_at":     time.Now(),
			"mcps":           []string{},
		})
	})

	// /secrets/token is a no-op — the test uses no distributor.
	mux.HandleFunc("/secrets/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	m.srv = httptest.NewServer(mux)
	return m
}

// --- test template ----------------------------------------------------------

// minimalGosutoYAML is a small valid Gosuto config used as the test template.
const minimalGosutoYAML = `version: "1"
agent_id: "{{.AgentName}}"
trust:
  rooms: ["!admin:example.com"]
  senders: ["{{.OperatorMXID}}"]
limits:
  max_tokens_per_day: 1000
capabilities: []
`

func newTestTemplateRegistry(t *testing.T) *templates.Registry {
	t.Helper()
	fs := fstest.MapFS{
		"test-template/gosuto.yaml": &fstest.MapFile{Data: []byte(minimalGosutoYAML)},
	}
	return templates.NewRegistry(fs)
}

// --- fixture ----------------------------------------------------------------

func newProvisionFixture(t *testing.T, acp *mockACPServer) (
	*commands.Handlers, *appstore.Store, *capturingSender,
) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-provision-test-*.db")
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

	sender := &capturingSender{}
	rt := &stubRuntime{controlURL: acp.srv.URL}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    rt,
		Templates:  newTestTemplateRegistry(t),
		RoomSender: sender,
	})
	return h, s, sender
}

// waitFor polls fn every 50 ms until it returns true or the deadline elapses.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// --- test: HandleAgentsCreate starts pipeline and reaches healthy -----------

func TestProvisioningPipeline_FullSuccess(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	h, s, sender := newProvisionFixture(t, acp)

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name testbot --template test-template --image gitai:test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	evt := &struct{ Sender id.UserID }{Sender: "@admin:example.com"}
	_ = evt

	resp, createErr := h.HandleAgentsCreate(
		context.Background(), cmd,
		fakeEvent("@admin:example.com"),
	)
	if createErr != nil {
		t.Fatalf("HandleAgentsCreate: %v", createErr)
	}
	if !strings.Contains(resp, "testbot") {
		t.Errorf("expected agent name in response, got %q", resp)
	}
	if !strings.Contains(resp, "pipeline started") {
		t.Errorf("expected 'pipeline started' in response, got %q", resp)
	}

	// Wait for the provisioning pipeline to reach "healthy".
	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "testbot")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	// Verify final agent state.
	agent, err := s.GetAgent(context.Background(), "testbot")
	if err != nil {
		t.Fatalf("GetAgent after pipeline: %v", err)
	}
	if agent.Status != "running" {
		t.Errorf("expected status=running, got %q", agent.Status)
	}
	if agent.ProvisioningState != "healthy" {
		t.Errorf("expected provisioning_state=healthy, got %q", agent.ProvisioningState)
	}
	if !agent.GosutoVersion.Valid || agent.GosutoVersion.Int64 < 1 {
		t.Errorf("expected gosuto_version >= 1, got %v", agent.GosutoVersion)
	}

	// Verify that breadcrumbs were posted.
	msgs := sender.messages()
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"[1/5]", "[2/5]", "[3/5]", "[4/5]", "healthy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected breadcrumb %q in notices; got:\n%s", want, joined)
		}
	}

	// The Gosuto version stored in the DB should have a hash matching the
	// rendered template for agent "testbot".
	rendered := strings.ReplaceAll(minimalGosutoYAML, "{{.AgentName}}", "testbot")
	rendered = strings.ReplaceAll(rendered, "{{.OperatorMXID}}", "@admin:example.com")
	sum := sha256.Sum256([]byte(rendered))
	expectedHash := fmt.Sprintf("%x", sum)

	gv, err := s.GetLatestGosutoVersion(context.Background(), "testbot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if gv.Hash != expectedHash {
		t.Errorf("gosuto version hash mismatch: got %q, want %q", gv.Hash, expectedHash)
	}
}

// --- test: HandleAgentsCreate stores provisioning_state=pending initially --

func TestProvisioningPipeline_InitialStatePending(t *testing.T) {
	// Use a mock ACP server that hangs on /health so the pipeline stalls after
	// the container wait step — letting us observe provisioning_state=creating.
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// Hang until the test's client context is cancelled.
			<-r.Context().Done()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hangSrv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-init-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	masterKey := make([]byte, 32)
	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    &stubRuntime{controlURL: hangSrv.URL},
		Templates:  newTestTemplateRegistry(t),
		RoomSender: &capturingSender{},
	})

	router := commands.NewRouter("/ruriko")
	cmd, _ := router.Parse("/ruriko agents create --name stalled --template test-template --image gitai:test")
	if _, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleAgentsCreate: %v", err)
	}

	// Immediately after the handler returns the state should be "pending" or
	// "creating" — the pipeline is async and we haven't waited.
	agent, err := s.GetAgent(context.Background(), "stalled")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.ProvisioningState != "pending" && agent.ProvisioningState != "creating" {
		t.Errorf("expected pending or creating immediately after create, got %q", agent.ProvisioningState)
	}
}

// --- test: HandleAgentsCreate without template registry → legacy path ------

func TestHandleAgentsCreate_NoTemplateRegistry_LegacyPath(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-legacy-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	masterKey := make([]byte, 32)
	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:   s,
		Secrets: sec,
		Runtime: &stubRuntime{controlURL: acp.srv.URL},
		// No Templates → legacy path.
	})

	router := commands.NewRouter("/ruriko")
	cmd, _ := router.Parse("/ruriko agents create --name legacybot --template cron --image gitai:test")
	resp, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleAgentsCreate (legacy): %v", err)
	}
	// Should still succeed (legacy path; no pipeline).
	if !strings.Contains(resp, "legacybot") {
		t.Errorf("expected agent name in legacy response, got %q", resp)
	}
	// Should NOT say "pipeline started".
	if strings.Contains(resp, "pipeline started") {
		t.Errorf("unexpected 'pipeline started' in legacy response: %q", resp)
	}
}
