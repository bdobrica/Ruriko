package commands_test

// mesh_test.go — tests for R15.4 mesh topology computation.
//
// Verifies that:
//   - Provisioned agents have correct messaging targets based on peers
//   - Peer admin rooms are resolved from the agent inventory
//   - Operator "user" target is always included
//   - Missing peers are skipped gracefully
//   - Gosuto update changes messaging topology (UpdateAgentMeshTopology)

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// --- helpers ----------------------------------------------------------------

// newMeshTestStore creates a fresh in-memory SQLite store for mesh tests.
func newMeshTestStore(t *testing.T) *appstore.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "ruriko-mesh-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	f.Close()
	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedAgent creates an agent record and stores a Gosuto version with the given
// trust.adminRoom so the mesh resolver can look it up.
func seedAgent(t *testing.T, ctx context.Context, s *appstore.Store, agentID, adminRoom string) {
	t.Helper()

	agent := &appstore.Agent{
		ID:          agentID,
		DisplayName: agentID,
		Template:    agentID + "-agent",
		Status:      "running",
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("seedAgent(%s): CreateAgent: %v", agentID, err)
	}

	gosutoYAML := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: agentID},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{adminRoom},
			AllowedSenders: []string{"*"},
			AdminRoom:      adminRoom,
		},
	}
	yamlBytes, err := yaml.Marshal(&gosutoYAML)
	if err != nil {
		t.Fatalf("seedAgent(%s): marshal: %v", agentID, err)
	}

	gv := &appstore.GosutoVersion{
		AgentID:       agentID,
		Version:       1,
		Hash:          "seed-hash-" + agentID,
		YAMLBlob:      string(yamlBytes),
		CreatedByMXID: "@admin:example.com",
	}
	if err := s.CreateGosutoVersion(ctx, gv); err != nil {
		t.Fatalf("seedAgent(%s): CreateGosutoVersion: %v", agentID, err)
	}
}

// --- ResolveMeshTopology tests ----------------------------------------------

func TestResolveMeshTopology_WithPeers(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	// Seed two peer agents with known admin rooms.
	seedAgent(t, ctx, s, "kairo", "!kairo-room:localhost")
	seedAgent(t, ctx, s, "kumo", "!kumo-room:localhost")

	// Build a config for saito that references kairo and kumo as peers.
	cfg := &gosutospec.Config{
		Metadata: gosutospec.Metadata{Name: "saito"},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{
					{Name: "kairo", Role: "finance agent"},
					{Name: "kumo", Role: "news agent"},
				},
			},
		},
	}

	targets := commands.ResolveMeshTopology(ctx, cfg, s, "!user-room:localhost")

	// Expect: kairo target, kumo target, user target.
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d: %+v", len(targets), targets)
	}

	// Verify peer targets.
	targetMap := make(map[string]string, len(targets))
	for _, tgt := range targets {
		targetMap[tgt.Alias] = tgt.RoomID
	}

	if got := targetMap["kairo"]; got != "!kairo-room:localhost" {
		t.Errorf("kairo target room: got %q, want %q", got, "!kairo-room:localhost")
	}
	if got := targetMap["kumo"]; got != "!kumo-room:localhost" {
		t.Errorf("kumo target room: got %q, want %q", got, "!kumo-room:localhost")
	}
	if got := targetMap["user"]; got != "!user-room:localhost" {
		t.Errorf("user target room: got %q, want %q", got, "!user-room:localhost")
	}
}

func TestResolveMeshTopology_MissingPeerSkipped(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	// Only seed kairo. kumo does not exist.
	seedAgent(t, ctx, s, "kairo", "!kairo-room:localhost")

	cfg := &gosutospec.Config{
		Metadata: gosutospec.Metadata{Name: "saito"},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{
					{Name: "kairo", Role: "finance agent"},
					{Name: "kumo", Role: "news agent"},
				},
			},
		},
	}

	targets := commands.ResolveMeshTopology(ctx, cfg, s, "!user-room:localhost")

	// kairo should be present, kumo should be skipped, user always present.
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (kairo + user), got %d: %+v", len(targets), targets)
	}

	targetMap := make(map[string]string, len(targets))
	for _, tgt := range targets {
		targetMap[tgt.Alias] = tgt.RoomID
	}

	if _, ok := targetMap["kairo"]; !ok {
		t.Error("expected kairo target to be present")
	}
	if _, ok := targetMap["kumo"]; ok {
		t.Error("expected kumo target to be absent (not provisioned)")
	}
	if _, ok := targetMap["user"]; !ok {
		t.Error("expected user target to be present")
	}
}

