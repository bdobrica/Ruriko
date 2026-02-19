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
