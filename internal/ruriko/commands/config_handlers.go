package commands

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// permittedConfigKeys is the allowlist of runtime-tunable knobs that operators
// may write via /ruriko config set.  Unknown keys are rejected to prevent
// accidental misuse or injection of unexpected configuration.
var permittedConfigKeys = map[string]struct{}{
	"nlp.model":      {},
	"nlp.endpoint":   {},
	"nlp.rate-limit": {},
}

// isPermittedKey reports whether key is in the allowlist.
func isPermittedKey(key string) bool {
	_, ok := permittedConfigKeys[key]
	return ok
}

// permittedKeyList returns the allowlist as a human-readable comma-joined string
// for use in error messages.
func permittedKeyList() string {
	keys := make([]string, 0, len(permittedConfigKeys))
	for k := range permittedConfigKeys {
		keys = append(keys, "`"+k+"`")
	}
	// Stable order for deterministic output.
	// (Iterate over a fixed slice to avoid map ordering non-determinism.)
	ordered := []string{"`nlp.model`", "`nlp.endpoint`", "`nlp.rate-limit`"}
	_ = keys // suppress unused-variable warning; ordered is constructed manually
	return strings.Join(ordered, ", ")
}

// HandleConfigSet stores a runtime configuration value.
//
// Usage: /ruriko config set <key> <value>
//
// Only keys in the permittedConfigKeys allowlist are accepted; all others are
// rejected with an error listing the permitted options.  The configStore must
// be non-nil (it is always initialised in app.New).
func (h *Handlers) HandleConfigSet(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	if h.configStore == nil {
		return "", fmt.Errorf("config store is not available")
	}

	if len(cmd.Args) < 2 {
		return "", fmt.Errorf("usage: /ruriko config set <key> <value>\n\nPermitted keys: %s", permittedKeyList())
	}

	key := cmd.Args[0]
	value := cmd.Args[1]

	if !isPermittedKey(key) {
		return "", fmt.Errorf("unknown config key %q — permitted keys: %s", key, permittedKeyList())
	}

	if err := h.configStore.Set(ctx, key, value); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "config.set", key, "error", nil, err.Error())
		return "", fmt.Errorf("failed to set config: %w", err)
	}

	if err := h.store.WriteAudit(
		ctx, traceID, evt.Sender.String(), "config.set", key, "success",
		store.AuditPayload{"key": key, "value": value}, "",
	); err != nil {
		slog.Warn("audit write failed", "op", "config.set", "err", err)
	}

	return fmt.Sprintf("✓ `%s` set to `%s`. (trace: %s)", key, value, traceID), nil
}

// HandleConfigGet retrieves a runtime configuration value.
//
// Usage: /ruriko config get <key>
//
// When the key has not been set, the handler replies with a "(not set — using
// default)" notice so operators can distinguish an absent value from an empty
// string.
func (h *Handlers) HandleConfigGet(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	if h.configStore == nil {
		return "", fmt.Errorf("config store is not available")
	}

	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("usage: /ruriko config get <key>\n\nPermitted keys: %s", permittedKeyList())
	}

	key := cmd.Args[0]

	if !isPermittedKey(key) {
		return "", fmt.Errorf("unknown config key %q — permitted keys: %s", key, permittedKeyList())
	}

	value, err := h.configStore.Get(ctx, key)
	if errors.Is(err, config.ErrNotFound) {
		if err := h.store.WriteAudit(
			ctx, traceID, evt.Sender.String(), "config.get", key, "success",
			store.AuditPayload{"key": key, "found": false}, "",
		); err != nil {
			slog.Warn("audit write failed", "op", "config.get", "err", err)
		}
		return fmt.Sprintf("`%s`: (not set — using default) (trace: %s)", key, traceID), nil
	}
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "config.get", key, "error", nil, err.Error())
		return "", fmt.Errorf("failed to get config: %w", err)
	}

	if err := h.store.WriteAudit(
		ctx, traceID, evt.Sender.String(), "config.get", key, "success",
		store.AuditPayload{"key": key, "found": true}, "",
	); err != nil {
		slog.Warn("audit write failed", "op", "config.get", "err", err)
	}

	return fmt.Sprintf("`%s`: `%s` (trace: %s)", key, value, traceID), nil
}

// HandleConfigList shows all non-default (explicitly set) configuration values.
//
// Usage: /ruriko config list
//
// Only values that have been stored via config set are shown; keys that are
// still using their built-in defaults are omitted.
func (h *Handlers) HandleConfigList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	if h.configStore == nil {
		return "", fmt.Errorf("config store is not available")
	}

	entries, err := h.configStore.List(ctx)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "config.list", "", "error", nil, err.Error())
		return "", fmt.Errorf("failed to list config: %w", err)
	}

	if err := h.store.WriteAudit(
		ctx, traceID, evt.Sender.String(), "config.list", "", "success",
		store.AuditPayload{"count": len(entries)}, "",
	); err != nil {
		slog.Warn("audit write failed", "op", "config.list", "err", err)
	}

	if len(entries) == 0 {
		return fmt.Sprintf("No config values set — all keys are using their defaults. (trace: %s)", traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Runtime Config** (%d set)\n\n", len(entries)))
	sb.WriteString("```\n")
	// Use a stable order: iterate permitted keys in declaration order.
	orderedKeys := []string{"nlp.model", "nlp.endpoint", "nlp.rate-limit"}
	for _, k := range orderedKeys {
		if v, ok := entries[k]; ok {
			sb.WriteString(fmt.Sprintf("%-20s %s\n", k, v))
		}
	}
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleConfigUnset deletes a runtime configuration value, reverting the key
// to its built-in default.
//
// Usage: /ruriko config unset <key>
func (h *Handlers) HandleConfigUnset(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	if h.configStore == nil {
		return "", fmt.Errorf("config store is not available")
	}

	if len(cmd.Args) < 1 {
		return "", fmt.Errorf("usage: /ruriko config unset <key>\n\nPermitted keys: %s", permittedKeyList())
	}

	key := cmd.Args[0]

	if !isPermittedKey(key) {
		return "", fmt.Errorf("unknown config key %q — permitted keys: %s", key, permittedKeyList())
	}

	if err := h.configStore.Delete(ctx, key); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "config.unset", key, "error", nil, err.Error())
		return "", fmt.Errorf("failed to unset config: %w", err)
	}

	if err := h.store.WriteAudit(
		ctx, traceID, evt.Sender.String(), "config.unset", key, "success",
		store.AuditPayload{"key": key}, "",
	); err != nil {
		slog.Warn("audit write failed", "op", "config.unset", "err", err)
	}

	return fmt.Sprintf("✓ `%s` unset — reverted to default. (trace: %s)", key, traceID), nil
}
