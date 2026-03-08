package commands_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/event"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

func newTopologyFixture(t *testing.T, withApprovals bool) (*commands.Handlers, *appstore.Store) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-topology-test-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	_ = f.Close()

	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	sec, err := secrets.New(s, masterKey)
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}

	cfg := commands.HandlersConfig{Store: s, Secrets: sec}
	if withApprovals {
		cfg.Approvals = approvals.NewGate(approvals.NewStore(s.DB()), time.Hour)
	}

	return commands.NewHandlers(cfg), s
}

func seedTopologyAgent(t *testing.T, s *appstore.Store, agentID string, cfg gosutospec.Config) {
	t.Helper()

	agent := &appstore.Agent{ID: agentID, DisplayName: agentID, Template: "kumo-agent", Status: "running"}
	if err := s.CreateAgent(context.Background(), agent); err != nil {
		t.Fatalf("CreateAgent(%s): %v", agentID, err)
	}

	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	if _, err := gosutospec.Parse(raw); err != nil {
		t.Fatalf("seed gosuto invalid: %v", err)
	}

	gv := &appstore.GosutoVersion{
		AgentID:       agentID,
		Version:       1,
		Hash:          "seed-hash-" + agentID,
		YAMLBlob:      string(raw),
		CreatedByMXID: "@admin:example.com",
	}
	if err := s.CreateGosutoVersion(context.Background(), gv); err != nil {
		t.Fatalf("CreateGosutoVersion(%s): %v", agentID, err)
	}
}

func TestHandleTopologyPeerSet_RequiresApprovalThenApplies(t *testing.T) {
	h, s := newTopologyFixture(t, true)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
		},
	})

	cmd := parseCmd(t, "/ruriko topology peer-set kumo --alias marketbot --mxid @marketbot:localhost --room !marketbot-room:localhost --protocol marketbot.news.request.v1")

	resp, err := h.HandleTopologyPeerSet(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerSet (approval request): %v", err)
	}
	if !strings.Contains(resp, "Approval required") {
		t.Fatalf("expected approval-required response, got: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 1 {
		t.Fatalf("expected no new version before approval, got v%d", latest.Version)
	}

	cmd.Flags["_approved"] = "true"
	resp, err = h.HandleTopologyPeerSet(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerSet (approved): %v", err)
	}
	if !strings.Contains(resp, "Topology peer mapping added") {
		t.Fatalf("unexpected success response: %s", resp)
	}

	latest, err = s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected new version v2 after approval, got v%d", latest.Version)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(latest.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal latest gosuto: %v", err)
	}

	if len(cfg.Trust.TrustedPeers) != 1 {
		t.Fatalf("expected 1 trusted peer, got %d", len(cfg.Trust.TrustedPeers))
	}
	tp := cfg.Trust.TrustedPeers[0]
	if tp.Alias != "marketbot" || tp.MXID != "@marketbot:localhost" || tp.RoomID != "!marketbot-room:localhost" {
		t.Fatalf("unexpected trusted peer: %+v", tp)
	}
	if len(tp.Protocols) != 1 || tp.Protocols[0] != "marketbot.news.request.v1" {
		t.Fatalf("unexpected peer protocols: %+v", tp.Protocols)
	}

	if len(cfg.Messaging.AllowedTargets) != 1 {
		t.Fatalf("expected 1 messaging target, got %d", len(cfg.Messaging.AllowedTargets))
	}
	if cfg.Messaging.AllowedTargets[0].Alias != "marketbot" || cfg.Messaging.AllowedTargets[0].RoomID != "!marketbot-room:localhost" {
		t.Fatalf("unexpected messaging target: %+v", cfg.Messaging.AllowedTargets[0])
	}
}

