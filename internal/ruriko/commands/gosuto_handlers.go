package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

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

	return fmt.Sprintf(
		"**Gosuto config for %s** (v%d)\n\nHash: `%s`\nSet by: %s\nAt: %s\n\n```yaml\n%s\n```\n\n(trace: %s)",
		agentID, gv.Version,
		gv.Hash[:16]+"…",
		gv.CreatedByMXID,
		gv.CreatedAt.Format("2006-01-02 15:04:05 UTC"),
		strings.TrimRight(gv.YAMLBlob, "\n"),
		traceID,
	), nil
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

	return fmt.Sprintf(
		"**Gosuto diff %s** v%d → v%d\n\n```diff\n%s\n```\n\n(trace: %s)",
		agentID, fromN, toN, diff, traceID,
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

	if err := pushGosuto(ctx, agent.ControlURL.String, gv); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "gosuto.push", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to push Gosuto config: %w", err)
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

// ── helpers ──────────────────────────────────────────────────────────────────

// pushGosuto sends a Gosuto config to an agent via its ACP endpoint.
func pushGosuto(ctx context.Context, controlURL string, gv *store.GosutoVersion) error {
	traceID := trace.FromContext(ctx)
	slog.Info("pushing Gosuto config to agent", "control_url", controlURL, "version", gv.Version, "trace", traceID)
	client := acp.New(controlURL)
	// ApplyConfig is idempotent — retry up to 3 times on transient failures.
	return retry.Do(ctx, retry.DefaultConfig, func() error {
		return client.ApplyConfig(ctx, acp.ConfigApplyRequest{
			YAML: gv.YAMLBlob,
			Hash: gv.Hash,
		})
	})
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
