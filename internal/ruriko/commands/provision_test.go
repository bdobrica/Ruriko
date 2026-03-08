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

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
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
func (s *stubRuntime) Stop(_ context.Context, _ runtime.AgentHandle) error    { return nil }
func (s *stubRuntime) Start(_ context.Context, _ runtime.AgentHandle) error   { return nil }
func (s *stubRuntime) Restart(_ context.Context, _ runtime.AgentHandle) error { return nil }
func (s *stubRuntime) Remove(_ context.Context, _ runtime.AgentHandle) error  { return nil }
func (s *stubRuntime) List(_ context.Context) ([]runtime.AgentHandle, error)  { return nil, nil }
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
// return it, simulating a real Gitai agent. The gateways field may be
// pre-seeded (via setGateways) to simulate a running gateway supervisor.
type mockACPServer struct {
	mu          sync.Mutex
	appliedHash string
	gateways    []string
	srv         *httptest.Server
}

func (m *mockACPServer) setGateways(names []string) {
	m.mu.Lock()
	m.gateways = names
	m.mu.Unlock()
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
		gwCopy := append([]string{}, m.gateways...)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"agent_id":       "testbot",
			"version":        "dev",
			"gosuto_hash":    h,
			"uptime_seconds": 1.0,
			"started_at":     time.Now(),
			"mcps":           []string{},
			"gateways":       gwCopy,
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

// gatewayGosutoYAML is a Gosuto config that declares a webhook gateway with an
// HMAC secret reference.  Used by TestProvisioningPipeline_GatewayBearing to
// verify that gateway awareness is handled correctly during provisioning (R13.2).
const gatewayGosutoYAML = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
trust:
  allowedRooms: ["!admin:example.com"]
  allowedSenders: ["{{.OperatorMXID}}"]
gateways:
  - name: my-webhook
    type: webhook
    config:
      authType: hmac-sha256
      hmacSecretRef: "{{.AgentName}}.webhook-secret"
`

const peerAwareGosutoYAML = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: peer-template
trust:
  allowedRooms: ["!admin:example.com"]
  allowedSenders: ["{{.OperatorMXID}}"]
  trustedPeers:
    - mxid: "{{.PeerMXID}}"
      roomId: "{{.PeerRoom}}"
      alias: "{{.PeerAlias}}"
      protocols:
        - "{{.PeerProtocolID}}"
workflow:
  protocols:
    - id: "{{.PeerProtocolID}}"
      trigger:
        type: matrix.protocol_message
        prefix: "{{.PeerProtocolPrefix}}"
      steps: []
`

const kumoEnsureBootstrapYAML = "apiVersion: gosuto/v1\n" +
	"metadata:\n" +
	"  name: \"{{.AgentName}}\"\n" +
	"trust:\n" +
	"  allowedRooms: [\"!admin:example.com\"]\n" +
	"  allowedSenders: [\"{{.OperatorMXID}}\"]\n" +
	"  adminRoom: \"!admin:example.com\"\n"

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

// --- test: gateway-bearing Gosuto config is handled correctly (R13.2) ------

// TestProvisioningPipeline_GatewayBearing verifies that when the applied Gosuto
// config declares a gateway process and the mock agent's /status already lists
// that gateway as running, the provisioning pipeline:
//
//  1. Completes successfully (reaches "healthy").
//  2. Posts a breadcrumb that names the gateway.
//  3. Posts a breadcrumb that names the gateway secret reference discovered in
//     the Gosuto config (hmacSecretRef).
func TestProvisioningPipeline_GatewayBearing(t *testing.T) {
	acpSrv := newMockACPServer()
	defer acpSrv.srv.Close()

	// Pre-seed the running gateway list so /status always includes it from the
	// moment config is applied (simulates an agent that starts gateways quickly).
	acpSrv.setGateways([]string{"my-webhook"})

	f, err := os.CreateTemp(t.TempDir(), "ruriko-gw-test-*.db")
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

	// Build a template registry that carries the gateway-bearing Gosuto template.
	gwFS := fstest.MapFS{
		"gw-template/gosuto.yaml": &fstest.MapFile{Data: []byte(gatewayGosutoYAML)},
	}
	tmplReg := templates.NewRegistry(gwFS)

	sender := &capturingSender{}
	rt := &stubRuntime{controlURL: acpSrv.srv.URL}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    rt,
		Templates:  tmplReg,
		RoomSender: sender,
	})

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name gwbot --template gw-template --image gitai:test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	resp, createErr := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if createErr != nil {
		t.Fatalf("HandleAgentsCreate: %v", createErr)
	}
	if !strings.Contains(resp, "gwbot") {
		t.Errorf("expected agent name in response, got %q", resp)
	}

	// Wait for the provisioning pipeline to reach "healthy".
	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "gwbot")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	agent, err := s.GetAgent(context.Background(), "gwbot")
	if err != nil {
		t.Fatalf("GetAgent after pipeline: %v", err)
	}
	if agent.Status != "running" {
		t.Errorf("expected status=running, got %q", agent.Status)
	}
	if agent.ProvisioningState != "healthy" {
		t.Errorf("expected provisioning_state=healthy, got %q", agent.ProvisioningState)
	}

	// Breadcrumbs must mention the gateway name and the gateway secret ref.
	msgs := sender.messages()
	joined := strings.Join(msgs, "\n")

	if !strings.Contains(joined, "my-webhook") {
		t.Errorf("expected gateway name 'my-webhook' in breadcrumbs:\n%s", joined)
	}
	// The hmacSecretRef is "gwbot.webhook-secret" after template rendering.
	if !strings.Contains(joined, "gwbot.webhook-secret") {
		t.Errorf("expected gateway secret ref 'gwbot.webhook-secret' in breadcrumbs:\n%s", joined)
	}
}

