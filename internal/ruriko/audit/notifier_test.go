package audit_test

import (
	"context"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
)

// fakeSender records notices for assertion.
type fakeSender struct {
	notices []string
}

func (f *fakeSender) SendNotice(_, msg string) error {
	f.notices = append(f.notices, msg)
	return nil
}

func TestMatrixNotifier_SendsNotice(t *testing.T) {
	sender := &fakeSender{}
	n := audit.NewMatrixNotifier(sender, "!room:example.com")

	n.Notify(context.Background(), audit.Event{
		Kind:    audit.KindAgentCreated,
		Actor:   "@alice:example.com",
		Target:  "my-agent",
		Message: "created",
		TraceID: "t_abc123",
	})

	if len(sender.notices) != 1 {
		t.Fatalf("expected 1 notice, got %d", len(sender.notices))
	}
	msg := sender.notices[0]
	for _, want := range []string{"my-agent", "created", "t_abc123", "@alice:example.com"} {
		if !containsStr(msg, want) {
			t.Errorf("notice missing %q: %q", want, msg)
		}
	}
}

func TestMatrixNotifier_NoopWhenEmptyRoom(t *testing.T) {
	sender := &fakeSender{}
	n := audit.NewMatrixNotifier(sender, "")

	n.Notify(context.Background(), audit.Event{
		Kind:    audit.KindAgentDeleted,
		Message: "deleted",
	})

	if len(sender.notices) != 0 {
		t.Fatalf("expected no notices for empty room, got %d", len(sender.notices))
	}
}

func TestNoop(t *testing.T) {
	// Must not panic.
	audit.Noop{}.Notify(context.Background(), audit.Event{Kind: audit.KindError, Message: "boom"})
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsRune(s, sub))
}

func containsRune(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
