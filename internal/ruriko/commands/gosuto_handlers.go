package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/retry"
	"github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// GosutoVersionsRetainN is the maximum number of Gosuto versions to keep per
// agent. Older versions are pruned after each successful write.
const GosutoVersionsRetainN = 20

// HandleGosutoShow displays the current (or a specific) Gosuto config for an agent.
//
// Usage:
//
//	/ruriko gosuto show <agent>
//	/ruriko gosuto show <agent> --version <n>
func (h *Handlers) HandleGosutoShow(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto show <agent> [--version <n>]")
	}

	// Verify the agent exists.
	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.show", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	var gv *store.GosutoVersion
	var err error

	if vStr := cmd.GetFlag("version", ""); vStr != "" {
		var vNum int
		if _, scanErr := fmt.Sscanf(vStr, "%d", &vNum); scanErr != nil {
			return "", fmt.Errorf("--version must be an integer, got %q", vStr)
		}
		gv, err = h.store.GetGosutoVersion(ctx, agentID, vNum)
	} else {
		gv, err = h.store.GetLatestGosutoVersion(ctx, agentID)
	}

	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.show", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("no gosuto config found for agent %q: %w", agentID, err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.show", agentID, "success",
		store.AuditPayload{"version": gv.Version}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.show", "err", err)
	}

	return formatGosutoShow(agentID, gv, traceID), nil
}

