package builtin

import (
	"context"
	"fmt"
	"sync"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
)

// MatrixSendToolName is the canonical name of the built-in messaging tool
// exposed to the LLM. It uses a dot separator (not __) to make clear this is
// not an MCP tool and to avoid collisions with MCP-generated names.
const MatrixSendToolName = "matrix.send_message"

// MatrixSender is the subset of the Matrix client required by MatrixSendTool.
// It is satisfied by *matrix.Client and can be replaced with a recording
// stub in unit tests without requiring a real homeserver connection.
type MatrixSender interface {
	SendText(roomID, text string) error
}

// MessagingConfigProvider provides the active Gosuto config used to resolve
// targets and retrieve the current rate limit setting. It is satisfied by
// *gosuto.Loader.
type MessagingConfigProvider interface {
	Config() *gosutospec.Config
}

// MatrixSendTool implements the matrix.send_message built-in tool.
//
// On each Execute call it:
//  1. Validates the "target" and "message" arguments are present.
//  2. Resolves the target alias → Matrix room ID from
//     cfg.Messaging.AllowedTargets (default deny: unknown targets are
//     rejected with an error returned to the LLM).
//  3. Enforces the per-minute rate limit from cfg.Messaging.MaxMessagesPerMinute.
//  4. Sends the message via the Matrix client.
//  5. Returns a success or failure string to the LLM.
type MatrixSendTool struct {
	cfg    MessagingConfigProvider
	sender MatrixSender
	rl     *rateLimiter
}

// NewMatrixSendTool constructs a MatrixSendTool backed by the given config
// provider and Matrix sender. Both must be non-nil.
func NewMatrixSendTool(cfg MessagingConfigProvider, sender MatrixSender) *MatrixSendTool {
	return &MatrixSendTool{
		cfg:    cfg,
		sender: sender,
		rl:     &rateLimiter{},
	}
}

// Definition returns the LLM-facing tool specification for matrix.send_message.
func (t *MatrixSendTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.FunctionDef{
			Name: MatrixSendToolName,
			Description: "Send a message to another agent or user via Matrix. " +
				"Use one of the alias names listed in your Messaging Targets section.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target": map[string]interface{}{
						"type": "string",
						"description": "Alias of the target from the allowed targets list " +
							"(e.g. \"kairo\", \"user\"). Unknown aliases are rejected.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The message content to send.",
					},
				},
				"required": []string{"target", "message"},
			},
		},
	}
}

// Execute runs the matrix.send_message tool with the LLM-supplied arguments.
// It never logs message content at INFO level (only at DEBUG with redaction).
func (t *MatrixSendTool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	// 1. Validate arguments.
	targetAlias, ok := stringArg(args, "target")
	if !ok || targetAlias == "" {
		return "", fmt.Errorf("matrix.send_message: missing required argument 'target'")
	}
	message, ok := stringArg(args, "message")
	if !ok || message == "" {
		return "", fmt.Errorf("matrix.send_message: missing required argument 'message'")
	}

	// 2. Resolve target alias → room ID.
	cfg := t.cfg.Config()
	if cfg == nil {
		return "", fmt.Errorf("matrix.send_message: no Gosuto config loaded")
	}
	roomID, found := resolveTarget(cfg.Messaging.AllowedTargets, targetAlias)
	if !found {
		return "", fmt.Errorf("matrix.send_message: target %q is not in the allowed targets list", targetAlias)
	}

	// 3. Rate limit check.
	if !t.rl.allow(cfg.Messaging.MaxMessagesPerMinute) {
		return "", fmt.Errorf("matrix.send_message: rate limit exceeded (%d messages/minute)", cfg.Messaging.MaxMessagesPerMinute)
	}

	// 4. Send via Matrix client.
	if err := t.sender.SendText(roomID, message); err != nil {
		return "", fmt.Errorf("matrix.send_message: send failed: %w", err)
	}

	// 5. Return success to the LLM (room ID included for auditability).
	return fmt.Sprintf("Message sent to %q (%s).", targetAlias, roomID), nil
}

// resolveTarget looks up the room ID for the given alias in the targets list.
// Returns ("", false) when no matching alias is found.
func resolveTarget(targets []gosutospec.MessagingTarget, alias string) (string, bool) {
	for _, t := range targets {
		if t.Alias == alias {
			return t.RoomID, true
		}
	}
	return "", false
}

// stringArg extracts a string value from a JSON-decoded args map.
// Returns ("", false) when the key is absent or the value is not a string.
func stringArg(args map[string]interface{}, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// rateLimiter is a fixed-window rate limiter safe for concurrent use.
// A maxPerMinute of 0 means unlimited (always allows).
type rateLimiter struct {
	mu          sync.Mutex
	count       int
	windowStart time.Time
}

// allow reports whether a new message is permitted under the maxPerMinute
// limit. It increments the counter when the call is allowed.
func (r *rateLimiter) allow(maxPerMinute int) bool {
	if maxPerMinute <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.windowStart.IsZero() || now.Sub(r.windowStart) >= time.Minute {
		r.count = 0
		r.windowStart = now
	}
	if r.count >= maxPerMinute {
		return false
	}
	r.count++
	return true
}
