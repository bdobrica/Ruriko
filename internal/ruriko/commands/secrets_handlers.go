package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// HandleSecretsList lists all secret names and metadata (never values).
//
// Usage: /ruriko secrets list
func (h *Handlers) HandleSecretsList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	secs, err := h.secrets.List(ctx)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.list", "", "error", nil, err.Error())
		return "", fmt.Errorf("failed to list secrets: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.list", "", "success",
		store.AuditPayload{"count": len(secs)}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.list", "err", err)
	}

	if len(secs) == 0 {
		return fmt.Sprintf("No secrets stored.\n\n(trace: %s)", traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Secrets** (%d)\n\n", len(secs)))
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("%-30s %-15s %s\n", "NAME", "TYPE", "VERSION"))
	sb.WriteString(strings.Repeat("-", 60) + "\n")
	for _, s := range secs {
		sb.WriteString(fmt.Sprintf("%-30s %-15s v%d\n", s.Name, string(s.Type), s.RotationVersion))
	}
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))
	return sb.String(), nil
}

// HandleSecretsInfo shows metadata for a specific secret (never the value).
//
// Usage: /ruriko secrets info <name>
func (h *Handlers) HandleSecretsInfo(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	name, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets info <name>")
	}

	sec, err := h.secrets.GetMetadata(ctx, name)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.info", name, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.info", name, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.info", "secret", name, "err", err)
	}

	return fmt.Sprintf(`**Secret: %s**

Type:             %s
Rotation version: v%d
Created:          %s
Updated:          %s

⚠️  Secret values are never displayed.
(trace: %s)`,
		sec.Name,
		string(sec.Type),
		sec.RotationVersion,
		sec.CreatedAt.Format("2006-01-02 15:04:05 UTC"),
		sec.UpdatedAt.Format("2006-01-02 15:04:05 UTC"),
		traceID,
	), nil
}

// HandleSecretsSet stores a new secret or updates an existing one.
//
// When a Kuze server is configured (the common production path), the command
// generates a one-time HTTPS link and replies with it so the user can enter
// the secret value in their browser rather than pasting it into chat:
//
//	/ruriko secrets set <name> --type <type>
//
// When Kuze is NOT configured (dev/test mode), the old inline base64 path is
// still available as a fallback:
//
//	/ruriko secrets set <name> --type <type> --value <base64>
//
// Valid types: matrix_token, api_key, generic_json
func (h *Handlers) HandleSecretsSet(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	name, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets set <name> --type <type>")
	}

	secretType := cmd.GetFlag("type", "")
	if secretType == "" {
		return "", fmt.Errorf("usage: /ruriko secrets set <name> --type <type>\nValid types: matrix_token, api_key, generic_json")
	}

	switch secrets.Type(secretType) {
	case secrets.TypeMatrixToken, secrets.TypeAPIKey, secrets.TypeGenericJSON:
		// valid
	default:
		return "", fmt.Errorf("unknown secret type %q; valid types: matrix_token, api_key, generic_json", secretType)
	}

	// ── Kuze path (production) ──────────────────────────────────────────────
	if h.kuze != nil {
		return h.handleSecretsSetKuze(ctx, traceID, name, secretType, evt)
	}

	// ── Legacy inline path (dev / no-Kuze mode) ─────────────────────────────
	return h.handleSecretsSetInline(ctx, traceID, name, secretType, cmd, evt)
}

// handleSecretsSetKuze issues a one-time Kuze link for secret entry.
func (h *Handlers) handleSecretsSetKuze(
	ctx context.Context,
	traceID, name, secretType string,
	evt *event.Event,
) (string, error) {
	result, err := h.kuze.IssueHumanToken(ctx, name, secretType)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.set", name, "error", nil, err.Error())
		return "", fmt.Errorf("failed to generate secret-entry link: %w", err)
	}

	if logErr := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.set.link_issued", name, "success",
		store.AuditPayload{"type": secretType, "expires_at": result.ExpiresAt.String()}, ""); logErr != nil {
		slog.Warn("audit write failed", "op", "secrets.set.link_issued", "secret", name, "err", logErr)
	}

	return fmt.Sprintf(
		"🔐 Use this link to enter the secret **%s** (type: %s):\n\n"+
			"%s\n\n"+
			"⏰ Expires: %s\n"+
			"⚠️  Single-use — do not share this link.\n\n"+
			"(trace: %s)",
		name, secretType,
		result.Link,
		result.ExpiresAt.Format("2006-01-02 15:04:05 UTC"),
		traceID,
	), nil
}

// handleSecretsSetInline stores a secret from an inline base64-encoded value.
// This path is only taken when Kuze is not configured.
//
// Usage: /ruriko secrets set <name> --type <type> --value <base64>
func (h *Handlers) handleSecretsSetInline(
	ctx context.Context,
	traceID, name, secretType string,
	cmd *Command,
	evt *event.Event,
) (string, error) {
	b64Value := cmd.GetFlag("value", "")
	if b64Value == "" {
		return "", fmt.Errorf(
			"usage: /ruriko secrets set <name> --type <type> --value <base64>\n" +
				"Tip: configure KuzeBaseURL to use one-time secure links instead.",
		)
	}

	rawValue, err := base64.StdEncoding.DecodeString(b64Value)
	if err != nil {
		return "", fmt.Errorf("--value must be valid base64: %w", err)
	}

	if err := h.secrets.Set(ctx, name, secrets.Type(secretType), rawValue); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.set", name, "error", nil, err.Error())
		return "", fmt.Errorf("failed to store secret: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.set", name, "success",
		store.AuditPayload{"type": secretType}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.set", "secret", name, "err", err)
	}

	return fmt.Sprintf(
		"✅ Secret **%s** stored (type: %s)\n\n"+
			"⚠️ **SECURITY WARNING:** The secret value was transmitted as part of this Matrix message and is "+
			"visible in the room history to all room members and stored on the homeserver. "+
			"For sensitive secrets, use an encrypted direct message or an out-of-band mechanism.\n\n"+
			"(trace: %s)",
		name, secretType, traceID), nil
}

