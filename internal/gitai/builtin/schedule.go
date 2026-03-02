package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bdobrica/Ruriko/internal/gitai/gateway"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

const (
	ScheduleUpsertToolName  = "schedule.upsert"
	ScheduleDisableToolName = "schedule.disable"
	ScheduleListToolName    = "schedule.list"
)

// ScheduleStore is the subset of store.Store used by schedule built-in tools.
type ScheduleStore interface {
	CreateCronSchedule(gatewayName, cronExpression, tool string, payload store.CronSchedulePayload, enabled bool, nextTriggerAt time.Time) (int64, error)
	UpdateCronSchedule(id int64, cronExpression string, payload store.CronSchedulePayload, enabled bool, nextTriggerAt time.Time) error
	DisableCronSchedule(id int64) error
	ListCronSchedules(gatewayName string, enabledOnly bool) ([]store.CronSchedule, error)
}

// ScheduleUpsertTool creates or updates a DB-backed cron schedule.
type ScheduleUpsertTool struct {
	store   ScheduleStore
	now     func() time.Time
	nextTick func(expr string, now time.Time) (time.Time, error)
}

func NewScheduleUpsertTool(s ScheduleStore) *ScheduleUpsertTool {
	return &ScheduleUpsertTool{
		store:   s,
		now:     time.Now,
		nextTick: gateway.NextCronTick,
	}
}

func (t *ScheduleUpsertTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        ScheduleUpsertToolName,
			Description: "Create or update a persistent cron schedule that executes matrix.send_message.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "number",
						"description": "Existing schedule ID to update. Omit to create a new schedule.",
					},
					"gateway": map[string]interface{}{
						"type":        "string",
						"description": "Gateway name owning this schedule. Defaults to \"scheduler\".",
					},
					"cron_expression": map[string]interface{}{
						"type":        "string",
						"description": "Cron expression (5-field or @every <duration>).",
					},
					"target_alias": map[string]interface{}{
						"type":        "string",
						"description": "Messaging target alias from Gosuto messaging.allowedTargets.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message text to send when the schedule triggers.",
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the schedule is active. Defaults to true.",
					},
				},
				"required": []string{"cron_expression", "target_alias", "message"},
			},
		},
	}
}

func (t *ScheduleUpsertTool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	gatewayName := "scheduler"
	if v, ok := stringArg(args, "gateway"); ok && v != "" {
		gatewayName = v
	}
	cronExpr, ok := stringArg(args, "cron_expression")
	if !ok || cronExpr == "" {
		return "", fmt.Errorf("schedule.upsert: missing required argument 'cron_expression'")
	}
	targetAlias, ok := stringArg(args, "target_alias")
	if !ok || targetAlias == "" {
		return "", fmt.Errorf("schedule.upsert: missing required argument 'target_alias'")
	}
	message, ok := stringArg(args, "message")
	if !ok || message == "" {
		return "", fmt.Errorf("schedule.upsert: missing required argument 'message'")
	}
	enabled := true
	if raw, exists := args["enabled"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return "", fmt.Errorf("schedule.upsert: argument 'enabled' must be boolean")
		}
		enabled = v
	}

	now := t.now().UTC()
	next, err := t.nextTick(cronExpr, now)
	if err != nil {
		return "", fmt.Errorf("schedule.upsert: invalid cron_expression: %w", err)
	}
	if next.IsZero() {
		return "", fmt.Errorf("schedule.upsert: invalid cron_expression: no next trigger time")
	}

	payload := store.CronSchedulePayload{TargetAlias: targetAlias, Message: message}
	if id, hasID, err := optionalInt64Arg(args, "id"); err != nil {
		return "", fmt.Errorf("schedule.upsert: %w", err)
	} else if hasID {
		if err := t.store.UpdateCronSchedule(id, cronExpr, payload, enabled, next); err != nil {
			return "", fmt.Errorf("schedule.upsert: update schedule %d: %w", id, err)
		}
		return fmt.Sprintf("Updated schedule %d (gateway=%q, next=%s).", id, gatewayName, next.Format(time.RFC3339)), nil
	}

	id, err := t.store.CreateCronSchedule(gatewayName, cronExpr, MatrixSendToolName, payload, enabled, next)
	if err != nil {
		return "", fmt.Errorf("schedule.upsert: create schedule: %w", err)
	}
	return fmt.Sprintf("Created schedule %d (gateway=%q, next=%s).", id, gatewayName, next.Format(time.RFC3339)), nil
}

