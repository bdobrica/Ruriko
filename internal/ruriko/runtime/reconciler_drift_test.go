package runtime_test

// reconciler_drift_test.go — tests for the R5.3 agent registry drift detection
// and health-check tracking built into the Reconciler (R5.3).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	appstore "github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// mockACPChecker is a test double for runtime.ACPStatusChecker.
type mockACPChecker struct {
	healthErr  error
	healthResp *acp.HealthResponse
	statusErr  error
	statusResp *acp.StatusResponse
	// call counters
	healthCalls int
	statusCalls int
}

func (m *mockACPChecker) Health(_ context.Context) (*acp.HealthResponse, error) {
	m.healthCalls++
	if m.healthErr != nil {
		return nil, m.healthErr
	}
	if m.healthResp != nil {
		return m.healthResp, nil
	}
	return &acp.HealthResponse{Status: "ok"}, nil
}

func (m *mockACPChecker) Status(_ context.Context) (*acp.StatusResponse, error) {
	m.statusCalls++
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResp, nil
}

// makeACPFactory returns a factory that always hands out the same mock checker.
func makeACPFactory(mock *mockACPChecker) runtime.ACPClientFactory {
	return func(_, _ string) runtime.ACPStatusChecker { return mock }
}

// newHealthyAgent creates and inserts a running, healthy agent with the given
// ID into the store.  The control URL is set to "http://fake:8765" so the
// reconciler's ACP guard (control_url IS NOT NULL) passes.
func newHealthyAgent(t *testing.T, s *appstore.Store, id string) *appstore.Agent {
	t.Helper()
	a := &appstore.Agent{
		ID:                id,
		DisplayName:       id,
		Template:          "cron",
		Status:            "running",
		ProvisioningState: "healthy",
	}
	a.ControlURL.String = "http://fake:8765"
	a.ControlURL.Valid = true
	if err := s.CreateAgent(context.Background(), a); err != nil {
		t.Fatalf("CreateAgent %s: %v", id, err)
	}
	// Agents are enabled by default (SQL DEFAULT 1), but the Go struct has
	// Enabled=false at zero value.  Read back so the test has the real DB state.
	got, err := s.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAgent %s: %v", id, err)
	}
	return got
}

// TestReconciler_UpdatesLastHealthCheck verifies that a successful ACP /health
// response causes last_health_check to be written into the DB.
func TestReconciler_UpdatesLastHealthCheck(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "hc-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-hc-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	mock := &mockACPChecker{statusResp: &acp.StatusResponse{GosutoHash: ""}}

	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:         time.Second,
		ACPClientFactory: makeACPFactory(mock),
	})

	before := time.Now()
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := s.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.LastHealthCheck.Valid {
		t.Fatal("expected LastHealthCheck to be set after successful ACP /health")
	}
	if got.LastHealthCheck.Time.Before(before) {
		t.Errorf("LastHealthCheck %v is before test start %v", got.LastHealthCheck.Time, before)
	}
}

