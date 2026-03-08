package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

func TestIsAgentBreadcrumbMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "emoji breadcrumb",
			text: "📨 Sent message to kairo (trace=abc123)",
			want: true,
		},
		{
			name: "plain breadcrumb",
			text: "Sent message to kairo (trace=abc123)",
			want: true,
		},
		{
			name: "missing trace marker",
			text: "📨 Sent message to kairo",
			want: false,
		},
		{
			name: "normal sentence",
			text: "can you list agents",
			want: false,
		},
		{
			name: "empty",
			text: "",
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isAgentBreadcrumbMessage(tt.text); got != tt.want {
				t.Fatalf("isAgentBreadcrumbMessage(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestShouldIgnoreAgentBreadcrumb_KnownAgentSender(t *testing.T) {
	t.Parallel()

	s, err := store.New(filepath.Join(t.TempDir(), "ruriko-test.db"))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	err = s.CreateAgent(context.Background(), &store.Agent{
		ID:          "saito",
		MXID:        sql.NullString{String: "@saito:example.com", Valid: true},
		DisplayName: "Saito",
		Template:    "saito-agent",
		Status:      "running",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	a := &App{store: s}
	if !a.shouldIgnoreAgentBreadcrumb(context.Background(), "@saito:example.com", "📨 Sent message to kairo (trace=abc)") {
		t.Fatal("expected known-agent breadcrumb to be ignored")
	}

	if a.shouldIgnoreAgentBreadcrumb(context.Background(), "@alice:example.com", "📨 Sent message to kairo (trace=abc)") {
		t.Fatal("expected unknown sender breadcrumb not to be ignored")
	}

	if a.shouldIgnoreAgentBreadcrumb(context.Background(), "@saito:example.com", "hello there") {
		t.Fatal("expected non-breadcrumb message not to be ignored")
	}
}
