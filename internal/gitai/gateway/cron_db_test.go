package gateway

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

type dbCronStoreStub struct {
	mu sync.Mutex

	schedules []store.CronSchedule

	ensureBootstrapCalls int
	lastBootstrapGateway string
	lastBootstrapPayload store.CronSchedulePayload

	markTriggeredCalls int
	lastMarkedID       int64
	lastMarkedNext     time.Time
}

func (s *dbCronStoreStub) EnsureBootstrapCronSchedule(gatewayName, cronExpression, tool string, payload store.CronSchedulePayload, nextTriggerAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureBootstrapCalls++
	s.lastBootstrapGateway = gatewayName
	s.lastBootstrapPayload = payload
	return nil
}

func (s *dbCronStoreStub) ListDueCronSchedules(gatewayName string, now time.Time) ([]store.CronSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.CronSchedule, 0)
	for _, row := range s.schedules {
		if row.GatewayName != gatewayName || !row.Enabled {
			continue
		}
		if !row.NextTriggerAt.After(now) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *dbCronStoreStub) MarkCronScheduleTriggered(id int64, firedAt, nextTriggerAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markTriggeredCalls++
	s.lastMarkedID = id
	s.lastMarkedNext = nextTriggerAt
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastTriggerAt = sql.NullTime{Time: firedAt, Valid: true}
			s.schedules[i].NextTriggerAt = nextTriggerAt
		}
	}
	return nil
}

func (s *dbCronStoreStub) MarkCronScheduleNextRun(id int64, nextTriggerAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].NextTriggerAt = nextTriggerAt
		}
	}
	return nil
}

func (s *dbCronStoreStub) DisableCronSchedule(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].Enabled = false
		}
	}
	return nil
}

func TestManager_DBBackedCron_DispatchesDueSchedule(t *testing.T) {
	start := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	storeStub := &dbCronStoreStub{
		schedules: []store.CronSchedule{
			{
				ID:             1,
				GatewayName:    "scheduler",
				CronExpression: "*/15 * * * *",
				Tool:           "matrix.send_message",
				Payload:        store.CronSchedulePayload{TargetAlias: "kairo", Message: "tick"},
				Enabled:        true,
				NextTriggerAt:  start.Add(-time.Minute),
			},
		},
	}

	dispatchCh := make(chan map[string]interface{}, 1)
	mgr := NewManagerWithClock("http://127.0.0.1:8765", clk)
	mgr.EnableDBSchedules(storeStub, func(_ context.Context, gatewayName, tool string, args map[string]interface{}) error {
		if gatewayName != "scheduler" {
			t.Fatalf("gatewayName = %q, want %q", gatewayName, "scheduler")
		}
		if tool != "matrix.send_message" {
			t.Fatalf("tool = %q, want %q", tool, "matrix.send_message")
		}
		dispatchCh <- args
		return nil
	})
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{{
		Name: "scheduler",
		Type: "cron",
		Config: map[string]string{
			"source": "db",
		},
	}})

	select {
	case args := <-dispatchCh:
		if args["target"] != "kairo" {
			t.Errorf("target = %v, want %q", args["target"], "kairo")
		}
		if args["message"] != "tick" {
			t.Errorf("message = %v, want %q", args["message"], "tick")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DB-backed cron dispatch")
	}

	storeStub.mu.Lock()
	defer storeStub.mu.Unlock()
	if storeStub.markTriggeredCalls == 0 {
		t.Fatal("expected MarkCronScheduleTriggered to be called")
	}
}

func TestManager_DBBackedCron_EnsuresBootstrapSchedule(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC))
	storeStub := &dbCronStoreStub{}

	mgr := NewManagerWithClock("http://127.0.0.1:8765", clk)
	mgr.EnableDBSchedules(storeStub, func(_ context.Context, _, _ string, _ map[string]interface{}) error { return nil })
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{{
		Name: "scheduler",
		Type: "cron",
		Config: map[string]string{
			"source":     "db",
			"expression": "*/15 * * * *",
			"target":     "kairo",
			"payload":    "Time for a portfolio check.",
		},
	}})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		storeStub.mu.Lock()
		calls := storeStub.ensureBootstrapCalls
		gateway := storeStub.lastBootstrapGateway
		target := storeStub.lastBootstrapPayload.TargetAlias
		storeStub.mu.Unlock()
		if calls > 0 {
			if gateway != "scheduler" {
				t.Fatalf("bootstrap gateway = %q, want %q", gateway, "scheduler")
			}
			if target != "kairo" {
				t.Fatalf("bootstrap target = %q, want %q", target, "kairo")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expected EnsureBootstrapCronSchedule to be called")
}
