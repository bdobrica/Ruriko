package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/app"
)

// noopStore satisfies the statusProvider interface.
type noopStore struct{ count int }

func (n *noopStore) AgentCount(_ context.Context) (int, error) { return n.count, nil }

// nlpStatusStub satisfies the NLPStatusProvider interface.
type nlpStatusStub struct{ status string }

func (s *nlpStatusStub) NLPProviderStatus() string { return s.status }

func TestHealthServer_Health(t *testing.T) {
	hs := app.NewHealthServer("127.0.0.1:0", &noopStore{count: 3})

	// Use httptest to call the handler directly without a real listen socket.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	hs.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
}

func TestHealthServer_Status(t *testing.T) {
	hs := app.NewHealthServer("127.0.0.1:0", &noopStore{count: 5})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	hs.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %v", resp["status"])
	}
	if int(resp["agent_count"].(float64)) != 5 {
		t.Errorf("expected agent_count 5, got %v", resp["agent_count"])
	}
}

// TestHealthServer_StatusNLPProvider verifies that the /status endpoint
// includes an nlp_provider field that reflects the value from the wired
// NLPStatusProvider.
func TestHealthServer_StatusNLPProvider(t *testing.T) {
	cases := []struct {
		name      string
		nlpStatus string
		wantField string
	}{
		{"ok", "ok", "ok"},
		{"degraded", "degraded", "degraded"},
		{"unavailable", "unavailable", "unavailable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := app.NewHealthServer("127.0.0.1:0", &noopStore{count: 1})
			hs.SetNLPStatusProvider(&nlpStatusStub{status: tc.nlpStatus})

			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			w := httptest.NewRecorder()
			hs.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp["nlp_provider"] != tc.wantField {
				t.Errorf("nlp_provider: expected %q, got %v", tc.wantField, resp["nlp_provider"])
			}
		})
	}
}

// TestHealthServer_StatusNLPProvider_NoProvider verifies that /status returns
// nlp_provider="unavailable" when no NLPStatusProvider is wired in.
func TestHealthServer_StatusNLPProvider_NoProvider(t *testing.T) {
	hs := app.NewHealthServer("127.0.0.1:0", &noopStore{count: 0})
	// No SetNLPStatusProvider call.

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	hs.ServeHTTP(w, req)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["nlp_provider"] != "unavailable" {
		t.Errorf("expected nlp_provider='unavailable' without provider, got %v", resp["nlp_provider"])
	}
}
