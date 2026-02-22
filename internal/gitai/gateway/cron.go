// Package gateway implements built-in inbound event gateways for Gitai agents.
//
// Currently provides:
//   - Cron gateway: fires cron.tick events on a configurable 5-field cron schedule,
//     posting them to the agent's local ACP POST /events/{source} endpoint so that
//     the turn engine handles them identically to externally triggered events.
//
// The gateway manager (Manager) reconciles running cron jobs against the active
// Gosuto config, starting/stopping/restarting them as needed. It is wired into
// App.Run and the ApplyConfig callback so that gateway changes take effect
// without an agent restart.
//
// Clock injection: the Manager accepts an optional clock interface so that tests
// can advance time precisely without relying on wall-clock sleeps.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// ────────────────────────────────────────────────────────────────────────────
// Clock abstraction (testability)
// ────────────────────────────────────────────────────────────────────────────

// clock is an interface over time.Now and time.After, allowing tests to
// substitute a controlled fake clock that advances on demand.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock delegates to the standard library.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ────────────────────────────────────────────────────────────────────────────
// Cron expression parser
// ────────────────────────────────────────────────────────────────────────────

// cronSchedule holds the sets of matching values for each of the 5 cron fields.
// The standard 5-field format is:
//
//	minute(0-59)  hour(0-23)  day-of-month(1-31)  month(1-12)  day-of-week(0-6)
type cronSchedule struct {
	minute     []int
	hour       []int
	dayOfMonth []int
	month      []int
	dayOfWeek  []int
}

// parseCron parses a 5-field cron expression (space-separated) and returns
// a compiled schedule. Supported field syntax:
//
//   - every value in the allowed range
//     */N        every Nth value (step)
//     N          single value
//     N-M        range [N, M] inclusive
//     N-M/S      range with step S
//     A,B,C      list of values
func parseCron(expr string) (*cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have exactly 5 fields, got %d in %q", len(fields), expr)
	}

	parse := func(field string, min, max int) ([]int, error) {
		return parseCronField(field, min, max)
	}

	minute, err := parse(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field %q: %w", fields[0], err)
	}
	hour, err := parse(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field %q: %w", fields[1], err)
	}
	dayOfMonth, err := parse(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field %q: %w", fields[2], err)
	}
	month, err := parse(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field %q: %w", fields[3], err)
	}
	dayOfWeek, err := parse(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field %q: %w", fields[4], err)
	}

	return &cronSchedule{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dayOfMonth,
		month:      month,
		dayOfWeek:  dayOfWeek,
	}, nil
}

// parseCronField parses a single cron field into the set of matching integer
// values within [min, max] inclusive.
func parseCronField(field string, min, max int) ([]int, error) {
	// Handle step: */N or range/N
	if idx := strings.LastIndex(field, "/"); idx != -1 {
		stepStr := field[idx+1:]
		step, err := strconv.Atoi(stepStr)
		if err != nil || step <= 0 {
			return nil, fmt.Errorf("invalid step value %q", stepStr)
		}
		base := field[:idx]
		var start, end int
		if base == "*" {
			start, end = min, max
		} else if rangeIdx := strings.Index(base, "-"); rangeIdx != -1 {
			s, e, err := parseRange(base, min, max)
			if err != nil {
				return nil, err
			}
			start, end = s, e
		} else {
			v, err := strconv.Atoi(base)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", base)
			}
			start, end = v, max
		}
		if err := checkRange(start, end, min, max); err != nil {
			return nil, err
		}
		var vals []int
		for v := start; v <= end; v += step {
			vals = append(vals, v)
		}
		return vals, nil
	}

	// Wildcard
	if field == "*" {
		vals := make([]int, max-min+1)
		for i := range vals {
			vals[i] = min + i
		}
		return vals, nil
	}

	// List: A,B,C
	if strings.Contains(field, ",") {
		parts := strings.Split(field, ",")
		seen := make(map[int]bool)
		var vals []int
		for _, p := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return nil, fmt.Errorf("invalid list value %q", p)
			}
			if v < min || v > max {
				return nil, fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
			}
			if !seen[v] {
				seen[v] = true
				vals = append(vals, v)
			}
		}
		sort.Ints(vals)
		return vals, nil
	}

	// Range: N-M
	if strings.Contains(field, "-") {
		start, end, err := parseRange(field, min, max)
		if err != nil {
			return nil, err
		}
		if err := checkRange(start, end, min, max); err != nil {
			return nil, err
		}
		vals := make([]int, end-start+1)
		for i := range vals {
			vals[i] = start + i
		}
		return vals, nil
	}

	// Single value
	v, err := strconv.Atoi(field)
	if err != nil {
		return nil, fmt.Errorf("invalid value %q", field)
	}
	if v < min || v > max {
		return nil, fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
	}
	return []int{v}, nil
}

