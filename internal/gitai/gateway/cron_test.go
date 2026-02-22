package gateway

// Tests for the built-in cron gateway (R12.3):
//   - parseCron / parseCronField: expression parsing correctness
//   - cronSchedule.Next: next-tick computation
//   - Manager: fires events at correct intervals (fake clock)
//   - Manager: stops cleanly on shutdown
//   - Manager: reconfigures when Gosuto config changes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// ────────────────────────────────────────────────────────────────────────────
// Fake clock for time-controlled tests
// ────────────────────────────────────────────────────────────────────────────

type fakeClock struct {
	mu           sync.Mutex
	current      time.Time
	waiters      []fakeWaiter
	totalWaiters int // monotonically increasing count of all After() calls ever made
}

type fakeWaiter struct {
	fireAt time.Time
	ch     chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{current: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	fireAt := c.current.Add(d)
	c.waiters = append(c.waiters, fakeWaiter{fireAt: fireAt, ch: ch})
	c.totalWaiters++
	return ch
}

// Advance moves the clock forward by d and fires any waiters whose deadline
// has passed. It is safe to call from test goroutines concurrently.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.current = c.current.Add(d)
	now := c.current
	var remaining []fakeWaiter
	for _, w := range c.waiters {
		if !now.Before(w.fireAt) {
			w.ch <- w.fireAt
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
	c.mu.Unlock()
}

// WaitForWaiter blocks until at least n total After() calls have been made
// (including calls already made before this function is invoked) or timeout
// elapses. Using the cumulative count means this works reliably even when some
// waiters have already been consumed (orphaned channels, previous ticks, etc.).
func (c *fakeClock) WaitForWaiter(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		have := c.totalWaiters
		c.mu.Unlock()
		if have >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// TotalWaiters returns the cumulative number of After() calls ever made.
func (c *fakeClock) TotalWaiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalWaiters
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// cronGW builds a minimal cron Gateway spec.
func cronGW(name, expr, payload string) gosutospec.Gateway {
	return gosutospec.Gateway{
		Name: name,
		Type: "cron",
		Config: map[string]string{
			"expression": expr,
			"payload":    payload,
		},
	}
}

// captureServer returns an httptest.Server that records every POST body it
// receives as a decoded envelope.Event. Each event is sent on the returned
// channel (buffered at 32 so tests never block the handler goroutine).
func captureServer(t *testing.T) (*httptest.Server, <-chan envelope.Event) {
	t.Helper()
	ch := make(chan envelope.Event, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/events/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var evt envelope.Event
		if err := json.Unmarshal(body, &evt); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Write the response BEFORE sending to the channel so that fire()
		// receives the 202 and returns before waitEvent() unblocks the test.
		// This prevents a race where the test calls Reconcile (cancelling the
		// goroutine's context) while fire() is still waiting for the response.
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"queued"}`)) //nolint:errcheck
		// Flush to ensure the response bytes are visible to the HTTP client.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		ch <- evt
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, ch
}

// waitEvent blocks until an event arrives on ch or the timeout elapses.
func waitEvent(t *testing.T, ch <-chan envelope.Event, timeout time.Duration) (envelope.Event, bool) {
	t.Helper()
	select {
	case evt := <-ch:
		return evt, true
	case <-time.After(timeout):
		return envelope.Event{}, false
	}
}

// ────────────────────────────────────────────────────────────────────────────
// parseCron / parseCronField tests
// ────────────────────────────────────────────────────────────────────────────

func TestParseCron_Valid(t *testing.T) {
	cases := []struct {
		expr string
	}{
		{"* * * * *"},        // every minute
		{"0 * * * *"},        // top of every hour
		{"*/15 * * * *"},     // every 15 minutes
		{"0 9 * * 1-5"},      // 09:00 on weekdays
		{"30 6 1,15 * *"},    // 06:30 on the 1st and 15th
		{"0 0 1 1 *"},        // once a year
		{"0-5 * * * *"},      // first 6 minutes of every hour
		{"0 8-18/2 * * 1-5"}, // every 2 hours 08-18 on weekdays
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			sched, err := parseCron(tc.expr)
			if err != nil {
				t.Fatalf("parseCron(%q) unexpected error: %v", tc.expr, err)
			}
			if sched == nil {
				t.Fatalf("parseCron(%q) returned nil schedule", tc.expr)
			}
		})
	}
}

func TestParseCron_Invalid(t *testing.T) {
	cases := []struct {
		expr string
		desc string
	}{
		{"* * * *", "only 4 fields"},
		{"* * * * * *", "6 fields"},
		{"60 * * * *", "minute out of range"},
		{"* 24 * * *", "hour out of range"},
		{"* * 0 * *", "day-of-month out of range (0)"},
		{"* * 32 * *", "day-of-month out of range (32)"},
		{"* * * 0 *", "month out of range (0)"},
		{"* * * 13 *", "month out of range (13)"},
		{"* * * * 7", "day-of-week out of range (7)"},
		{"abc * * * *", "non-numeric minute"},
		{"*/0 * * * *", "step zero"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := parseCron(tc.expr)
			if err == nil {
				t.Errorf("parseCron(%q) expected error, got nil", tc.expr)
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// cronSchedule.Next tests
// ────────────────────────────────────────────────────────────────────────────

func TestCronScheduleNext_EveryMinute(t *testing.T) {
	sched, err := parseCron("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Any given time T → next tick should be T+1 minute truncated to the minute.
	base := time.Date(2026, 1, 15, 10, 30, 45, 0, time.UTC)
	next := sched.Next(base)
	want := time.Date(2026, 1, 15, 10, 31, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

func TestCronScheduleNext_Every15Minutes(t *testing.T) {
	sched, err := parseCron("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	// At 10:07, the next */15 should be 10:15.
	base := time.Date(2026, 1, 15, 10, 7, 0, 0, time.UTC)
	next := sched.Next(base)
	want := time.Date(2026, 1, 15, 10, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

func TestCronScheduleNext_HourlyAtTop(t *testing.T) {
	sched, err := parseCron("0 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	next := sched.Next(base)
	want := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

func TestCronScheduleNext_WeekdayOnly(t *testing.T) {
	// "0 9 * * 1-5" → 09:00 Mon-Fri
	sched, err := parseCron("0 9 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-01-17 is a Saturday. Next match should be Monday 2026-01-19 09:00.
	base := time.Date(2026, 1, 17, 9, 0, 0, 0, time.UTC) // Saturday 09:00
	next := sched.Next(base)
	want := time.Date(2026, 1, 19, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", base, next, want)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Manager: fires events at correct intervals
// ────────────────────────────────────────────────────────────────────────────

// TestManager_FiresOnSchedule verifies that the Manager delivers a cron.tick
// event to the ACP /events/{source} endpoint after the first tick.
// Uses a fake clock so the test completes instantly.
func TestManager_FiresOnSchedule(t *testing.T) {
	srv, events := captureServer(t)

	// Start time: 10:07:00 UTC. First */15 tick at 10:15:00 (8 minutes away).
	start := time.Date(2026, 1, 15, 10, 7, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("scheduler", "*/15 * * * *", "Trigger scheduled check"),
	})

	// Wait until the goroutine has called After() and is sleeping.
	if !clk.WaitForWaiter(1, 2*time.Second) {
		t.Fatal("cron goroutine did not register a timer waiter in time")
	}

	// Advance the clock past the first 10:15 tick (8 minutes + a little).
	clk.Advance(9 * time.Minute)

	evt, ok := waitEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for first cron event")
	}
	if evt.Source != "scheduler" {
		t.Errorf("event.Source = %q, want %q", evt.Source, "scheduler")
	}
	if evt.Type != "cron.tick" {
		t.Errorf("event.Type = %q, want %q", evt.Type, "cron.tick")
	}
	if evt.Payload.Message != "Trigger scheduled check" {
		t.Errorf("event.Payload.Message = %q, want %q",
			evt.Payload.Message, "Trigger scheduled check")
	}
}

// TestManager_FiresMultipleTicks verifies that the gateway continues firing
// after the first tick.
func TestManager_FiresMultipleTicks(t *testing.T) {
	srv, events := captureServer(t)

	// Start at 10:00:00. "* * * * *" fires every minute.
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("every-min", "* * * * *", "tick"),
	})

	const ticks = 3
	for i := 0; i < ticks; i++ {
		// Wait for the goroutine to be sleeping on its next timer before advancing.
		// The cumulative waiter count must reach i+1 (first waiter registered at
		// startup, subsequent ones after each fire).
		if !clk.WaitForWaiter(i+1, 2*time.Second) {
			t.Fatalf("cron goroutine did not register timer for tick %d", i+1)
		}
		clk.Advance(time.Minute)
		_, ok := waitEvent(t, events, 2*time.Second)
		if !ok {
			t.Fatalf("timed out waiting for tick %d", i+1)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Manager: stops cleanly on shutdown
// ────────────────────────────────────────────────────────────────────────────

// TestManager_StopsCleanly starts a cron job then calls Stop. It verifies that
// Stop returns promptly (no deadlock or goroutine leak) and that no further
// events are delivered after Stop.
func TestManager_StopsCleanly(t *testing.T) {
	srv, events := captureServer(t)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)

	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("tick", "* * * * *", "hello"),
	})

	// Trigger one tick to confirm the job is running.
	if !clk.WaitForWaiter(1, 2*time.Second) {
		t.Fatal("cron goroutine did not register timer waiter")
	}
	clk.Advance(time.Minute)
	_, ok := waitEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for initial tick before stop")
	}

	// Wait for the goroutine to re-register its next timer so that Stop doesn't
	// race with an in-flight HTTP request inside fire().
	if !clk.WaitForWaiter(2, 2*time.Second) {
		t.Fatal("goroutine did not re-register timer after first tick")
	}

	// Stop and verify it returns promptly.
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK — stopped cleanly.
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3s; possible deadlock")
	}

	// Drain any in-flight event, then advance and verify no more arrive.
	select {
	case <-events:
	default:
	}
	clk.Advance(time.Minute)
	select {
	case <-events:
		t.Error("received event after Stop(); job was not shut down cleanly")
	case <-time.After(100 * time.Millisecond):
		// Good — no event delivered.
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Manager: reconfigures on Gosuto update
// ────────────────────────────────────────────────────────────────────────────

// TestManager_ReconfiguresOnExpressionChange verifies that when Reconcile is
// called with a changed cron expression, the old job is stopped and a new one
// is started with the updated schedule.
func TestManager_ReconfiguresOnExpressionChange(t *testing.T) {
	srv, events := captureServer(t)

	// Start at 10:00. Initial expression: every 5 minutes.
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("worker", "*/5 * * * *", "old"),
	})

	// Trigger first tick of the old schedule (5 minutes).
	if !clk.WaitForWaiter(1, 2*time.Second) {
		t.Fatal("cron goroutine did not register timer waiter (old schedule)")
	}
	clk.Advance(5 * time.Minute)
	evt, ok := waitEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for initial tick")
	}
	if evt.Payload.Message != "old" {
		t.Errorf("expected payload 'old', got %q", evt.Payload.Message)
	}

	// Wait for the goroutine to re-register its next timer (i.e. fire() has
	// fully completed and the goroutine is sleeping on the 10:10 tick). This
	// must happen BEFORE calling Reconcile so that the context cancellation
	// inside Reconcile doesn't race with an in-flight HTTP request.
	if !clk.WaitForWaiter(2, 2*time.Second) {
		t.Fatal("goroutine did not re-register timer after first tick")
	}

	// Reconfigure: change expression and payload.
	// Capture the cumulative waiter count so we can detect the new goroutine's
	// first After() call robustly, regardless of whether the old goroutine
	// registered an orphaned waiter before being cancelled.
	waitersBefore := clk.TotalWaiters()
	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("worker", "*/10 * * * *", "new"),
	})

	// Wait for exactly one new After() call from the freshly started goroutine.
	if !clk.WaitForWaiter(waitersBefore+1, 2*time.Second) {
		t.Fatal("new cron goroutine did not register timer waiter after reconfigure")
	}
	clk.Advance(5 * time.Minute) // → 10:10

	evt, ok = waitEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for tick after reconfigure")
	}
	if evt.Payload.Message != "new" {
		t.Errorf("expected payload 'new' after reconfigure, got %q", evt.Payload.Message)
	}
}

// TestManager_RemovesExpiredJob verifies that when Reconcile is called without
// a previously running job, the job is stopped and emits no further events.
func TestManager_RemovesExpiredJob(t *testing.T) {
	srv, events := captureServer(t)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{
		cronGW("removed", "* * * * *", "tick"),
	})

	// Confirm it fires.
	if !clk.WaitForWaiter(1, 2*time.Second) {
		t.Fatal("cron goroutine did not register timer waiter")
	}
	clk.Advance(time.Minute)
	_, ok := waitEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("timed out waiting for initial tick")
	}

	// Wait for the goroutine to re-register its next timer before removing it
	// to avoid racing with an in-flight fire() HTTP request.
	if !clk.WaitForWaiter(2, 2*time.Second) {
		t.Fatal("goroutine did not re-register timer after first tick")
	}

	// Remove the gateway.
	mgr.Reconcile(nil)

	// Drain any in-flight event, then advance and verify no more arrive.
	select {
	case <-events:
	default:
	}
	clk.Advance(time.Minute)
	select {
	case <-events:
		t.Error("received event after job was removed by Reconcile")
	case <-time.After(100 * time.Millisecond):
		// Good.
	}
}

// TestManager_NoOpForNonCronGateways verifies that gateways with type != "cron"
// are silently ignored by the Manager.
func TestManager_NoOpForNonCronGateways(t *testing.T) {
	srv, events := captureServer(t)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	mgr.Reconcile([]gosutospec.Gateway{
		{Name: "inbound-hook", Type: "webhook", Config: map[string]string{}},
		{Name: "custom", Command: "my-gateway-binary", Config: map[string]string{}},
	})

	clk.Advance(time.Minute)
	select {
	case <-events:
		t.Error("received unexpected event from non-cron gateway")
	case <-time.After(50 * time.Millisecond):
		// Good — no events.
	}
}

// ────────────────────────────────────────────────────────────────────────────
// ACPBaseURL helper
// ────────────────────────────────────────────────────────────────────────────

func TestACPBaseURL(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{":8765", "http://127.0.0.1:8765"},
		{"0.0.0.0:8765", "http://127.0.0.1:8765"},
		{"127.0.0.1:8765", "http://127.0.0.1:8765"},
		{":0", "http://127.0.0.1:0"},
	}
	for _, tc := range cases {
		got := ACPBaseURL(tc.addr)
		if got != tc.want {
			t.Errorf("ACPBaseURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Race detection helper
// ────────────────────────────────────────────────────────────────────────────

// TestManager_ConcurrentReconcile runs multiple Reconcile calls and Advance
// calls concurrently to verify no data race exists. Run with -race.
func TestManager_ConcurrentReconcile(t *testing.T) {
	srv, _ := captureServer(t)

	start := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)

	mgr := NewManagerWithClock(srv.URL, clk)
	defer mgr.Stop()

	var wg sync.WaitGroup
	var fired atomic.Int64

	// Advance time rapidly while Reconcile is also running.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			clk.Advance(time.Minute)
			fired.Add(1)
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			mgr.Reconcile([]gosutospec.Gateway{
				cronGW("job", "* * * * *", "ping"),
			})
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()
}
