package commands

// provision.go implements the automated agent provisioning pipeline (R5.2).
//
// After a container is spawned, runProvisioningPipeline drives the agent
// through these sequential steps in a background goroutine:
//
//  1. Wait for container to reach "running" state
//  2. Wait for ACP /health to respond
//  3. Render the Gosuto template and store as a new version
//  4. Apply Gosuto via ACP /config/apply
//  5. Verify ACP /status reports the correct config hash
//  6. Push bound secret tokens via the distributor (if available)
//  7. Mark the agent as healthy
//
// Progress broadcasts are posted back to the operator's Matrix room as notices
// (breadcrumbs) so the operator can follow along without tailing logs.
//
// The provisioning_state column (migration 0008) tracks which phase the agent
// is currently in: pending → creating → configuring → healthy | error.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// Provisioning pipeline timeouts (adjustable via constants for testability).
const (
	// provisionContainerTimeout is the maximum time to wait for the container
	// to transition from "created" to "running".
	provisionContainerTimeout = 60 * time.Second
	// provisionACPHealthTimeout is the maximum time to wait for ACP /health.
	provisionACPHealthTimeout = 90 * time.Second
	// provisionStatusTimeout is the timeout for the single ACP /status call
	// used to verify the config hash after Gosuto has been applied.
	provisionStatusTimeout = 15 * time.Second
	// provisionPollInterval is the sleep between successive polling attempts
	// (container status or ACP health).
	provisionPollInterval = 2 * time.Second
)

// provisionArgs bundles all inputs required by the async provisioning pipeline.
// All strings are safe to capture; no pointers to mutable state.
type provisionArgs struct {
	agentID      string
	template     string
	displayName  string
	handle       runtime.AgentHandle
	controlURL   string
	acpToken     string
	roomID       string
	operatorMXID string
	traceID      string
}