// formatGosutoShow renders the output for HandleGosutoShow. It tries to parse
// the stored YAML so that persona and instructions can be displayed as clearly
// labelled sections, making it easy to distinguish cosmetic from operational
// config. Falls back to raw YAML if parsing fails (e.g. legacy format).
func formatGosutoShow(agentID string, gv *store.GosutoVersion, traceID string) string {
	header := fmt.Sprintf(
		"**Gosuto config for %s** (v%d)\n\nHash: `%s`\nSet by: %s\nAt: %s",
		agentID, gv.Version,
		gv.Hash[:16]+"…",
		gv.CreatedByMXID,
		gv.CreatedAt.Format("2006-01-02 15:04:05 UTC"),
	)

	var cfg gosuto.Config
	if err := yaml.Unmarshal([]byte(gv.YAMLBlob), &cfg); err != nil {
		// Fall back to raw YAML display.
		return fmt.Sprintf(
			"%s\n\n```yaml\n%s\n```\n\n(trace: %s)",
			header, strings.TrimRight(gv.YAMLBlob, "\n"), traceID,
		)
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n")

	// ── Persona section ────────────────────────────────────────────────────
	sb.WriteString("\n**Persona** (cosmetic — tone, style, model)")
	if cfg.Persona.LLMProvider != "" || cfg.Persona.Model != "" {
		providerModel := strings.TrimSpace(cfg.Persona.LLMProvider + "/" + cfg.Persona.Model)
		if cfg.Persona.LLMProvider == "" {
			providerModel = cfg.Persona.Model
		} else if cfg.Persona.Model == "" {
			providerModel = cfg.Persona.LLMProvider
		}
		sb.WriteString("\nModel: ")
		sb.WriteString(providerModel)
		if cfg.Persona.Temperature != nil {
			sb.WriteString(fmt.Sprintf(" (temperature: %.1f)", *cfg.Persona.Temperature))
		}
	}
	if cfg.Persona.SystemPrompt != "" {
		prompt := strings.TrimSpace(cfg.Persona.SystemPrompt)
		firstLine := prompt
		if idx := strings.IndexByte(prompt, '\n'); idx > 0 {
			firstLine = prompt[:idx] + "…"
		} else if len(prompt) > 120 {
			firstLine = prompt[:120] + "…"
		}
		sb.WriteString("\nSystem prompt: ")
		sb.WriteString(firstLine)
	} else {
		sb.WriteString("\n_(no system prompt)_")
	}

	// ── Instructions section ───────────────────────────────────────────────
	sb.WriteString("\n\n**Instructions** (operational — workflow, peer awareness, user context)")
	instr := cfg.Instructions
	if instr.Role == "" && len(instr.Workflow) == 0 && instr.Context.User == "" && len(instr.Context.Peers) == 0 {
		sb.WriteString("\n_(no instructions configured)_")
	} else {
		if instr.Role != "" {
			role := strings.TrimSpace(instr.Role)
			firstLine := role
			if idx := strings.IndexByte(role, '\n'); idx > 0 {
				firstLine = role[:idx] + "…"
			} else if len(role) > 120 {
				firstLine = role[:120] + "…"
			}
			sb.WriteString("\nRole: ")
			sb.WriteString(firstLine)
		}
		if len(instr.Workflow) > 0 {
			sb.WriteString(fmt.Sprintf("\nWorkflow: %d step(s)", len(instr.Workflow)))
			for i, step := range instr.Workflow {
				trigger := strings.TrimSpace(step.Trigger)
				action := strings.TrimSpace(step.Action)
				if len(action) > 80 {
					action = action[:80] + "…"
				}
				sb.WriteString(fmt.Sprintf("\n  %d. [%s] → %s", i+1, trigger, action))
			}
		}
		if instr.Context.User != "" {
			userCtx := strings.TrimSpace(instr.Context.User)
			if len(userCtx) > 100 {
				userCtx = userCtx[:100] + "…"
			}
			sb.WriteString("\nUser context: ")
			sb.WriteString(userCtx)
		}
		if len(instr.Context.Peers) > 0 {
			peerNames := make([]string, len(instr.Context.Peers))
			for i, p := range instr.Context.Peers {
				peerNames[i] = p.Name
			}
			sb.WriteString("\nPeers: ")
			sb.WriteString(strings.Join(peerNames, ", "))
		}
	}

	// ── Full YAML ──────────────────────────────────────────────────────────
	sb.WriteString("\n\n**Full config (YAML)**")
	sb.WriteString("\n```yaml\n")
	sb.WriteString(strings.TrimRight(gv.YAMLBlob, "\n"))
	sb.WriteString("\n```")
	sb.WriteString(fmt.Sprintf("\n\n(trace: %s)", traceID))

	return sb.String()
}

// HandleGosutoVersions lists all stored Gosuto versions for an agent.
//
// Usage: /ruriko gosuto versions <agent>
func (h *Handlers) HandleGosutoVersions(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto versions <agent>")
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.versions", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	versions, err := h.store.ListGosutoVersions(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.versions", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to list versions: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.versions", agentID, "success",
		store.AuditPayload{"count": len(versions)}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.versions", "err", err)
	}

	if len(versions) == 0 {
		return fmt.Sprintf("No Gosuto versions found for agent **%s**.\n\n(trace: %s)", agentID, traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Gosuto versions for %s** (%d)\n\n", agentID, len(versions)))
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("%-5s  %-18s  %-18s  %s\n", "VER", "DATE", "BY", "HASH"))
	sb.WriteString(strings.Repeat("-", 72) + "\n")
	for _, v := range versions {
		byShort := v.CreatedByMXID
		if len(byShort) > 18 {
			byShort = byShort[:15] + "…"
		}
		sb.WriteString(fmt.Sprintf("v%-4d  %-18s  %-18s  %s…\n",
			v.Version,
			v.CreatedAt.Format("2006-01-02 15:04"),
			byShort,
			v.Hash[:16],
		))
	}
	sb.WriteString("```\n")
	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleGosutoDiff shows a line-by-line diff between two Gosuto versions.
//
// Usage: /ruriko gosuto diff <agent> --from <v1> --to <v2>
func (h *Handlers) HandleGosutoDiff(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto diff <agent> --from <v1> --to <v2>")
	}

	fromStr := cmd.GetFlag("from", "")
	toStr := cmd.GetFlag("to", "")
	if fromStr == "" || toStr == "" {
		return "", fmt.Errorf("usage: /ruriko gosuto diff <agent> --from <v1> --to <v2>")
	}

	var fromN, toN int
	if _, err := fmt.Sscanf(fromStr, "%d", &fromN); err != nil {
		return "", fmt.Errorf("--from must be an integer, got %q", fromStr)
	}
	if _, err := fmt.Sscanf(toStr, "%d", &toN); err != nil {
		return "", fmt.Errorf("--to must be an integer, got %q", toStr)
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.diff", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	fromGV, err := h.store.GetGosutoVersion(ctx, agentID, fromN)
	if err != nil {
		return "", fmt.Errorf("from version %d: %w", fromN, err)
	}
	toGV, err := h.store.GetGosutoVersion(ctx, agentID, toN)
	if err != nil {
		return "", fmt.Errorf("to version %d: %w", toN, err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.diff", agentID, "success",
		store.AuditPayload{"from": fromN, "to": toN}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.diff", "err", err)
	}

	if fromGV.Hash == toGV.Hash {
		return fmt.Sprintf("**Gosuto diff %s** v%d → v%d: No differences.\n\n(trace: %s)",
			agentID, fromN, toN, traceID), nil
	}

	diff := diffLines(fromGV.YAMLBlob, toGV.YAMLBlob)

	// Determine which high-level sections changed to make the diff more readable.
	sectionNote := gosutoDiffSections(fromGV.YAMLBlob, toGV.YAMLBlob)

	return fmt.Sprintf(
		"**Gosuto diff %s** v%d → v%d\n\n%s\n```diff\n%s\n```\n\n(trace: %s)",
		agentID, fromN, toN, sectionNote, diff, traceID,
	), nil
}

// HandleGosutoSet parses, validates, and stores a new Gosuto version.
//
// Usage: /ruriko gosuto set <agent> --content <base64-encoded-yaml>
func (h *Handlers) HandleGosutoSet(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto set <agent> --content <base64-yaml>")
	}

	b64 := cmd.GetFlag("content", "")
	if b64 == "" {
		return "", fmt.Errorf("--content is required (base64-encoded YAML)")
	}

	// Check the agent exists first — cheap DB lookup avoids wasting
	// cycles on base64 decode and Gosuto validation for a missing agent.
	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Validate inputs before requesting approval so that only valid
	// operations enter the approval queue.
	rawYAML, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Try URL-safe base64 as fallback
		rawYAML, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("--content must be valid base64: %w", err)
		}
	}

	// Validate before storing.
	if _, err := gosuto.Parse(rawYAML); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("invalid Gosuto config: %w", err)
	}

	// Require approval for Gosuto config changes (after validation passes).
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "gosuto.set", agentID, cmd, evt); needed {
		return msg, err
	}

	// Compute SHA-256 hash.
	sum := sha256.Sum256(rawYAML)
	hash := fmt.Sprintf("%x", sum)

	// Check for duplicate (same hash as latest).
	if latest, err := h.store.GetLatestGosutoVersion(ctx, agentID); err == nil {
		if latest.Hash == hash {
			return fmt.Sprintf(
				"ℹ️  Gosuto config for **%s** is unchanged (matches v%d).\n\n(trace: %s)",
				agentID, latest.Version, traceID,
			), nil
		}
	}

	nextVer, err := h.store.NextGosutoVersion(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to determine next version: %w", err)
	}

	gv := &store.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          hash,
		YAMLBlob:      string(rawYAML),
		CreatedByMXID: evt.Sender.String(),
	}

	if err := h.store.CreateGosutoVersion(ctx, gv); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to store Gosuto version: %w", err)
	}

	// Prune old versions.
	if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
		slog.Warn("gosuto prune failed", "agent", agentID, "err", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set", agentID, "success",
		store.AuditPayload{"version": nextVer, "hash": hash[:16]}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.set", "err", err)
	}

	return fmt.Sprintf(
		"✅ Gosuto config for **%s** stored as **v%d** (hash: `%s…`)\n\nRun `/ruriko gosuto push %s` to apply it to the running agent.\n\n(trace: %s)",
		agentID, nextVer, hash[:16], agentID, traceID,
	), nil
}