func TestResolveMeshTopology_NoPeers_OnlyUser(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	cfg := &gosutospec.Config{
		Metadata: gosutospec.Metadata{Name: "standalone"},
	}

	targets := commands.ResolveMeshTopology(ctx, cfg, s, "!user-room:localhost")

	if len(targets) != 1 {
		t.Fatalf("expected 1 target (user only), got %d: %+v", len(targets), targets)
	}
	if targets[0].Alias != "user" || targets[0].RoomID != "!user-room:localhost" {
		t.Errorf("unexpected user target: %+v", targets[0])
	}
}

func TestResolveMeshTopology_NoOperatorRoom_NoPeers_Empty(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	cfg := &gosutospec.Config{
		Metadata: gosutospec.Metadata{Name: "isolated"},
	}

	targets := commands.ResolveMeshTopology(ctx, cfg, s, "")

	if len(targets) != 0 {
		t.Fatalf("expected 0 targets, got %d: %+v", len(targets), targets)
	}
}

// --- InjectMeshTopology tests -----------------------------------------------

func TestInjectMeshTopology_InjectsTargets(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	// Seed kumo as a peer.
	seedAgent(t, ctx, s, "kumo", "!kumo-room:localhost")

	// Build a Gosuto YAML with a peer reference.
	inputCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kairo-room:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kairo-room:localhost",
		},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{
					{Name: "kumo", Role: "news agent"},
				},
			},
		},
	}
	inputYAML, err := yaml.Marshal(&inputCfg)
	if err != nil {
		t.Fatalf("marshal input config: %v", err)
	}

	// Inject mesh topology.
	outputYAML, err := commands.InjectMeshTopology(ctx, inputYAML, s, "!user-room:localhost")
	if err != nil {
		t.Fatalf("InjectMeshTopology: %v", err)
	}

	// Parse the output to verify.
	var outputCfg gosutospec.Config
	if err := yaml.Unmarshal(outputYAML, &outputCfg); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if len(outputCfg.Messaging.AllowedTargets) != 2 {
		t.Fatalf("expected 2 messaging targets, got %d: %+v",
			len(outputCfg.Messaging.AllowedTargets), outputCfg.Messaging.AllowedTargets)
	}

	targetMap := make(map[string]string, len(outputCfg.Messaging.AllowedTargets))
	for _, tgt := range outputCfg.Messaging.AllowedTargets {
		targetMap[tgt.Alias] = tgt.RoomID
	}

	if got := targetMap["kumo"]; got != "!kumo-room:localhost" {
		t.Errorf("kumo room: got %q, want %q", got, "!kumo-room:localhost")
	}
	if got := targetMap["user"]; got != "!user-room:localhost" {
		t.Errorf("user room: got %q, want %q", got, "!user-room:localhost")
	}

	// Default rate limit should be applied.
	if outputCfg.Messaging.MaxMessagesPerMinute != 30 {
		t.Errorf("expected maxMessagesPerMinute=30, got %d", outputCfg.Messaging.MaxMessagesPerMinute)
	}
}