func TestHandleTopologyPeerRemove_RemovesTrustAndMessagingAlias(t *testing.T) {
	h, s := newTopologyFixture(t, false)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
			TrustedPeers: []gosutospec.TrustedPeer{
				{
					Alias:     "marketbot",
					MXID:      "@marketbot:localhost",
					RoomID:    "!marketbot-room:localhost",
					Protocols: []string{"marketbot.news.request.v1"},
				},
			},
		},
		Messaging: gosutospec.Messaging{
			AllowedTargets: []gosutospec.MessagingTarget{
				{Alias: "marketbot", RoomID: "!marketbot-room:localhost"},
				{Alias: "user", RoomID: "!user-room:localhost"},
			},
		},
	})

	cmd := parseCmd(t, "/ruriko topology peer-remove kumo --alias marketbot")
	resp, err := h.HandleTopologyPeerRemove(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerRemove: %v", err)
	}
	if !strings.Contains(resp, "Topology peer mapping removed") {
		t.Fatalf("unexpected response: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected new version v2, got v%d", latest.Version)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(latest.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal latest gosuto: %v", err)
	}

	if len(cfg.Trust.TrustedPeers) != 0 {
		t.Fatalf("expected no trusted peers after removal, got %+v", cfg.Trust.TrustedPeers)
	}
	if len(cfg.Messaging.AllowedTargets) != 1 {
		t.Fatalf("expected only user target to remain, got %+v", cfg.Messaging.AllowedTargets)
	}
	if cfg.Messaging.AllowedTargets[0].Alias != "user" {
		t.Fatalf("expected remaining target alias=user, got %+v", cfg.Messaging.AllowedTargets[0])
	}
}

func TestHandleTopologyRefresh_StoresUpdatedMeshVersion(t *testing.T) {
	h, s := newTopologyFixture(t, false)

	seedTopologyAgent(t, s, "marketbot", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "marketbot"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!marketbot-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!marketbot-admin:localhost",
		},
	})

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
		},
		Instructions: gosutospec.Instructions{
			Context: gosutospec.InstructionsContext{
				Peers: []gosutospec.PeerRef{{Name: "marketbot", Role: "analysis peer"}},
			},
		},
	})

	cmd := parseCmd(t, "/ruriko topology refresh kumo")
	resp, err := h.HandleTopologyRefresh(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyRefresh: %v", err)
	}
	if !strings.Contains(resp, "Topology for **kumo** refreshed") {
		t.Fatalf("unexpected response: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected refresh to create v2, got v%d", latest.Version)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(latest.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal latest gosuto: %v", err)
	}

	if len(cfg.Messaging.AllowedTargets) != 2 {
		t.Fatalf("expected 2 messaging targets (peer + user), got %+v", cfg.Messaging.AllowedTargets)
	}

	aliasToRoom := map[string]string{}
	for _, target := range cfg.Messaging.AllowedTargets {
		aliasToRoom[target.Alias] = target.RoomID
	}
	if aliasToRoom["marketbot"] != "!marketbot-admin:localhost" {
		t.Fatalf("marketbot target mismatch: %+v", cfg.Messaging.AllowedTargets)
	}
	if aliasToRoom["user"] != "!test:example.com" {
		t.Fatalf("user target mismatch: %+v", cfg.Messaging.AllowedTargets)
	}
}

func TestHandleTopologyPeerRemove_PushTrue_AppliesViaACP(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	h, s := newTopologyFixture(t, false)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
			TrustedPeers: []gosutospec.TrustedPeer{
				{Alias: "marketbot", MXID: "@marketbot:localhost", RoomID: "!marketbot-room:localhost", Protocols: []string{"marketbot.news.request.v1"}},
			},
		},
		Messaging: gosutospec.Messaging{
			AllowedTargets: []gosutospec.MessagingTarget{{Alias: "marketbot", RoomID: "!marketbot-room:localhost"}},
		},
	})

	if err := s.UpdateAgentHandle(context.Background(), "kumo", "cid-kumo", acp.srv.URL, "gitai:test"); err != nil {
		t.Fatalf("UpdateAgentHandle: %v", err)
	}

	cmd := parseCmd(t, "/ruriko topology peer-remove kumo --alias marketbot --push true")
	resp, err := h.HandleTopologyPeerRemove(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerRemove: %v", err)
	}
	if !strings.Contains(resp, "Gosuto v2 pushed to running agent") {
		t.Fatalf("expected push confirmation in response, got: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}

	acp.mu.Lock()
	appliedHash := acp.appliedHash
	acp.mu.Unlock()
	if appliedHash != latest.Hash {
		t.Fatalf("expected ACP applied hash %q, got %q", latest.Hash, appliedHash)
	}
}

func TestHandleTopologyPeerRemove_PushTrue_NoControlURL_NonFatal(t *testing.T) {
	h, s := newTopologyFixture(t, false)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
			TrustedPeers: []gosutospec.TrustedPeer{
				{Alias: "marketbot", MXID: "@marketbot:localhost", RoomID: "!marketbot-room:localhost", Protocols: []string{"marketbot.news.request.v1"}},
			},
		},
		Messaging: gosutospec.Messaging{
			AllowedTargets: []gosutospec.MessagingTarget{{Alias: "marketbot", RoomID: "!marketbot-room:localhost"}},
		},
	})

	cmd := parseCmd(t, "/ruriko topology peer-remove kumo --alias marketbot --push true")
	resp, err := h.HandleTopologyPeerRemove(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("expected non-fatal push warning, got error: %v", err)
	}
	if !strings.Contains(resp, "Push requested") {
		t.Fatalf("expected push warning in response, got: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected version to still be stored as v2, got v%d", latest.Version)
	}
}

