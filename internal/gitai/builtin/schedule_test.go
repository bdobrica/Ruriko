package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

type scheduleStoreStub struct {
	nextID     int64
	items      map[int64]store.CronSchedule
	createErr  error
	updateErr  error
	disableErr error
	listErr    error
}

func newScheduleStoreStub() *scheduleStoreStub {
	return &scheduleStoreStub{
		nextID: 1,
		items:  make(map[int64]store.CronSchedule),
	}
}

func (s *scheduleStoreStub) CreateCronSchedule(gatewayName, cronExpression, tool string, payload store.CronSchedulePayload, enabled bool, nextTriggerAt time.Time) (int64, error) {
	if s.createErr != nil {
		return 0, s.createErr
	}
	id := s.nextID
	s.nextID++
	s.items[id] = store.CronSchedule{
		ID:             id,
		GatewayName:    gatewayName,
		CronExpression: cronExpression,
		Tool:           tool,
		Payload:        payload,
		Enabled:        enabled,
		NextTriggerAt:  nextTriggerAt,
	}
	return id, nil
}

func (s *scheduleStoreStub) UpdateCronSchedule(id int64, cronExpression string, payload store.CronSchedulePayload, enabled bool, nextTriggerAt time.Time) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	item, ok := s.items[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	item.CronExpression = cronExpression
	item.Payload = payload
	item.Enabled = enabled
	item.NextTriggerAt = nextTriggerAt
	s.items[id] = item
	return nil
}

func (s *scheduleStoreStub) DisableCronSchedule(id int64) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	item, ok := s.items[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	item.Enabled = false
	s.items[id] = item
	return nil
}

func (s *scheduleStoreStub) ListCronSchedules(gatewayName string, enabledOnly bool) ([]store.CronSchedule, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]store.CronSchedule, 0, len(s.items))
	for _, item := range s.items {
		if gatewayName != "" && item.GatewayName != gatewayName {
			continue
		}
		if enabledOnly && !item.Enabled {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func TestScheduleUpsertTool_Create(t *testing.T) {
	s := newScheduleStoreStub()
	tool := NewScheduleUpsertTool(s)
	tool.now = func() time.Time { return time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC) }
	tool.nextTick = func(expr string, now time.Time) (time.Time, error) {
		if expr != "*/15 * * * *" {
			t.Fatalf("expr = %q, want %q", expr, "*/15 * * * *")
		}
		return now.Add(15 * time.Minute), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"cron_expression": "*/15 * * * *",
		"target_alias":    "kairo",
		"message":         "Time for check",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	item, ok := s.items[1]
	if !ok {
		t.Fatal("expected created schedule with ID 1")
	}
	if item.Tool != MatrixSendToolName {
		t.Errorf("tool = %q, want %q", item.Tool, MatrixSendToolName)
	}
	if item.Payload.TargetAlias != "kairo" {
		t.Errorf("target = %q, want %q", item.Payload.TargetAlias, "kairo")
	}
}

func TestScheduleUpsertTool_Update(t *testing.T) {
	s := newScheduleStoreStub()
	s.items[7] = store.CronSchedule{
		ID:             7,
		GatewayName:    "scheduler",
		CronExpression: "*/10 * * * *",
		Tool:           MatrixSendToolName,
		Payload:        store.CronSchedulePayload{TargetAlias: "kairo", Message: "old"},
		Enabled:        true,
		NextTriggerAt:  time.Now().UTC(),
	}

	tool := NewScheduleUpsertTool(s)
	tool.now = time.Now
	tool.nextTick = func(_ string, now time.Time) (time.Time, error) { return now.Add(time.Hour), nil }

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"id":              float64(7),
		"cron_expression": "0 8 * * *",
		"target_alias":    "user",
		"message":         "Morning report",
		"enabled":         false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := s.items[7].Payload.TargetAlias; got != "user" {
		t.Errorf("target = %q, want %q", got, "user")
	}
	if s.items[7].Enabled {
		t.Error("expected updated schedule to be disabled")
	}
}

func TestScheduleDisableTool_DisablesByID(t *testing.T) {
	s := newScheduleStoreStub()
	s.items[3] = store.CronSchedule{ID: 3, Enabled: true}
	tool := NewScheduleDisableTool(s)
	_, err := tool.Execute(context.Background(), map[string]interface{}{"id": float64(3)})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if s.items[3].Enabled {
		t.Fatal("expected schedule to be disabled")
	}
}

func TestScheduleListTool_ReturnsJSON(t *testing.T) {
	s := newScheduleStoreStub()
	s.items[1] = store.CronSchedule{
		ID:             1,
		GatewayName:    "scheduler",
		CronExpression: "*/15 * * * *",
		Tool:           MatrixSendToolName,
		Payload:        store.CronSchedulePayload{TargetAlias: "kairo", Message: "tick"},
		Enabled:        true,
		NextTriggerAt:  time.Date(2026, 3, 2, 10, 15, 0, 0, time.UTC),
	}
	tool := NewScheduleListTool(s)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"enabled_only": true})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("len(decoded) = %d, want 1", len(decoded))
	}
	if decoded[0]["target_alias"] != "kairo" {
		t.Errorf("target_alias = %v, want %q", decoded[0]["target_alias"], "kairo")
	}
}