// TestReconciler_UpdatesActualGosutoHashFromStatus verifies that the hash
// reported by ACP /status is persisted into actual_gosuto_hash.
func TestReconciler_UpdatesActualGosutoHashFromStatus(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "hash-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-hash-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	const reportedHash = "abc123def456abc123def456abc123def456abc123def456abc1234567890abc"
	mock := &mockACPChecker{statusResp: &acp.StatusResponse{GosutoHash: reportedHash}}

	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:         time.Second,
		ACPClientFactory: makeACPFactory(mock),
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := s.GetAgent(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.ActualGosutoHash.Valid {
		t.Fatal("expected ActualGosutoHash to be set")
	}
	if got.ActualGosutoHash.String != reportedHash {
		t.Errorf("actual hash = %q; want %q", got.ActualGosutoHash.String, reportedHash)
	}
}

// TestReconciler_DetectsGosutoDrift verifies that the reconciler fires an alert
// when the desired Gosuto hash (set by Ruriko) differs from the hash the agent
// reports via ACP /status.
func TestReconciler_DetectsGosutoDrift(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "drift-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-drift-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	const desiredHash = "desired0000000000000000000000000000000000000000000000000000000000"
	const actualHash = "actual11111111111111111111111111111111111111111111111111111111111"

	// Set the desired hash in the DB.
	if err := s.SetAgentDesiredGosutoHash(context.Background(), a.ID, desiredHash); err != nil {
		t.Fatalf("SetAgentDesiredGosutoHash: %v", err)
	}

	mock := &mockACPChecker{statusResp: &acp.StatusResponse{GosutoHash: actualHash}}

	var alerts []string
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:         time.Second,
		ACPClientFactory: makeACPFactory(mock),
		AlertFunc: func(agentID, msg string) {
			alerts = append(alerts, agentID+": "+msg)
		},
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Expect at least one drift alert for our agent.
	driftFound := false
	for _, a := range alerts {
		if len(a) > 0 && contains(a, "drift-agent") && contains(a, "drift") {
			driftFound = true
			break
		}
	}
	if !driftFound {
		t.Errorf("expected drift alert for drift-agent; got alerts: %v", alerts)
	}

	// actual_gosuto_hash should be persisted.
	got, err := s.GetAgent(context.Background(), "drift-agent")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.ActualGosutoHash.Valid || got.ActualGosutoHash.String != actualHash {
		t.Errorf("actual_gosuto_hash = %q; want %q", got.ActualGosutoHash.String, actualHash)
	}
}

// TestReconciler_NoDriftWhenHashesMatch verifies that no drift alert is raised
// when desired and actual hashes are identical.
func TestReconciler_NoDriftWhenHashesMatch(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "sync-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-sync-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	const hash = "syncced0000000000000000000000000000000000000000000000000000000000"
	if err := s.SetAgentDesiredGosutoHash(context.Background(), a.ID, hash); err != nil {
		t.Fatalf("SetAgentDesiredGosutoHash: %v", err)
	}

	mock := &mockACPChecker{statusResp: &acp.StatusResponse{GosutoHash: hash}}

	var driftAlerts []string
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:         time.Second,
		ACPClientFactory: makeACPFactory(mock),
		AlertFunc: func(agentID, msg string) {
			if contains(msg, "drift") {
				driftAlerts = append(driftAlerts, agentID)
			}
		},
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(driftAlerts) > 0 {
		t.Errorf("unexpected drift alert for agents %v when hashes match", driftAlerts)
	}
}

// TestReconciler_AlertsOnStaleHealthCheck verifies that the reconciler fires
// an alert when last_health_check is older than HealthStaleThreshold.
func TestReconciler_AlertsOnStaleHealthCheck(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "stale-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-stale-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	// Write a last_health_check that is 10 minutes in the past.
	staleTime := time.Now().Add(-10 * time.Minute)
	_, err := s.DB().ExecContext(context.Background(),
		"UPDATE agents SET last_health_check = ? WHERE id = ?", staleTime, a.ID)
	if err != nil {
		t.Fatalf("set stale last_health_check: %v", err)
	}

	mock := &mockACPChecker{
		healthResp: &acp.HealthResponse{Status: "ok"},
		statusResp: &acp.StatusResponse{},
	}

	var staleAlerts []string
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:             time.Second,
		ACPClientFactory:     makeACPFactory(mock),
		HealthStaleThreshold: 5 * time.Minute, // threshold is 5 min; check is 10 min old
		AlertFunc: func(agentID, msg string) {
			if contains(msg, "stale") {
				staleAlerts = append(staleAlerts, agentID)
			}
		},
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(staleAlerts) == 0 {
		t.Error("expected stale health check alert; got none")
	}
}

// TestReconciler_SkipsACPForDisabledAgent verifies that the reconciler does
// not call the ACP client for an agent that has been administratively disabled.
func TestReconciler_SkipsACPForDisabledAgent(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "disabled-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-disabled-agent"}}
	rt.statuses[a.ID] = runtime.StateRunning

	// Disable the agent.
	if err := s.SetAgentEnabled(context.Background(), a.ID, false); err != nil {
		t.Fatalf("SetAgentEnabled: %v", err)
	}

	mock := &mockACPChecker{statusResp: &acp.StatusResponse{}}
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval:         time.Second,
		ACPClientFactory: makeACPFactory(mock),
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if mock.healthCalls != 0 || mock.statusCalls != 0 {
		t.Errorf("ACP should not be called for disabled agent; health=%d status=%d",
			mock.healthCalls, mock.statusCalls)
	}
}

