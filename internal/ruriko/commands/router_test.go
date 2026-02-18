package commands_test

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/internal/ruriko/commands"
)

func TestParseCommand_Basic(t *testing.T) {
	router := commands.NewRouter("/ruriko")

	tests := []struct {
		input     string
		wantName  string
		wantSub   string
		wantArgs  []string
		wantFlags map[string]string
		wantErr   bool
	}{
		{
			input:    "/ruriko help",
			wantName: "help",
			wantSub:  "",
			wantArgs: []string{},
		},
		{
			input:    "/ruriko ping",
			wantName: "ping",
			wantSub:  "",
		},
		{
			input:    "/ruriko agents list",
			wantName: "agents",
			wantSub:  "list",
			wantArgs: []string{},
		},
		{
			input:    "/ruriko agents show weatherbot",
			wantName: "agents",
			wantSub:  "show",
			wantArgs: []string{"weatherbot"},
		},
		{
			input:     "/ruriko agents create --template cron --name weatherbot",
			wantName:  "agents",
			wantSub:   "create",
			wantArgs:  []string{},
			wantFlags: map[string]string{"template": "cron", "name": "weatherbot"},
		},
		{
			input:    "/ruriko audit tail 20",
			wantName: "audit",
			wantSub:  "tail",
			wantArgs: []string{"20"},
		},
		{
			input:    "/ruriko trace t_abc123",
			wantName: "trace",
			wantSub:  "t_abc123",
			wantArgs: []string{},
		},
		{
			input:   "not a command",
			wantErr: true,
		},
		{
			input:   "/ruriko",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, err := router.Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cmd.Name != tt.wantName {
				t.Errorf("Name: got %q, want %q", cmd.Name, tt.wantName)
			}
			if cmd.Subcommand != tt.wantSub {
				t.Errorf("Subcommand: got %q, want %q", cmd.Subcommand, tt.wantSub)
			}

			if tt.wantArgs != nil {
				if len(cmd.Args) != len(tt.wantArgs) {
					t.Errorf("Args length: got %d, want %d (args=%v)", len(cmd.Args), len(tt.wantArgs), cmd.Args)
				} else {
					for i, want := range tt.wantArgs {
						if cmd.Args[i] != want {
							t.Errorf("Args[%d]: got %q, want %q", i, cmd.Args[i], want)
						}
					}
				}
			}

			if tt.wantFlags != nil {
				for k, v := range tt.wantFlags {
					got, ok := cmd.Flags[k]
					if !ok {
						t.Errorf("missing flag %q", k)
					} else if got != v {
						t.Errorf("flag %q: got %q, want %q", k, got, v)
					}
				}
			}
		})
	}
}

// TestParseCommand_InternalFlagStripping verifies that flags prefixed with "_"
// are silently stripped during parsing and cannot be injected by user input.
// This prevents bypass of the approval gate via "--_approved true".
func TestParseCommand_InternalFlagStripping(t *testing.T) {
	router := commands.NewRouter("/ruriko")

	tests := []struct {
		input         string
		strippedFlags []string // flags that must NOT appear in the parsed command
		keptFlags     map[string]string
	}{
		{
			// Classic bypass attempt
			input:         "/ruriko agents delete mybot --_approved true",
			strippedFlags: []string{"_approved"},
		},
		{
			// Full injection with all internal flags
			input:         "/ruriko agents delete mybot --_approved true --_approval_id abc123 --_trace_id t_xyz",
			strippedFlags: []string{"_approved", "_approval_id", "_trace_id"},
		},
		{
			// Mixed: internal flags stripped, regular flags kept
			input:         "/ruriko gosuto set mybot --_approved true --content abc",
			strippedFlags: []string{"_approved"},
			keptFlags:     map[string]string{"content": "abc"},
		},
		{
			// Internal boolean flag (no value)
			input:         "/ruriko agents delete mybot --_approved",
			strippedFlags: []string{"_approved"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, err := router.Parse(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for _, flag := range tt.strippedFlags {
				if _, ok := cmd.Flags[flag]; ok {
					t.Errorf("internal flag %q must be stripped from user input but was present", flag)
				}
			}

			for k, v := range tt.keptFlags {
				if got, ok := cmd.Flags[k]; !ok {
					t.Errorf("regular flag %q must be kept but was missing", k)
				} else if got != v {
					t.Errorf("flag %q: got %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestRouteCommand_UnknownCommand(t *testing.T) {
	router := commands.NewRouter("/ruriko")
	ctx := context.Background()

	_, err := router.Route(ctx, "/ruriko notacommand", &event.Event{})
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

func TestRouteCommand_RegisteredHandler(t *testing.T) {
	router := commands.NewRouter("/ruriko")
	called := false

	router.Register("ping", func(ctx context.Context, cmd *commands.Command, evt *event.Event) (string, error) {
		called = true
		return "pong", nil
	})

	ctx := context.Background()
	response, err := router.Route(ctx, "/ruriko ping", &event.Event{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}
	if response != "pong" {
		t.Errorf("response: got %q, want %q", response, "pong")
	}
}

func TestCommandGetFlag(t *testing.T) {
	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents create --template cron --name weatherbot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := cmd.GetFlag("template", ""); got != "cron" {
		t.Errorf("GetFlag(template): got %q, want %q", got, "cron")
	}
	if got := cmd.GetFlag("name", ""); got != "weatherbot" {
		t.Errorf("GetFlag(name): got %q, want %q", got, "weatherbot")
	}
	if got := cmd.GetFlag("missing", "default"); got != "default" {
		t.Errorf("GetFlag(missing): got %q, want %q", got, "default")
	}
}

func TestCommandGetArg(t *testing.T) {
	router := commands.NewRouter("/ruriko")
	cmd, err := router.Parse("/ruriko agents show weatherbot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if val, ok := cmd.GetArg(0); !ok || val != "weatherbot" {
		t.Errorf("GetArg(0): got (%q, %v), want (%q, true)", val, ok, "weatherbot")
	}
	if _, ok := cmd.GetArg(1); ok {
		t.Error("GetArg(1): expected false for out-of-bounds, got true")
	}
}

func TestCommandFullCommand(t *testing.T) {
	router := commands.NewRouter("/ruriko")

	cmd, _ := router.Parse("/ruriko agents list")
	if got := cmd.FullCommand(); got != "agents list" {
		t.Errorf("FullCommand: got %q, want %q", got, "agents list")
	}

	cmd, _ = router.Parse("/ruriko ping")
	if got := cmd.FullCommand(); got != "ping" {
		t.Errorf("FullCommand: got %q, want %q", got, "ping")
	}
}