// HandleGosutoRollback reverts an agent to a previous Gosuto version by
// re-saving it as a new version entry (preserving the audit trail).
//
// Usage: /ruriko gosuto rollback <agent> --to <version>
func (h *Handlers) HandleGosutoRollback(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto rollback <agent> --to <version>")
	}

	toStr := cmd.GetFlag("to", "")
	if toStr == "" {
		return "", fmt.Errorf("--to <version> is required")
	}

	// Validate inputs before requesting approval so that only valid
	// operations enter the approval queue.
	var targetVer int
	if _, err := fmt.Sscanf(toStr, "%d", &targetVer); err != nil {
		return "", fmt.Errorf("--to must be an integer, got %q", toStr)
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.rollback", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Require approval for Gosuto rollback (after validation passes).
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "gosuto.rollback", agentID, cmd, evt); needed {
		return msg, err
	}

	// Load the target version.
	target, err := h.store.GetGosutoVersion(ctx, agentID, targetVer)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.rollback", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("version %d not found: %w", targetVer, err)
	}

	// No-op detection: if the rollback target's content is identical to the
	// current latest version, creating a new version would increment the
	// version counter without changing anything.
	if latest, err := h.store.GetLatestGosutoVersion(ctx, agentID); err == nil {
		if latest.Hash == target.Hash {
			return fmt.Sprintf(
				"ℹ️  Gosuto config for **%s** is already at the content of v%d (current: v%d, hash: %s…)\n\nNo new version created.\n\n(trace: %s)",
				agentID, targetVer, latest.Version, latest.Hash[:8], traceID,
			), nil
		}
	}

	// Determine the new version number.
	nextVer, err := h.store.NextGosutoVersion(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("failed to determine next version: %w", err)
	}

	gv := &store.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          target.Hash,
		YAMLBlob:      target.YAMLBlob,
		CreatedByMXID: evt.Sender.String(),
	}

	if err := h.store.CreateGosutoVersion(ctx, gv); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.rollback", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to store rollback version: %w", err)
	}

	if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
		slog.Warn("gosuto prune failed", "agent", agentID, "err", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.rollback", agentID, "success",
		store.AuditPayload{"rolled_back_from": targetVer, "new_version": nextVer}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.rollback", "err", err)
	}

	return fmt.Sprintf(
		"↩️  Gosuto for **%s** rolled back to v%d content, saved as **v%d**\n\nRun `/ruriko gosuto push %s` to apply.\n\n(trace: %s)",
		agentID, targetVer, nextVer, agentID, traceID,
	), nil
}

