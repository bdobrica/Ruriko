package builtin

// Tests for the built-in tool registry and the matrix.send_message tool.
//
// Coverage:
//   Registry:
//     - Register, IsBuiltin, Get, Definitions
//   MatrixSendTool:
//     - Message sent to allowed target succeeds
//     - Message to unknown target is rejected
//     - Rate limit is enforced
//     - Tool is visible to LLM in tool list
//     - Missing required arguments are rejected
//     - No Gosuto config → error
//
// All tests use local stubs — no real Matrix homeserver is required.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// --- stubs ---

// stubSender records every SendText call without doing real Matrix I/O.
type stubSender struct {
	calls []sendCall
	err   error // if non-nil, SendText returns this error
}

type sendCall struct {
	roomID  string
	message string
}

func (s *stubSender) SendText(roomID, text string) error {
	s.calls = append(s.calls, sendCall{roomID: roomID, message: text})
	return s.err
}

// staticConfigProvider returns a fixed *gosutospec.Config (or nil).
type staticConfigProvider struct {
	cfg *gosutospec.Config
}

func (p *staticConfigProvider) Config() *gosutospec.Config { return p.cfg }

// configWithMessaging builds a minimal Gosuto config with messaging targets and rate limit.
func configWithMessaging(targets []gosutospec.MessagingTarget, maxPerMin int) *gosutospec.Config {
	return &gosutospec.Config{
		APIVersion: gosutospec.SpecVersion,
		Metadata:   gosutospec.Metadata{Name: "test-agent"},
		Messaging: gosutospec.Messaging{
			AllowedTargets:       targets,
			MaxMessagesPerMinute: maxPerMin,
		},
	}
}

// --- Registry tests ---

func TestRegistry_RegisterAndLookup(t *testing.T) {
	reg := New()
	tool := NewMatrixSendTool(&staticConfigProvider{}, &stubSender{})
	reg.Register(tool)

	if !reg.IsBuiltin(MatrixSendToolName) {
		t.Errorf("IsBuiltin(%q) = false, want true after registration", MatrixSendToolName)
	}
	if got := reg.Get(MatrixSendToolName); got == nil {
		t.Errorf("Get(%q) = nil, want registered tool", MatrixSendToolName)
	}
	if !reg.IsBuiltin("matrix.send_message") {
		t.Errorf("IsBuiltin(literal name) = false")
	}
}

func TestRegistry_IsBuiltin_UnknownName(t *testing.T) {
	reg := New()
	if reg.IsBuiltin("does.not.exist") {
		t.Error("IsBuiltin on empty registry returned true")
	}
}

func TestRegistry_Definitions_ContainsTool(t *testing.T) {
	reg := New()
	reg.Register(NewMatrixSendTool(&staticConfigProvider{}, &stubSender{}))

	defs := reg.Definitions()
	if len(defs) != 1 {
		t.Fatalf("Definitions() length = %d, want 1", len(defs))
	}
	if defs[0].Function.Name != MatrixSendToolName {
		t.Errorf("Definitions()[0].Function.Name = %q, want %q", defs[0].Function.Name, MatrixSendToolName)
	}
	if defs[0].Type != "function" {
		t.Errorf("Definitions()[0].Type = %q, want %q", defs[0].Type, "function")
	}
}

func TestRegistry_DuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration, got none")
		}
	}()
	reg := New()
	reg.Register(NewMatrixSendTool(&staticConfigProvider{}, &stubSender{}))
	reg.Register(NewMatrixSendTool(&staticConfigProvider{}, &stubSender{})) // should panic
}

// --- MatrixSendTool: tool definition (R15.2: tool visible to LLM) ---

func TestMatrixSendTool_Definition_ExposedToLLM(t *testing.T) {
	tool := NewMatrixSendTool(&staticConfigProvider{}, &stubSender{})
	def := tool.Definition()

	if def.Function.Name != MatrixSendToolName {
		t.Errorf("Name = %q, want %q", def.Function.Name, MatrixSendToolName)
	}
	if def.Function.Description == "" {
		t.Error("Description must not be empty")
	}
	params, ok := def.Function.Parameters.(map[string]interface{})
	if !ok {
		t.Fatal("Parameters is not map[string]interface{}")
	}
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters.properties is missing or wrong type")
	}
	if _, hasTarget := props["target"]; !hasTarget {
		t.Error("Parameters.properties missing 'target'")
	}
	if _, hasMessage := props["message"]; !hasMessage {
		t.Error("Parameters.properties missing 'message'")
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("Parameters.required is missing or wrong type")
	}
	requiredSet := make(map[string]bool, len(required))
	for _, r := range required {
		requiredSet[r] = true
	}
	if !requiredSet["target"] {
		t.Error("'target' is not in required fields")
	}
	if !requiredSet["message"] {
		t.Error("'message' is not in required fields")
	}
}

// --- MatrixSendTool: Execute (R15.2: message sent to allowed target succeeds) ---

func TestMatrixSendTool_Execute_AllowedTarget_Succeeds(t *testing.T) {
	sender := &stubSender{}
	cfg := configWithMessaging([]gosutospec.MessagingTarget{
		{RoomID: "!kairo:localhost", Alias: "kairo"},
		{RoomID: "!user-dm:localhost", Alias: "user"},
	}, 0 /* unlimited */)

	tool := NewMatrixSendTool(&staticConfigProvider{cfg}, sender)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"target":  "kairo",
		"message": "Hello, Kairo!",
	})

	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	if sender.calls[0].roomID != "!kairo:localhost" {
		t.Errorf("sent to room %q, want %q", sender.calls[0].roomID, "!kairo:localhost")
	}
	if sender.calls[0].message != "Hello, Kairo!" {
		t.Errorf("sent message %q, want %q", sender.calls[0].message, "Hello, Kairo!")
	}
	if !strings.Contains(result, "kairo") {
		t.Errorf("success result %q should mention target alias", result)
	}
}

