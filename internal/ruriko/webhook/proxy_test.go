package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/webhook"
)

// ---- helpers and stubs ------------------------------------------------------

// fakeAgent returns an enabled agent with the given control URL and ACP token.
func fakeAgent(controlURL, acpToken string) *store.Agent {
	a := &store.Agent{
		ID:      "agent-1",
		Enabled: true,
		Status:  "running",
	}
	a.ControlURL = sql.NullString{String: controlURL, Valid: true}
	if acpToken != "" {
		a.ACPToken = sql.NullString{String: acpToken, Valid: true}
	}
	return a
}

// gosutoWithWebhookGateway returns a minimal Gosuto YAML blob that defines a
// single webhook gateway named gatewayName.
func gosutoWithWebhookGateway(gatewayName, authType, hmacRef string) string {
	authLine := ""
	if authType != "" {
		authLine = fmt.Sprintf("        authType: %q\n", authType)
	}
	hmacLine := ""
	if hmacRef != "" {
		hmacLine = fmt.Sprintf("        hmacSecretRef: %q\n", hmacRef)
	}
	return fmt.Sprintf(`apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
gateways:
  - name: %s
    type: webhook
    config:
%s%s`, gatewayName, authLine, hmacLine)
}

// gosutoWithCronGateway returns a Gosuto YAML that has a cron gateway (not a
// webhook), used to verify the proxy rejects non-webhook sources.
func gosutoWithCronGateway(gatewayName string) string {
	return fmt.Sprintf(`apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms: ["*"]
  allowedSenders: ["*"]
gateways:
  - name: %s
    type: cron
    config:
      expression: "*/15 * * * *"
`, gatewayName)
}

// fakeAgentStore implements the agentStore interface.  Returns a fixed agent
// (when agentID matches) and a fixed GosutoVersion (when agentID matches).
type fakeAgentStore struct {
	agent      *store.Agent
	gosutoYAML string
}

func (f *fakeAgentStore) GetAgent(_ context.Context, id string) (*store.Agent, error) {
	if f.agent == nil || f.agent.ID != id {
		return nil, fmt.Errorf("agent not found: %s", id)
	}
	return f.agent, nil
}

func (f *fakeAgentStore) GetLatestGosutoVersion(_ context.Context, agentID string) (*store.GosutoVersion, error) {
	if f.gosutoYAML == "" || (f.agent != nil && f.agent.ID != agentID) {
		return nil, fmt.Errorf("no gosuto version for %s", agentID)
	}
	return &store.GosutoVersion{AgentID: agentID, YAMLBlob: f.gosutoYAML}, nil
}

// fakeSecretsStore implements the secretsGetter interface.
type fakeSecretsStore struct {
	secrets map[string][]byte
}

func (f *fakeSecretsStore) Get(_ context.Context, name string) ([]byte, error) {
	v, ok := f.secrets[name]
	if !ok {
		return nil, fmt.Errorf("secret not found: %s", name)
	}
	return v, nil
}

// computeHMAC generates an X-Hub-Signature-256 value for the given body and key.
func computeHMAC(key, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newProxy creates a Proxy backed by the fake stores and mounts it on a
// new ServeMux, returning both the proxy and the mux (for use with httptest).
func newProxy(agent *store.Agent, gosutoYAML string, secrets map[string][]byte, rateLimit int) (*webhook.Proxy, *http.ServeMux) {
	st := &fakeAgentStore{agent: agent, gosutoYAML: gosutoYAML}
	sec := &fakeSecretsStore{secrets: secrets}
	p := webhook.New(st, sec, webhook.Config{RateLimit: rateLimit})
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	return p, mux
}

// ---- tests ------------------------------------------------------------------

// TestWebhookProxy_HappyPath_Bearer verifies that a valid bearer-authed
// webhook delivery is forwarded to the agent and the agent's 202 is returned.
func TestWebhookProxy_HappyPath_Bearer(t *testing.T) {
	const token = "secret-acp-token"
	const body = `{"event":"push"}`

	// Fake ACP server that expects the right call and returns 202.
	acpGot := false
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/deploy-hook" {
			t.Errorf("acp: unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("acp: missing/wrong Authorization header")
		}
		acpGot = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, token)
	_, mux := newProxy(agent, gosutoWithWebhookGateway("deploy-hook", "bearer", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/deploy-hook",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rr.Code)
	}
	if !acpGot {
		t.Error("ACP server was not called")
	}
}

// TestWebhookProxy_HappyPath_HMAC verifies that a valid HMAC-authed webhook
// delivery is forwarded to the agent.
func TestWebhookProxy_HappyPath_HMAC(t *testing.T) {
	const hmacKey = "my-hmac-secret"
	const secretRef = "agent.webhook-hmac"
	body := []byte(`{"event":"release"}`)

	acpCalled := false
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acpCalled = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, "acp-token")
	secrets := map[string][]byte{secretRef: []byte(hmacKey)}
	_, mux := newProxy(agent, gosutoWithWebhookGateway("deploy-hook", "hmac-sha256", secretRef), secrets, 100)

	sig := computeHMAC([]byte(hmacKey), body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/deploy-hook",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if !acpCalled {
		t.Error("ACP server was not called")
	}
}