// HandleGosutoPush pushes the current Gosuto config to a running agent via ACP.
//
// Usage: /ruriko gosuto push <agent>
func (h *Handlers) HandleGosutoPush(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto push <agent>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.push", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if !agent.ControlURL.Valid || agent.ControlURL.String == "" {
		return "", fmt.Errorf("agent %q has no control URL; is it running?", agentID)
	}

	gv, err := h.store.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.push", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("no Gosuto config stored for agent %q", agentID)
	}

	if err := pushGosuto(ctx, agent.ControlURL.String, agent.ACPToken.String, gv); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.push", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to push Gosuto config: %w", err)
	}

	// R5.3: record the hash we just pushed as the desired state so the
	// reconciler can detect drift if the agent later reports a different hash.
	if err := h.store.SetAgentDesiredGosutoHash(ctx, agentID, gv.Hash); err != nil {
		slog.Warn("failed to record desired gosuto hash", "agent", agentID, "err", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.push", agentID, "success",
		store.AuditPayload{"version": gv.Version, "hash": gv.Hash[:16]}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.push", "err", err)
	}

	return fmt.Sprintf(
		"📤 Gosuto v%d pushed to **%s**\n\n(trace: %s)",
		gv.Version, agentID, traceID,
	), nil
}

// HandleSecretsPush forces a secret sync to a running agent via ACP.
//
// Usage: /ruriko secrets push <agent>
func (h *Handlers) HandleSecretsPush(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko secrets push <agent>")
	}

	if h.distributor == nil {
		return "", fmt.Errorf("secrets distributor is not configured")
	}

	n, err := h.distributor.PushToAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.push", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("secrets push failed: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "secrets.push", agentID, "success",
		store.AuditPayload{"pushed": n}, ""); err != nil {
		slog.Warn("audit write failed", "op", "secrets.push", "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindSecretsPushed, Actor: evt.Sender.String(), Target: agentID,
		Message: fmt.Sprintf("pushed %d secret(s)", n), TraceID: traceID,
	})

	return fmt.Sprintf("📤 Pushed **%d** secret(s) to **%s**\n\n(trace: %s)", n, agentID, traceID), nil
}