func TestProvisioningPipeline_PeerTopologyFlags_NonKairoWiring(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-peer-test-*.db")
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

	peerFS := fstest.MapFS{
		"peer-template/gosuto.yaml": &fstest.MapFile{Data: []byte(peerAwareGosutoYAML)},
	}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    &stubRuntime{controlURL: acp.srv.URL},
		Templates:  templates.NewRegistry(peerFS),
		RoomSender: &capturingSender{},
		AdminRooms: []string{"!admin:example.com"},
	})

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name atlasbot --template peer-template --image gitai:test --peer-alias atlas --peer-mxid @atlas:example.com --peer-room !atlas-room:example.com --peer-protocol-id atlas.news.request.v1 --peer-protocol-prefix ATLAS_NEWS_REQUEST")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleAgentsCreate: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "atlasbot")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	gv, err := s.GetLatestGosutoVersion(context.Background(), "atlasbot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}

	cfg, err := gosutospec.Parse([]byte(gv.YAMLBlob))
	if err != nil {
		t.Fatalf("gosuto.Parse: %v", err)
	}

	if len(cfg.Trust.TrustedPeers) != 1 {
		t.Fatalf("trustedPeers count = %d, want 1", len(cfg.Trust.TrustedPeers))
	}
	peer := cfg.Trust.TrustedPeers[0]
	if peer.Alias != "atlas" {
		t.Fatalf("trusted peer alias = %q, want atlas", peer.Alias)
	}
	if peer.MXID != "@atlas:example.com" {
		t.Fatalf("trusted peer mxid = %q, want @atlas:example.com", peer.MXID)
	}
	if peer.RoomID != "!atlas-room:example.com" {
		t.Fatalf("trusted peer roomId = %q, want !atlas-room:example.com", peer.RoomID)
	}
	if len(peer.Protocols) != 1 || peer.Protocols[0] != "atlas.news.request.v1" {
		t.Fatalf("trusted peer protocols = %#v, want [atlas.news.request.v1]", peer.Protocols)
	}

	if len(cfg.Workflow.Protocols) != 1 {
		t.Fatalf("workflow protocols count = %d, want 1", len(cfg.Workflow.Protocols))
	}
	if cfg.Workflow.Protocols[0].ID != "atlas.news.request.v1" {
		t.Fatalf("workflow protocol id = %q, want atlas.news.request.v1", cfg.Workflow.Protocols[0].ID)
	}
	if cfg.Workflow.Protocols[0].Trigger.Prefix != "ATLAS_NEWS_REQUEST" {
		t.Fatalf("workflow protocol prefix = %q, want ATLAS_NEWS_REQUEST", cfg.Workflow.Protocols[0].Trigger.Prefix)
	}
}

