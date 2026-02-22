package supervisor

import (
"os"
"strings"
"testing"
"time"

gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// ─────────────────────────────────────────────────────────────────────────────
// Pure-function unit tests (no processes required)
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildGatewayEnv_ContainsGatewayTargetURL(t *testing.T) {
gw := gosutospec.Gateway{
Name:    "my-gw",
Command: "/usr/bin/my-gateway",
}
env := buildGatewayEnv(gw, "http://127.0.0.1:8765", nil)

want := "GATEWAY_TARGET_URL=http://127.0.0.1:8765/events/my-gw"
for _, e := range env {
if e == want {
return
}
}
t.Errorf("GATEWAY_TARGET_URL not found in env; want %q", want)
}

func TestBuildGatewayEnv_InjectsGATEWAYPrefixedConfig(t *testing.T) {
gw := gosutospec.Gateway{
Name:    "my-gw",
Command: "/usr/bin/my-gateway",
Config: map[string]string{
"interval": "30",
"topic":    "finance",
},
}
env := buildGatewayEnv(gw, "http://127.0.0.1:9000", nil)

wants := []string{"GATEWAY_INTERVAL=30", "GATEWAY_TOPIC=finance"}
for _, want := range wants {
found := false
for _, e := range env {
if e == want {
found = true
break
}
}
if !found {
t.Errorf("expected %q in env, not found", want)
}
}
}

func TestBuildGatewayEnv_InjectsSecretEnvAndSpecEnv(t *testing.T) {
gw := gosutospec.Gateway{
Name:    "secure-gw",
Command: "/usr/bin/gw",
Env:     map[string]string{"STATIC_VAR": "hello"},
}
secretEnv := map[string]string{"API_KEY": "supersecret"}
env := buildGatewayEnv(gw, "http://127.0.0.1:8765", secretEnv)

for _, want := range []string{"API_KEY=supersecret", "STATIC_VAR=hello"} {
found := false
for _, e := range env {
if e == want {
found = true
break
}
}
if !found {
t.Errorf("expected %q in env, not found", want)
}
}
}

func TestExternalGatewayChanged_NoChange(t *testing.T) {
gw := gosutospec.Gateway{
Command:     "/bin/gw",
Args:        []string{"--port", "9090"},
Env:         map[string]string{"FOO": "bar"},
Config:      map[string]string{"key": "val"},
AutoRestart: true,
}
if externalGatewayChanged(gw, gw) {
t.Error("expected no change when spec is identical")
}
}

func TestExternalGatewayChanged_CommandChange(t *testing.T) {
old := gosutospec.Gateway{Command: "/bin/gw-v1"}
newGW := gosutospec.Gateway{Command: "/bin/gw-v2"}
if !externalGatewayChanged(old, newGW) {
t.Error("expected change detected when Command differs")
}
}

func TestExternalGatewayChanged_ArgsChange(t *testing.T) {
old := gosutospec.Gateway{Command: "/bin/gw", Args: []string{"--a"}}
newGW := gosutospec.Gateway{Command: "/bin/gw", Args: []string{"--b"}}
if !externalGatewayChanged(old, newGW) {
t.Error("expected change detected when Args differ")
}
}

func TestExternalGatewayChanged_EnvChange(t *testing.T) {
old := gosutospec.Gateway{Command: "/bin/gw", Env: map[string]string{"X": "1"}}
newGW := gosutospec.Gateway{Command: "/bin/gw", Env: map[string]string{"X": "2"}}
if !externalGatewayChanged(old, newGW) {
t.Error("expected change detected when Env differs")
}
}

func TestExternalGatewayChanged_ConfigChange(t *testing.T) {
old := gosutospec.Gateway{Command: "/bin/gw", Config: map[string]string{"k": "v1"}}
newGW := gosutospec.Gateway{Command: "/bin/gw", Config: map[string]string{"k": "v2"}}
if !externalGatewayChanged(old, newGW) {
t.Error("expected change detected when Config differs")
}
}

func TestExternalGatewayChanged_AutoRestartChange(t *testing.T) {
old := gosutospec.Gateway{Command: "/bin/gw", AutoRestart: false}
newGW := gosutospec.Gateway{Command: "/bin/gw", AutoRestart: true}
if !externalGatewayChanged(old, newGW) {
t.Error("expected change detected when AutoRestart differs")
}
}

// ─────────────────────────────────────────────────────────────────────────────
// Process lifecycle tests (require /bin/sh and /bin/sleep on the host)
// ─────────────────────────────────────────────────────────────────────────────

// TestExternalGatewaySupervisor_EnvironmentInjection starts an external gateway
// that writes its environment to a temp file, then verifies the expected vars.
func TestExternalGatewaySupervisor_EnvironmentInjection(t *testing.T) {
tmpFile, err := os.CreateTemp(t.TempDir(), "gw-env-*.txt")
if err != nil {
t.Fatal(err)
}
tmpFile.Close()
envPath := tmpFile.Name()

gw := gosutospec.Gateway{
Name:    "env-writer",
Command: "/bin/sh",
Args:    []string{"-c", "env > " + envPath},
Config:  map[string]string{"interval": "60"},
AutoRestart: false,
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765")
s.ApplySecrets(map[string]string{"TEST_SECRET": "mysecret"})
s.Reconcile([]gosutospec.Gateway{gw})
defer s.Stop()

// Poll for the one-shot process to write the file (up to 3 s).
deadline := time.Now().Add(3 * time.Second)
for {
data, readErr := os.ReadFile(envPath)
if readErr == nil && len(data) > 0 {
content := string(data)
wantVars := []string{
"GATEWAY_TARGET_URL=http://127.0.0.1:8765/events/env-writer",
"GATEWAY_INTERVAL=60",
"TEST_SECRET=mysecret",
}
for _, want := range wantVars {
if !strings.Contains(content, want) {
t.Errorf("expected %q in gateway environment, not found\nenv output:\n%s", want, content)
}
}
return
}
if time.Now().After(deadline) {
t.Fatal("timed out waiting for gateway process to write env file")
}
time.Sleep(50 * time.Millisecond)
}
}

// TestExternalGatewaySupervisor_StartAndStop verifies that a running external
// gateway process is cleanly terminated when Stop() is called.
func TestExternalGatewaySupervisor_StartAndStop(t *testing.T) {
gw := gosutospec.Gateway{
Name:        "long-running",
Command:     "/bin/sleep",
Args:        []string{"60"},
AutoRestart: false,
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765")
s.Reconcile([]gosutospec.Gateway{gw})

time.Sleep(100 * time.Millisecond)

s.mu.RLock()
_, running := s.processes["long-running"]
s.mu.RUnlock()
if !running {
t.Fatal("expected process to be tracked after Reconcile")
}

stopDone := make(chan struct{})
go func() {
s.Stop()
close(stopDone)
}()

select {
case <-stopDone:
// Good.
case <-time.After(10 * time.Second):
t.Fatal("Stop() did not return within timeout")
}

s.mu.RLock()
remaining := len(s.processes)
s.mu.RUnlock()
if remaining != 0 {
t.Errorf("expected 0 tracked processes after Stop, got %d", remaining)
}
}

// TestExternalGatewaySupervisor_AutoRestart verifies that a crashing external
// gateway is restarted when autoRestart is true.
func TestExternalGatewaySupervisor_AutoRestart(t *testing.T) {
tmpFile, err := os.CreateTemp(t.TempDir(), "gw-restart-*.txt")
if err != nil {
t.Fatal(err)
}
tmpFile.Close()
counterPath := tmpFile.Name()

gw := gosutospec.Gateway{
Name:        "crasher",
Command:     "/bin/sh",
Args:        []string{"-c", "echo tick >> " + counterPath + "; exit 1"},
AutoRestart: true,
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765").
withRestartDelay(20 * time.Millisecond)
s.Reconcile([]gosutospec.Gateway{gw})

// Wait for at least 3 restart attempts.
deadline := time.Now().Add(5 * time.Second)
for {
data, _ := os.ReadFile(counterPath)
trimmed := strings.TrimSpace(string(data))
lines := 0
if trimmed != "" {
lines = strings.Count(trimmed, "\n") + 1
}
if lines >= 3 {
break
}
if time.Now().After(deadline) {
t.Fatalf("process was not restarted enough times within timeout; got %d restart(s)", lines)
}
time.Sleep(30 * time.Millisecond)
}

s.Stop()
}

// TestExternalGatewaySupervisor_Reconcile_AddAndRemove verifies that Reconcile
// starts new gateways and stops removed ones without touching unchanged ones.
func TestExternalGatewaySupervisor_Reconcile_AddAndRemove(t *testing.T) {
gw1 := gosutospec.Gateway{
Name:        "gw-one",
Command:     "/bin/sleep",
Args:        []string{"60"},
AutoRestart: false,
}
gw2 := gosutospec.Gateway{
Name:        "gw-two",
Command:     "/bin/sleep",
Args:        []string{"60"},
AutoRestart: false,
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765")

s.Reconcile([]gosutospec.Gateway{gw1, gw2})
time.Sleep(100 * time.Millisecond)

s.mu.RLock()
count := len(s.processes)
s.mu.RUnlock()
if count != 2 {
t.Fatalf("expected 2 processes after initial Reconcile, got %d", count)
}

// Remove gw1 — only gw2 should keep running.
s.Reconcile([]gosutospec.Gateway{gw2})
time.Sleep(100 * time.Millisecond)

s.mu.RLock()
_, hasGW1 := s.processes["gw-one"]
_, hasGW2 := s.processes["gw-two"]
s.mu.RUnlock()

if hasGW1 {
t.Error("expected gw-one to be stopped after Reconcile removed it")
}
if !hasGW2 {
t.Error("expected gw-two to still be running after Reconcile")
}

// Remove all.
s.Reconcile([]gosutospec.Gateway{})
time.Sleep(100 * time.Millisecond)

s.mu.RLock()
remaining := len(s.processes)
s.mu.RUnlock()
if remaining != 0 {
t.Errorf("expected 0 processes after empty Reconcile, got %d", remaining)
}

s.Stop()
}

// TestExternalGatewaySupervisor_ReconcileIgnoresBuiltInGateways verifies that
// built-in gateway types (cron, webhook) are not managed by this supervisor.
func TestExternalGatewaySupervisor_ReconcileIgnoresBuiltInGateways(t *testing.T) {
gateways := []gosutospec.Gateway{
{Name: "scheduler", Type: "cron", Config: map[string]string{"expression": "*/5 * * * *"}},
{Name: "hook", Type: "webhook"},
{Name: "external", Command: "/bin/sleep", Args: []string{"60"}},
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765")
s.Reconcile(gateways)
defer s.Stop()

time.Sleep(100 * time.Millisecond)

s.mu.RLock()
_, hasCron := s.processes["scheduler"]
_, hasWebhook := s.processes["hook"]
_, hasExternal := s.processes["external"]
total := len(s.processes)
s.mu.RUnlock()

if hasCron {
t.Error("supervisor should not manage built-in cron gateway")
}
if hasWebhook {
t.Error("supervisor should not manage built-in webhook gateway")
}
if !hasExternal {
t.Error("supervisor should manage external gateway (Command set)")
}
if total != 1 {
t.Errorf("expected 1 process (external only), got %d", total)
}
}

// TestExternalGatewaySupervisor_ReconcileRestartsChangedGateway verifies that
// a gateway whose config changes is stopped and a new process is started.
func TestExternalGatewaySupervisor_ReconcileRestartsChangedGateway(t *testing.T) {
gw := gosutospec.Gateway{
Name:        "mutable",
Command:     "/bin/sleep",
Args:        []string{"60"},
AutoRestart: false,
}

s := NewExternalGatewaySupervisor("http://127.0.0.1:8765")
s.Reconcile([]gosutospec.Gateway{gw})
time.Sleep(100 * time.Millisecond)

s.mu.RLock()
firstProc := s.processes["mutable"]
s.mu.RUnlock()
if firstProc == nil {
t.Fatal("expected process to be tracked after initial Reconcile")
}

// Change the args — should trigger stop + restart with new spec.
gwChanged := gosutospec.Gateway{
Name:        "mutable",
Command:     "/bin/sleep",
Args:        []string{"61"},
AutoRestart: false,
}
s.Reconcile([]gosutospec.Gateway{gwChanged})
time.Sleep(100 * time.Millisecond)

s.mu.RLock()
secondProc := s.processes["mutable"]
s.mu.RUnlock()
if secondProc == nil {
t.Fatal("expected a new process to be tracked after config change")
}
if secondProc == firstProc {
t.Error("expected a new process object after config change, got the same pointer")
}

s.Stop()
}
