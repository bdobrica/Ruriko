package approvals

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

func newApprovalTestGate(t *testing.T) (*Gate, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, nil), db
}

func TestRequest_Approved_Executes(t *testing.T) {
	gate, _ := newApprovalTestGate(t)
	ctx := trace.WithTraceID(context.Background(), "t-approved")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = gate.RecordDecision("appr_t-approved", store.ApprovalApproved, "@approver:example.com", "ok")
	}()

	err := gate.Request(ctx, "!approvals:example.com", "@user:example.com", "builtin.call", "matrix.send_message", map[string]interface{}{"caller_context": "workflow"}, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("expected approved request to succeed, got: %v", err)
	}
}

func TestRequest_Denied_Refuses(t *testing.T) {
	gate, _ := newApprovalTestGate(t)
	ctx := trace.WithTraceID(context.Background(), "t-denied")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = gate.RecordDecision("appr_t-denied", store.ApprovalDenied, "@approver:example.com", "no")
	}()

	err := gate.Request(ctx, "!approvals:example.com", "@user:example.com", "builtin.call", "matrix.send_message", map[string]interface{}{"caller_context": "workflow"}, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected denied request to fail, got nil")
	}
	if !strings.Contains(err.Error(), "operation denied") {
		t.Fatalf("expected deny error, got: %v", err)
	}
}

func TestRequest_Timeout_Denies(t *testing.T) {
	gate, db := newApprovalTestGate(t)
	ctx := trace.WithTraceID(context.Background(), "t-timeout")

	err := gate.Request(ctx, "!approvals:example.com", "@user:example.com", "builtin.call", "matrix.send_message", map[string]interface{}{"caller_context": "workflow"}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout request to fail, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout deny error, got: %v", err)
	}

	status, statusErr := db.GetApprovalStatus("appr_t-timeout")
	if statusErr != nil {
		t.Fatalf("GetApprovalStatus failed: %v", statusErr)
	}
	if status != store.ApprovalDenied {
		t.Fatalf("status = %q, want %q", status, store.ApprovalDenied)
	}
}