func TestHandleAgentsCreate_InvalidPeerMXIDRejected(t *testing.T) {
	h, _, _ := newHandlerFixture(t)
	cmd := parseCmd(t, "/ruriko agents create --name atlasbot --template cron --image img:latest --peer-mxid atlas:example.com")

	_, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@alice:example.com"))
	if err == nil {
		t.Fatal("expected invalid --peer-mxid error, got nil")
	}
	if !strings.Contains(err.Error(), "--peer-mxid must start with '@'") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProvisioningPipeline_CanonicalKumoRunsEnsureIfMissing(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-kumo-ensure-test-*.db")
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

	// Seed canonical peer so post-provision ensure can resolve kairo admin room.
	if err := s.CreateAgent(context.Background(), &appstore.Agent{ID: "kairo", DisplayName: "kairo", Template: "kairo-agent", Status: "running"}); err != nil {
		t.Fatalf("CreateAgent(kairo): %v", err)
	}
	kairoCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kairo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kairo-admin:localhost",
		},
	}
	kairoRaw, err := yaml.Marshal(&kairoCfg)
	if err != nil {
		t.Fatalf("marshal kairo cfg: %v", err)
	}
	if err := s.CreateGosutoVersion(context.Background(), &appstore.GosutoVersion{
		AgentID:       "kairo",
		Version:       1,
		Hash:          "seed-hash-kairo",
		YAMLBlob:      string(kairoRaw),
		CreatedByMXID: "@admin:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion(kairo): %v", err)
	}

	kumoFS := fstest.MapFS{
		"kumo-agent/gosuto.yaml": &fstest.MapFile{Data: []byte(kumoEnsureBootstrapYAML)},
	}

	sender := &capturingSender{}
	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    &stubRuntime{controlURL: acp.srv.URL},
		Templates:  templates.NewRegistry(kumoFS),
		RoomSender: sender,
		AdminRooms: []string{"!admin:example.com"},
	})

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name kumo --template kumo-agent --image gitai:test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleAgentsCreate: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "kumo")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	gv, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion(kumo): %v", err)
	}
	if gv.Version < 2 {
		t.Fatalf("expected post-provision ensure to create follow-up version, got v%d", gv.Version)
	}

	cfg, err := gosutospec.Parse([]byte(gv.YAMLBlob))
	if err != nil {
		t.Fatalf("gosuto.Parse(kumo): %v", err)
	}

	if len(cfg.Trust.TrustedPeers) != 1 {
		t.Fatalf("trustedPeers count = %d, want 1", len(cfg.Trust.TrustedPeers))
	}
	peer := cfg.Trust.TrustedPeers[0]
	if peer.Alias != "kairo" || peer.MXID != "@kairo:localhost" || peer.RoomID != "!kairo-admin:localhost" {
		t.Fatalf("unexpected trusted peer from ensure flow: %+v", peer)
	}
	if len(peer.Protocols) != 1 || peer.Protocols[0] != "kairo.news.request.v1" {
		t.Fatalf("unexpected ensured peer protocols: %+v", peer.Protocols)
	}

	foundKairoTarget := false
	for _, target := range cfg.Messaging.AllowedTargets {
		if target.Alias == "kairo" && target.RoomID == "!kairo-admin:localhost" {
			foundKairoTarget = true
			break
		}
	}
	if !foundKairoTarget {
		t.Fatalf("expected ensured kairo messaging target in %+v", cfg.Messaging.AllowedTargets)
	}

	joined := strings.Join(sender.messages(), "\n")
	if !strings.Contains(joined, "Post-provision ensure") {
		t.Fatalf("expected post-provision ensure breadcrumb, got:\n%s", joined)
	}
}