// --- MatrixSendTool: Execute (R15.2: message to unknown target is rejected) ---

func TestMatrixSendTool_Execute_UnknownTarget_Rejected(t *testing.T) {
	sender := &stubSender{}
	cfg := configWithMessaging([]gosutospec.MessagingTarget{
		{RoomID: "!kairo:localhost", Alias: "kairo"},
	}, 0)

	tool := NewMatrixSendTool(&staticConfigProvider{cfg}, sender)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"target":  "unknown-agent",
		"message": "Hello!",
	})

	if err == nil {
		t.Fatal("expected error for unknown target, got nil")
	}
	if !strings.Contains(err.Error(), "not in the allowed targets list") {
		t.Errorf("error message %q should mention allowlist rejection", err.Error())
	}
	if len(sender.calls) != 0 {
		t.Error("SendText must not be called when target is unknown")
	}
}

// --- MatrixSendTool: Execute (R15.2: rate limit is enforced) ---

func TestMatrixSendTool_Execute_RateLimitEnforced(t *testing.T) {
	sender := &stubSender{}
	const maxPerMin = 3
	cfg := configWithMessaging([]gosutospec.MessagingTarget{
		{RoomID: "!room:localhost", Alias: "peer"},
	}, maxPerMin)

	tool := NewMatrixSendTool(&staticConfigProvider{cfg}, sender)
	args := map[string]interface{}{"target": "peer", "message": "ping"}

	// The first maxPerMin calls should succeed.
	for i := 0; i < maxPerMin; i++ {
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	// The next call must be rejected by the rate limiter.
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected rate-limit error on call beyond limit, got nil")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("error message %q should mention rate limit", err.Error())
	}
	// Sender should have been called exactly maxPerMin times.
	if len(sender.calls) != maxPerMin {
		t.Errorf("sender called %d times, want %d", len(sender.calls), maxPerMin)
	}
}

func TestMatrixSendTool_Execute_UnlimitedRate(t *testing.T) {
	sender := &stubSender{}
	cfg := configWithMessaging([]gosutospec.MessagingTarget{
		{RoomID: "!room:localhost", Alias: "peer"},
	}, 0 /* unlimited */)

	tool := NewMatrixSendTool(&staticConfigProvider{cfg}, sender)
	args := map[string]interface{}{"target": "peer", "message": "ping"}

	for i := 0; i < 100; i++ {
		if _, err := tool.Execute(context.Background(), args); err != nil {
			t.Fatalf("call %d with unlimited rate: unexpected error: %v", i+1, err)
		}
	}
}

// --- MatrixSendTool: Execute — argument validation ---

func TestMatrixSendTool_Execute_MissingTarget(t *testing.T) {
	tool := NewMatrixSendTool(&staticConfigProvider{configWithMessaging(nil, 0)}, &stubSender{})
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"message": "Hello!",
	})
	if err == nil {
		t.Fatal("expected error for missing 'target'")
	}
	if !strings.Contains(err.Error(), "missing required argument 'target'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMatrixSendTool_Execute_MissingMessage(t *testing.T) {
	tool := NewMatrixSendTool(&staticConfigProvider{configWithMessaging(nil, 0)}, &stubSender{})
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"target": "kairo",
	})
	if err == nil {
		t.Fatal("expected error for missing 'message'")
	}
	if !strings.Contains(err.Error(), "missing required argument 'message'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMatrixSendTool_Execute_NoConfig(t *testing.T) {
	tool := NewMatrixSendTool(&staticConfigProvider{nil /* no config */}, &stubSender{})
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"target":  "kairo",
		"message": "Hello!",
	})
	if err == nil {
		t.Fatal("expected error when no Gosuto config is loaded")
	}
	if !strings.Contains(err.Error(), "no Gosuto config loaded") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- MatrixSendTool: sender error is propagated ---

func TestMatrixSendTool_Execute_SenderError(t *testing.T) {
	sender := &stubSender{err: fmt.Errorf("matrix server unavailable")}
	cfg := configWithMessaging([]gosutospec.MessagingTarget{
		{RoomID: "!room:localhost", Alias: "peer"},
	}, 0)
	tool := NewMatrixSendTool(&staticConfigProvider{cfg}, sender)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"target":  "peer",
		"message": "hi",
	})
	if err == nil {
		t.Fatal("expected sender error to be propagated")
	}
	if !strings.Contains(err.Error(), "send failed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// --- resolveTarget ---

func TestResolveTarget_Found(t *testing.T) {
	targets := []gosutospec.MessagingTarget{
		{RoomID: "!a:localhost", Alias: "alpha"},
		{RoomID: "!b:localhost", Alias: "beta"},
	}
	id, ok := resolveTarget(targets, "beta")
	if !ok {
		t.Fatal("resolveTarget returned false for known alias")
	}
	if id != "!b:localhost" {
		t.Errorf("got room %q, want %q", id, "!b:localhost")
	}
}

func TestResolveTarget_NotFound(t *testing.T) {
	targets := []gosutospec.MessagingTarget{
		{RoomID: "!a:localhost", Alias: "alpha"},
	}
	_, ok := resolveTarget(targets, "gamma")
	if ok {
		t.Error("resolveTarget returned true for unknown alias")
	}
}

func TestResolveTarget_EmptyList(t *testing.T) {
	_, ok := resolveTarget(nil, "anyone")
	if ok {
		t.Error("resolveTarget returned true for empty target list")
	}
}