func TestInjectMeshTopology_PreservesExistingRateLimit(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	seedAgent(t, ctx, s, "kumo", "!kumo-room:localhost")

	inputCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kairo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kairo-room:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kairo-room:localhost",
		},
		Messaging: gosutospec.Messaging{
			MaxMessagesPerMinute: 60,
		},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{
					{Name: "kumo", Role: "news agent"},
				},
			},
		},
	}
	inputYAML, err := yaml.Marshal(&inputCfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	outputYAML, err := commands.InjectMeshTopology(ctx, inputYAML, s, "!user-room:localhost")
	if err != nil {
		t.Fatalf("InjectMeshTopology: %v", err)
	}

	var outputCfg gosutospec.Config
	if err := yaml.Unmarshal(outputYAML, &outputCfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Existing rate limit should be preserved (not overwritten by default 30).
	if outputCfg.Messaging.MaxMessagesPerMinute != 60 {
		t.Errorf("expected maxMessagesPerMinute=60 (preserved), got %d", outputCfg.Messaging.MaxMessagesPerMinute)
	}
}

func TestInjectMeshTopology_NoPeersNoOperator_Unchanged(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	inputCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "isolated"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!room:localhost"},
			AllowedSenders: []string{"*"},
		},
	}
	inputYAML, err := yaml.Marshal(&inputCfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	outputYAML, err := commands.InjectMeshTopology(ctx, inputYAML, s, "")
	if err != nil {
		t.Fatalf("InjectMeshTopology: %v", err)
	}

	// With no peers and no operator room, the YAML should be returned unchanged.
	if string(outputYAML) != string(inputYAML) {
		t.Errorf("expected unchanged YAML when no targets to inject\n"+
			"input:  %s\noutput: %s", inputYAML, outputYAML)
	}
}

// --- UpdateAgentMeshTopology tests ------------------------------------------

func TestUpdateAgentMeshTopology_UpdatesExistingAgent(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	// Seed kairo and kumo with known rooms.
	seedAgent(t, ctx, s, "kairo", "!kairo-room:localhost")
	seedAgent(t, ctx, s, "kumo", "!kumo-room:localhost")

	// Seed saito with a Gosuto that has peers but no messaging targets yet.
	saitoAgent := &appstore.Agent{
		ID:          "saito",
		DisplayName: "saito",
		Template:    "saito-agent",
		Status:      "running",
	}
	if err := s.CreateAgent(ctx, saitoAgent); err != nil {
		t.Fatalf("create saito: %v", err)
	}

	saitoCfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "saito"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!saito-room:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!saito-room:localhost",
		},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{
					{Name: "kairo", Role: "finance agent"},
				},
			},
		},
	}
	saitoYAML, _ := yaml.Marshal(&saitoCfg)
	saitoGV := &appstore.GosutoVersion{
		AgentID:       "saito",
		Version:       1,
		Hash:          "saito-v1-hash",
		YAMLBlob:      string(saitoYAML),
		CreatedByMXID: "@admin:example.com",
	}
	if err := s.CreateGosutoVersion(ctx, saitoGV); err != nil {
		t.Fatalf("create saito gosuto: %v", err)
	}

	// Run the mesh topology update.
	newGV, err := commands.UpdateAgentMeshTopology(ctx, "saito", s, "!user-room:localhost", "@admin:example.com")
	if err != nil {
		t.Fatalf("UpdateAgentMeshTopology: %v", err)
	}
	if newGV == nil {
		t.Fatal("expected a new Gosuto version, got nil")
	}
	if newGV.Version != 2 {
		t.Errorf("expected version 2, got %d", newGV.Version)
	}

	// Parse the new version to check targets.
	var updatedCfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(newGV.YAMLBlob), &updatedCfg); err != nil {
		t.Fatalf("unmarshal updated gosuto: %v", err)
	}

	// Should have kairo + user.
	if len(updatedCfg.Messaging.AllowedTargets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %+v",
			len(updatedCfg.Messaging.AllowedTargets), updatedCfg.Messaging.AllowedTargets)
	}

	targetMap := make(map[string]string, len(updatedCfg.Messaging.AllowedTargets))
	for _, tgt := range updatedCfg.Messaging.AllowedTargets {
		targetMap[tgt.Alias] = tgt.RoomID
	}
	if got := targetMap["kairo"]; got != "!kairo-room:localhost" {
		t.Errorf("kairo room: got %q, want %q", got, "!kairo-room:localhost")
	}
	if got := targetMap["user"]; got != "!user-room:localhost" {
		t.Errorf("user room: got %q, want %q", got, "!user-room:localhost")
	}
}

