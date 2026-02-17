package runtime_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// mockRuntime satisfies runtime.Runtime for testing.
type mockRuntime struct {
	handles  []runtime.AgentHandle
	statuses map[string]runtime.ContainerState
}

func (m *mockRuntime) Spawn(_ context.Context, spec runtime.AgentSpec) (runtime.AgentHandle, error) {
	h := runtime.AgentHandle{AgentID: spec.ID, ContainerID: "mock-" + spec.ID}
	m.handles = append(m.handles, h)
	m.statuses[spec.ID] = runtime.StateRunning
	return h, nil
}

func (m *mockRuntime) Stop(_ context.Context, h runtime.AgentHandle) error {
	m.statuses[h.AgentID] = runtime.StateStopped
	return nil
}

func (m *mockRuntime) Restart(_ context.Context, h runtime.AgentHandle) error {
	m.statuses[h.AgentID] = runtime.StateRunning
	return nil
}

func (m *mockRuntime) Status(_ context.Context, h runtime.AgentHandle) (runtime.RuntimeStatus, error) {
	state, ok := m.statuses[h.AgentID]
	if !ok {
		state = runtime.StateUnknown
	}
	return runtime.RuntimeStatus{
		AgentID:     h.AgentID,
		ContainerID: h.ContainerID,
		State:       state,
		StartedAt:   time.Now().Add(-5 * time.Minute),
	}, nil
}

func (m *mockRuntime) List(_ context.Context) ([]runtime.AgentHandle, error) {
	return m.handles, nil
}

func (m *mockRuntime) Remove(_ context.Context, h runtime.AgentHandle) error {
	delete(m.statuses, h.AgentID)
	filtered := m.handles[:0]
	for _, hh := range m.handles {
		if hh.AgentID != h.AgentID {
			filtered = append(filtered, hh)
		}
	}
	m.handles = filtered
	return nil
}

func newTestStore(t *testing.T) *appstore.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "reconciler-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err := appstore.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newMockRuntime() *mockRuntime {
	return &mockRuntime{statuses: make(map[string]runtime.ContainerState)}
}

func TestReconciler_NoAgents(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{Interval: time.Second})
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile with no agents: %v", err)
	}
}

func TestReconciler_RunningAgentUpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	// Create a running agent with a known container ID
	agent := &appstore.Agent{
		ID:          "agent-1",
		DisplayName: "Agent 1",
		Template:    "cron",
		Status:      "running",
	}
	agent.ContainerID.String = "mock-agent-1"
	agent.ContainerID.Valid = true
	if err := s.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Set agent as running in mock runtime
	rt.handles = []runtime.AgentHandle{{AgentID: "agent-1", ContainerID: "mock-agent-1"}}
	rt.statuses["agent-1"] = runtime.StateRunning

	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{Interval: time.Second})
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// LastSeen should be updated
	got, err := s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.LastSeen.Valid {
		t.Error("expected LastSeen to be set after reconcile of running agent")
	}
}

func TestReconciler_DetectsMissingContainer(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	var alerted string
	agent := &appstore.Agent{
		ID:          "lost-agent",
		DisplayName: "Lost Agent",
		Template:    "cron",
		Status:      "running",
	}
	agent.ContainerID.String = "mock-lost-agent"
	agent.ContainerID.Valid = true
	if err := s.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Runtime reports NO handles — container is gone
	rt.handles = []runtime.AgentHandle{}

	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval: time.Second,
		AlertFunc: func(agentID, msg string) {
			alerted = agentID
		},
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if alerted != "lost-agent" {
		t.Errorf("expected alert for lost-agent, got %q", alerted)
	}

	// Status should be updated to error
	got, err := s.GetAgent("lost-agent")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Status != "error" {
		t.Errorf("expected status=error, got %q", got.Status)
	}
}

func TestReconciler_ExitedContainerMarkedStopped(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	agent := &appstore.Agent{
		ID:          "exiting-agent",
		DisplayName: "Exiting Agent",
		Template:    "cron",
		Status:      "running",
	}
	agent.ContainerID.String = "mock-exiting-agent"
	agent.ContainerID.Valid = true
	if err := s.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	rt.handles = []runtime.AgentHandle{{AgentID: "exiting-agent", ContainerID: "mock-exiting-agent"}}
	rt.statuses["exiting-agent"] = runtime.StateExited

	var alerted string
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:  time.Second,
		AlertFunc: func(id, _ string) { alerted = id },
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := s.GetAgent("exiting-agent")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Status != "stopped" {
		t.Errorf("expected status=stopped, got %q", got.Status)
	}
	if alerted != "exiting-agent" {
		t.Errorf("expected alert, got %q", alerted)
	}
}

func TestReconciler_SkipsStoppedAgents(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	// Stopped agent — reconciler should not touch it
	agent := &appstore.Agent{
		ID:          "idle-agent",
		DisplayName: "Idle Agent",
		Template:    "cron",
		Status:      "stopped",
	}
	if err := s.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// No container in runtime
	rt.handles = []runtime.AgentHandle{}

	alertCount := 0
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:  time.Second,
		AlertFunc: func(_, _ string) { alertCount++ },
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if alertCount != 0 {
		t.Errorf("expected no alerts for stopped agent, got %d", alertCount)
	}

	got, _ := s.GetAgent("idle-agent")
	if got.Status != "stopped" {
		t.Errorf("stopped agent status was changed unexpectedly to %q", got.Status)
	}
}

func TestReconciler_SteadyState(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	// Create two running agents
	for _, id := range []string{"alpha", "beta"} {
		a := &appstore.Agent{
			ID: id, DisplayName: id, Template: "cron", Status: "running",
		}
		a.ContainerID.String = "mock-" + id
		a.ContainerID.Valid = true
		s.CreateAgent(a)
		rt.handles = append(rt.handles, runtime.AgentHandle{AgentID: id, ContainerID: "mock-" + id})
		rt.statuses[id] = runtime.StateRunning
	}

	alertCount := 0
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:  time.Second,
		AlertFunc: func(_, _ string) { alertCount++ },
	})

	// Multiple reconcile rounds — no alerts expected
	for i := 0; i < 3; i++ {
		if err := rec.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile round %d: %v", i, err)
		}
	}

	if alertCount != 0 {
		t.Errorf("expected 0 alerts in steady state, got %d", alertCount)
	}
}
