package app
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