func parseRange(s string, min, max int) (start, end int, err error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range %q", s)
	}
	start, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range start %q", parts[0])
	}
	end, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range end %q", parts[1])
	}
	return start, end, nil
}

func checkRange(start, end, min, max int) error {
	if start < min || end > max || start > end {
		return fmt.Errorf("range [%d, %d] out of bounds [%d, %d]", start, end, min, max)
	}
	return nil
}

// Next returns the next time after now that matches the schedule. It searches
// forward at minute resolution. Returns the zero time if no match is found
// within one year (should not happen in practice for valid schedules).
func (s *cronSchedule) Next(now time.Time) time.Time {
	// Advance to the start of the next minute, zero out sub-minute precision.
	t := now.Add(time.Minute).Truncate(time.Minute)

	// Search forward for up to 366 days × 24 hours × 60 minutes.
	for range 366 * 24 * 60 {
		if containsInt(s.month, int(t.Month())) &&
			containsInt(s.dayOfMonth, t.Day()) &&
			containsInt(s.dayOfWeek, int(t.Weekday())) &&
			containsInt(s.hour, t.Hour()) &&
			containsInt(s.minute, t.Minute()) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{} // should never occur with valid cron expressions
}

func containsInt(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// ────────────────────────────────────────────────────────────────────────────
// Cron gateway manager
// ────────────────────────────────────────────────────────────────────────────

// cronJob represents a single running cron gateway goroutine.
type cronJob struct {
	name   string
	spec   gosutospec.Gateway
	cancel context.CancelFunc
	done   chan struct{}
}

// Manager manages built-in cron gateways, reconciling them against the active
// Gosuto config. It mirrors the supervisor.Supervisor pattern: New() creates
// an idle manager; Reconcile() starts/stops jobs; Stop() tears everything down.
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*cronJob
	acpURL string
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc
	clk    clock
}

// NewManager returns a new Manager that will POST cron events to acpURL
// (e.g. "http://127.0.0.1:8765"). Call Reconcile to start jobs.
func NewManager(acpURL string) *Manager {
	return NewManagerWithClock(acpURL, realClock{})
}

// NewManagerWithClock is like NewManager but injects a custom clock. Intended
// for tests that need to advance time without wall-clock sleeps.
func NewManagerWithClock(acpURL string, clk clock) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		jobs:   make(map[string]*cronJob),
		acpURL: acpURL,
		client: &http.Client{Timeout: 10 * time.Second},
		ctx:    ctx,
		cancel: cancel,
		clk:    clk,
	}
}

// Reconcile ensures exactly the cron gateways described in gateways are
// running. Gateways whose name no longer appears, or whose expression/payload
// has changed, are stopped before the new version is started.
//
// Non-cron gateways (type != "cron") are silently ignored — they are managed
// elsewhere (external supervisor, webhook handler, etc.).
func (m *Manager) Reconcile(gateways []gosutospec.Gateway) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Index the wanted cron gateways.
	wanted := make(map[string]gosutospec.Gateway)
	for _, gw := range gateways {
		if gw.Type == "cron" {
			wanted[gw.Name] = gw
		}
	}

	// Stop jobs no longer in the spec or whose config changed.
	for name, job := range m.jobs {
		newSpec, ok := wanted[name]
		if !ok || cronSpecChanged(job.spec, newSpec) {
			slog.Info("gateway/cron: stopping cron job", "name", name)
			job.cancel()
			// Wait for the goroutine to exit so we don't leave orphans.
			// The mutex is held here — startLocked must not be called from
			// the goroutine itself (it isn't).
			<-job.done
			delete(m.jobs, name)
		}
	}

	// Start jobs that are wanted but not yet running.
	for name, gw := range wanted {
		if _, running := m.jobs[name]; !running {
			m.startLocked(gw)
		}
	}
}

