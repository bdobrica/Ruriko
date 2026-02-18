package commands

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
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

// HandleAgentsCreate provisions a new agent container.
//
// Usage: /ruriko agents create --name <id> --template <tmpl> --image <image>
func (h *Handlers) HandleAgentsCreate(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

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

	// Insert agent record with status=creating
	agent := &store.Agent{
		ID:          agentID,
		DisplayName: displayName,
		Template:    template,
		Status:      "creating",
	}
	agent.Image.String = image
	agent.Image.Valid = true

	if err := h.store.CreateAgent(ctx, agent); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to create agent record: %w", err)
	}

	if h.runtime == nil {
		h.store.UpdateAgentStatus(ctx, agentID, "stopped")
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "success",
			store.AuditPayload{"note": "no runtime configured, agent created as stopped"}, "")
		return fmt.Sprintf("✅ Agent **%s** created (no runtime configured, status: stopped)\n\n(trace: %s)", agentID, traceID), nil
	}

	// Spawn container via runtime
	spec := runtime.AgentSpec{
		ID:          agentID,
		DisplayName: displayName,
		Image:       image,
		Template:    template,
	}

	handle, err := h.runtime.Spawn(ctx, spec)
	if err != nil {
		h.store.UpdateAgentStatus(ctx, agentID, "error")
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to spawn container: %w", err)
	}

	// Persist container details
	if err := h.store.UpdateAgentHandle(ctx, agentID, handle.ContainerID, handle.ControlURL, image); err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("container spawned but failed to save handle: %w", err)
	}

	h.store.UpdateAgentStatus(ctx, agentID, "running")
	h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.create", agentID, "success",
		store.AuditPayload{"container_id": truncateID(handle.ContainerID, 12), "control_url": handle.ControlURL}, "")

	return fmt.Sprintf(`✅ Agent **%s** created and started

Template:    %s
Image:       %s
Container:   %s
Control URL: %s

(trace: %s)`,
		agentID, template, image, truncateID(handle.ContainerID, 12), handle.ControlURL, traceID,
	), nil
}

// HandleAgentsStop stops a running agent container.
//
// Usage: /ruriko agents stop <name>
func (h *Handlers) HandleAgentsStop(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

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

	return fmt.Sprintf("⏹️  Agent **%s** stopped\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsStart starts a stopped agent container.
//
// Usage: /ruriko agents start <name>
func (h *Handlers) HandleAgentsStart(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

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

	return fmt.Sprintf("▶️  Agent **%s** started\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsRespawn stops and recreates an agent container (fresh state).
//
// Usage: /ruriko agents respawn <name>
func (h *Handlers) HandleAgentsRespawn(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

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

	return fmt.Sprintf("🔄 Agent **%s** respawned\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsDelete removes an agent and its container.
//
// Usage: /ruriko agents delete <name>
func (h *Handlers) HandleAgentsDelete(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	agentID, _ := cmd.GetArg(0)
	if agentID == "" {
		return "", fmt.Errorf("usage: /ruriko agents delete <name>")
	}

	// Require approval for agent deletion.
	if msg, needed, err := h.requestApprovalIfNeeded(ctx, "agents.delete", agentID, cmd, evt); needed {
		return msg, err
	}

	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.delete", agentID, "error", nil, err.Error())
		return "", fmt.Errorf("agent not found: %s", agentID)
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

	return fmt.Sprintf("🗑️  Agent **%s** deleted\n\n(trace: %s)", agentID, traceID), nil
}

// HandleAgentsStatus shows the live runtime status of an agent container.
//
// Usage: /ruriko agents status <name>
func (h *Handlers) HandleAgentsStatus(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

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
		acpClient := acp.New(agent.ControlURL.String)
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
