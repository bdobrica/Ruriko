package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
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
// The command always generates a one-time HTTPS link and replies with it so
// the user can enter the secret value in their browser rather than pasting it
// into chat:
//
//	/ruriko secrets set <name> --type <type>
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

	if h.kuze == nil {
		return "", fmt.Errorf(
			"secure secret entry requires Kuze; configure KUZE_BASE_URL and HTTP_ADDR, then rerun: /ruriko secrets set %s --type %s",
			name, secretType,
		)
	}

	return h.handleSecretsSetKuze(ctx, traceID, name, secretType, evt)
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

// HandleSecretsRotate replaces the encrypted value and increments rotation_version.
//
// Usage: /ruriko secrets rotate <name>
func (h *Handlers) HandleSecretsRotate(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	name, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets rotate <name>")
	}

	if h.kuze == nil {
		return "", fmt.Errorf(
			"secure rotation requires Kuze; configure KUZE_BASE_URL and HTTP_ADDR, then rerun: /ruriko secrets rotate %s",
			name,
		)
	}

	// Verify the secret exists before entering the approval gate so that
	// only valid operations are queued for approval.
	meta, err := h.secrets.GetMetadata(ctx, name)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate", name, "error", nil, err.Error())
		return "", fmt.Errorf("secret not found: %s", name)
	}

	// Require approval for secret rotation (after input validation passes).
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "secrets.rotate", name, cmd, evt); needed {
		return msg, err
	}

	result, err := h.kuze.IssueHumanToken(ctx, name, string(meta.Type))
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate", name, "error", nil, err.Error())
		return "", fmt.Errorf("failed to generate rotate link: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.rotate.link_issued", name, "success",
		store.AuditPayload{"type": string(meta.Type), "expires_at": result.ExpiresAt.String()}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.rotate.link_issued", "secret", name, "err", err)
	}

	return fmt.Sprintf(
		"🔄 Use this link to rotate secret **%s** (type: %s):\n\n"+
			"%s\n\n"+
			"⏰ Expires: %s\n"+
			"⚠️  Single-use — do not share this link.\n\n"+
			"(trace: %s)",
		name, string(meta.Type),
		result.Link,
		result.ExpiresAt.Format("2006-01-02 15:04:05 UTC"),
		traceID,
	), nil
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
