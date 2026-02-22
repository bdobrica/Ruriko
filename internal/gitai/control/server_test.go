package control_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/control"
)

// --- helpers ---------------------------------------------------------------

func newTestServer(token string) *control.Server {
	return control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.0.1-test",
		StartedAt: time.Now(),
		Token:     token,
		GosutoHash: func() string {
			return "deadbeefdeadbeefdeadbeefdeadbeef"
		},
		MCPNames: func() []string {
			return []string{"brave-search"}
		},
		ApplyConfig: func(yaml, hash string) error {
			return nil
		},
		ApplySecrets: func(secrets map[string]string) error {
			return nil
		},
		RequestRestart: func() {},
		RequestCancel:  func() {},
	})
}

func startTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := newTestServer(token)
	// Use httptest.Server wrapping the underlying http.Server's handler so
	// we get an auto-allocated port without needing to call Start().
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)
	return ts
}

// --- Auth middleware tests (R2.1) ------------------------------------------

func TestAuthMiddleware_RejectsUnauthenticated(t *testing.T) {
	srv := newTestServer("my-secret-token")
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	// No Authorization header → 401.
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_RejectsBadToken(t *testing.T) {
	srv := newTestServer("my-secret-token")
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_AcceptsValidToken(t *testing.T) {
	srv := newTestServer("my-secret-token")
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer my-secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_NoTokenMeansOpen(t *testing.T) {
	// Token="" → auth disabled → any request is fine.
	srv := newTestServer("")
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Idempotency tests (R2.2) ---------------------------------------------

func TestIdempotency_DuplicateConfigApply(t *testing.T) {
	callCount := 0
	srv := control.New(":0", control.Handlers{
		AgentID:   "test",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplyConfig: func(yaml, hash string) error {
			callCount++
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.ConfigApplyRequest{
		YAML: "metadata:\n  name: test",
		Hash: "abcdef1234567890",
	})
	key := "idem-key-12345"

	// First call — should invoke ApplyConfig.
	req1, _ := http.NewRequest("POST", ts.URL+"/config/apply", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Idempotency-Key", key)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", resp1.StatusCode)
	}

	// Second call with same key — cached, ApplyConfig NOT called again.
	req2, _ := http.NewRequest("POST", ts.URL+"/config/apply", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Idempotency-Key", key)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", resp2.StatusCode)
	}

	if callCount != 1 {
		t.Errorf("ApplyConfig called %d times; want 1 (idempotency should prevent duplicates)", callCount)
	}
}

func TestIdempotency_DifferentKeysCallTwice(t *testing.T) {
	callCount := 0
	srv := control.New(":0", control.Handlers{
		AgentID:   "test",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplyConfig: func(yaml, hash string) error {
			callCount++
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.ConfigApplyRequest{
		YAML: "metadata:\n  name: test",
		Hash: "abcdef1234567890",
	})

	for _, key := range []string{"key-A", "key-B"} {
		req, _ := http.NewRequest("POST", ts.URL+"/config/apply", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request key=%s: %v", key, err)
		}
		resp.Body.Close()
	}

	if callCount != 2 {
		t.Errorf("ApplyConfig called %d times; want 2 (different keys)", callCount)
	}
}

// --- Cancel endpoint tests (R2.5) -----------------------------------------

func TestCancelEndpoint(t *testing.T) {
	cancelCalled := make(chan struct{}, 1)
	srv := control.New(":0", control.Handlers{
		AgentID:   "test",
		Version:   "v0.1",
		StartedAt: time.Now(),
		RequestCancel: func() {
			cancelCalled <- struct{}{}
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/tasks/cancel", "", nil)
	if err != nil {
		t.Fatalf("POST /tasks/cancel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", resp.StatusCode)
	}

	// Verify the body contains "cancelling".
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "cancelling") {
		t.Errorf("body %q does not contain 'cancelling'", string(b))
	}

	// Give the goroutine a moment to fire.
	select {
	case <-cancelCalled:
		// OK
	case <-time.After(time.Second):
		t.Error("RequestCancel was not called within 1s")
	}
}

func TestCancelEndpoint_Unavailable(t *testing.T) {
	// When RequestCancel is nil, the endpoint should return 503.
	srv := control.New(":0", control.Handlers{
		AgentID:   "test",
		Version:   "v0.1",
		StartedAt: time.Now(),
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/tasks/cancel", "", nil)
	if err != nil {
		t.Fatalf("POST /tasks/cancel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

// --- Secrets token (R4.2) --------------------------------------------------

// fakeKuzeServer creates an httptest.Server that simulates the Kuze
// GET /kuze/redeem/<token> endpoint. It verifies X-Agent-ID, and returns
// a redeemResponse with the base64-encoded secret value for known tokens.
func fakeKuzeServer(t *testing.T, agentID string, tokenSecrets map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := strings.TrimPrefix(r.URL.Path, "/kuze/redeem/")
		if token == "" {
			http.NotFound(w, r)
			return
		}

		claimed := r.Header.Get("X-Agent-ID")
		if claimed != agentID {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "agent identity mismatch"})
			return
		}

		b64val, ok := tokenSecrets[token]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{"error": "token not valid or already used"})
			return
		}

		// Remove from map to simulate single-use burn.
		delete(tokenSecrets, token)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"secret_ref":  "ref-for-" + token,
			"secret_type": "api_key",
			"value":       b64val,
		})
	}))
}

func TestSecretsToken_SingleLease(t *testing.T) {
	tokenSecrets := map[string]string{
		"tok-aaa": "c2VjcmV0LXZhbHVlLWFhYQ==", // base64("secret-value-aaa")
	}
	kuze := fakeKuzeServer(t, "test-agent", tokenSecrets)
	defer kuze.Close()

	var applied map[string]string
	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplySecrets: func(secrets map[string]string) error {
			applied = secrets
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.SecretsTokenRequest{
		Leases: []control.SecretLease{
			{
				SecretRef:       "openai_api_key",
				RedemptionToken: "tok-aaa",
				KuzeURL:         kuze.URL + "/kuze/redeem/tok-aaa",
			},
		},
	})

	resp, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	if len(applied) != 1 {
		t.Fatalf("expected 1 secret applied, got %d", len(applied))
	}
	if v, ok := applied["openai_api_key"]; !ok || v != "c2VjcmV0LXZhbHVlLWFhYQ==" {
		t.Errorf("unexpected applied value: %v", applied)
	}
}

func TestSecretsToken_MultipleLeases(t *testing.T) {
	tokenSecrets := map[string]string{
		"tok-bbb": "dmFsdWUtYmJi", // base64("value-bbb")
		"tok-ccc": "dmFsdWUtY2Nj", // base64("value-ccc")
	}
	kuze := fakeKuzeServer(t, "test-agent", tokenSecrets)
	defer kuze.Close()

	var applied map[string]string
	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplySecrets: func(secrets map[string]string) error {
			applied = secrets
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.SecretsTokenRequest{
		Leases: []control.SecretLease{
			{SecretRef: "key-b", RedemptionToken: "tok-bbb", KuzeURL: kuze.URL + "/kuze/redeem/tok-bbb"},
			{SecretRef: "key-c", RedemptionToken: "tok-ccc", KuzeURL: kuze.URL + "/kuze/redeem/tok-ccc"},
		},
	})

	resp, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}

	if len(applied) != 2 {
		t.Fatalf("expected 2 secrets applied, got %d", len(applied))
	}
}

func TestSecretsToken_AllFail_ReturnsBadGateway(t *testing.T) {
	kuze := fakeKuzeServer(t, "test-agent", map[string]string{})
	defer kuze.Close()

	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplySecrets: func(secrets map[string]string) error {
			t.Error("ApplySecrets should not be called when all redemptions fail")
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.SecretsTokenRequest{
		Leases: []control.SecretLease{
			{SecretRef: "ghost", RedemptionToken: "tok-bad", KuzeURL: kuze.URL + "/kuze/redeem/tok-bad"},
		},
	})

	resp, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestSecretsToken_SecondRedemptionFails(t *testing.T) {
	tokenSecrets := map[string]string{
		"tok-once": "c2luZ2xl", // base64("single")
	}
	kuze := fakeKuzeServer(t, "test-agent", tokenSecrets)
	defer kuze.Close()

	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.1",
		StartedAt: time.Now(),
		ApplySecrets: func(secrets map[string]string) error {
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	leaseBody := func() []byte {
		b, _ := json.Marshal(control.SecretsTokenRequest{
			Leases: []control.SecretLease{
				{SecretRef: "once-key", RedemptionToken: "tok-once", KuzeURL: kuze.URL + "/kuze/redeem/tok-once"},
			},
		})
		return b
	}

	// First call — succeeds.
	resp1, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(leaseBody()))
	if err != nil {
		t.Fatalf("first POST: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d", resp1.StatusCode)
	}

	// Second call — token consumed, all fail → 502.
	resp2, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(leaseBody()))
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("second call: expected 502, got %d", resp2.StatusCode)
	}
}

func TestSecretsToken_EmptyLeases_OK(t *testing.T) {
	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.1",
		StartedAt: time.Now(),
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.SecretsTokenRequest{Leases: nil})

	resp, err := http.Post(ts.URL+"/secrets/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSecretsToken_WrongMethod(t *testing.T) {
	ts := startTestServer(t, "")
	resp, err := http.Get(ts.URL + "/secrets/token")
	if err != nil {
		t.Fatalf("GET /secrets/token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// --- R4.4: Deprecate Direct Secret Push ------------------------------------

// TestSecretsApply_DisabledByDefault verifies that POST /secrets/apply returns
// 410 Gone when DirectSecretPushEnabled is false (the production default).
// Secrets must only flow via POST /secrets/token + Kuze token redemption.
func TestSecretsApply_DisabledByDefault(t *testing.T) {
	// DirectSecretPushEnabled defaults to false — not set in newTestServer.
	ts := startTestServer(t, "")

	body, _ := json.Marshal(control.SecretsApplyRequest{
		Secrets: map[string]string{"openai_api_key": "c2VjcmV0"},
	})
	resp, err := http.Post(ts.URL+"/secrets/apply", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/apply: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410 Gone (direct push disabled), got %d", resp.StatusCode)
	}

	var errBody map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err == nil {
		if msg, ok := errBody["error"]; !ok || msg == "" {
			t.Error("expected non-empty error message in 410 response body")
		}
	}
}

// TestSecretsApply_EnabledWithFlag verifies that POST /secrets/apply is
// functional when DirectSecretPushEnabled=true (dev / migration mode).
func TestSecretsApply_EnabledWithFlag(t *testing.T) {
	applied := make(map[string]string)

	srv := control.New(":0", control.Handlers{
		AgentID:                 "test-agent",
		Version:                 "v0.1",
		StartedAt:               time.Now(),
		DirectSecretPushEnabled: true, // explicitly enable legacy path
		ApplySecrets: func(secrets map[string]string) error {
			for k, v := range secrets {
				applied[k] = v
			}
			return nil
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	defer ts.Close()

	body, _ := json.Marshal(control.SecretsApplyRequest{
		Secrets: map[string]string{"openai_api_key": "c2VjcmV0"},
	})
	resp, err := http.Post(ts.URL+"/secrets/apply", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /secrets/apply: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK with flag enabled, got %d: %s", resp.StatusCode, b)
	}
	if v, ok := applied["openai_api_key"]; !ok || v != "c2VjcmV0" {
		t.Errorf("secret not applied correctly: got %v", applied)
	}
}

// TestSecretsApply_DisabledIgnoresBody verifies that the 410 response is
// returned regardless of the request body content (bad JSON, empty, etc.).
func TestSecretsApply_DisabledIgnoresBody(t *testing.T) {
	ts := startTestServer(t, "")

	resp, err := http.Post(ts.URL+"/secrets/apply", "application/json", strings.NewReader(`{bad json`))
	if err != nil {
		t.Fatalf("POST /secrets/apply: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410 Gone even with invalid body, got %d", resp.StatusCode)
	}
}

// --- R12.1: ACP Event Ingress Endpoint ------------------------------------

// makeTestGosutoConfig returns a minimal Gosuto config with the given gateway
// names registered as cron gateways, used to populate Handlers.ActiveConfig
// in event ingress tests.
func makeTestGosutoConfig(gatewayNames []string, maxEventsPerMinute int) *gosutospec.Config {
	gateways := make([]gosutospec.Gateway, len(gatewayNames))
	for i, name := range gatewayNames {
		gateways[i] = gosutospec.Gateway{
			Name: name,
			Type: "cron",
			Config: map[string]string{
				"expression": "*/15 * * * *",
				"payload":    "tick",
			},
		}
	}
	return &gosutospec.Config{
		APIVersion: "gosuto/v1",
		Metadata:   gosutospec.Metadata{Name: "test-agent"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"*"},
			AllowedSenders: []string{"*"},
		},
		Limits:   gosutospec.Limits{MaxEventsPerMinute: maxEventsPerMinute},
		Gateways: gateways,
	}
}

// newEventTestServer creates a Server pre-wired for event ingress tests.
// received is incremented each time HandleEvent is called.
func newEventTestServer(t *testing.T, token string, cfg *gosutospec.Config, received *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		Version:   "v0.0.1-test",
		StartedAt: time.Now(),
		Token:     token,
		GosutoHash: func() string {
			return "deadbeefdeadbeefdeadbeefdeadbeef"
		},
		MCPNames: func() []string { return nil },
		ApplyConfig: func(yaml, hash string) error {
			return nil
		},
		ApplySecrets: func(secrets map[string]string) error {
			return nil
		},
		RequestRestart: func() {},
		RequestCancel:  func() {},
		ActiveConfig: func() *gosutospec.Config {
			return cfg
		},
		HandleEvent: func(_ context.Context, _ *envelope.Event) {
			if received != nil {
				received.Add(1)
			}
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)
	return ts
}

// validEvent builds a valid Event envelope for the given source.
func validEvent(source string) envelope.Event {
	return envelope.Event{
		Source: source,
		Type:   "cron.tick",
		TS:     time.Now().UTC(),
		Payload: envelope.EventPayload{
			Message: "scheduled trigger",
		},
	}
}

// postEvent sends a POST /events/{source} with the given event body and token.
func postEvent(t *testing.T, ts *httptest.Server, source string, evt envelope.Event, token string) *http.Response {
	t.Helper()
	body, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/events/"+source, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events/%s: %v", source, err)
	}
	return resp
}

// TestEventIngress_ValidEventAccepted verifies that a well-formed event from a
// known gateway source is accepted with 202 and forwarded to HandleEvent.
func TestEventIngress_ValidEventAccepted(t *testing.T) {
	var received atomic.Int32
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "", cfg, &received)

	resp := postEvent(t, ts, "scheduler", validEvent("scheduler"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestEventIngress_UnknownSourceRejected verifies that a source name not
// present in the active Gosuto config returns 404.
func TestEventIngress_UnknownSourceRejected(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "", cfg, nil)

	resp := postEvent(t, ts, "ghost-source", validEvent("ghost-source"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, b)
	}
}

// TestEventIngress_MalformedJSONRejected verifies that a request body that is
// not valid JSON is rejected with 400.
func TestEventIngress_MalformedJSONRejected(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "", cfg, nil)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events/scheduler", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestEventIngress_InvalidEnvelopeRejected verifies that a structurally invalid
// event envelope (missing required fields) is rejected with 400.
func TestEventIngress_InvalidEnvelopeRejected(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "", cfg, nil)

	// Zero-value TS should trigger validation error.
	bad := envelope.Event{Source: "scheduler", Type: "cron.tick"} // TS is zero
	resp := postEvent(t, ts, "scheduler", bad, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for zero TS, got %d: %s", resp.StatusCode, b)
	}
}

// TestEventIngress_SourceMismatchRejected verifies that an envelope whose
// declared Source field does not match the URL path parameter is rejected.
func TestEventIngress_SourceMismatchRejected(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler", "webhook"}, 0)
	ts := newEventTestServer(t, "", cfg, nil)

	// URL says "scheduler" but envelope says "webhook".
	evt := validEvent("webhook")
	resp := postEvent(t, ts, "scheduler", evt, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for source mismatch, got %d: %s", resp.StatusCode, b)
	}
}

// TestEventIngress_NilHandleEventReturns503 verifies that the endpoint returns
// 503 when Handlers.HandleEvent is not configured (e.g. during startup).
func TestEventIngress_NilHandleEventReturns503(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	srv := control.New(":0", control.Handlers{
		AgentID:      "test-agent",
		StartedAt:    time.Now(),
		ActiveConfig: func() *gosutospec.Config { return cfg },
		// HandleEvent intentionally nil.
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)

	resp := postEvent(t, ts, "scheduler", validEvent("scheduler"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 503, got %d: %s", resp.StatusCode, b)
	}
}

// TestEventIngress_RateLimitExceeded verifies that events beyond the
// MaxEventsPerMinute budget receive a 429 Too Many Requests response.
func TestEventIngress_RateLimitExceeded(t *testing.T) {
	const limit = 3
	var received atomic.Int32

	cfg := makeTestGosutoConfig([]string{"scheduler"}, limit)
	ts := newEventTestServer(t, "", cfg, &received)

	var lastStatus int
	for i := 0; i < limit+2; i++ {
		resp := postEvent(t, ts, "scheduler", validEvent("scheduler"), "")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode
	}

	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("expected 429 after exceeding limit, got %d", lastStatus)
	}
	if received.Load() != limit {
		t.Errorf("expected exactly %d events forwarded before rate limit, got %d", limit, received.Load())
	}
}

// TestEventIngress_WrongMethodRejected verifies that non-POST requests to the
// event ingress endpoint are rejected with 405 Method Not Allowed.
func TestEventIngress_WrongMethodRejected(t *testing.T) {
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "", cfg, nil)

	resp, err := http.Get(ts.URL + "/events/scheduler")
	if err != nil {
		t.Fatalf("GET /events/scheduler: %v", err)
	}
	defer resp.Body.Close()

	// Go 1.22+ http.ServeMux returns 405 for method-mismatched registered patterns.
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// TestEventIngress_NoActiveConfigAllowsAnySource verifies that when
// Handlers.ActiveConfig is nil (dev/test mode) the server accepts any source
// name without validation.
func TestEventIngress_NoActiveConfigAllowsAnySource(t *testing.T) {
	var received atomic.Int32
	// ActiveConfig intentionally nil — no gateway name validation.
	srv := control.New(":0", control.Handlers{
		AgentID:   "test-agent",
		StartedAt: time.Now(),
		HandleEvent: func(_ context.Context, _ *envelope.Event) {
			received.Add(1)
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)

	resp := postEvent(t, ts, "any-source", validEvent("any-source"), "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 with nil ActiveConfig, got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestEventIngress_LocalhostBypassesAuth verifies that a request from the
// loopback address (as all httptest connections are) is accepted even when a
// bearer token is configured, because built-in gateways connect from localhost.
func TestEventIngress_LocalhostBypassesAuth(t *testing.T) {
	var received atomic.Int32
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)

	// Token is set; client does NOT supply it.
	// httptest connects over 127.0.0.1 → localhost bypass should accept.
	ts := newEventTestServer(t, "super-secret-token", cfg, &received)

	resp := postEvent(t, ts, "scheduler", validEvent("scheduler"), "" /* no token */)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 (localhost bypass), got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestEventIngress_BearerTokenAccepted verifies that a non-localhost request
// (simulated by including the correct bearer token) is accepted when the
// token matches Handlers.Token.
// Note: httptest servers always use loopback, so this test verifies the token
// path via the happy-path: correct token → accepted (localhost bypass is also
// active, but the goal is to confirm the token header is processed correctly).
func TestEventIngress_BearerTokenAccepted(t *testing.T) {
	var received atomic.Int32
	cfg := makeTestGosutoConfig([]string{"scheduler"}, 0)
	ts := newEventTestServer(t, "my-token", cfg, &received)

	resp := postEvent(t, ts, "scheduler", validEvent("scheduler"), "my-token")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
}

// --- R12.4: Built-in Webhook Gateway ----------------------------------------

// makeWebhookTestGosutoConfig returns a minimal Gosuto config with a single
// webhook gateway configured at the given authType.
// Pass authType="" or "bearer" for bearer auth; "hmac-sha256" for HMAC auth.
// When authType is hmac-sha256, hmacSecretRef is the secret ref name.
func makeWebhookTestGosutoConfig(name, authType, hmacSecretRef string) *gosutospec.Config {
	cfg := map[string]string{}
	if authType != "" {
		cfg["authType"] = authType
	}
	if hmacSecretRef != "" {
		cfg["hmacSecretRef"] = hmacSecretRef
	}
	return &gosutospec.Config{
		APIVersion: "gosuto/v1",
		Metadata:   gosutospec.Metadata{Name: "test-agent"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"*"},
			AllowedSenders: []string{"*"},
		},
		Gateways: []gosutospec.Gateway{
			{Name: name, Type: "webhook", Config: cfg},
		},
	}
}

// computeHubSignature returns the "sha256=<hex>" HMAC-SHA256 signature for body using secret.
func computeHubSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newWebhookTestServer creates a control.Server wired for webhook gateway tests.
// secrets maps ref name → value bytes for GetSecret lookups.
// received is incremented each time HandleEvent fires.
func newWebhookTestServer(
	t *testing.T,
	acpToken string,
	cfg *gosutospec.Config,
	secrets map[string][]byte,
	received *atomic.Int32,
) *httptest.Server {
	t.Helper()
	srv := control.New(":0", control.Handlers{
		AgentID:      "test-agent",
		Version:      "v0.0.1-test",
		StartedAt:    time.Now(),
		Token:        acpToken,
		GosutoHash:   func() string { return "deadbeef" },
		MCPNames:     func() []string { return nil },
		ApplyConfig:  func(yaml, hash string) error { return nil },
		ApplySecrets: func(sec map[string]string) error { return nil },
		ActiveConfig: func() *gosutospec.Config { return cfg },
		GetSecret: func(ref string) ([]byte, error) {
			if secrets == nil {
				return nil, fmt.Errorf("secret %q not found", ref)
			}
			val, ok := secrets[ref]
			if !ok {
				return nil, fmt.Errorf("secret %q not found", ref)
			}
			return val, nil
		},
		HandleEvent: func(_ context.Context, _ *envelope.Event) {
			if received != nil {
				received.Add(1)
			}
		},
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)
	return ts
}

// postWebhook sends a POST /events/{source} with a raw JSON body simulating an
// external webhook delivery. It optionally includes an HMAC signature header.
func postWebhook(t *testing.T, ts *httptest.Server, source string, body []byte, hmacSig, bearerToken string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/events/"+source, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if hmacSig != "" {
		req.Header.Set("X-Hub-Signature-256", hmacSig)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events/%s: %v", source, err)
	}
	return resp
}

// TestWebhookIngress_BearerAuthAccepted verifies that a webhook gateway with
// bearer auth wraps the raw body in an Event envelope and forwards it.
// (httptest connections are on localhost, so the bearer check is bypassed ─
// the test confirms the body is wrapped and HandleEvent is called.)
func TestWebhookIngress_BearerAuthAccepted(t *testing.T) {
	var received atomic.Int32
	cfg := makeWebhookTestGosutoConfig("github", "bearer", "")
	ts := newWebhookTestServer(t, "", cfg, nil, &received)

	body := []byte(`{"action":"opened","number":1}`)
	resp := postWebhook(t, ts, "github", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestWebhookIngress_DefaultAuthIsBearerAccepted verifies that a webhook
// gateway with no explicit authType defaults to bearer and still accepts
// deliveries (localhost bypass active from httptest).
func TestWebhookIngress_DefaultAuthIsBearerAccepted(t *testing.T) {
	var received atomic.Int32
	// authType is empty string → defaults to "bearer" in handleWebhookEvent
	cfg := makeWebhookTestGosutoConfig("hook", "", "")
	ts := newWebhookTestServer(t, "", cfg, nil, &received)

	body := []byte(`{"type":"payment.succeeded"}`)
	resp := postWebhook(t, ts, "hook", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 (default bearer), got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestWebhookIngress_HMACAuthAccepted verifies that a webhook delivery with a
// correct X-Hub-Signature-256 header passes validation and is forwarded.
func TestWebhookIngress_HMACAuthAccepted(t *testing.T) {
	var received atomic.Int32
	hmacSecret := []byte("super-secret-webhook-key")
	cfg := makeWebhookTestGosutoConfig("github", "hmac-sha256", "github.hmac-secret")
	ts := newWebhookTestServer(t, "", cfg, map[string][]byte{
		"github.hmac-secret": hmacSecret,
	}, &received)

	body := []byte(`{"action":"pushed","ref":"refs/heads/main"}`)
	sig := computeHubSignature(hmacSecret, body)

	resp := postWebhook(t, ts, "github", body, sig, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestWebhookIngress_HMACWrongSignatureRejected verifies that a delivery with
// an incorrect X-Hub-Signature-256 signature receives 401 Unauthorized.
func TestWebhookIngress_HMACWrongSignatureRejected(t *testing.T) {
	hmacSecret := []byte("super-secret-webhook-key")
	cfg := makeWebhookTestGosutoConfig("github", "hmac-sha256", "github.hmac-secret")
	ts := newWebhookTestServer(t, "", cfg, map[string][]byte{
		"github.hmac-secret": hmacSecret,
	}, nil)

	body := []byte(`{"action":"pushed"}`)
	wrongSig := computeHubSignature([]byte("wrong-secret"), body)

	resp := postWebhook(t, ts, "github", body, wrongSig, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_HMACMissingSignatureHeaderRejected verifies that a
// delivery to an HMAC-authenticated webhook with no signature header returns 401.
func TestWebhookIngress_HMACMissingSignatureHeaderRejected(t *testing.T) {
	hmacSecret := []byte("super-secret-webhook-key")
	cfg := makeWebhookTestGosutoConfig("alerts", "hmac-sha256", "alerts.hmac-secret")
	ts := newWebhookTestServer(t, "", cfg, map[string][]byte{
		"alerts.hmac-secret": hmacSecret,
	}, nil)

	body := []byte(`{"alert":"disk-full"}`)
	// No HMAC signature header supplied.
	resp := postWebhook(t, ts, "alerts", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 for missing HMAC header, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_HMACSecretNotFound verifies that if the HMAC secret ref
// is not present in the agent's secret store the request is rejected with 401.
func TestWebhookIngress_HMACSecretNotFound(t *testing.T) {
	cfg := makeWebhookTestGosutoConfig("stripe", "hmac-sha256", "stripe.hmac-secret")
	// secrets map is empty — ref not found.
	ts := newWebhookTestServer(t, "", cfg, map[string][]byte{}, nil)

	body := []byte(`{"type":"invoice.paid"}`)
	dummySig := "sha256=" + strings.Repeat("aa", 32)

	resp := postWebhook(t, ts, "stripe", body, dummySig, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401 when HMAC secret not in store, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_GetSecretNilReturns503 verifies that when GetSecret is
// nil (not wired) and the gateway uses HMAC auth, the endpoint returns 503.
func TestWebhookIngress_GetSecretNilReturns503(t *testing.T) {
	cfg := makeWebhookTestGosutoConfig("hook", "hmac-sha256", "hook.secret")
	srv := control.New(":0", control.Handlers{
		AgentID:      "test-agent",
		StartedAt:    time.Now(),
		ActiveConfig: func() *gosutospec.Config { return cfg },
		GetSecret:    nil, // intentionally nil
		HandleEvent:  func(_ context.Context, _ *envelope.Event) {},
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)

	body := []byte(`{"action":"fired"}`)
	sig := computeHubSignature([]byte("any-secret"), body)
	resp := postWebhook(t, ts, "hook", body, sig, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 503 when GetSecret is nil, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_UnsupportedAuthTypeReturns400 verifies that an unknown
// authType in the gateway config returns 400 Bad Request.
func TestWebhookIngress_UnsupportedAuthTypeReturns400(t *testing.T) {
	cfg := makeWebhookTestGosutoConfig("hook", "oauth2", "") // not supported
	ts := newWebhookTestServer(t, "", cfg, nil, nil)

	body := []byte(`{"event":"triggered"}`)
	resp := postWebhook(t, ts, "hook", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for unsupported authType, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_UnknownSourceRejected verifies that a webhook delivery to
// an unknown source (not in Gosuto config) returns 404.
func TestWebhookIngress_UnknownSourceRejected(t *testing.T) {
	cfg := makeWebhookTestGosutoConfig("github", "bearer", "")
	ts := newWebhookTestServer(t, "", cfg, nil, nil)

	body := []byte(`{"action":"opened"}`)
	resp := postWebhook(t, ts, "unknown-hook", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404 for unknown webhook source, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_EventEnvelopeBodyRejected verifies that a webhook gateway
// does NOT try to parse the raw body as an Event envelope — it wraps it as-is.
// This is a regression guard: if the caller posts a pre-formed Event envelope
// to a webhook-type gateway, the handler should still wrap it (not reject it).
func TestWebhookIngress_EventEnvelopeBodyAccepted(t *testing.T) {
	var received atomic.Int32
	cfg := makeWebhookTestGosutoConfig("events-sink", "bearer", "")
	ts := newWebhookTestServer(t, "", cfg, nil, &received)

	// An Event-envelope-shaped JSON body sent by mistake — should be wrapped.
	envBody, _ := json.Marshal(envelope.Event{
		Source:  "events-sink",
		Type:    "cron.tick",
		TS:      time.Now().UTC(),
		Payload: envelope.EventPayload{Message: "tick"},
	})
	resp := postWebhook(t, ts, "events-sink", envBody, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 (wrapped envelope body), got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestWebhookIngress_HandleEventNilReturns503 verifies that if HandleEvent is
// nil (not wired) the webhook endpoint returns 503.
func TestWebhookIngress_HandleEventNilReturns503(t *testing.T) {
	cfg := makeWebhookTestGosutoConfig("hook", "bearer", "")
	srv := control.New(":0", control.Handlers{
		AgentID:      "test-agent",
		StartedAt:    time.Now(),
		ActiveConfig: func() *gosutospec.Config { return cfg },
		// HandleEvent intentionally nil
	})
	ts := httptest.NewServer(srv.TestHandler())
	t.Cleanup(ts.Close)

	resp := postWebhook(t, ts, "hook", []byte(`{}`), "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 503 when HandleEvent is nil, got %d: %s", resp.StatusCode, b)
	}
}

// TestWebhookIngress_NonJSONBodyAccepted verifies that non-JSON webhook bodies
// are accepted and wrapped (stored under the "raw" data key).
func TestWebhookIngress_NonJSONBodyAccepted(t *testing.T) {
	var received atomic.Int32
	cfg := makeWebhookTestGosutoConfig("legacy", "bearer", "")
	ts := newWebhookTestServer(t, "", cfg, nil, &received)

	body := []byte(`payload=hello&other=world`)
	resp := postWebhook(t, ts, "legacy", body, "", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 for non-JSON body, got %d: %s", resp.StatusCode, b)
	}
	if received.Load() != 1 {
		t.Errorf("expected HandleEvent called once, got %d", received.Load())
	}
}

// TestWebhookIngress_RateLimitEnforced verifies that excessive webhook
// deliveries to the same source are rejected with 429.
func TestWebhookIngress_RateLimitEnforced(t *testing.T) {
	const limit = 2
	var received atomic.Int32
	cfg := &gosutospec.Config{
		APIVersion: "gosuto/v1",
		Metadata:   gosutospec.Metadata{Name: "test-agent"},
		Trust: gosutospec.Trust{
			AllowedRooms:   []string{"*"},
			AllowedSenders: []string{"*"},
		},
		Limits: gosutospec.Limits{MaxEventsPerMinute: limit},
		Gateways: []gosutospec.Gateway{
			{Name: "hook", Type: "webhook", Config: map[string]string{}},
		},
	}
	ts := newWebhookTestServer(t, "", cfg, nil, &received)

	body := []byte(`{"ping":true}`)
	var lastStatus int
	for i := 0; i < limit+2; i++ {
		resp := postWebhook(t, ts, "hook", body, "", "")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode
	}

	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("expected 429 after exceeding limit, got %d", lastStatus)
	}
	if received.Load() != limit {
		t.Errorf("expected %d events forwarded before rate limit, got %d", limit, received.Load())
	}
}