// runProvisioningPipeline executes the post-spawn provisioning pipeline
// asynchronously. It must be called in its own goroutine. ctx should be a
// long-lived background context (not the per-request context from the Matrix
// event handler, which may be cancelled before the pipeline finishes).
func (h *Handlers) runProvisioningPipeline(ctx context.Context, args provisionArgs) {
	agentID := args.agentID
	traceID := args.traceID
	ctx = trace.WithTraceID(ctx, traceID)

	// --- helpers ---------------------------------------------------------

	// send posts a notice to the operator's Matrix room. Best-effort: failures
	// are logged but do not abort the pipeline.
	send := func(msg string) {
		if h.roomSender == nil || args.roomID == "" {
			return
		}
		if err := h.roomSender.SendNotice(args.roomID, msg); err != nil {
			slog.Warn("provision: breadcrumb send failed", "agent", agentID, "err", err)
		}
	}

	setState := func(state string) {
		if err := h.store.UpdateAgentProvisioningState(ctx, agentID, state); err != nil {
			slog.Warn("provision: failed to update provisioning state",
				"agent", agentID, "state", state, "err", err)
		}
	}

	failStep := func(step string, err error) {
		slog.Error("provision: step failed",
			"agent", agentID, "step", step, "trace", traceID, "err", err)
		setState("error")
		h.store.UpdateAgentStatus(ctx, agentID, "error")
		send(fmt.Sprintf("❌ Provisioning **%s** failed at step *%s*: %v\n\n(trace: %s)",
			agentID, step, err, traceID))
		h.store.WriteAudit(ctx, traceID, args.operatorMXID, "agents.provision", agentID, "error",
			store.AuditPayload{"step": step}, err.Error())
		h.notifier.Notify(ctx, audit.Event{
			Kind: audit.KindError, Actor: args.operatorMXID, Target: agentID,
			Message: fmt.Sprintf("provisioning failed at %s: %v", step, err), TraceID: traceID,
		})
	}

	// --- step 1: wait for container to reach "running" -------------------
	if h.runtime != nil && args.handle.ContainerID != "" {
		setState("creating")
		send(fmt.Sprintf("⏳ [1/5] Waiting for container **%s** to start...", agentID))

		waitCtx, cancel := context.WithTimeout(ctx, provisionContainerTimeout)
		if err := pollContainerRunning(waitCtx, h.runtime, args.handle); err != nil {
			cancel()
			failStep("container-start", err)
			return
		}
		cancel()
		send(fmt.Sprintf("✅ [1/5] Container **%s** is running", agentID))
	} else {
		// No runtime (dev/test) — skip container wait.
		send(fmt.Sprintf("ℹ️  [1/5] Skipping container health check (no runtime configured)"))
	}

	// --- step 2: wait for ACP /health to respond -------------------------
	setState("configuring")
	send(fmt.Sprintf("⏳ [2/5] Waiting for ACP health check on **%s**...", agentID))

	acpClient := acp.New(args.controlURL, acp.Options{Token: args.acpToken})

	waitCtx, cancel := context.WithTimeout(ctx, provisionACPHealthTimeout)
	if err := pollACPHealth(waitCtx, acpClient); err != nil {
		cancel()
		failStep("acp-health", err)
		return
	}
	cancel()
	send(fmt.Sprintf("✅ [2/5] ACP responding on **%s**", agentID))

	// --- step 3: load template, validate, and store Gosuto version -------
	send(fmt.Sprintf("⏳ [3/5] Rendering Gosuto template *%s* for **%s**...", args.template, agentID))

	if h.templates == nil {
		failStep("gosuto-template", fmt.Errorf("no template registry configured; cannot render %q", args.template))
		return
	}

	vars := templates.TemplateVars{
		AgentName:    agentID,
		DisplayName:  args.displayName,
		OperatorMXID: args.operatorMXID,
	}
	renderedYAML, err := h.templates.Render(args.template, vars)
	if err != nil {
		failStep("gosuto-render", fmt.Errorf("render template %q: %w", args.template, err))
		return
	}

	sum := sha256.Sum256(renderedYAML)
	hash := fmt.Sprintf("%x", sum)

	// Avoid storing a duplicate version if the template was already applied.
	var gv *store.GosutoVersion
	if latest, latestErr := h.store.GetLatestGosutoVersion(ctx, agentID); latestErr == nil && latest.Hash == hash {
		gv = latest
		slog.Debug("provision: gosuto hash unchanged; reusing existing version",
			"agent", agentID, "version", gv.Version, "hash", hash[:8])
	} else {
		nextVer, err := h.store.NextGosutoVersion(ctx, agentID)
		if err != nil {
			failStep("gosuto-store", fmt.Errorf("next gosuto version: %w", err))
			return
		}

		gv = &store.GosutoVersion{
			AgentID:       agentID,
			Version:       nextVer,
			Hash:          hash,
			YAMLBlob:      string(renderedYAML),
			CreatedByMXID: args.operatorMXID,
		}
		if err := h.store.CreateGosutoVersion(ctx, gv); err != nil {
			failStep("gosuto-store", fmt.Errorf("store gosuto version: %w", err))
			return
		}
		if err := h.store.PruneGosutoVersions(ctx, agentID, GosutoVersionsRetainN); err != nil {
			slog.Warn("provision: gosuto prune failed", "agent", agentID, "err", err)
		}
	}

	// --- step 4: push Gosuto via ACP /config/apply -----------------------
	if err := pushGosuto(ctx, args.controlURL, args.acpToken, gv); err != nil {
		failStep("gosuto-push", err)
		return
	}
	send(fmt.Sprintf("✅ [3/5] Gosuto v%d (hash: `%s…`) applied to **%s**",
		gv.Version, hash[:8], agentID))

	// Record which Gosuto version is now active on the agent.
	if err := h.store.UpdateAgentGosutoApplied(ctx, agentID, gv.Version); err != nil {
		slog.Warn("provision: failed to record applied gosuto version",
			"agent", agentID, "version", gv.Version, "err", err)
	}
	// R5.3: record the pushed hash as the desired state so the reconciler
	// can detect drift in subsequent reconciliation passes.
	if err := h.store.SetAgentDesiredGosutoHash(ctx, agentID, hash); err != nil {
		slog.Warn("provision: failed to record desired gosuto hash",
			"agent", agentID, "hash", hash[:8], "err", err)
	}

	// --- step 5: verify /status reports the correct config hash ----------
	send(fmt.Sprintf("⏳ [4/5] Verifying config hash on **%s**...", agentID))

	statusCtx, statusCancel := context.WithTimeout(ctx, provisionStatusTimeout)
	statusResp, err := acpClient.Status(statusCtx)
	statusCancel()
	if err != nil {
		failStep("status-verify", fmt.Errorf("ACP /status: %w", err))
		return
	}
	if statusResp.GosutoHash != "" && statusResp.GosutoHash != hash {
		failStep("status-verify",
			fmt.Errorf("config hash mismatch: agent reports %q, expected %q",
				statusResp.GosutoHash, hash))
		return
	}
	// R5.3: persist the hash the agent is actually running so the registry
	// starts in sync (desired == actual) right after provisioning.
	if reportedHash := statusResp.GosutoHash; reportedHash == "" {
		reportedHash = hash // agent doesn't echo hash yet; assume it applied correctly
		if err := h.store.SetAgentActualGosutoHash(ctx, agentID, reportedHash); err != nil {
			slog.Warn("provision: failed to record actual gosuto hash",
				"agent", agentID, "hash", reportedHash[:8], "err", err)
		}
	} else {
		if err := h.store.SetAgentActualGosutoHash(ctx, agentID, reportedHash); err != nil {
			slog.Warn("provision: failed to record actual gosuto hash",
				"agent", agentID, "hash", reportedHash[:8], "err", err)
		}
	}

	// R13.2: check gateway processes reported by /status against those declared
	// in the applied Gosuto config. Build the run-set from statusResp.Gateways.
	expectedGWs, gwSecretRefs := gosutoGatewaySummary(renderedYAML)
	runningSet := make(map[string]struct{}, len(statusResp.Gateways))
	for _, gw := range statusResp.Gateways {
		runningSet[gw] = struct{}{}
	}
	var gwLines []string
	for _, name := range expectedGWs {
		if _, ok := runningSet[name]; ok {
			gwLines = append(gwLines, fmt.Sprintf("  ✅ %s (running)", name))
		} else {
			// Non-fatal: gateway may still be starting.
			slog.Warn("provision: expected gateway not yet listed in /status",
				"agent", agentID, "gateway", name)
			gwLines = append(gwLines, fmt.Sprintf("  ⚠️  %s (not yet running)", name))
		}
	}
	if len(gwLines) > 0 {
		send(fmt.Sprintf("✅ [4/5] Config hash verified on **%s** — gateways:\n"+strings.Join(gwLines, "\n"), agentID))
	} else {
		send(fmt.Sprintf("✅ [4/5] Config hash verified on **%s** (no gateways configured)", agentID))
	}
	slog.Info("provision: gateway summary",
		"agent", agentID,
		"expected", len(expectedGWs),
		"running", len(statusResp.Gateways),
		"gateway_secrets", len(gwSecretRefs))

	// --- step 6: push secrets via distributor ----------------------------
	// gwSecretRefs contains any gateway-referenced secret names (e.g. HMAC keys)
	// discovered in the rendered Gosuto config; they are included in the
	// distributor's PushToAgent call below alongside all other bound secrets.
	var gwSecretNote string
	if len(gwSecretRefs) > 0 {
		gwSecretNote = fmt.Sprintf(" (includes %d gateway secret(s): %s)",
			len(gwSecretRefs), strings.Join(gwSecretRefs, ", "))
	}
	if h.distributor != nil {
		send(fmt.Sprintf("⏳ [5/5] Pushing secrets to **%s**%s...", agentID, gwSecretNote))
		n, err := h.distributor.PushToAgent(ctx, agentID)
		if err != nil {
			// Non-fatal: the agent is healthy, but it may not yet have secrets.
			slog.Warn("provision: secrets push failed (non-fatal)",
				"agent", agentID, "err", err)
			send(fmt.Sprintf("⚠️  [5/5] Secrets push warning (agent is healthy): %v", err))
		} else if n > 0 {
			send(fmt.Sprintf("✅ [5/5] %d secret(s) distributed to **%s**", n, agentID))
		} else {
			send(fmt.Sprintf("✅ [5/5] No secrets bound to **%s** yet", agentID))
		}
	} else {
		if gwSecretNote != "" {
			send(fmt.Sprintf("⚠️  [5/5] No secrets distributor — gateway secrets will not be pushed to **%s**%s", agentID, gwSecretNote))
		} else {
			send(fmt.Sprintf("ℹ️  [5/5] No secrets distributor — skipping secret push for **%s**", agentID))
		}
	}

	// --- done: mark healthy ----------------------------------------------
	setState("healthy")
	h.store.UpdateAgentStatus(ctx, agentID, "running")
	h.store.WriteAudit(ctx, traceID, args.operatorMXID, "agents.provision", agentID, "success",
		store.AuditPayload{"gosuto_hash": hash[:16], "gosuto_version": gv.Version}, "")
	h.notifier.Notify(ctx, audit.Event{
		Kind:    audit.KindAgentCreated,
		Actor:   args.operatorMXID,
		Target:  agentID,
		Message: fmt.Sprintf("provisioned and healthy (gosuto v%d, hash %s…)", gv.Version, hash[:8]),
		TraceID: traceID,
	})
	send(fmt.Sprintf("🎉 Agent **%s** is provisioned and healthy!\n\n(trace: %s)", agentID, traceID))
}

