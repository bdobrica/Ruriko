package app

// Tests for R15.5: Audit and Observability for matrix.send_message.
//
// Coverage:
//   - executeBuiltinTool emits an INFO slog record with the expected fields
//     (agent_id, target, room_id, status) on every matrix.send_message call.
//   - INFO log never contains message content.
//   - Admin room breadcrumb is posted on success; includes target alias and trace ID.
//   - No breadcrumb is posted on failure.
//   - msgOutbound counter is incremented on success, not on failure.
//   - Status field in the log record is "error" when the call fails.
//
// All tests are whitebox (package app) and use lightweight in-process stubs —
// no Matrix homeserver is required.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
)

// --- stubs ---

// auditRecordingEventSender records SendText calls.  Used as the App's
// eventSender to capture breadcrumbs posted to the admin room.
type auditRecordingEventSender struct {
	mu    sync.Mutex
	calls []auditSendCall
}

type auditSendCall struct {
	roomID string
	text   string
}

func (s *auditRecordingEventSender) SendText(roomID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, auditSendCall{roomID, text})
	return nil
}

// snapshot returns a copy of all recorded calls.
func (s *auditRecordingEventSender) snapshot() []auditSendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]auditSendCall, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// --- slog capturing infrastructure ---

// capturedLogRecord holds the fields of a single slog.Record captured during
// a test.
type capturedLogRecord struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

// capturingLogHandler captures every slog record emitted through the default
// logger.  WithAttrs returns the same handler (attrs from slog.With calls are
// not needed for the tests here, which only check direct log.Info attrs).
type capturingLogHandler struct {
	mu   sync.Mutex
	recs []capturedLogRecord
}

func (h *capturingLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *capturingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler         { return h }
func (h *capturingLogHandler) WithGroup(_ string) slog.Handler              { return h }
func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedLogRecord{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	return nil
}

// snapshot returns a copy of all captured records.
func (h *capturingLogHandler) snapshot() []capturedLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]capturedLogRecord, len(h.recs))
	copy(cp, h.recs)
	return cp
}

// installCapturingLogger replaces the global slog default with a capturing
// handler and restores the original on t.Cleanup.
func installCapturingLogger(t *testing.T) *capturingLogHandler {
	t.Helper()
	h := &capturingLogHandler{}
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
	slog.SetDefault(slog.New(h))
	return h
}

// --- helpers ---

// newAuditTestApp constructs a minimal App for audit logging tests:
//   - Gosuto loader with gosutoYAML applied
//   - Policy engine wired to the same loader
//   - Built-in registry with matrix.send_message (backed by toolPolicyStubbedSender)
//   - eventSender set to eventSnd for breadcrumb capture
//   - cfg.AgentID set to "test-agent-r155"
//
// toolPolicyConfigProvider and toolPolicyStubbedSender are defined in
// tools_policy_test.go in the same package.
func newAuditTestApp(t *testing.T, gosutoYAML string, eventSnd eventMatrixSender) *App {
	t.Helper()

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(gosutoYAML)); err != nil {
		t.Fatalf("gosuto loader Apply: %v", err)
	}

	reg := builtin.New()
	reg.Register(builtin.NewMatrixSendTool(&toolPolicyConfigProvider{ldr: ldr}, toolPolicyStubbedSender{}))

	return &App{
		cfg:         &Config{AgentID: "test-agent-r155"},
		gosutoLdr:   ldr,
		policyEng:   policy.New(ldr),
		builtinReg:  reg,
		eventSender: eventSnd,
		cancelCh:    make(chan struct{}, 1),
	}
}

// makeMatrixSendToolCall builds an llm.ToolCall for matrix.send_message with
// the given target alias and message content.
func makeMatrixSendToolCall(target, message string) llm.ToolCall {
	raw, _ := json.Marshal(map[string]string{"target": target, "message": message})
	return llm.ToolCall{
		ID:   "call_audit_test",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      builtin.MatrixSendToolName,
			Arguments: string(raw),
		},
	}
}

// --- R15.5 audit logging tests ---

// TestAuditMessagingSend_LogsAtInfo verifies that executeBuiltinTool emits an
// INFO slog record for matrix.send_message with the required fields.
func TestAuditMessagingSend_LogsAtInfo(t *testing.T) {
	h := installCapturingLogger(t)
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, &auditRecordingEventSender{})

	ctx := trace.WithTraceID(context.Background(), "trace_abc123")
	tc := makeMatrixSendToolCall("kairo", "Hello, Kairo!")
	_, _ = a.executeBuiltinTool(ctx, "@user:example.com", tc)

	var found *capturedLogRecord
	for _, rec := range h.snapshot() {
		if rec.message == "matrix.send_message" && rec.level == slog.LevelInfo {
			cp := rec
			found = &cp
			break
		}
	}
	if found == nil {
		t.Fatal(`audit: no INFO log record with message "matrix.send_message" was emitted`)
	}
	if got := found.attrs["agent_id"]; got != "test-agent-r155" {
		t.Errorf("audit log agent_id = %q, want %q", got, "test-agent-r155")
	}
	if got := found.attrs["target"]; got != "kairo" {
		t.Errorf("audit log target = %q, want %q", got, "kairo")
	}
	if got := found.attrs["room_id"]; got != "!kairo-admin:localhost" {
		t.Errorf("audit log room_id = %q, want %q", got, "!kairo-admin:localhost")
	}
	if got := found.attrs["status"]; got != "success" {
		t.Errorf("audit log status = %q, want %q", got, "success")
	}
}

