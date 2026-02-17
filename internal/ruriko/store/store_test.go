package store_test

import (
	"os"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	// Use a temp file that is cleaned up after the test
	f, err := os.CreateTemp(t.TempDir(), "ruriko-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp db file: %v", err)
	}
	f.Close()

	s, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return s
}

// --- Agents ---

func TestCreateAndGetAgent(t *testing.T) {
	s := newTestStore(t)

	agent := &store.Agent{
		ID:          "weatherbot",
		DisplayName: "Weather Bot",
		Template:    "cron",
		Status:      "stopped",
	}

	if err := s.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := s.GetAgent("weatherbot")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	if got.ID != "weatherbot" {
		t.Errorf("ID: got %q, want %q", got.ID, "weatherbot")
	}
	if got.DisplayName != "Weather Bot" {
		t.Errorf("DisplayName: got %q, want %q", got.DisplayName, "Weather Bot")
	}
	if got.Template != "cron" {
		t.Errorf("Template: got %q, want %q", got.Template, "cron")
	}
	if got.Status != "stopped" {
		t.Errorf("Status: got %q, want %q", got.Status, "stopped")
	}
}

func TestGetAgent_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing agent, got nil")
	}
}

func TestListAgents_Empty(t *testing.T) {
	s := newTestStore(t)

	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestListAgents(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []string{"bot1", "bot2", "bot3"} {
		if err := s.CreateAgent(&store.Agent{
			ID:          id,
			DisplayName: id,
			Template:    "cron",
			Status:      "stopped",
		}); err != nil {
			t.Fatalf("CreateAgent(%s): %v", id, err)
		}
	}

	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}
}

func TestUpdateAgentStatus(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateAgent(&store.Agent{
		ID:          "testbot",
		DisplayName: "Test Bot",
		Template:    "cron",
		Status:      "stopped",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := s.UpdateAgentStatus("testbot", "running"); err != nil {
		t.Fatalf("UpdateAgentStatus: %v", err)
	}

	got, err := s.GetAgent("testbot")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("Status: got %q, want %q", got.Status, "running")
	}
}

func TestUpdateAgentStatus_NotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.UpdateAgentStatus("nonexistent", "running")
	if err == nil {
		t.Fatal("expected error for missing agent, got nil")
	}
}

func TestDeleteAgent(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateAgent(&store.Agent{
		ID:          "todelete",
		DisplayName: "To Delete",
		Template:    "cron",
		Status:      "stopped",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := s.DeleteAgent("todelete"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, err := s.GetAgent("todelete")
	if err == nil {
		t.Fatal("expected error after deletion, got nil")
	}
}

// --- Audit Log ---

func TestWriteAndReadAuditLog(t *testing.T) {
	s := newTestStore(t)

	err := s.WriteAudit(
		"t_abc123",
		"@admin:example.com",
		"agents.list",
		"",
		"success",
		store.AuditPayload{"count": 5},
		"",
	)
	if err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}

	entries, err := s.GetAuditLog(10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.TraceID != "t_abc123" {
		t.Errorf("TraceID: got %q, want %q", e.TraceID, "t_abc123")
	}
	if e.ActorMXID != "@admin:example.com" {
		t.Errorf("ActorMXID: got %q, want %q", e.ActorMXID, "@admin:example.com")
	}
	if e.Action != "agents.list" {
		t.Errorf("Action: got %q, want %q", e.Action, "agents.list")
	}
	if e.Result != "success" {
		t.Errorf("Result: got %q, want %q", e.Result, "success")
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestGetAuditByTrace(t *testing.T) {
	s := newTestStore(t)

	// Write multiple audit entries for the same trace
	traceID := "t_multistep"
	actions := []string{"command.received", "db.query", "response.sent"}

	for _, action := range actions {
		if err := s.WriteAudit(traceID, "@admin:example.com", action, "", "success", nil, ""); err != nil {
			t.Fatalf("WriteAudit(%s): %v", action, err)
		}
	}

	// Write one with a different trace
	if err := s.WriteAudit("t_other", "@admin:example.com", "other.action", "", "success", nil, ""); err != nil {
		t.Fatalf("WriteAudit(other): %v", err)
	}

	entries, err := s.GetAuditByTrace(traceID)
	if err != nil {
		t.Fatalf("GetAuditByTrace: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for trace, got %d", len(entries))
	}

	for i, entry := range entries {
		if entry.TraceID != traceID {
			t.Errorf("entry[%d] TraceID: got %q, want %q", i, entry.TraceID, traceID)
		}
	}
}

func TestAuditLog_ErrorEntry(t *testing.T) {
	s := newTestStore(t)

	err := s.WriteAudit(
		"t_err123",
		"@admin:example.com",
		"agents.delete",
		"bot1",
		"error",
		nil,
		"agent not found",
	)
	if err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}

	entries, err := s.GetAuditLog(10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one audit entry")
	}

	e := entries[0]
	if !e.ErrorMessage.Valid {
		t.Error("ErrorMessage should be valid")
	}
	if e.ErrorMessage.String != "agent not found" {
		t.Errorf("ErrorMessage: got %q, want %q", e.ErrorMessage.String, "agent not found")
	}
	if !e.Target.Valid || e.Target.String != "bot1" {
		t.Errorf("Target: got %q, want %q", e.Target.String, "bot1")
	}
}

func TestAuditLog_Limit(t *testing.T) {
	s := newTestStore(t)

	// Write 20 entries
	for i := 0; i < 20; i++ {
		if err := s.WriteAudit("t_bulk", "@admin:example.com", "bulk.action", "", "success", nil, ""); err != nil {
			t.Fatalf("WriteAudit: %v", err)
		}
		// Brief sleep to ensure distinct timestamps
		time.Sleep(time.Millisecond)
	}

	entries, err := s.GetAuditLog(5)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("expected 5 entries with limit=5, got %d", len(entries))
	}
}

// --- Migrations ---

func TestMigrations_Idempotent(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "ruriko-test-idempotent-*.db")
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}
	f.Close()

	// Open same database twice - migrations should only run once
	s1, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()

	s2, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
}
