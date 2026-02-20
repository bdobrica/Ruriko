package acp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
)

// --- Auth header tests (R2.1) ---------------------------------------------

func TestClient_SendsBearerToken(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(acp.HealthResponse{Status: "ok", AgentID: "test"})
	}))
	defer ts.Close()

	client := acp.New(ts.URL, acp.Options{Token: "tok-abc"})
	_, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Authorization header = %q; want %q", gotAuth, "Bearer tok-abc")
	}
}

func TestClient_NoTokenNoHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(acp.HealthResponse{Status: "ok", AgentID: "test"})
	}))
	defer ts.Close()

	client := acp.New(ts.URL) // no options
	_, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q; want empty", gotAuth)
	}
}

// --- X-Request-ID / X-Idempotency-Key tests (R2.2) -------------------------

func TestClient_SendsRequestID(t *testing.T) {
	var gotReqID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID = r.Header.Get("X-Request-ID")
		json.NewEncoder(w).Encode(acp.HealthResponse{Status: "ok"})
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_, _ = client.Health(context.Background())
	if gotReqID == "" {
		t.Error("expected X-Request-ID header on GET request")
	}
}

func TestClient_SendsIdempotencyKeyOnMutation(t *testing.T) {
	var gotIdemKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("X-Idempotency-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_ = client.Restart(context.Background())
	if gotIdemKey == "" {
		t.Error("expected X-Idempotency-Key header on POST /process/restart")
	}
}

func TestClient_NoIdempotencyKeyOnReadOps(t *testing.T) {
	var gotIdemKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdemKey = r.Header.Get("X-Idempotency-Key")
		json.NewEncoder(w).Encode(acp.HealthResponse{Status: "ok"})
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_, _ = client.Health(context.Background())
	if gotIdemKey != "" {
		t.Errorf("expected no X-Idempotency-Key on GET, got %q", gotIdemKey)
	}
}

// --- Per-operation timeout tests (R2.3) -----------------------------------

func TestClient_HealthTimesOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // way longer than the 2s health timeout
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "Client.Timeout") {
		t.Errorf("expected timeout-related error, got: %v", err)
	}
}

// --- Response safety tests (R2.4) -----------------------------------------

func TestClient_OversizedResponseDoesNotCrash(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write 2 MiB — more than the 1 MiB limit.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data := strings.Repeat("x", 2<<20)
		fmt.Fprint(w, data)
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_, err := client.Health(context.Background())
	// The response won't parse as valid JSON (it's just "xxx…"), so we
	// expect an unmarshal error — the important thing is no OOM/panic.
	if err == nil {
		t.Error("expected error parsing garbage but got nil")
	}
}

func TestClient_ErrorResponseIncludesBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "custom error text")
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "custom error text") {
		t.Errorf("expected error to contain body snippet, got: %v", err)
	}
}

// --- Cancel endpoint test (R2.5) ------------------------------------------

func TestClient_Cancel(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]string{"status": "cancelling"})
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	err := client.Cancel(context.Background())
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/tasks/cancel" {
		t.Errorf("expected POST /tasks/cancel, got %s %s", gotMethod, gotPath)
	}
}

// --- ApplySecretsToken test (R4.2) ----------------------------------------

func TestClient_ApplySecretsToken(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody acp.SecretsTokenRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)

		// Verify idempotency key is present.
		if r.Header.Get("X-Idempotency-Key") == "" {
			t.Error("expected X-Idempotency-Key on mutating request")
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := acp.New(ts.URL)
	err := client.ApplySecretsToken(context.Background(), acp.SecretsTokenRequest{
		Leases: []acp.SecretLease{
			{
				SecretRef:       "openai_api_key",
				RedemptionToken: "tok-abc123",
				KuzeURL:         "http://kuze.local/kuze/redeem/tok-abc123",
			},
			{
				SecretRef:       "finnhub_api_key",
				RedemptionToken: "tok-def456",
				KuzeURL:         "http://kuze.local/kuze/redeem/tok-def456",
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplySecretsToken: %v", err)
	}

	if gotMethod != "POST" {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/secrets/token" {
		t.Errorf("path = %q; want /secrets/token", gotPath)
	}
	if len(gotBody.Leases) != 2 {
		t.Fatalf("expected 2 leases in body, got %d", len(gotBody.Leases))
	}
	if gotBody.Leases[0].SecretRef != "openai_api_key" {
		t.Errorf("lease[0].SecretRef = %q; want openai_api_key", gotBody.Leases[0].SecretRef)
	}
}