// TestAuditMessagingSend_NeverLogsContent verifies that the INFO log record
// for matrix.send_message never contains the message body.
// Invariant §8: secrets (and in general user content) must never leak into
// INFO-level logs.
func TestAuditMessagingSend_NeverLogsContent(t *testing.T) {
	h := installCapturingLogger(t)
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, &auditRecordingEventSender{})

	tc := makeMatrixSendToolCall("kairo", "super-secret-content")
	_, _ = a.executeBuiltinTool(context.Background(), "@user:example.com", tc)

	const sensitive = "super-secret-content"
	for _, rec := range h.snapshot() {
		if rec.level != slog.LevelInfo {
			continue
		}
		if strings.Contains(rec.message, sensitive) {
			t.Errorf("INFO log message %q contains content", rec.message)
		}
		for k, v := range rec.attrs {
			if strings.Contains(v, sensitive) {
				t.Errorf("INFO log attr %q = %q contains content (invariant §8)", k, v)
			}
		}
	}
}

// TestAuditMessagingSend_PostsBreadcrumbToAdminRoom verifies that a successful
// matrix.send_message call posts an audit breadcrumb to the agent's admin room.
// The breadcrumb must reference the target alias and the current trace ID.
func TestAuditMessagingSend_PostsBreadcrumbToAdminRoom(t *testing.T) {
	sender := &auditRecordingEventSender{}
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, sender)

	const testTraceID = "trace_breadcrumb_test"
	ctx := trace.WithTraceID(context.Background(), testTraceID)
	tc := makeMatrixSendToolCall("kairo", "Hello!")
	_, err := a.executeBuiltinTool(ctx, "@user:example.com", tc)
	if err != nil {
		t.Fatalf("executeBuiltinTool: unexpected error: %v", err)
	}

	var breadcrumb *auditSendCall
	for i, call := range sender.snapshot() {
		if call.roomID == "!admin-room:example.com" {
			cp := sender.snapshot()[i]
			breadcrumb = &cp
			break
		}
	}
	if breadcrumb == nil {
		t.Fatal("audit: no breadcrumb was sent to the admin room (!admin-room:example.com)")
	}
	if !strings.Contains(breadcrumb.text, "kairo") {
		t.Errorf("breadcrumb %q does not mention target alias \"kairo\"", breadcrumb.text)
	}
	if !strings.Contains(breadcrumb.text, testTraceID) {
		t.Errorf("breadcrumb %q does not include trace ID %q", breadcrumb.text, testTraceID)
	}
}

// TestAuditMessagingSend_ErrorPath_NoBreadcrumb verifies that no breadcrumb is
// posted to the admin room when matrix.send_message fails (e.g. unknown target).
func TestAuditMessagingSend_ErrorPath_NoBreadcrumb(t *testing.T) {
	sender := &auditRecordingEventSender{}
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, sender)

	// Unknown target → MatrixSendTool.Execute returns an error.
	tc := makeMatrixSendToolCall("unknown-agent", "Hello!")
	_, _ = a.executeBuiltinTool(context.Background(), "@user:example.com", tc)

	for _, call := range sender.snapshot() {
		if call.roomID == "!admin-room:example.com" {
			t.Errorf("breadcrumb must not be posted on error; got send to admin room: %+v", call)
		}
	}
}

// TestAuditMessagingSend_IncrementsCounterOnSuccess verifies that the
// msgOutbound atomic counter increments once per successful call.
func TestAuditMessagingSend_IncrementsCounterOnSuccess(t *testing.T) {
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, &auditRecordingEventSender{})

	ctx := context.Background()
	tc := makeMatrixSendToolCall("kairo", "Hello!")

	if got := a.msgOutbound.Load(); got != 0 {
		t.Fatalf("initial counter = %d, want 0", got)
	}
	_, _ = a.executeBuiltinTool(ctx, "@user:example.com", tc)
	if got := a.msgOutbound.Load(); got != 1 {
		t.Errorf("counter after 1 success = %d, want 1", got)
	}
	_, _ = a.executeBuiltinTool(ctx, "@user:example.com", tc)
	if got := a.msgOutbound.Load(); got != 2 {
		t.Errorf("counter after 2 successes = %d, want 2", got)
	}
}

// TestAuditMessagingSend_CounterNotIncrementedOnError verifies that the
// msgOutbound counter stays at zero when matrix.send_message fails.
func TestAuditMessagingSend_CounterNotIncrementedOnError(t *testing.T) {
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, &auditRecordingEventSender{})

	// Unknown target → Execute returns error; counter must not increment.
	tc := makeMatrixSendToolCall("unknown", "Hello!")
	_, _ = a.executeBuiltinTool(context.Background(), "@user:example.com", tc)

	if got := a.msgOutbound.Load(); got != 0 {
		t.Errorf("counter after failed call = %d, want 0", got)
	}
}

// TestAuditMessagingSend_LogsStatusError verifies that the INFO log record for
// a failed matrix.send_message call has status="error".
func TestAuditMessagingSend_LogsStatusError(t *testing.T) {
	h := installCapturingLogger(t)
	a := newAuditTestApp(t, toolsPolicyTestGosutoYAML_WithMessaging, &auditRecordingEventSender{})

	tc := makeMatrixSendToolCall("unknown-target", "Hello!")
	_, _ = a.executeBuiltinTool(context.Background(), "@user:example.com", tc)

	var found *capturedLogRecord
	for _, rec := range h.snapshot() {
		if rec.message == "matrix.send_message" && rec.level == slog.LevelInfo {
			cp := rec
			found = &cp
			break
		}
	}
	if found == nil {
		t.Fatal(`audit: no INFO log record with message "matrix.send_message" was emitted on error path`)
	}
	if got := found.attrs["status"]; got != "error" {
		t.Errorf("audit log status = %q, want %q on error path", got, "error")
	}
}