func TestHandleTopologyPeerSet_PushTrue_ApprovalDecisionDispatchesAndApplies(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	h, s := newTopologyFixture(t, true)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
		},
	})

	if err := s.UpdateAgentHandle(context.Background(), "kumo", "cid-kumo", acp.srv.URL, "gitai:test"); err != nil {
		t.Fatalf("UpdateAgentHandle: %v", err)
	}

	h.SetDispatch(func(ctx context.Context, action string, cmd *commands.Command, evt *event.Event) (string, error) {
		if action != "topology.peer-set" {
			return "", nil
		}
		return h.HandleTopologyPeerSet(ctx, cmd, evt)
	})

	requestCmd := parseCmd(t, "/ruriko topology peer-set kumo --alias marketbot --mxid @marketbot:localhost --room !marketbot-room:localhost --protocol marketbot.news.request.v1 --push true")

	requestResp, err := h.HandleTopologyPeerSet(context.Background(), requestCmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerSet (request): %v", err)
	}
	if !strings.Contains(requestResp, "Approval required") {
		t.Fatalf("expected approval-required response, got: %s", requestResp)
	}

	approvalStore := approvals.NewStore(s.DB())
	pending, err := approvalStore.List(context.Background(), string(approvals.StatusPending))
	if err != nil {
		t.Fatalf("approval list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	if pending[0].Action != "topology.peer-set" {
		t.Fatalf("unexpected pending action: %s", pending[0].Action)
	}

	decisionResp, err := h.HandleApprovalDecision(context.Background(), "approve "+pending[0].ID, fakeEvent("@reviewer:example.com"))
	if err != nil {
		t.Fatalf("HandleApprovalDecision: %v", err)
	}
	if !strings.Contains(decisionResp, "Approved by") {
		t.Fatalf("expected approval decision response, got: %s", decisionResp)
	}
	if !strings.Contains(decisionResp, "Gosuto v2 pushed to running agent") {
		t.Fatalf("expected push confirmation in approval decision response, got: %s", decisionResp)
	}

	approved, err := approvalStore.Get(context.Background(), pending[0].ID)
	if err != nil {
		t.Fatalf("approval get: %v", err)
	}
	if approved.Status != approvals.StatusApproved {
		t.Fatalf("expected approval status approved, got %s", approved.Status)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected new version v2 after approval, got v%d", latest.Version)
	}

	var cfg gosutospec.Config
	if err := yaml.Unmarshal([]byte(latest.YAMLBlob), &cfg); err != nil {
		t.Fatalf("unmarshal latest gosuto: %v", err)
	}
	if len(cfg.Trust.TrustedPeers) != 1 {
		t.Fatalf("expected 1 trusted peer, got %d", len(cfg.Trust.TrustedPeers))
	}

	acp.mu.Lock()
	appliedHash := acp.appliedHash
	acp.mu.Unlock()
	if appliedHash != latest.Hash {
		t.Fatalf("expected ACP applied hash %q, got %q", latest.Hash, appliedHash)
	}
}

func TestHandleTopologyPeerEnsure_NoOpWhenAlreadyPresent(t *testing.T) {
	h, s := newTopologyFixture(t, true)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
			TrustedPeers: []gosutospec.TrustedPeer{
				{Alias: "marketbot", MXID: "@marketbot:localhost", RoomID: "!marketbot-room:localhost", Protocols: []string{"marketbot.news.request.v1"}},
			},
		},
		Messaging: gosutospec.Messaging{
			AllowedTargets: []gosutospec.MessagingTarget{{Alias: "marketbot", RoomID: "!marketbot-room:localhost"}},
		},
	})

	cmd := parseCmd(t, "/ruriko topology peer-ensure kumo --alias marketbot --mxid @marketbot:localhost --room !marketbot-room:localhost --protocol marketbot.news.request.v1")
	resp, err := h.HandleTopologyPeerEnsure(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerEnsure: %v", err)
	}
	if !strings.Contains(resp, "already satisfies ensure requirements") {
		t.Fatalf("unexpected response: %s", resp)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 1 {
		t.Fatalf("expected no new version for no-op ensure, got v%d", latest.Version)
	}
}

