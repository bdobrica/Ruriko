package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// agentIDPattern defines valid agent ID characters.
var agentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateAgentID returns an error if id is not a valid agent identifier.
// Valid IDs must start with a lowercase letter or digit, contain only
// lowercase letters, digits and hyphens, and be at most 63 characters long.
func validateAgentID(id string) error {
	if id == "" {
		return fmt.Errorf("agent ID must not be empty")
	}
	if !agentIDPattern.MatchString(id) {
		return fmt.Errorf("agent ID %q is invalid: must match ^[a-z0-9][a-z0-9-]{0,62}$", id)
	}
	return nil
}

// truncateID returns up to n bytes of s (safe alternative to s[:n]).
func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// generateACPToken returns a 32-char hex string (128 bits of entropy) suitable
// for use as a bearer token on ACP requests.
func generateACPToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HandleAgentsCreate provisions a new agent container.
//
// Usage: /ruriko agents create --name <id> --template <tmpl> --image <image>
//
// When a template registry is available the handler spawns the container
// synchronously (so a container ID is immediately persisted), then launches
// the async provisioning pipeline (R5.2) which:
//
//  1. Waits for the container to reach "running"
//  2. Waits for ACP /health to respond
//  3. Renders the Gosuto template and pushes it via ACP /config/apply
//  4. Verifies /status reports the correct config hash
//  5. Pushes bound secrets via the distributor
//
// Progress breadcrumbs are posted back to the originating Matrix room.
func (h *Handlers) HandleAgentsCreate(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID := cmd.GetFlag("name", "")
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents create --name <id> --template <template> --image <image>")
	}

	if err := validateAgentID(agentID); err != nil {
		return "", err
	}

	template := cmd.GetFlag("template", "")
	if template == "" {
		return "", fmt.Errorf("--template is required")
	}

	image := cmd.GetFlag("image", "")
	if image == "" {
		return "", fmt.Errorf("--image is required")
	}

	displayName := cmd.GetFlag("display-name", agentID)

	// Check that agent ID is not already taken
	if existing, _ := h.store.GetAgent(ctx, agentID); existing != nil {
		return "", fmt.Errorf("agent %q already exists", agentID)
	}

	// Generate a per-agent ACP bearer token (R2.1).
	acpToken, err := generateACPToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate ACP token: %w", err)
	}

	// Insert agent record with status=creating and provisioning_state=pending.
	agent := &store.Agent{
		ID:                agentID,
		DisplayName:       displayName,
		Template:          template,
		Status:            "creating",
		ProvisioningState: "pending",
	}
	agent.Image.String = image
	agent.Image.Valid = true
	agent.ACPToken.String = acpToken
	agent.ACPToken.Valid = true

	if err := h.store.CreateAgent(ctx, agent); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to create agent record: %w", err)
	}

	// --- no runtime path ------------------------------------------------
	if h.runtime == nil {
		h.store.UpdateAgentStatus(ctx, agentID, "stopped")
		h.store.UpdateAgentProvisioningState(ctx, agentID, "")
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "success",
			store.AuditPayload{"note": "no runtime configured, agent created as stopped"}, "")
		h.notifier.Notify(ctx, audit.Event{
			Kind: audit.KindAgentCreated, Actor: evt.Sender.String(), Target: agentID,
			Message: "created (no runtime; status: stopped)", TraceID: traceID,
		})
		return fmt.Sprintf("✅ Agent **%s** created (no runtime configured, status: stopped)\n\n(trace: %s)", agentID, traceID), nil
	}

	// --- Matrix account provisioning ------------------------------------
	// Register a Matrix account for the agent so it can connect to the
	// homeserver.  This must happen before the container is spawned so the
	// credentials are available as environment variables at start-up.
	var agentMXID, agentAccessToken string
	if h.provisioner != nil {
		account, err := h.provisioner.Register(ctx, agentID, displayName)
		if err != nil {
			h.store.UpdateAgentStatus(ctx, agentID, "error")
			h.store.UpdateAgentProvisioningState(ctx, agentID, "error")
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to provision Matrix account for agent %s: %w", agentID, err)
		}
		agentMXID = account.UserID.String()
		agentAccessToken = account.AccessToken
		// Persist the MXID immediately.
		if err := h.store.UpdateAgentMXID(ctx, agentID, agentMXID); err != nil {
			slog.Warn("failed to persist agent MXID", "agent", agentID, "mxid", agentMXID, "err", err)
		}
		// Invite agent to admin rooms so Ruriko can communicate with it.
		if inviteErrs := h.provisioner.InviteToRooms(ctx, account.UserID); len(inviteErrs) > 0 {
			for _, invErr := range inviteErrs {
				slog.Warn("provision: failed to invite agent to room", "agent", agentID, "err", invErr)
			}
		}
	} else {
		slog.Warn("provision: no Matrix provisioner configured; agent container will lack MATRIX_USER_ID and MATRIX_ACCESS_TOKEN",
			"agent", agentID)
	}

	// --- spawn container ------------------------------------------------
	agentEnv := map[string]string{
		"GITAI_AGENT_ID":  agentID,
		"GITAI_ACP_TOKEN": acpToken,
	}
	if h.matrixHomeserver != "" {
		agentEnv["MATRIX_HOMESERVER"] = h.matrixHomeserver
	}
	if agentMXID != "" {
		agentEnv["MATRIX_USER_ID"] = agentMXID
	}
	if agentAccessToken != "" {
		agentEnv["MATRIX_ACCESS_TOKEN"] = agentAccessToken
	}

	spec := runtime.AgentSpec{
		ID:          agentID,
		DisplayName: displayName,
		Image:       image,
		Template:    template,
		Env:         agentEnv,
	}

	handle, err := h.runtime.Spawn(ctx, spec)
	if err != nil {
		h.store.UpdateAgentStatus(ctx, agentID, "error")
		h.store.UpdateAgentProvisioningState(ctx, agentID, "error")
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to spawn container: %w", err)
	}

	// Persist container details immediately so the reconciler and other
	// readers always find a valid handle even while the pipeline is running.
	if err := h.store.UpdateAgentHandle(ctx, agentID, handle.ContainerID, handle.ControlURL, image); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("container spawned but failed to save handle: %w", err)
	}

	// --- with template registry: async pipeline -------------------------
	if h.templates != nil {
		pipelineArgs := provisionArgs{
			agentID:      agentID,
			template:     template,
			displayName:  displayName,
			handle:       handle,
			controlURL:   handle.ControlURL,
			acpToken:     acpToken,
			roomID:       evt.RoomID.String(),
			operatorMXID: evt.Sender.String(),
			traceID:      traceID,
		}

		// Launch the pipeline in a background goroutine using a detached context
		// so it is not cancelled when the Matrix event handler returns.
		bgCtx := trace.WithTraceID(context.Background(), traceID)
		go h.runProvisioningPipeline(bgCtx, pipelineArgs)

		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "success",
			store.AuditPayload{
				"container_id": truncateID(handle.ContainerID, 12),
				"control_url":  handle.ControlURL,
				"pipeline":     "async",
			}, "")

		return fmt.Sprintf(`⏳ Agent **%s** container spawned — provisioning pipeline started

Template:    %s
Image:       %s
Container:   %s
Control URL: %s

You will receive breadcrumb updates in this room as each step completes.

(trace: %s)`,
			agentID, template, image, truncateID(handle.ContainerID, 12), handle.ControlURL, traceID,
		), nil
	}

	// --- without template registry: legacy immediate path ---------------
	// The container is running but no Gosuto has been applied.  Operators
	// can push config manually with `/ruriko gosuto push <name>` later.
	slog.Warn("provision: no template registry configured; Gosuto will not be applied to new agent",
		"agent", agentID, "template", template)

	h.store.UpdateAgentStatus(ctx, agentID, "running")
	h.store.UpdateAgentProvisioningState(ctx, agentID, "")
	h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "success",
		store.AuditPayload{
			"container_id": truncateID(handle.ContainerID, 12),
			"control_url":  handle.ControlURL,
			"note":         "no template registry; gosuto not applied",
		}, "")
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindAgentCreated, Actor: evt.Sender.String(), Target: agentID,
		Message: fmt.Sprintf("created and started (container: %s; no gosuto applied)",
			truncateID(handle.ContainerID, 12)), TraceID: traceID,
	})

	return fmt.Sprintf(`✅ Agent **%s** created and started

Template:    %s
Image:       %s
Container:   %s
Control URL: %s

⚠️  No template registry configured — Gosuto was not applied.
Use /ruriko gosuto push %s after storing a config.

(trace: %s)`,
		agentID, template, image, truncateID(handle.ContainerID, 12), handle.ControlURL, agentID, traceID,
	), nil
}