// HandleGosutoSetInstructions updates only the instructions section of the
// current Gosuto config for an agent, leaving persona and all other sections
// unchanged. The existing config is loaded, the instructions section is
// replaced, the result is validated, and a new version is stored.
//
// Usage:
//
//	/ruriko gosuto set-instructions <agent> --content <base64-yaml>
//
// The --content flag is a base64-encoded YAML document containing the
// instructions section fields: role, workflow, and context.
//
// Example YAML (before base64 encoding):
//
//	role: |
//	  You are a scheduling coordinator.
//	workflow:
//	  - trigger: "cron.tick event received"
//	    action: "Send a trigger message to each peer agent."
//	context:
//	  user: "The user is the sole approver."
//	  peers: []
func (h *Handlers) HandleGosutoSetInstructions(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto set-instructions <agent> --content <base64-yaml>")
	}

	b64 := cmd.GetFlag("content", "")
	if b64 == "" {
		return "", fmt.Errorf("--content is required (base64-encoded YAML of the instructions section)")
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-instructions", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	rawSection, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		rawSection, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("--content must be valid base64: %w", err)
		}
	}

	var newInstructions gosuto.Instructions
	if err := yaml.Unmarshal(rawSection, &newInstructions); err != nil {
		return "", fmt.Errorf("invalid instructions YAML: %w", err)
	}

	// Require approval for Gosuto config changes.
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "gosuto.set-instructions", agentID, cmd, evt); needed {
		return msg, err
	}

	gv, noChange, err := h.patchCurrentGosuto(ctx, agentID, evt.Sender.String(), func(cfg *gosuto.Config) {
		cfg.Instructions = newInstructions
	})
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-instructions", agentID, "error", nil, err.Error())
		return "", err
	}

	if noChange {
		return fmt.Sprintf(
			"ℹ️  Instructions for **%s** are unchanged (matches v%d).\n\n(trace: %s)",
			agentID, gv.Version, traceID,
		), nil
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-instructions", agentID, "success",
		store.AuditPayload{"version": gv.Version, "hash": gv.Hash[:16]}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.set-instructions", "err", err)
	}

	return fmt.Sprintf(
		"✅ Instructions for **%s** updated — stored as Gosuto **v%d** (hash: `%s…`)\n\nRun `/ruriko gosuto push %s` to apply it to the running agent.\n\n(trace: %s)",
		agentID, gv.Version, gv.Hash[:16], agentID, traceID,
	), nil
}

// HandleGosutoSetPersona updates only the persona section of the current
// Gosuto config for an agent, leaving instructions and all other sections
// unchanged. The existing config is loaded, the persona section is replaced,
// the result is validated, and a new version is stored.
//
// Usage:
//
//	/ruriko gosuto set-persona <agent> --content <base64-yaml>
//
// The --content flag is a base64-encoded YAML document containing the persona
// section fields: systemPrompt, llmProvider, model, temperature, apiKeySecretRef.
//
// Example YAML (before base64 encoding):
//
//	systemPrompt: "You are Kairo, a meticulous financial analyst."
//	llmProvider: openai
//	model: gpt-4o
//	temperature: 0.2
func (h *Handlers) HandleGosutoSetPersona(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko gosuto set-persona <agent> --content <base64-yaml>")
	}

	b64 := cmd.GetFlag("content", "")
	if b64 == "" {
		return "", fmt.Errorf("--content is required (base64-encoded YAML of the persona section)")
	}

	if _, err := h.store.GetAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-persona", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	rawSection, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		rawSection, err = base64.URLEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("--content must be valid base64: %w", err)
		}
	}

	var newPersona gosuto.Persona
	if err := yaml.Unmarshal(rawSection, &newPersona); err != nil {
		return "", fmt.Errorf("invalid persona YAML: %w", err)
	}

	// Require approval for Gosuto config changes.
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "gosuto.set-persona", agentID, cmd, evt); needed {
		return msg, err
	}

	gv, noChange, err := h.patchCurrentGosuto(ctx, agentID, evt.Sender.String(), func(cfg *gosuto.Config) {
		cfg.Persona = newPersona
	})
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-persona", agentID, "error", nil, err.Error())
		return "", err
	}

	if noChange {
		return fmt.Sprintf(
			"ℹ️  Persona for **%s** is unchanged (matches v%d).\n\n(trace: %s)",
			agentID, gv.Version, traceID,
		), nil
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.set-persona", agentID, "success",
		store.AuditPayload{"version": gv.Version, "hash": gv.Hash[:16]}, ""); err != nil {
		slog.Warn("audit write failed", "op", "gosuto.set-persona", "err", err)
	}

	return fmt.Sprintf(
		"✅ Persona for **%s** updated — stored as Gosuto **v%d** (hash: `%s…`)\n\nRun `/ruriko gosuto push %s` to apply it to the running agent.\n\n(trace: %s)",
		agentID, gv.Version, gv.Hash[:16], agentID, traceID,
	), nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// pushGosuto sends a Gosuto config to an agent via its ACP endpoint.
