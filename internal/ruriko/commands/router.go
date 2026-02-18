// Package commands provides command parsing and routing for Ruriko
package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/event"
)

// Command represents a parsed command
type Command struct {
	Name       string
	Subcommand string
	Args       []string
	Flags      map[string]string
	RawText    string
}

// ErrNotACommand is returned by Parse when the message does not start with the
// command prefix. Callers should use errors.Is to distinguish this expected
// case from real errors.
var ErrNotACommand = errors.New("not a command (missing prefix)")

// Handler is a function that handles a command
type Handler func(ctx context.Context, cmd *Command, evt *event.Event) (string, error)

// Router routes commands to handlers
type Router struct {
	handlers map[string]Handler
	prefix   string
}

// NewRouter creates a new command router
func NewRouter(prefix string) *Router {
	return &Router{
		handlers: make(map[string]Handler),
		prefix:   prefix,
	}
}

// Register registers a command handler
func (r *Router) Register(command string, handler Handler) {
	r.handlers[command] = handler
}

// Parse parses a message into a command
func (r *Router) Parse(text string) (*Command, error) {
	text = strings.TrimSpace(text)

	// Check if message starts with our prefix
	if !strings.HasPrefix(text, r.prefix) {
		return nil, ErrNotACommand
	}

	// Remove prefix
	text = strings.TrimSpace(strings.TrimPrefix(text, r.prefix))
	if text == "" {
		return nil, fmt.Errorf("empty command")
	}

	// Split into parts
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	cmd := &Command{
		Name:    parts[0],
		Args:    []string{},
		Flags:   make(map[string]string),
		RawText: text,
	}

	// Parse subcommand and arguments
	if len(parts) > 1 {
		// Check if second part is a subcommand or argument
		if !strings.HasPrefix(parts[1], "-") {
			cmd.Subcommand = parts[1]
			parts = parts[2:]
		} else {
			parts = parts[1:]
		}

		// Parse remaining arguments and flags
		for i := 0; i < len(parts); i++ {
			part := parts[i]

			// Flag (starts with --)
			if strings.HasPrefix(part, "--") {
				flagName := strings.TrimPrefix(part, "--")

				// Check if flag has a value
				if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "--") {
					cmd.Flags[flagName] = parts[i+1]
					i++ // Skip next part
				} else {
					cmd.Flags[flagName] = "true"
				}
			} else {
				// Regular argument
				cmd.Args = append(cmd.Args, part)
			}
		}
	}

	return cmd, nil
}

// Dispatch calls the registered handler for the given action key directly,
// without parsing the raw text.  This is used by the approval workflow to
// re-execute a gated operation after it has been approved.
func (r *Router) Dispatch(ctx context.Context, action string, cmd *Command, evt *event.Event) (string, error) {
	handler, ok := r.handlers[action]
	if !ok {
		return "", fmt.Errorf("no handler registered for action %q", action)
	}
	return handler(ctx, cmd, evt)
}

// Route parses and routes a command to its handler
func (r *Router) Route(ctx context.Context, text string, evt *event.Event) (string, error) {
	cmd, err := r.Parse(text)
	if err != nil {
		return "", err
	}

	// Build handler key
	handlerKey := cmd.Name
	if cmd.Subcommand != "" {
		handlerKey = cmd.Name + "." + cmd.Subcommand
	}

	// Find handler
	handler, ok := r.handlers[handlerKey]
	if !ok {
		// Try just the command name
		handler, ok = r.handlers[cmd.Name]
		if !ok {
			return "", fmt.Errorf("unknown command: %s", handlerKey)
		}
	}

	// Execute handler
	return handler(ctx, cmd, evt)
}

// GetFlag returns a flag value with a default
func (c *Command) GetFlag(name, defaultValue string) string {
	if val, ok := c.Flags[name]; ok {
		return val
	}
	return defaultValue
}

// HasFlag checks if a flag is present
func (c *Command) HasFlag(name string) bool {
	_, ok := c.Flags[name]
	return ok
}

// GetArg returns an argument by index
func (c *Command) GetArg(index int) (string, bool) {
	if index < 0 || index >= len(c.Args) {
		return "", false
	}
	return c.Args[index], true
}

// FullCommand returns the full command string
func (c *Command) FullCommand() string {
	if c.Subcommand != "" {
		return c.Name + " " + c.Subcommand
	}
	return c.Name
}