// TestWebhookProxy_UnknownAgent verifies that requests for a non-existent
// agent return 404.
func TestWebhookProxy_UnknownAgent(t *testing.T) {
	agent := fakeAgent("http://localhost:9999", "token")
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/nonexistent/hook",
		strings.NewReader("{}"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// TestWebhookProxy_UnknownSource verifies that an unrecognised source name
// (gateway not in Gosuto config) returns 404.
func TestWebhookProxy_UnknownSource(t *testing.T) {
	const token = "tok"
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, token)
	_, mux := newProxy(agent, gosutoWithWebhookGateway("real-hook", "", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/missing-hook",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// TestWebhookProxy_NonWebhookGateway verifies that a source that exists in
// Gosuto but has a non-webhook type (e.g. cron) is rejected with 404.
func TestWebhookProxy_NonWebhookGateway(t *testing.T) {
	const token = "tok"
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, token)
	_, mux := newProxy(agent, gosutoWithCronGateway("my-cron"), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/my-cron",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 (not a webhook gateway), got %d", rr.Code)
	}
}

// TestWebhookProxy_RateLimiting verifies that a burst of requests from the
// same agent beyond the configured limit returns 429.
func TestWebhookProxy_RateLimiting(t *testing.T) {
	const limit = 3
	const token = "tok"

	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, token)
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "bearer", ""), nil, limit)

	sendRequest := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
			strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	// First `limit` requests should be forwarded (202).
	for i := 0; i < limit; i++ {
		if code := sendRequest(); code != http.StatusAccepted {
			t.Errorf("request %d: expected 202, got %d", i+1, code)
		}
	}

	// Next request must be rate-limited (429).
	if code := sendRequest(); code != http.StatusTooManyRequests {
		t.Errorf("over-limit request: expected 429, got %d", code)
	}
}

// TestWebhookProxy_InvalidBearerToken verifies that a wrong bearer token
// is rejected with 401.
func TestWebhookProxy_InvalidBearerToken(t *testing.T) {
	const realToken = "correct"
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, realToken)
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "bearer", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// TestWebhookProxy_InvalidHMACSignature verifies that a mismatched HMAC
// signature is rejected with 401.
func TestWebhookProxy_InvalidHMACSignature(t *testing.T) {
	const secretRef = "agent.hmac-key"
	const realKey = "real-secret"

	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, "acp-token")
	secrets := map[string][]byte{secretRef: []byte(realKey)}
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "hmac-sha256", secretRef), secrets, 100)

	// Sign with a DIFFERENT key.
	body := []byte(`{"event":"push"}`)
	badSig := computeHMAC([]byte("wrong-key"), body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
		strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", badSig)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// TestWebhookProxy_DisabledAgent verifies that an administratively disabled
// agent returns 404 (same as not found, to avoid information leakage).
func TestWebhookProxy_DisabledAgent(t *testing.T) {
	agent := fakeAgent("http://localhost:9999", "tok")
	agent.Enabled = false

	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for disabled agent, got %d", rr.Code)
	}
}

// TestWebhookProxy_MethodNotAllowed verifies that non-POST requests return 405.
func TestWebhookProxy_MethodNotAllowed(t *testing.T) {
	agent := fakeAgent("http://localhost:9999", "")
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "", ""), nil, 100)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/agent-1/hook", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

// TestWebhookProxy_AgentUnavailable verifies that a 502 is returned when
// the agent's ACP server is unreachable.
func TestWebhookProxy_AgentUnavailable(t *testing.T) {
	const token = "tok"
	// Point to a port that has no listener.
	agent := fakeAgent("http://127.0.0.1:19999", token)
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "bearer", ""), nil, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rr.Code)
	}
}

// TestWebhookProxy_MissingHMACHeader verifies that an HMAC-protected endpoint
// rejects requests without the X-Hub-Signature-256 header.
func TestWebhookProxy_MissingHMACHeader(t *testing.T) {
	const secretRef = "agent.hmac-key"
	acpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer acpSrv.Close()

	agent := fakeAgent(acpSrv.URL, "")
	secrets := map[string][]byte{secretRef: []byte("some-secret")}
	_, mux := newProxy(agent, gosutoWithWebhookGateway("hook", "hmac-sha256", secretRef), secrets, 100)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/agent-1/hook",
		strings.NewReader("{}"))
	// No X-Hub-Signature-256 header.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}