// HandleSecretsRotate replaces the encrypted value and increments rotation_version.
//
// Usage: /ruriko secrets rotate <name> --value <base64>
func (h *Handlers) HandleSecretsRotate(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	name, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets rotate <name> --value <base64>")
	}

	b64Value := cmd.GetFlag("value", "")
	if b64Value == "" {
		return "", fmt.Errorf("usage: /ruriko secrets rotate <name> --value <base64>")
	}

	// Validate base64 before requesting approval so that only valid
	// operations enter the approval queue.
	rawValue, err := base64.StdEncoding.DecodeString(b64Value)
	if err != nil {
		return "", fmt.Errorf("--value must be valid base64: %w", err)
	}

	// Verify the secret exists before entering the approval gate so that
	// only valid operations are queued for approval.
	if _, err := h.secrets.GetMetadata(ctx, name); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate", name, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %s", name)
	}

	// Require approval for secret rotation (after input validation passes).
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "secrets.rotate", name, cmd, evt); needed {
		return msg, err
	}

	if err := h.secrets.Rotate(ctx, name, rawValue); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate", name, "error", nil, err.Error())
		return "", fmt.Errorf("failed to rotate secret: %w", err)
	}

	// Read updated metadata to report new version
	meta, _ := h.secrets.GetMetadata(ctx, name)
	version := 0
	if meta != nil {
		version = meta.RotationVersion
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate", name, "success",
		store.AuditPayload{"rotation_version": version}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.rotate", "secret", name, "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindSecretsRotated, Actor: evt.Sender.String(), Target: name,
		Message: fmt.Sprintf("rotated to v%d", version), TraceID: traceID,
	})

	return fmt.Sprintf(
		"🔄 Secret **%s** rotated to v%d\n\n"+
			"⚠️ **SECURITY WARNING:** The new secret value was transmitted as part of this Matrix message and is "+
			"visible in the room history to all room members and stored on the homeserver. "+
			"For sensitive secrets, use an encrypted direct message or an out-of-band mechanism.\n\n"+
			"(trace: %s)",
		name, version, traceID), nil
}

// HandleSecretsDelete removes a stored secret.
//
// Usage: /ruriko secrets delete <name>
func (h *Handlers) HandleSecretsDelete(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	name, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets delete <name>")
	}

	// Verify the secret exists before entering the approval gate so that
	// only valid operations are queued for approval.
	if _, err := h.secrets.GetMetadata(ctx, name); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.delete", name, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %s", name)
	}

	// Require approval for secret deletion.
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "secrets.delete", name, cmd, evt); needed {
		return msg, err
	}

	if err := h.secrets.Delete(ctx, name); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.delete", name, "error", nil, err.Error())
		return "", fmt.Errorf("failed to delete secret: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.delete", name, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.delete", "secret", name, "err", err)
	}

	return fmt.Sprintf("🗑️  Secret **%s** deleted\n\n(trace: %s)", name, traceID), nil
}

// HandleSecretsBind grants an agent access to a secret.
//
// Usage: /ruriko secrets bind <agent> <secret> [--scope <scope>]
// Default scope: read
func (h *Handlers) HandleSecretsBind(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets bind <agent> <secret> [--scope <scope>]")
	}

	secretName, ok := cmd.GetArg(1)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets bind <agent> <secret> [--scope <scope>]")
	}

	scope := cmd.GetFlag("scope", "read")

	// Ensure the agent exists before creating the binding.
	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.bind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Ensure the secret exists before creating the binding.
	if _, err := h.secrets.GetMetadata(ctx, secretName); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.bind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %s", secretName)
	}

	if err := h.secrets.Bind(ctx, agentID, secretName, scope); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.bind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("failed to bind secret: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.bind", agentID+"/"+secretName, "success",
		store.AuditPayload{"scope": scope}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.bind", "agent", agentID, "secret", secretName, "err", err)
	}

	return fmt.Sprintf("✅ Agent **%s** granted access to **%s** (scope: %s)\n\n(trace: %s)",
		agentID, secretName, scope, traceID), nil
}

// HandleSecretsUnbind revokes an agent's access to a secret.
//
// Usage: /ruriko secrets unbind <agent> <secret>
func (h *Handlers) HandleSecretsUnbind(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets unbind <agent> <secret>")
	}

	secretName, ok := cmd.GetArg(1)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets unbind <agent> <secret>")
	}

	// Ensure the agent exists before attempting the unbind.
	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.unbind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Ensure the secret exists before attempting the unbind.
	if _, err := h.secrets.GetMetadata(ctx, secretName); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.unbind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %s", secretName)
	}

	if err := h.secrets.Unbind(ctx, agentID, secretName); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.unbind", agentID+"/"+secretName, "error", nil, err.Error())
		return "", fmt.Errorf("failed to unbind secret: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.unbind", agentID+"/"+secretName, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.unbind", "agent", agentID, "secret", secretName, "err", err)
	}

	return fmt.Sprintf("🔒 Agent **%s** access to **%s** revoked\n\n(trace: %s)",
		agentID, secretName, traceID), nil
}
