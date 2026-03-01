package commands_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

type recoveryRuntime struct {
	spawnCount int
	startErr   error
	restartErr error
	lastSpec   runtime.AgentSpec
}

func (r *recoveryRuntime) Spawn(_ context.Context, spec runtime.AgentSpec) (runtime.AgentHandle, error) {
	r.spawnCount++
	r.lastSpec = spec
	return runtime.AgentHandle{
		AgentID:       spec.ID,
		ContainerID:   fmt.Sprintf("recovered-%d", r.spawnCount),
		ContainerName: runtime.ContainerNameFor(spec.ID),
		ControlURL:    "http://127.0.0.1:8765",
	}, nil
}

func (r *recoveryRuntime) Stop(_ context.Context, _ runtime.AgentHandle) error   { return nil }
func (r *recoveryRuntime) Remove(_ context.Context, _ runtime.AgentHandle) error { return nil }
func (r *recoveryRuntime) List(_ context.Context) ([]runtime.AgentHandle, error) { return nil, nil }
func (r *recoveryRuntime) Status(_ context.Context, _ runtime.AgentHandle) (runtime.RuntimeStatus, error) {
	return runtime.RuntimeStatus{State: runtime.StateRunning}, nil
}
func (r *recoveryRuntime) Start(_ context.Context, _ runtime.AgentHandle) error { return r.startErr }
func (r *recoveryRuntime) Restart(_ context.Context, _ runtime.AgentHandle) error {
	return r.restartErr
}

func newRecoveryFixture(t *testing.T, rt runtime.Runtime) (*commands.Handlers, *appstore.Store, *secrets.Store) {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "ruriko-recovery-test-*.db")
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
		Store:            s,
		Secrets:          sec,
		Runtime:          rt,
		MatrixHomeserver: "http://tuwunel:8008",
	})
	return h, s, sec
}

func parseRecoveryCmd(t *testing.T, text string) *commands.Command {
	t.Helper()
	r := commands.NewRouter("/ruriko")
	cmd, err := r.Parse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return cmd
}

func recoveryEvent() *event.Event {
	return &event.Event{Sender: id.UserID("@admin:localhost"), RoomID: id.RoomID("!admin:localhost")}
}

func TestHandleAgentsStart_RecoversWhenContainerMissing(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-live-test")
	t.Setenv("RURIKO_NLP_API_KEY", "")

	rt := &recoveryRuntime{}
	h, s, sec := newRecoveryFixture(t, rt)
	ctx := context.Background()

	agent := &appstore.Agent{ID: "saito", DisplayName: "saito", Template: "saito-agent", Status: "stopped"}
	agent.Image.String = "gitai:latest"
	agent.Image.Valid = true
	agent.ACPToken.String = "tok-123"
	agent.ACPToken.Valid = true
	agent.MXID.String = "@saito:localhost"
	agent.MXID.Valid = true
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := sec.Set(ctx, "agent.saito.matrix_token", secrets.TypeMatrixToken, []byte("mx-token")); err != nil {
		t.Fatalf("Set matrix token: %v", err)
	}

	cmd := parseRecoveryCmd(t, "/ruriko agents start saito")
	resp, err := h.HandleAgentsStart(ctx, cmd, recoveryEvent())
	if err != nil {
		t.Fatalf("HandleAgentsStart: %v", err)
	}
	if !strings.Contains(resp, "recovered and started") {
		t.Fatalf("expected recovery response, got %q", resp)
	}
	if rt.spawnCount != 1 {
		t.Fatalf("expected one recovery spawn, got %d", rt.spawnCount)
	}
	if got := rt.lastSpec.Env["MATRIX_ACCESS_TOKEN"]; got != "mx-token" {
		t.Fatalf("MATRIX_ACCESS_TOKEN mismatch: got %q", got)
	}
	if got := rt.lastSpec.Env["LLM_API_KEY"]; got != "sk-live-test" {
		t.Fatalf("LLM_API_KEY mismatch: got %q", got)
	}

	updated, err := s.GetAgent(ctx, "saito")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !updated.ContainerID.Valid || !strings.Contains(updated.ContainerID.String, "recovered-") {
		t.Fatalf("expected recovered container handle persisted, got %+v", updated.ContainerID)
	}
}

