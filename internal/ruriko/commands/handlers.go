package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// Handlers holds all command handlers and dependencies
type Handlers struct {
	store   *store.Store
	secrets *secrets.Store
}

// NewHandlers creates a new Handlers instance
func NewHandlers(s *store.Store, sec *secrets.Store) *Handlers {
	return &Handlers{store: s, secrets: sec}
}

// HandleHelp shows available commands
func (h *Handlers) HandleHelp(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	help := `**Ruriko Control Plane**

**General Commands:**
• /ruriko help - Show this help message
• /ruriko version - Show version information
• /ruriko ping - Health check

**Agent Commands:**
• /ruriko agents list - List all agents
• /ruriko agents show <name> - Show agent details
• /ruriko agents create --template <name> --name <agent_name> - Create agent (TODO)
• /ruriko agents stop <name> - Stop agent (TODO)
• /ruriko agents start <name> - Start agent (TODO)

**Secrets Commands (admin only):**
• /ruriko secrets list - List secret names and metadata
• /ruriko secrets set <name> --type <type> --value <base64> - Store a secret
• /ruriko secrets info <name> - Show secret metadata
• /ruriko secrets rotate <name> --value <base64> - Rotate secret to new value
• /ruriko secrets delete <name> - Delete a secret
• /ruriko secrets bind <agent> <secret> --scope <scope> - Grant agent access
• /ruriko secrets unbind <agent> <secret> - Revoke agent access
• /ruriko agents delete <name> - Delete agent (TODO)

**Audit Commands:**
• /ruriko audit tail [n] - Show recent audit entries
• /ruriko trace <trace_id> - Show all events for a trace

**Secrets Commands:** (TODO)
**Gosuto Commands:** (TODO)
**Approvals Commands:** (TODO)
`
	return help, nil
}

// HandleVersion shows version information
func (h *Handlers) HandleVersion(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	return fmt.Sprintf("**Ruriko Control Plane**\nVersion: %s\nCommit: %s\nBuild Time: %s",
		version.Version, version.GitCommit, version.BuildTime), nil
}

// HandlePing responds with a health check
func (h *Handlers) HandlePing(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Write audit log
	err := h.store.WriteAudit(
		traceID,
		evt.Sender.String(),
		"ping",
		"",
		"success",
		nil,
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to write audit: %w", err)
	}

	return fmt.Sprintf("🏓 Pong! (trace: %s)", traceID), nil
}

