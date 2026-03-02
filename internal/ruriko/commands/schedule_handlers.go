package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
)

func (h *Handlers) resolveAgentACPClient(ctx context.Context, agentID string) (*acp.Client, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent ID must not be empty")
	}
	agent, err := h.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	if !agent.ControlURL.Valid || strings.TrimSpace(agent.ControlURL.String) == "" {
		return nil, fmt.Errorf("agent %s has no control URL; is it running?", agentID)
	}
	token := ""
	if agent.ACPToken.Valid {
		token = agent.ACPToken.String
	}
	return acp.New(agent.ControlURL.String, acp.Options{Token: token}), nil
}

// HandleScheduleUpsert creates or updates a schedule on an agent via ACP tool call.
//
// Usage:
//
//	/ruriko schedule upsert --agent <id> --cron <expr> --target <alias> --message <text> [--id <n>] [--enabled true|false]
func (h *Handlers) HandleScheduleUpsert(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID := cmd.GetFlag("agent", "saito")
	cronExpr := strings.TrimSpace(cmd.GetFlag("cron", ""))
	targetAlias := strings.TrimSpace(cmd.GetFlag("target", ""))
	message := cmd.GetFlag("message", "")

	if cronExpr == "" || targetAlias == "" || strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("usage: /ruriko schedule upsert --agent <id> --cron <expr> --target <alias> --message <text> [--id <n>] [--enabled true|false]")
	}

	args := map[string]interface{}{
		"cron_expression": cronExpr,
		"target_alias":    targetAlias,
		"message":         message,
	}
	if rawID := strings.TrimSpace(cmd.GetFlag("id", "")); rawID != "" {
		scheduleID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || scheduleID <= 0 {
			return "", fmt.Errorf("invalid --id %q: must be a positive integer", rawID)
		}
		args["id"] = float64(scheduleID)
	}
	if rawEnabled := strings.TrimSpace(cmd.GetFlag("enabled", "")); rawEnabled != "" {
		enabled, err := strconv.ParseBool(rawEnabled)
		if err != nil {
			return "", fmt.Errorf("invalid --enabled %q: expected true or false", rawEnabled)
		}
		args["enabled"] = enabled
	}

	acpClient, err := h.resolveAgentACPClient(ctx, agentID)
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.upsert", agentID, "error", nil, err.Error())
		return "", err
	}

	resp, err := acpClient.CallTool(ctx, acp.ToolCallRequest{
		ToolRef: "schedule.upsert",
		Args:    args,
		Sender:  evt.Sender.String(),
	})
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.upsert", agentID, "error", args, err.Error())
		return "", fmt.Errorf("schedule upsert failed: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.upsert", agentID, "success", args, ""); err != nil {
		slog.Warn("audit write failed", "op", "schedule.upsert", "agent", agentID, "err", err)
	}

	return fmt.Sprintf("✅ %s\n\n(trace: %s)", resp.Result, traceID), nil
}

// HandleScheduleDisable disables a schedule on an agent via ACP tool call.
//
// Usage:
//
//	/ruriko schedule disable --agent <id> --id <n>
func (h *Handlers) HandleScheduleDisable(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID := cmd.GetFlag("agent", "saito")
	rawID := strings.TrimSpace(cmd.GetFlag("id", ""))
	if rawID == "" {
		return "", fmt.Errorf("usage: /ruriko schedule disable --agent <id> --id <n>")
	}
	scheduleID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || scheduleID <= 0 {
		return "", fmt.Errorf("invalid --id %q: must be a positive integer", rawID)
	}

	acpClient, err := h.resolveAgentACPClient(ctx, agentID)
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.disable", agentID, "error", nil, err.Error())
		return "", err
	}

	args := map[string]interface{}{"id": float64(scheduleID)}
	resp, err := acpClient.CallTool(ctx, acp.ToolCallRequest{
		ToolRef: "schedule.disable",
		Args:    args,
		Sender:  evt.Sender.String(),
	})
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.disable", agentID, "error", args, err.Error())
		return "", fmt.Errorf("schedule disable failed: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.disable", agentID, "success", args, ""); err != nil {
		slog.Warn("audit write failed", "op", "schedule.disable", "agent", agentID, "err", err)
	}

	return fmt.Sprintf("✅ %s\n\n(trace: %s)", resp.Result, traceID), nil
}

// HandleScheduleList lists schedules from an agent via ACP tool call.
//
// Usage:
//
//	/ruriko schedule list --agent <id> [--enabled true|false] [--gateway <name>]
func (h *Handlers) HandleScheduleList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	agentID := cmd.GetFlag("agent", "saito")

	args := map[string]interface{}{}
	if gatewayName := strings.TrimSpace(cmd.GetFlag("gateway", "")); gatewayName != "" {
		args["gateway"] = gatewayName
	}
	if rawEnabled := strings.TrimSpace(cmd.GetFlag("enabled", "")); rawEnabled != "" {
		enabledOnly, err := strconv.ParseBool(rawEnabled)
		if err != nil {
			return "", fmt.Errorf("invalid --enabled %q: expected true or false", rawEnabled)
		}
		args["enabled_only"] = enabledOnly
	}

	acpClient, err := h.resolveAgentACPClient(ctx, agentID)
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.list", agentID, "error", nil, err.Error())
		return "", err
	}

	resp, err := acpClient.CallTool(ctx, acp.ToolCallRequest{
		ToolRef: "schedule.list",
		Args:    args,
		Sender:  evt.Sender.String(),
	})
	if err != nil {
		_ = h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.list", agentID, "error", args, err.Error())
		return "", fmt.Errorf("schedule list failed: %w", err)
	}

	if err := h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "schedule.list", agentID, "success", args, ""); err != nil {
		slog.Warn("audit write failed", "op", "schedule.list", "agent", agentID, "err", err)
	}

	return fmt.Sprintf("📅 Schedules (%s):\n%s\n\n(trace: %s)", agentID, resp.Result, traceID), nil
}