// TestReconciler_SkipsACPWithoutFactory ensures that when no ACPClientFactory
// is configured the reconciler remains backward-compatible and no ACP calls
// are attempted.
func TestReconciler_SkipsACPWithoutFactory(t *testing.T) {
	s := newTestStore(t)
	rt := newMockRuntime()

	a := newHealthyAgent(t, s, "no-factory-agent")
	rt.handles = []runtime.AgentHandle{{AgentID: a.ID, ContainerID: "mock-no-factory"}}
	rt.statuses[a.ID] = runtime.StateRunning

	acpCallCount := 0
	anyFactory := func(_, _ string) runtime.ACPStatusChecker {
		acpCallCount++
		return &mockACPChecker{statusResp: &acp.StatusResponse{}}
	}
	_ = anyFactory // not passed to the reconciler

	// No ACPClientFactory — the reconciler should not call it.
	rec := runtime.NewReconciler(rt, s, runtime.ReconcilerConfig{
		Interval: time.Second,
		// ACPClientFactory intentionally omitted
	})

	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if acpCallCount != 0 {
		t.Errorf("factory should not be called when not configured")
	}
}

// TestStore_ListDriftingAgents verifies the store query for drifting agents.
func TestStore_ListDriftingAgents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Agent A: desired hash set but actual hash is different → drifting.
	a1 := newHealthyAgent(t, s, "drifter")
	if err := s.SetAgentDesiredGosutoHash(ctx, a1.ID, "desired_hash_111"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentActualGosutoHash(ctx, a1.ID, "actual_hash_222"); err != nil {
		t.Fatal(err)
	}

	// Agent B: desired == actual → NOT drifting.
	a2 := newHealthyAgent(t, s, "in-sync")
	if err := s.SetAgentDesiredGosutoHash(ctx, a2.ID, "same_hash_333"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentActualGosutoHash(ctx, a2.ID, "same_hash_333"); err != nil {
		t.Fatal(err)
	}

	// Agent C: no desired hash → NOT drifting.
	newHealthyAgent(t, s, "no-desired")

	// Agent D: disabled with drift → NOT returned (disabled).
	a4 := newHealthyAgent(t, s, "disabled-drifter")
	if err := s.SetAgentDesiredGosutoHash(ctx, a4.ID, "desired_ddd"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentActualGosutoHash(ctx, a4.ID, "actual_ddd"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentEnabled(ctx, a4.ID, false); err != nil {
		t.Fatal(err)
	}

	drifting, err := s.ListDriftingAgents(ctx)
	if err != nil {
		t.Fatalf("ListDriftingAgents: %v", err)
	}

	if len(drifting) != 1 {
		t.Fatalf("expected 1 drifting agent, got %d: %v", len(drifting), agentIDs(drifting))
	}
	if drifting[0].ID != "drifter" {
		t.Errorf("expected drifter, got %q", drifting[0].ID)
	}
}

// TestStore_SetAgentEnabled verifies that SetAgentEnabled toggles the enabled flag.
func TestStore_SetAgentEnabled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := newHealthyAgent(t, s, "toggle-agent")
	if !a.Enabled {
		t.Error("agent should be enabled by default")
	}

	if err := s.SetAgentEnabled(ctx, a.ID, false); err != nil {
		t.Fatalf("SetAgentEnabled false: %v", err)
	}
	got, _ := s.GetAgent(ctx, a.ID)
	if got.Enabled {
		t.Error("agent should be disabled after SetAgentEnabled(false)")
	}

	if err := s.SetAgentEnabled(ctx, a.ID, true); err != nil {
		t.Fatalf("SetAgentEnabled true: %v", err)
	}
	got, _ = s.GetAgent(ctx, a.ID)
	if !got.Enabled {
		t.Error("agent should be re-enabled after SetAgentEnabled(true)")
	}
}

// TestStore_DesiredActualGosutoHash verifies round-trips for desired/actual hash setters.
func TestStore_DesiredActualGosutoHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := newHealthyAgent(t, s, "hash-roundtrip")

	// Initially both hashes are NULL.
	if a.DesiredGosutoHash.Valid || a.ActualGosutoHash.Valid {
		t.Error("expected both hashes to be NULL initially")
	}

	const desired = "desired_hash_aabbccddeeff"
	const actual = "actual_hash_112233445566"

	if err := s.SetAgentDesiredGosutoHash(ctx, a.ID, desired); err != nil {
		t.Fatalf("SetAgentDesiredGosutoHash: %v", err)
	}
	if err := s.SetAgentActualGosutoHash(ctx, a.ID, actual); err != nil {
		t.Fatalf("SetAgentActualGosutoHash: %v", err)
	}

	got, err := s.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.DesiredGosutoHash.Valid || got.DesiredGosutoHash.String != desired {
		t.Errorf("desired hash = %q; want %q", got.DesiredGosutoHash.String, desired)
	}
	if !got.ActualGosutoHash.Valid || got.ActualGosutoHash.String != actual {
		t.Errorf("actual hash = %q; want %q", got.ActualGosutoHash.String, actual)
	}
}

// TestStore_UpdateAgentHealthCheck verifies that UpdateAgentHealthCheck sets
// last_health_check to a recent timestamp.
func TestStore_UpdateAgentHealthCheck(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := newHealthyAgent(t, s, "hc-roundtrip")
	if a.LastHealthCheck.Valid {
		t.Error("expected LastHealthCheck to be NULL initially")
	}

	before := time.Now()
	if err := s.UpdateAgentHealthCheck(ctx, a.ID); err != nil {
		t.Fatalf("UpdateAgentHealthCheck: %v", err)
	}

	got, err := s.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !got.LastHealthCheck.Valid {
		t.Fatal("LastHealthCheck should be set after UpdateAgentHealthCheck")
	}
	if got.LastHealthCheck.Time.Before(before) {
		t.Errorf("LastHealthCheck.Time %v is before test start %v",
			got.LastHealthCheck.Time, before)
	}
}

// TestStore_AgentEnabledByDefaultInDB verifies the SQL DEFAULT 1 is respected.
func TestStore_AgentEnabledByDefaultInDB(t *testing.T) {
	s := newTestStore(t)
	a := newHealthyAgent(t, s, "default-enabled")
	if !a.Enabled {
		t.Error("new agent should be enabled by default (SQL DEFAULT 1)")
	}
}

// TestStore_ListDriftingAgentsMissingActual verifies that an agent with a
// desired hash but no actual hash (NULL) is included in the drift list.
func TestStore_ListDriftingAgentsMissingActual(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := newHealthyAgent(t, s, "missing-actual")
	if err := s.SetAgentDesiredGosutoHash(ctx, a.ID, "some_desired_hash"); err != nil {
		t.Fatal(err)
	}
	// actual_gosuto_hash is NULL — never been set

	drifting, err := s.ListDriftingAgents(ctx)
	if err != nil {
		t.Fatalf("ListDriftingAgents: %v", err)
	}
	if len(drifting) != 1 || drifting[0].ID != a.ID {
		t.Errorf("expected agent with NULL actual hash to appear as drifting; got %v", agentIDs(drifting))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// contains is a simple substring check.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// agentIDs extracts agent IDs for error messages.
func agentIDs(agents []*appstore.Agent) []string {
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.ID
	}
	return ids
}