// HandleAgentsList lists all agents
func (h *Handlers) HandleAgentsList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Query agents
	agents, err := h.store.ListAgents()
	if err != nil {
		h.store.WriteAudit(traceID, evt.Sender.String(), "agents.list", "", "error", nil, err.Error())
		return "", fmt.Errorf("failed to list agents: %w", err)
	}

	// Write audit log
	err = h.store.WriteAudit(
		traceID,
		evt.Sender.String(),
		"agents.list",
		"",
		"success",
		store.AuditPayload{"count": len(agents)},
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to write audit: %w", err)
	}

	// Format response
	if len(agents) == 0 {
		return fmt.Sprintf("No agents found. (trace: %s)\n\nCreate your first agent with:\n/ruriko agents create --template cron --name myagent", traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Agents (%d)**\n\n", len(agents)))

	for _, agent := range agents {
		statusEmoji := "❓"
		switch agent.Status {
		case "running":
			statusEmoji = "✅"
		case "stopped":
			statusEmoji = "⏹️"
		case "starting":
			statusEmoji = "🔄"
		case "error":
			statusEmoji = "❌"
		}

		sb.WriteString(fmt.Sprintf("%s **%s** (%s)\n", statusEmoji, agent.ID, agent.Status))
		sb.WriteString(fmt.Sprintf("  Template: %s\n", agent.Template))
		if agent.MXID.Valid {
			sb.WriteString(fmt.Sprintf("  MXID: %s\n", agent.MXID.String))
		}
		if agent.LastSeen.Valid {
			sb.WriteString(fmt.Sprintf("  Last Seen: %s\n", agent.LastSeen.Time.Format(time.RFC3339)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleAgentsShow shows details for a specific agent
func (h *Handlers) HandleAgentsShow(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Get agent name from arguments
	agentName, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko agents show <name>")
	}

	// Query agent
	agent, err := h.store.GetAgent(agentName)
	if err != nil {
		h.store.WriteAudit(traceID, evt.Sender.String(), "agents.show", agentName, "error", nil, err.Error())
		return "", fmt.Errorf("failed to get agent: %w", err)
	}

	// Write audit log
	err = h.store.WriteAudit(
		traceID,
		evt.Sender.String(),
		"agents.show",
		agentName,
		"success",
		nil,
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to write audit: %w", err)
	}

	// Format response
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Agent: %s**\n\n", agent.ID))
	sb.WriteString(fmt.Sprintf("**Display Name:** %s\n", agent.DisplayName))
	sb.WriteString(fmt.Sprintf("**Template:** %s\n", agent.Template))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", agent.Status))

	if agent.MXID.Valid {
		sb.WriteString(fmt.Sprintf("**Matrix ID:** %s\n", agent.MXID.String))
	}

	if agent.RuntimeVersion.Valid {
		sb.WriteString(fmt.Sprintf("**Runtime Version:** %s\n", agent.RuntimeVersion.String))
	}

	if agent.GosutoVersion.Valid {
		sb.WriteString(fmt.Sprintf("**Gosuto Version:** %d\n", agent.GosutoVersion.Int64))
	}

	if agent.LastSeen.Valid {
		sb.WriteString(fmt.Sprintf("**Last Seen:** %s\n", agent.LastSeen.Time.Format(time.RFC3339)))
	}

	sb.WriteString(fmt.Sprintf("**Created:** %s\n", agent.CreatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("**Updated:** %s\n", agent.UpdatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleAuditTail shows recent audit entries
func (h *Handlers) HandleAuditTail(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Get limit from arguments
	limit := 10
	if limitStr, ok := cmd.GetArg(0); ok {
		fmt.Sscanf(limitStr, "%d", &limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	// Query audit log
	entries, err := h.store.GetAuditLog(limit)
	if err != nil {
		return "", fmt.Errorf("failed to get audit log: %w", err)
	}

	// Write audit log
	err = h.store.WriteAudit(
		traceID,
		evt.Sender.String(),
		"audit.tail",
		"",
		"success",
		store.AuditPayload{"limit": limit},
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to write audit: %w", err)
	}

	// Format response
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Recent Audit Entries (last %d)**\n\n", limit))

	for _, entry := range entries {
		resultEmoji := "✅"
		if entry.Result == "error" {
			resultEmoji = "❌"
		} else if entry.Result == "denied" {
			resultEmoji = "🚫"
		}

		sb.WriteString(fmt.Sprintf("%s `%s` **%s** by %s\n",
			resultEmoji,
			entry.Timestamp.Format("15:04:05"),
			entry.Action,
			entry.ActorMXID,
		))

		if entry.Target.Valid {
			sb.WriteString(fmt.Sprintf("   Target: %s\n", entry.Target.String))
		}
		if entry.ErrorMessage.Valid {
			sb.WriteString(fmt.Sprintf("   Error: %s\n", entry.ErrorMessage.String))
		}

		sb.WriteString(fmt.Sprintf("   Trace: %s\n\n", entry.TraceID))
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleTrace shows all audit entries for a trace ID
func (h *Handlers) HandleTrace(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()

	// Get trace ID from subcommand position (e.g. /ruriko trace t_abc123)
	searchTraceID := cmd.Subcommand
	if searchTraceID == "" {
		return "", fmt.Errorf("usage: /ruriko trace <trace_id>")
	}

	// Query audit log
	entries, err := h.store.GetAuditByTrace(searchTraceID)
	if err != nil {
		h.store.WriteAudit(traceID, evt.Sender.String(), "trace", searchTraceID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to get trace: %w", err)
	}

	// Write audit log
	err = h.store.WriteAudit(
		traceID,
		evt.Sender.String(),
		"trace",
		searchTraceID,
		"success",
		store.AuditPayload{"entries": len(entries)},
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to write audit: %w", err)
	}

	// Format response
	if len(entries) == 0 {
		return fmt.Sprintf("No entries found for trace: %s\n\n(trace: %s)", searchTraceID, traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Trace: %s** (%d entries)\n\n", searchTraceID, len(entries)))

	for i, entry := range entries {
		resultEmoji := "✅"
		if entry.Result == "error" {
			resultEmoji = "❌"
		} else if entry.Result == "denied" {
			resultEmoji = "🚫"
		}

		sb.WriteString(fmt.Sprintf("%d. %s `%s` **%s** by %s\n",
			i+1,
			resultEmoji,
			entry.Timestamp.Format("15:04:05.000"),
			entry.Action,
			entry.ActorMXID,
		))

		if entry.Target.Valid {
			sb.WriteString(fmt.Sprintf("   Target: %s\n", entry.Target.String))
		}
		if entry.PayloadJSON.Valid {
			sb.WriteString(fmt.Sprintf("   Payload: %s\n", entry.PayloadJSON.String))
		}
		if entry.ErrorMessage.Valid {
			sb.WriteString(fmt.Sprintf("   Error: %s\n", entry.ErrorMessage.String))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}