// pollContainerRunning polls the runtime at provisionPollInterval intervals
// until the container reaches ContainerState "running". Returns an error if
// the context is cancelled before the container starts, or if the container
// exits abnormally.
func pollContainerRunning(ctx context.Context, rt runtime.Runtime, handle runtime.AgentHandle) error {
	var lastState runtime.ContainerState
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for container to reach 'running' (last state: %q): %w",
				lastState, ctx.Err())
		case <-time.After(provisionPollInterval):
		}

		status, err := rt.Status(ctx, handle)
		if err != nil {
			slog.Debug("provision: container status check error (retrying)", "err", err)
			continue
		}
		lastState = status.State
		switch status.State {
		case runtime.StateRunning:
			return nil
		case runtime.StateExited, runtime.StateRemoving:
			return fmt.Errorf("container exited prematurely with exit code %d", status.ExitCode)
		default:
			// created/paused/unknown — keep polling
		}
	}
}

// gosutoGatewaySummary parses the rendered Gosuto YAML and returns two slices:
//   - names: the Name of each gateway declared in the config
//   - secretRefs: any secret references used by gateways (e.g. hmacSecretRef
//     values in gateway Config maps), deduplicated and sorted
//
// The function is intentionally lenient: parse errors silently return empty
// slices so that the provisioning pipeline is not broken by an unexpected
// template format (R13.2).
func gosutoGatewaySummary(data []byte) (names []string, secretRefs []string) {
	var partial struct {
		Gateways []struct {
			Name   string            `yaml:"name"`
			Config map[string]string `yaml:"config"`
		} `yaml:"gateways"`
	}
	_ = yaml.Unmarshal(data, &partial)

	seen := make(map[string]struct{})
	for _, gw := range partial.Gateways {
		if gw.Name != "" {
			names = append(names, gw.Name)
		}
		// Gateway secret references live in the config map under known keys.
		for _, key := range []string{"hmacSecretRef", "passwordSecretRef", "tokenSecretRef"} {
			if ref, ok := gw.Config[key]; ok && ref != "" {
				if _, dup := seen[ref]; !dup {
					secretRefs = append(secretRefs, ref)
					seen[ref] = struct{}{}
				}
			}
		}
	}
	return names, secretRefs
}

// pollACPHealth polls GET /health at provisionPollInterval intervals until the
// ACP server responds successfully, or the context is cancelled.
func pollACPHealth(ctx context.Context, client *acp.Client) error {
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timeout waiting for ACP to become healthy (last error: %v): %w",
					lastErr, ctx.Err())
			}
			return fmt.Errorf("timeout waiting for ACP to become healthy: %w", ctx.Err())
		case <-time.After(provisionPollInterval):
		}

		_, err := client.Health(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		slog.Debug("provision: ACP not ready yet (retrying)", "err", err)
	}
}
