package approvals_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// newTestStore opens a temporary SQLite database (with migrations applied) and
// returns an approvals.Store backed by it.  The DB is closed when the test ends.
func newTestStore(t *testing.T) *approvals.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "approvals-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()

	s, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return approvals.NewStore(s.DB())
}

// --- Store tests ---

func TestApproval_CreateAndGet(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	ap, err := as.Create(ctx, "agents.delete", "myagent", "{}", "@alice:example.com", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if ap.ID == "" {
		t.Error("expected non-empty ID")
	}
	if ap.Status != approvals.StatusPending {
		t.Errorf("expected status pending, got %q", ap.Status)
	}
	if ap.Action != "agents.delete" {
		t.Errorf("expected action agents.delete, got %q", ap.Action)
	}
	if ap.Target != "myagent" {
		t.Errorf("expected target myagent, got %q", ap.Target)
	}

	got, err := as.Get(ctx, ap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != ap.ID {
		t.Errorf("ID mismatch: %q vs %q", got.ID, ap.ID)
	}
}

func TestApproval_GetNotFound(t *testing.T) {
	as := newTestStore(t)
	_, err := as.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing approval")
	}
}

func TestApproval_Approve(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	ap, err := as.Create(ctx, "secrets.delete", "mysecret", "{}", "@alice:example.com", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := as.Approve(ctx, ap.ID, "@bob:example.com", "looks good"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	got, err := as.Get(ctx, ap.ID)
	if err != nil {
		t.Fatalf("Get after approve: %v", err)
	}
	if got.Status != approvals.StatusApproved {
		t.Errorf("expected approved, got %q", got.Status)
	}
	if got.ResolvedByMXID == nil || *got.ResolvedByMXID != "@bob:example.com" {
		t.Errorf("unexpected resolved_by: %v", got.ResolvedByMXID)
	}
}

func TestApproval_Deny(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	ap, err := as.Create(ctx, "agents.delete", "badagent", "{}", "@alice:example.com", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := as.Deny(ctx, ap.ID, "@bob:example.com", "not today"); err != nil {
		t.Fatalf("Deny: %v", err)
	}

	got, err := as.Get(ctx, ap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != approvals.StatusDenied {
		t.Errorf("expected denied, got %q", got.Status)
	}
	if got.ResolveReason == nil || *got.ResolveReason != "not today" {
		t.Errorf("unexpected reason: %v", got.ResolveReason)
	}
}

func TestApproval_DoubleApprove(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	ap, _ := as.Create(ctx, "agents.delete", "myagent", "{}", "@alice:example.com", time.Hour)
	_ = as.Approve(ctx, ap.ID, "@bob:example.com", "")

	// Second approval attempt must fail.
	err := as.Approve(ctx, ap.ID, "@charlie:example.com", "")
	if err == nil {
		t.Fatal("expected error on double-approve")
	}
}

func TestApproval_List_FilterByStatus(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	ap1, _ := as.Create(ctx, "agents.delete", "agent1", "{}", "@alice:example.com", time.Hour)
	ap2, _ := as.Create(ctx, "secrets.delete", "secret1", "{}", "@alice:example.com", time.Hour)
	_ = as.Approve(ctx, ap1.ID, "@bob:example.com", "")
	_ = ap2 // stays pending

	pending, err := as.List(ctx, string(approvals.StatusPending))
	if err != nil {
		t.Fatalf("List(pending): %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}

	approved, err := as.List(ctx, string(approvals.StatusApproved))
	if err != nil {
		t.Fatalf("List(approved): %v", err)
	}
	if len(approved) != 1 {
		t.Errorf("expected 1 approved, got %d", len(approved))
	}

	all, err := as.List(ctx, "")
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total, got %d", len(all))
	}
}

func TestApproval_ExpireStale(t *testing.T) {
	as := newTestStore(t)
	ctx := context.Background()

	// Create an approval with a very short TTL so it expires immediately.
	ap, _ := as.Create(ctx, "agents.delete", "myagent", "{}", "@alice:example.com", -time.Millisecond)

	n, err := as.ExpireStale(ctx)
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 expired, got %d", n)
	}

	got, _ := as.Get(ctx, ap.ID)
	if got.Status != approvals.StatusExpired {
		t.Errorf("expected expired, got %q", got.Status)
	}
}

func TestApproval_IsExpired(t *testing.T) {
	a := &approvals.Approval{
		Status:    approvals.StatusPending,
		ExpiresAt: time.Now().Add(-time.Second),
	}
	if !a.IsExpired() {
		t.Error("expected IsExpired() == true for past deadline")
	}

	b := &approvals.Approval{
		Status:    approvals.StatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if b.IsExpired() {
		t.Error("expected IsExpired() == false for future deadline")
	}

	c := &approvals.Approval{
		Status:    approvals.StatusApproved,
		ExpiresAt: time.Now().Add(-time.Second),
	}
	if c.IsExpired() {
		t.Error("expected IsExpired() == false for already-resolved approval")
	}
}

// --- Parser tests ---

func TestParseDecision_Approve(t *testing.T) {
	d, err := approvals.ParseDecision("approve abc123")
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if !d.Approve {
		t.Error("expected Approve == true")
	}
	if d.ApprovalID != "abc123" {
		t.Errorf("expected ID abc123, got %q", d.ApprovalID)
	}
}

func TestParseDecision_ApproveWithReason(t *testing.T) {
	d, err := approvals.ParseDecision("approve abc123 looks good to me")
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if d.Reason != "looks good to me" {
		t.Errorf("reason: %q", d.Reason)
	}
}

func TestParseDecision_DenyWithReason(t *testing.T) {
	d, err := approvals.ParseDecision(`deny abc123 reason="too risky"`)
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if d.Approve {
		t.Error("expected Approve == false")
	}
	if d.ApprovalID != "abc123" {
		t.Errorf("expected ID abc123, got %q", d.ApprovalID)
	}
	if d.Reason != "too risky" {
		t.Errorf("reason: %q", d.Reason)
	}
}

func TestParseDecision_DenyPlainReason(t *testing.T) {
	d, err := approvals.ParseDecision("deny abc123 not authorised")
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if d.Reason != "not authorised" {
		t.Errorf("reason: %q", d.Reason)
	}
}

func TestParseDecision_DenyNoReason(t *testing.T) {
	_, err := approvals.ParseDecision("deny abc123")
	if err == nil {
		t.Fatal("expected error for deny without reason")
	}
}

func TestParseDecision_NotADecision(t *testing.T) {
	_, err := approvals.ParseDecision("hello world")
	if !errors.Is(err, approvals.ErrNotADecision) {
		t.Errorf("expected ErrNotADecision, got %v", err)
	}
}

func TestParseDecision_CaseInsensitive(t *testing.T) {
	d, err := approvals.ParseDecision("Approve ABC123")
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if d.ApprovalID != "ABC123" {
		t.Errorf("expected ID ABC123, got %q", d.ApprovalID)
	}
}

func TestParseDecision_MissingID(t *testing.T) {
	_, err := approvals.ParseDecision("approve")
	if err == nil {
		t.Fatal("expected error for approve with no ID")
	}
}

// --- Gate tests ---

func TestGate_Request(t *testing.T) {
	as := newTestStore(t)
	gate := approvals.NewGate(as, time.Hour)
	ctx := context.Background()

	ap, err := gate.Request(ctx, "agents.delete", "myagent",
		[]string{"myagent"}, map[string]string{}, "@alice:example.com")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if ap.ID == "" {
		t.Error("expected non-empty ID")
	}
	if ap.Status != approvals.StatusPending {
		t.Errorf("expected pending, got %q", ap.Status)
	}
}

func TestGate_DecodeParams(t *testing.T) {
	as := newTestStore(t)
	gate := approvals.NewGate(as, time.Hour)
	ctx := context.Background()

	args := []string{"myagent"}
	flags := map[string]string{"force": "true"}

	ap, _ := gate.Request(ctx, "agents.delete", "myagent", args, flags, "@alice:example.com")

	params, err := approvals.DecodeParams(ap.ParamsJSON)
	if err != nil {
		t.Fatalf("DecodeParams: %v", err)
	}
	if len(params.Args) != 1 || params.Args[0] != "myagent" {
		t.Errorf("args: %v", params.Args)
	}
	if params.Flags["force"] != "true" {
		t.Errorf("flags: %v", params.Flags)
	}
}

func TestIsGated(t *testing.T) {
	gated := []string{"agents.delete", "agents.disable", "secrets.delete", "secrets.rotate", "gosuto.set", "gosuto.rollback"}
	for _, a := range gated {
		if !approvals.IsGated(a) {
			t.Errorf("expected %q to be gated", a)
		}
	}

	notGated := []string{"agents.list", "agents.show", "secrets.list", "ping", "help"}
	for _, a := range notGated {
		if approvals.IsGated(a) {
			t.Errorf("expected %q to NOT be gated", a)
		}
	}
}