func TestHandleAgentsStart_RecoversWhenContainerIDIsStale(t *testing.T) {
	rt := &recoveryRuntime{startErr: errors.New("Error response from daemon: No such container: deadbeef")}
	h, s, sec := newRecoveryFixture(t, rt)
	ctx := context.Background()

	agent := &appstore.Agent{ID: "kairo", DisplayName: "kairo", Template: "kairo-agent", Status: "stopped"}
	agent.Image.String = "gitai:latest"
	agent.Image.Valid = true
	agent.ACPToken.String = "tok-456"
	agent.ACPToken.Valid = true
	agent.MXID.String = "@kairo:localhost"
	agent.MXID.Valid = true
	agent.ContainerID.String = "deadbeef"
	agent.ContainerID.Valid = true
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := sec.Set(ctx, "agent.kairo.matrix_token", secrets.TypeMatrixToken, []byte("mx-token-kairo")); err != nil {
		t.Fatalf("Set matrix token: %v", err)
	}

	cmd := parseRecoveryCmd(t, "/ruriko agents start kairo")
	resp, err := h.HandleAgentsStart(ctx, cmd, recoveryEvent())
	if err != nil {
		t.Fatalf("HandleAgentsStart: %v", err)
	}
	if !strings.Contains(resp, "recovered and started") {
		t.Fatalf("expected recovery response, got %q", resp)
	}
	if rt.spawnCount != 1 {
		t.Fatalf("expected one recovery spawn, got %d", rt.spawnCount)
	}
}

func TestHandleAgentsCreate_DuplicateWithNoContainerHintsRecovery(t *testing.T) {
	rt := &recoveryRuntime{}
	h, s, _ := newRecoveryFixture(t, rt)
	ctx := context.Background()

	agent := &appstore.Agent{ID: "kumo", DisplayName: "kumo", Template: "kumo-agent", Status: "stopped"}
	agent.Image.String = "gitai:latest"
	agent.Image.Valid = true
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	cmd := parseRecoveryCmd(t, "/ruriko agents create --name kumo --template kumo-agent --image gitai:latest")
	_, err := h.HandleAgentsCreate(ctx, cmd, recoveryEvent())
	if err == nil {
		t.Fatal("expected duplicate create error")
	}
	if !strings.Contains(err.Error(), "/ruriko agents start kumo") {
		t.Fatalf("expected recovery hint in error, got %v", err)
	}
}

func TestHandleAgentsRespawn_RecoversWhenContainerMissing(t *testing.T) {
	rt := &recoveryRuntime{}
	h, s, sec := newRecoveryFixture(t, rt)
	ctx := context.Background()

	agent := &appstore.Agent{ID: "kumo", DisplayName: "kumo", Template: "kumo-agent", Status: "stopped"}
	agent.Image.String = "gitai:latest"
	agent.Image.Valid = true
	agent.ACPToken.String = "tok-789"
	agent.ACPToken.Valid = true
	agent.MXID.String = "@kumo:localhost"
	agent.MXID.Valid = true
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := sec.Set(ctx, "agent.kumo.matrix_token", secrets.TypeMatrixToken, []byte("mx-token-kumo")); err != nil {
		t.Fatalf("Set matrix token: %v", err)
	}

	cmd := parseRecoveryCmd(t, "/ruriko agents respawn kumo")
	resp, err := h.HandleAgentsRespawn(ctx, cmd, recoveryEvent())
	if err != nil {
		t.Fatalf("HandleAgentsRespawn: %v", err)
	}
	if !strings.Contains(resp, "recovered and respawned") {
		t.Fatalf("expected recovery response, got %q", resp)
	}
	if rt.spawnCount != 1 {
		t.Fatalf("expected one recovery spawn, got %d", rt.spawnCount)
	}
}

func TestHandleAgentsStart_RecoveryFailsWithoutMatrixIdentity(t *testing.T) {
	rt := &recoveryRuntime{}
	h, s, _ := newRecoveryFixture(t, rt)
	ctx := context.Background()

	agent := &appstore.Agent{ID: "legacy", DisplayName: "legacy", Template: "cron-agent", Status: "stopped"}
	agent.Image.String = "gitai:latest"
	agent.Image.Valid = true
	agent.ACPToken.String = "tok-legacy"
	agent.ACPToken.Valid = true
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	cmd := parseRecoveryCmd(t, "/ruriko agents start legacy")
	_, err := h.HandleAgentsStart(ctx, cmd, recoveryEvent())
	if err == nil {
		t.Fatal("expected error for missing matrix identity")
	}
	if !strings.Contains(err.Error(), "agents matrix register legacy") {
		t.Fatalf("expected matrix register hint, got %v", err)
	}
	if rt.spawnCount != 0 {
		t.Fatalf("expected no spawn when matrix identity is missing, got %d", rt.spawnCount)
	}
}