// ScheduleDisableTool disables a DB-backed cron schedule.
type ScheduleDisableTool struct {
	store ScheduleStore
}

func NewScheduleDisableTool(s ScheduleStore) *ScheduleDisableTool {
	return &ScheduleDisableTool{store: s}
}

func (t *ScheduleDisableTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        ScheduleDisableToolName,
			Description: "Disable a persistent cron schedule by ID.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "number",
						"description": "Schedule ID to disable.",
					},
				},
				"required": []string{"id"},
			},
		},
	}
}

func (t *ScheduleDisableTool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	id, err := requiredInt64Arg(args, "id")
	if err != nil {
		return "", fmt.Errorf("schedule.disable: %w", err)
	}
	if err := t.store.DisableCronSchedule(id); err != nil {
		return "", fmt.Errorf("schedule.disable: disable schedule %d: %w", id, err)
	}
	return fmt.Sprintf("Disabled schedule %d.", id), nil
}

// ScheduleListTool lists DB-backed cron schedules.
type ScheduleListTool struct {
	store ScheduleStore
}

func NewScheduleListTool(s ScheduleStore) *ScheduleListTool {
	return &ScheduleListTool{store: s}
}

func (t *ScheduleListTool) Definition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        ScheduleListToolName,
			Description: "List persistent cron schedules.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"gateway": map[string]interface{}{
						"type":        "string",
						"description": "Optional gateway filter.",
					},
					"enabled_only": map[string]interface{}{
						"type":        "boolean",
						"description": "When true, returns only enabled schedules.",
					},
				},
			},
		},
	}
}

func (t *ScheduleListTool) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	gatewayName := ""
	if v, ok := stringArg(args, "gateway"); ok {
		gatewayName = v
	}
	enabledOnly := false
	if raw, exists := args["enabled_only"]; exists {
		v, ok := raw.(bool)
		if !ok {
			return "", fmt.Errorf("schedule.list: argument 'enabled_only' must be boolean")
		}
		enabledOnly = v
	}
	items, err := t.store.ListCronSchedules(gatewayName, enabledOnly)
	if err != nil {
		return "", fmt.Errorf("schedule.list: %w", err)
	}

	type outItem struct {
		ID            int64  `json:"id"`
		Gateway       string `json:"gateway"`
		Cron          string `json:"cron_expression"`
		Tool          string `json:"tool"`
		TargetAlias   string `json:"target_alias"`
		Message       string `json:"message"`
		Enabled       bool   `json:"enabled"`
		NextTriggerAt string `json:"next_trigger_at"`
		LastTriggerAt string `json:"last_trigger_at,omitempty"`
	}

	out := make([]outItem, 0, len(items))
	for _, item := range items {
		row := outItem{
			ID:            item.ID,
			Gateway:       item.GatewayName,
			Cron:          item.CronExpression,
			Tool:          item.Tool,
			TargetAlias:   item.Payload.TargetAlias,
			Message:       item.Payload.Message,
			Enabled:       item.Enabled,
			NextTriggerAt: item.NextTriggerAt.UTC().Format(time.RFC3339),
		}
		if item.LastTriggerAt.Valid {
			row.LastTriggerAt = item.LastTriggerAt.Time.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}

	blob, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("schedule.list: encode result: %w", err)
	}
	return string(blob), nil
}

func requiredInt64Arg(args map[string]interface{}, key string) (int64, error) {
	id, has, err := optionalInt64Arg(args, key)
	if err != nil {
		return 0, err
	}
	if !has {
		return 0, fmt.Errorf("missing required argument %q", key)
	}
	return id, nil
}

func optionalInt64Arg(args map[string]interface{}, key string) (int64, bool, error) {
	raw, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	n, ok := raw.(float64)
	if !ok {
		return 0, false, fmt.Errorf("argument %q must be numeric", key)
	}
	if n < 1 || n != float64(int64(n)) {
		return 0, false, fmt.Errorf("argument %q must be a positive integer", key)
	}
	return int64(n), true, nil
}