// Stop cancels all running cron jobs and waits for them to exit.
func (m *Manager) Stop() {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, job := range m.jobs {
		slog.Info("gateway/cron: stopping cron job on shutdown", "name", name)
		job.cancel()
		<-job.done
	}
	m.jobs = make(map[string]*cronJob)
}

// startLocked starts a single cron job. Caller must hold m.mu.
func (m *Manager) startLocked(gw gosutospec.Gateway) {
	expr := gw.Config["expression"]
	sched, err := parseCron(expr)
	if err != nil {
		slog.Error("gateway/cron: invalid cron expression; job not started",
			"name", gw.Name, "expression", expr, "err", err)
		return
	}

	ctx, cancel := context.WithCancel(m.ctx)
	job := &cronJob{
		name:   gw.Name,
		spec:   gw,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.jobs[gw.Name] = job

	slog.Info("gateway/cron: starting cron job",
		"name", gw.Name, "expression", expr)
	go m.runJob(ctx, job, sched)
}

// runJob runs the event-fire loop for a single cron gateway. It blocks until
// ctx is cancelled.
func (m *Manager) runJob(ctx context.Context, job *cronJob, sched *cronSchedule) {
	defer close(job.done)

	for {
		next := sched.Next(m.clk.Now())
		if next.IsZero() {
			slog.Error("gateway/cron: could not compute next tick; stopping job",
				"name", job.name)
			return
		}

		delay := next.Sub(m.clk.Now())
		if delay < 0 {
			delay = 0
		}

		select {
		case <-ctx.Done():
			slog.Info("gateway/cron: cron job stopped", "name", job.name)
			return
		case <-m.clk.After(delay):
			m.fire(ctx, job)
		}
	}
}

// fire constructs a cron.tick Event envelope and POSTs it to the ACP event
// ingress endpoint. Errors are logged but do not stop the job.
func (m *Manager) fire(ctx context.Context, job *cronJob) {
	now := m.clk.Now()
	payload := job.spec.Config["payload"]

	evt := envelope.Event{
		Source: job.name,
		Type:   "cron.tick",
		TS:     now,
		Payload: envelope.EventPayload{
			Message: payload,
		},
	}

	body, err := json.Marshal(evt)
	if err != nil {
		slog.Error("gateway/cron: failed to marshal event",
			"name", job.name, "err", err)
		return
	}

	url := m.acpURL + "/events/" + job.name
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Error("gateway/cron: failed to create HTTP request",
			"name", job.name, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		slog.Warn("gateway/cron: failed to deliver event to ACP",
			"name", job.name, "err", err)
		return
	}
	defer resp.Body.Close()
	// Drain body to enable HTTP keep-alive reuse.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		slog.Warn("gateway/cron: unexpected ACP response",
			"name", job.name, "status", resp.StatusCode)
		return
	}

	slog.Info("gateway/cron: event delivered",
		"name", job.name, "type", "cron.tick")
}

// cronSpecChanged reports whether the cron-relevant parts of the gateway spec
// have changed (expression or payload). When true, the old job must be stopped
// and a new one started with the updated config.
func cronSpecChanged(old, newSpec gosutospec.Gateway) bool {
	return old.Config["expression"] != newSpec.Config["expression"] ||
		old.Config["payload"] != newSpec.Config["payload"]
}

// ACPBaseURL converts an ACP listen address (e.g. ":8765" or "0.0.0.0:8765")
// into an http:// URL pointing at the loopback interface. Built-in gateways
// always connect via localhost so they bypass the ACP bearer-token check.
func ACPBaseURL(addr string) string {
	// addr may be ":8765", "0.0.0.0:8765", "127.0.0.1:8765", etc.
	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		return "http://127.0.0.1:8765"
	}
	port := addr[colonIdx+1:]
	if port == "" {
		return "http://127.0.0.1:8765"
	}
	return "http://127.0.0.1:" + port
}