// HandleAgentsStop stops a running agent container.
//
// Usage: /ruriko agents stop <name>
func (h *Handlers) HandleAgentsStop(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents stop <name>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.stop", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if agent.Status == "stopped" {
		return fmt.Sprintf("⚠️  Agent **%s** is already stopped\n\n(trace: %s)", agentID, traceID), nil
	}

	if h.runtime != nil && agent.ContainerID.Valid {
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		if err := h.runtime.Stop(ctx, handle); err != nil {
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.stop", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to stop container: %w", err)
		}
	}

	h.store.UpdateAgentStatus(ctx, agentID, "stopped")
	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.stop", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.stop", "agent", agentID, "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindAgentStopped, Actor: evt.Sender.String(), Target: agentID,
		Message: "stopped", TraceID: traceID,
	})

	return fmt.Sprintf("⏹️  Agent **%s** stopped\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsStart starts a stopped agent container.
//
// Usage: /ruriko agents start <name>
func (h *Handlers) HandleAgentsStart(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents start <name>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.start", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if agent.Status == "running" {
		return fmt.Sprintf("⚠️  Agent **%s** is already running\n\n(trace: %s)", agentID, traceID), nil
	}

	if h.runtime != nil {
		if !agent.ContainerID.Valid {
			return "", fmt.Errorf("agent %s has no container; use 'agents create' first", agentID)
		}
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		if err := h.runtime.Start(ctx, handle); err != nil {
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.start", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to start container: %w", err)
		}
	}

	h.store.UpdateAgentStatus(ctx, agentID, "running")
	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.start", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.start", "agent", agentID, "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindAgentStarted, Actor: evt.Sender.String(), Target: agentID,
		Message: "started", TraceID: traceID,
	})

	return fmt.Sprintf("▶️  Agent **%s** started\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsRespawn stops and recreates an agent container (fresh state).
//
// Usage: /ruriko agents respawn <name>
func (h *Handlers) HandleAgentsRespawn(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents respawn <name>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.respawn", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if h.runtime != nil && agent.ContainerID.Valid {
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		if err := h.runtime.Restart(ctx, handle); err != nil {
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.respawn", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to respawn container: %w", err)
		}
	}

	h.store.UpdateAgentStatus(ctx, agentID, "running")
	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.respawn", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.respawn", "agent", agentID, "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindAgentRespawned, Actor: evt.Sender.String(), Target: agentID,
		Message: "respawned", TraceID: traceID,
	})

	return fmt.Sprintf("🔄 Agent **%s** respawned\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsDelete removes an agent and its container.
//
// Usage: /ruriko agents delete <name>
func (h *Handlers) HandleAgentsDelete(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents delete <name>")
	}

	// Check the agent exists before requesting approval so that only
	// actionable deletions enter the approval queue.
	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.delete", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Require approval for agent deletion (after existence check passes).
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "agents.delete", agentID, cmd, evt); needed {
		return msg, err
	}

	if h.runtime != nil && agent.ContainerID.Valid {
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		if err := h.runtime.Remove(ctx, handle); err != nil {
			h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.delete", agentID, "error", nil, err.Error())
			return "", fmt.Errorf("failed to remove container: %w", err)
		}
	}

	if err := h.store.DeleteAgent(ctx, agentID); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.delete", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to delete agent record: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.delete", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.delete", "agent", agentID, "err", err)
	}
	h.notifier.Notify(ctx, audit.Event{
		Kind: audit.KindAgentDeleted, Actor: evt.Sender.String(), Target: agentID,
		Message: "deleted", TraceID: traceID,
	})

	return fmt.Sprintf("🗑️  Agent **%s** deleted\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsStatus shows the live runtime status of an agent container.
//
// Usage: /ruriko agents status <name>
func (h *Handlers) HandleAgentsStatus(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents status <name>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.status", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Agent: %s**\n\n", agentID))
	sb.WriteString(fmt.Sprintf("Display Name: %s\n", agent.DisplayName))
	sb.WriteString(fmt.Sprintf("Template:     %s\n", agent.Template))
	sb.WriteString(fmt.Sprintf("DB Status:    %s\n", agent.Status))

	if agent.Image.Valid {
		sb.WriteString(fmt.Sprintf("Image:        %s\n", agent.Image.String))
	}
	if agent.ContainerID.Valid {
		sb.WriteString(fmt.Sprintf("Container:    %s\n", truncateID(agent.ContainerID.String, 12)))
	}
	if agent.ControlURL.Valid {
		sb.WriteString(fmt.Sprintf("Control URL:  %s\n", agent.ControlURL.String))
	}

	// Live container status
	if h.runtime != nil && agent.ContainerID.Valid {
		handle := runtime.AgentHandle{
			AgentID:     agentID,
			ContainerID: agent.ContainerID.String,
		}
		rtStatus, err := h.runtime.Status(ctx, handle)
		if err == nil {
			sb.WriteString(fmt.Sprintf("State:        %s", string(rtStatus.State)))
			if rtStatus.ExitCode != 0 {
				sb.WriteString(fmt.Sprintf(" (exit %d)", rtStatus.ExitCode))
			}
			sb.WriteString("\n")
			if !rtStatus.StartedAt.IsZero() {
				sb.WriteString(fmt.Sprintf("Started At:   %s\n", rtStatus.StartedAt.Format("2006-01-02 15:04:05")))
			}
		}
	}

	// ACP health check
	if agent.ControlURL.Valid && agent.ControlURL.String != "" {
		acpClient := acp.New(agent.ControlURL.String, acp.Options{Token: agent.ACPToken.String})
		health, err := acpClient.Health(ctx)
		if err != nil {
			sb.WriteString("ACP Health:   ❌ unreachable\n")
		} else {
			sb.WriteString(fmt.Sprintf("ACP Health:   ✅ %s\n", health.Status))
		}
	}

	if agent.LastSeen.Valid {
		sb.WriteString(fmt.Sprintf("Last Seen:    %s\n", agent.LastSeen.Time.Format("2006-01-02 15:04:05")))
	}

	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.status", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.status", "agent", agentID, "err", err)
	}
	return sb.String(), nil
}

// HandleAgentsCancel cancels the currently in-flight task on a running agent
// by calling POST /tasks/cancel on the agent's ACP endpoint.
//
// Usage: /ruriko agents cancel <name>
func (h *Handlers) HandleAgentsCancel(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents cancel <name>")
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.cancel", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	if !agent.ControlURL.Valid || agent.ControlURL.String == "" {
		return "", fmt.Errorf("agent %s has no control URL; is it running?", agentID)
	}

	acpClient := acp.New(agent.ControlURL.String, acp.Options{Token: agent.ACPToken.String})
	if err := acpClient.Cancel(ctx); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.cancel", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("cancel request failed: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.cancel", agentID, "success", nil, ""); err != nil {
		slog.Warn("audit write failed", "op", "agents.cancel", "agent", agentID, "err", err)
	}

	return fmt.Sprintf("⛔ Task cancel sent to **%s**\n\n(trace: %s)", agentID, traceID), nil
}