func pushGosuto(ctx context.Context, controlURL, acpToken string, gv *store.GosutoVersion) error {
	traceID := trace.FromContext(ctx)
	slog.Info("pushing Gosuto config to agent", "control_url", controlURL, "version", gv.Version, "trace", traceID)
	client := acp.New(controlURL, acp.Options{Token: acpToken})
	// ApplyConfig is idempotent — retry up to 3 times on transient failures.
	return retry.Do(ctx, retry.DefaultConfig, func() error {
		return client.ApplyConfig(ctx, acp.ConfigApplyRequest{
			YAML: gv.YAMLBlob,
			Hash: gv.Hash,
		})
	})
}

// patchCurrentGosuto loads the latest Gosuto version for agentID, applies fn
// to modify the parsed Config, re-serialises, validates, and stores a new
// version. It returns the resulting GosutoVersion and a boolean indicating
// whether the content was unchanged (true = no new version was created, the
// returned *GosutoVersion is the existing latest). An error is returned for
// any persistent failure.
func (h *Handlers) patchCurrentGosuto(
	ctx context.Context,
	agentID, createdByMXID string,
	fn func(cfg *gosuto.Config),
) (gv *store.GosutoVersion, noChange bool, err error) {
	// Load current version.
	current, err := h.store.GetLatestGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, false, fmt.Errorf("no gosuto config found for agent %q: %w", agentID, err)
	}

	// Parse current config.
	var cfg gosuto.Config
	if err := yaml.Unmarshal([]byte(current.YAMLBlob), &cfg); err != nil {
		return nil, false, fmt.Errorf("failed to parse current gosuto config: %w", err)
	}

	// Apply the caller's mutation.
	fn(&cfg)

	// Re-serialise.
	newYAML, err := yaml.Marshal(&cfg)
	if err != nil {
		return nil, false, fmt.Errorf("failed to serialise patched config: %w", err)
	}

	// Full validation.
	if _, err := gosuto.Parse(newYAML); err != nil {
		return nil, false, fmt.Errorf("patched config is invalid: %w", err)
	}

	// Hash.
	sum := sha256.Sum256(newYAML)
	hash := fmt.Sprintf("%x", sum)

	// No-op: same content as current.
	if current.Hash == hash {
		return current, true, nil
	}

	// Determine next version number.
	nextVer, err := h.store.NextGosutoVersion(ctx, agentID)
	if err != nil {
		return nil, false, fmt.Errorf("next gosuto version: %w", err)
	}

	newGV := &store.GosutoVersion{
		AgentID:       agentID,
		Version:       nextVer,
		Hash:          hash,
		YAMLBlob:      string(newYAML),
		CreatedByMXID: createdByMXID,
	}
	if err := h.store.CreateGosutoVersion(ctx, newGV); err != nil {
		return nil, false, fmt.Errorf("store gosuto version: %w", err)
	}
	if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
		slog.Warn("gosuto prune failed", "agent", agentID, "err", err)
	}

	return newGV, false, nil
}