func TestUpdateAgentMeshTopology_NoChangeReturnsNil(t *testing.T) {
	ctx := context.Background()
	s := newMeshTestStore(t)

	// Seed an agent with no peers (and no operator room will be provided).
	agent := &appstore.Agent{
		ID:          "lonely",
		DisplayName: "lonely",
		Template:    "lonely-agent",
		Status:      "running",
	}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	cfg := gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "lonely"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!room:localhost"},
			AllowedSenders: []string{"*"},
		},
	}
	yamlBytes, _ := yaml.Marshal(&cfg)
	gv := &appstore.GosutoVersion{
		AgentID:       "lonely",
		Version:       1,
		Hash:          "lonely-hash",
		YAMLBlob:      string(yamlBytes),
		CreatedByMXID: "@admin:example.com",
	}
	if err := s.CreateGosutoVersion(ctx, gv); err != nil {
		t.Fatalf("create gosuto: %v", err)
	}

	// No operator room, no peers → no change.
	result, err := commands.UpdateAgentMeshTopology(ctx, "lonely", s, "", "@admin:example.com")
	if err != nil {
		t.Fatalf("UpdateAgentMeshTopology: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil (no change), got version %d", result.Version)
	}
}

// --- Provisioning pipeline integration test ---------------------------------

// meshGosutoYAML is a Gosuto template with peer references for mesh topology testing.
const meshGosutoYAML = `apiVersion: gosuto/v1
metadata:
  name: "{{.AgentName}}"
  template: mesh-test
trust:
  allowedRooms:
    - "!admin:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!admin:example.com"
instructions:
  role: "test agent"
  context:
    peers:
      - name: "peer-a"
        role: "helper agent A"
      - name: "peer-b"
        role: "helper agent B"
`

func TestProvisioningPipeline_InjectsMeshTargets(t *testing.T) {
	acpSrv := newMockACPServer()
	defer acpSrv.srv.Close()

	s := newMeshTestStore(t)

	// Seed peer agents that the provisioned agent references.
	seedAgent(t, ctx(t), s, "peer-a", "!peer-a-room:localhost")
	seedAgent(t, ctx(t), s, "peer-b", "!peer-b-room:localhost")

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	meshFS := fstest.MapFS{
		"mesh-test/gosuto.yaml": &fstest.MapFile{Data: []byte(meshGosutoYAML)},
	}
	tmplReg := templates.NewRegistry(meshFS)
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
	cmd, err := router.Parse("/ruriko agents create --name meshbot --template mesh-test --image gitai:test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	resp, createErr := h.HandleAgentsCreate(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if createErr != nil {
		t.Fatalf("HandleAgentsCreate: %v", createErr)
	}
	if !strings.Contains(resp, "meshbot") {
		t.Errorf("expected agent name in response, got %q", resp)
	}

	// Wait for the provisioning pipeline to complete.
	waitFor(t, 10*time.Second, func() bool {
		agent, err := s.GetAgent(context.Background(), "meshbot")
		if err != nil {
			return false
		}
		return agent.ProvisioningState == "healthy"
	})

	// Verify the stored Gosuto version includes mesh targets.
	gv, err := s.GetLatestGosutoVersion(context.Background(), "meshbot")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}

	var storedCfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &storedCfg); err != nil {
		t.Fatalf("unmarshal stored gosuto: %v", err)
	}

	// Expect: peer-a, peer-b, user (operator room from the fakeEvent).
	if len(storedCfg.Messaging.AllowedTargets) != 3 {
		t.Fatalf("expected 3 messaging targets, got %d: %+v",
			len(storedCfg.Messaging.AllowedTargets), storedCfg.Messaging.AllowedTargets)
	}

	targetMap := make(map[string]string, len(storedCfg.Messaging.AllowedTargets))
	for _, tgt := range storedCfg.Messaging.AllowedTargets {
		targetMap[tgt.Alias] = tgt.RoomID
	}

	if got := targetMap["peer-a"]; got != "!peer-a-room:localhost" {
		t.Errorf("peer-a target: got %q, want %q", got, "!peer-a-room:localhost")
	}
	if got := targetMap["peer-b"]; got != "!peer-b-room:localhost" {
		t.Errorf("peer-b target: got %q, want %q", got, "!peer-b-room:localhost")
	}
	if _, ok := targetMap["user"]; !ok {
		t.Error("expected 'user' target to be present")
	}

	// Verify default rate limit was applied.
	if storedCfg.Messaging.MaxMessagesPerMinute != 30 {
		t.Errorf("expected maxMessagesPerMinute=30, got %d", storedCfg.Messaging.MaxMessagesPerMinute)
	}
}

// ctx is a test helper that returns context.Background().
func ctx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