func TestProvisioningPipeline_CanonicalKumoEnsure_ApprovalDecisionReplay(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-kumo-ensure-approval-test-*.db")
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

	if err := s.CreateAgent(context.Background(), &appstore.Agent{ID: "kairo", DisplayName: "kairo", Template: "kairo-agent", Status: "running"}); err != nil {
		t.Fatalf("CreateAgent(kairo): %v", err)
	}
	kairoCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kairo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kairo-admin:localhost",
		},
	}
	kairoRaw, err := yaml.Marshal(&kairoCfg)
	if err != nil {
		t.Fatalf("marshal kairo cfg: %v", err)
	}
	if err := s.CreateGosutoVersion(context.Background(), &appstore.GosutoVersion{
		AgentID:       "kairo",
		Version:       1,
		Hash:          "seed-hash-kairo",
		YAMLBlob:      string(kairoRaw),
		CreatedByMXID: "@admin:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion(kairo): %v", err)
	}

	kumoFS := fstest.MapFS{
		"kumo-agent/gosuto.yaml": &fstest.MapFile{Data: []byte(kumoEnsureBootstrapYAML)},
	}

	sender := &capturingSender{}
	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    &stubRuntime{controlURL: acp.srv.URL},
		Templates:  templates.NewRegistry(kumoFS),
		RoomSender: sender,
		AdminRooms: []string{"!admin:example.com"},
		Approvals:  approvals.NewGate(approvals.NewStore(s.DB()), time.Hour),
	})

	h.SetDispatch(func(ctx context.Context, action string, cmd *commands.Command, evt *event.Event) (string, error) {
		switch action {
		case "topology.peer-ensure":
			return h.HandleTopologyPeerEnsure(ctx, cmd, evt)
		default:
			return "", fmt.Errorf("unsupported action in test dispatch: %s", action)
		}
	})

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name kumo --template kumo-agent --image gitai:test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleAgentsCreate: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "kumo")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	approvalStore := approvals.NewStore(s.DB())
	waitFor(t, 10*time.Second, func() bool {
		pending, err := approvalStore.List(context.Background(), string(approvals.StatusPending))
		if err != nil {
			return false
		}
		return len(pending) == 1 && pending[0].Action == "topology.peer-ensure"
	})

	before, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion(before approval): %v", err)
	}
	if before.Version != 1 {
		t.Fatalf("expected v1 before approval replay, got v%d", before.Version)
	}

	pending, err := approvalStore.List(context.Background(), string(approvals.StatusPending))
	if err != nil {
		t.Fatalf("approval list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	decisionResp, err := h.HandleApprovalDecision(context.Background(), "approve "+pending[0].ID, fakeEvent("@reviewer:example.com"))
	if err != nil {
		t.Fatalf("HandleApprovalDecision: %v", err)
	}
	if !strings.Contains(decisionResp, "Approved by") {
		t.Fatalf("expected approval decision response, got: %s", decisionResp)
	}
	if !strings.Contains(decisionResp, "Gosuto v2 pushed to running agent") {
		t.Fatalf("expected push confirmation after approval decision replay, got: %s", decisionResp)
	}

	after, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion(after approval): %v", err)
	}
	if after.Version != 2 {
		t.Fatalf("expected v2 after approval replay, got v%d", after.Version)
	}

	approved, err := approvalStore.Get(context.Background(), pending[0].ID)
	if err != nil {
		t.Fatalf("approval get: %v", err)
	}
	if approved.Status != approvals.StatusApproved {
		t.Fatalf("expected approval status approved, got %s", approved.Status)
	}

	acp.mu.Lock()
	appliedHash := acp.appliedHash
	acp.mu.Unlock()
	if appliedHash != after.Hash {
		t.Fatalf("expected ACP applied hash %q, got %q", after.Hash, appliedHash)
	}

	joined := strings.Join(sender.messages(), "\n")
	if !strings.Contains(joined, "Approval required") {
		t.Fatalf("expected approval-required breadcrumb from canonical ensure, got:\n%s", joined)
	}
}