// gosutoDiffSections inspects two Gosuto YAML blobs and returns a sentence
// summarising which high-level sections (persona, instructions, or other)
// have changed. Used by HandleGosutoDiff to annotate the output.
func gosutoDiffSections(fromYAML, toYAML string) string {
	var from, to gosuto.Config
	fromOK := yaml.Unmarshal([]byte(fromYAML), &from) == nil
	toOK := yaml.Unmarshal([]byte(toYAML), &to) == nil

	if !fromOK || !toOK {
		return "_Section analysis unavailable (YAML parse error)_"
	}

	// Serialise each section independently for comparison.
	fromPersona, _ := yaml.Marshal(from.Persona)
	toPersona, _ := yaml.Marshal(to.Persona)
	fromInstr, _ := yaml.Marshal(from.Instructions)
	toInstr, _ := yaml.Marshal(to.Instructions)

	personaChanged := string(fromPersona) != string(toPersona)
	instrChanged := string(fromInstr) != string(toInstr)

	// Also detect changes outside persona/instructions (policy, trust, etc.)
	from.Persona = gosuto.Persona{}
	to.Persona = gosuto.Persona{}
	from.Instructions = gosuto.Instructions{}
	to.Instructions = gosuto.Instructions{}
	fromRest, _ := yaml.Marshal(from)
	toRest, _ := yaml.Marshal(to)
	otherChanged := string(fromRest) != string(toRest)

	var parts []string
	if personaChanged {
		parts = append(parts, "**persona**")
	}
	if instrChanged {
		parts = append(parts, "**instructions**")
	}
	if otherChanged {
		parts = append(parts, "**policy/trust/other**")
	}

	if len(parts) == 0 {
		return "_No section differences detected._"
	}
	return "Changed sections: " + strings.Join(parts, ", ")
}

// diffLines computes a simple unified-style diff of two YAML strings.
// Lines present only in a are prefixed with "-", lines only in b with "+",
// and shared lines are prefixed with " ".
//
// NOTE: The underlying LCS algorithm compares lines as raw strings. If two
// distinct sections of a config contain identical lines (e.g. repeated
// "enabled: true" entries under different keys), the LCS may match them
// across sections, producing output that omits moved or reordered blocks.
// This is expected LCS behaviour. Operators diffing configs with highly
// repetitive structure should be aware that the output may appear to show
// no change for lines that were actually moved.
func diffLines(a, b string) string {
	aLines := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bLines := strings.Split(strings.TrimRight(b, "\n"), "\n")

	lcs := lcsLines(aLines, bLines)
	if lcs == nil {
		return fmt.Sprintf("(configs differ — %d / %d lines; too large for line-by-line diff)", len(aLines), len(bLines))
	}

	var sb strings.Builder
	ai, bi, li := 0, 0, 0

	for li < len(lcs) {
		for ai < len(aLines) && aLines[ai] != lcs[li] {
			sb.WriteString("- ")
			sb.WriteString(aLines[ai])
			sb.WriteString("\n")
			ai++
		}
		for bi < len(bLines) && bLines[bi] != lcs[li] {
			sb.WriteString("+ ")
			sb.WriteString(bLines[bi])
			sb.WriteString("\n")
			bi++
		}
		sb.WriteString("  ")
		sb.WriteString(lcs[li])
		sb.WriteString("\n")
		ai++
		bi++
		li++
	}
	// Remaining lines after LCS is exhausted.
	for ; ai < len(aLines); ai++ {
		sb.WriteString("- ")
		sb.WriteString(aLines[ai])
		sb.WriteString("\n")
	}
	for ; bi < len(bLines); bi++ {
		sb.WriteString("+ ")
		sb.WriteString(bLines[bi])
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// maxDiffLines is the maximum number of lines we will attempt to diff via the
// O(n·m) LCS algorithm.  Configs larger than this get a simple "configs differ"
// message instead of a line-by-line diff.
const maxDiffLines = 2000

// lcsLines computes the Longest Common Subsequence of two string slices.
// Uses the standard DP approach.  Suitable for the config sizes expected here
// (hundreds of lines at most).  Returns nil if either input exceeds
// maxDiffLines.
func lcsLines(a, b []string) []string {
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		return nil
	}

	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack.
	result := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			result = append(result, a[i-1])
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	// Reverse.
	for l, r := 0, len(result)-1; l < r; l, r = l+1, r-1 {
		result[l], result[r] = result[r], result[l]
	}
	return result
}