func TestHandleTopologyPeerEnsure_ConflictingAliasRefuses(t *testing.T) {
	h, s := newTopologyFixture(t, false)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
			TrustedPeers: []gosutospec.TrustedPeer{
				{Alias: "marketbot", MXID: "@other:localhost", RoomID: "!other-room:localhost", Protocols: []string{"other.protocol.v1"}},
			},
		},
	})

	cmd := parseCmd(t, "/ruriko topology peer-ensure kumo --alias marketbot --mxid @marketbot:localhost --room !marketbot-room:localhost --protocol marketbot.news.request.v1")
	_, err := h.HandleTopologyPeerEnsure(context.Background(), cmd, fakeEvent("@admin:example.com"))
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "existing trusted peer alias maps") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleTopologyPeerEnsure_PushTrue_ApprovalDecisionDispatchesAndApplies(t *testing.T) {
	acp := newMockACPServer()
	defer acp.srv.Close()

	h, s := newTopologyFixture(t, true)

	seedTopologyAgent(t, s, "kumo", gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "kumo"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"!kumo-admin:localhost"},
			AllowedSenders: []string{"*"},
			AdminRoom:      "!kumo-admin:localhost",
		},
	})

	if err := s.UpdateAgentHandle(context.Background(), "kumo", "cid-kumo", acp.srv.URL, "gitai:test"); err != nil {
		t.Fatalf("UpdateAgentHandle: %v", err)
	}

	h.SetDispatch(func(ctx context.Context, action string, cmd *commands.Command, evt *event.Event) (string, error) {
		switch action {
		case "topology.peer-ensure":
			return h.HandleTopologyPeerEnsure(ctx, cmd, evt)
		default:
			return "", nil
		}
	})

	requestCmd := parseCmd(t, "/ruriko topology peer-ensure kumo --alias marketbot --mxid @marketbot:localhost --room !marketbot-room:localhost --protocol marketbot.news.request.v1 --push true")

	requestResp, err := h.HandleTopologyPeerEnsure(context.Background(), requestCmd, fakeEvent("@admin:example.com"))
	if err != nil {
		t.Fatalf("HandleTopologyPeerEnsure (request): %v", err)
	}
	if !strings.Contains(requestResp, "Approval required") {
		t.Fatalf("expected approval-required response, got: %s", requestResp)
	}

	approvalStore := approvals.NewStore(s.DB())
	pending, err := approvalStore.List(context.Background(), string(approvals.StatusPending))
	if err != nil {
		t.Fatalf("approval list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	if pending[0].Action != "topology.peer-ensure" {
		t.Fatalf("unexpected pending action: %s", pending[0].Action)
	}

	decisionResp, err := h.HandleApprovalDecision(context.Background(), "approve "+pending[0].ID, fakeEvent("@reviewer:example.com"))
	if err != nil {
		t.Fatalf("HandleApprovalDecision: %v", err)
	}
	if !strings.Contains(decisionResp, "Approved by") {
		t.Fatalf("expected approval decision response, got: %s", decisionResp)
	}
	if !strings.Contains(decisionResp, "Gosuto v2 pushed to running agent") {
		t.Fatalf("expected push confirmation in approval decision response, got: %s", decisionResp)
	}

	approved, err := approvalStore.Get(context.Background(), pending[0].ID)
	if err != nil {
		t.Fatalf("approval get: %v", err)
	}
	if approved.Status != approvals.StatusApproved {
		t.Fatalf("expected approval status approved, got %s", approved.Status)
	}

	latest, err := s.GetLatestGosutoVersion(context.Background(), "kumo")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}
	if latest.Version != 2 {
		t.Fatalf("expected new version v2 after approval, got v%d", latest.Version)
	}

	acp.mu.Lock()
	appliedHash := acp.appliedHash
	acp.mu.Unlock()
	if appliedHash != latest.Hash {
		t.Fatalf("expected ACP applied hash %q, got %q", latest.Hash, appliedHash)
	}
}
