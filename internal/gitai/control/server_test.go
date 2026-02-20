package control_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