func TestProvisioningPipeline_CanonicalKumoEnsure_ResolvesNonDefaultPeerAliasRoom(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-kumo-ensure-nondefault-peer-room-test-*.db")
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

	if err := s.CreateAgent(context.Background(), &appstore.Agent{ID: "atlas", DisplayName: "atlas", Template: "atlas-agent", Status: "running"}); err != nil {
		t.Fatalf("CreateAgent(atlas): %v", err)
	}
	atlasCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "atlas"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!atlas-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!atlas-admin:localhost",
		},
	}
	atlasRaw, err := yaml.Marshal(&atlasCfg)
	if err != nil {
		t.Fatalf("marshal atlas cfg: %v", err)
	}
	if err := s.CreateGosutoVersion(context.Background(), &appstore.GosutoVersion{
		AgentID:       "atlas",
		Version:       1,
		Hash:          "seed-hash-atlas",
		YAMLBlob:      string(atlasRaw),
		CreatedByMXID: "@admin:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion(atlas): %v", err)
	}

	kumoFS := fstest.MapFS{
		"kumo-agent/gosuto.yaml": &fstest.MapFile{Data: []byte(kumoEnsureBootstrapYAML)},
	}

	h := commands.NewHandlers(commands.HandlersConfig{
		Store:      s,
		Secrets:    sec,
		Runtime:    &stubRuntime{controlURL: acp.srv.URL},
		Templates:  templates.NewRegistry(kumoFS),
		RoomSender: &capturingSender{},
		AdminRooms: []string{"!admin:example.com"},
	})

	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --name kumo --template kumo-agent --image gitai:test --peer-alias atlas --peer-mxid @atlas:localhost --peer-protocol-id atlas.news.request.v1 --peer-protocol-prefix ATLAS_NEWS_REQUEST")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, err := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com")); err != nil {
		t.Fatalf("HandleAgentsCreate: %v", err)
	}

	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "kumo")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	gv, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion(kumo): %v", err)
	}
	if gv.Version < 2 {
		t.Fatalf("expected post-provision ensure to create follow-up version, got v%d", gv.Version)
	}

	cfg, err := gosutospec.Parse([]byte(gv.YAMLBlob))
	if err != nil {
		t.Fatalf("gosuto.Parse(kumo): %v", err)
	}

	if len(cfg.Trust.TrustedPeers) != 1 {
		t.Fatalf("trustedPeers count = %d, want 1", len(cfg.Trust.TrustedPeers))
	}
	peer := cfg.Trust.TrustedPeers[0]
	if peer.Alias != "atlas" || peer.MXID != "@atlas:localhost" || peer.RoomID != "!atlas-admin:localhost" {
		t.Fatalf("unexpected trusted peer from non-default alias resolution: %+v", peer)
	}
	if len(peer.Protocols) != 1 || peer.Protocols[0] != "atlas.news.request.v1" {
		t.Fatalf("unexpected ensured peer protocols: %+v", peer.Protocols)
	}

	foundAtlasTarget := false
	for _, target := range cfg.Messaging.AllowedTargets {
		if target.Alias == "atlas" && target.RoomID == "!atlas-admin:localhost" {
			foundAtlasTarget = true
			break
		}
	}
	if !foundAtlasTarget {
		t.Fatalf("expected ensured atlas messaging target in %+v", cfg.Messaging.AllowedTargets)
	}
}
