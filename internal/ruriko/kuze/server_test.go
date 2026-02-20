package kuze_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bdobrica/Ruriko/internal/ruriko/kuze"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
)

// --- helpers -----------------------------------------------------------------

// testDB opens an in-memory SQLite DB and creates the kuze_tokens table
// (with the full schema including agent_id and purpose columns added in
// migration 0007).
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS kuze_tokens (
token       TEXT    PRIMARY KEY,
secret_ref  TEXT    NOT NULL,
secret_type TEXT    NOT NULL,
created_at  TEXT    NOT NULL,
expires_at  TEXT    NOT NULL,
used        INTEGER NOT NULL DEFAULT 0,
agent_id    TEXT,
purpose     TEXT
)`)
	if err != nil {
		t.Fatalf("create kuze_tokens: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeSecrets is a minimal in-memory implementation of secretsSetter +
// secretsGetter used by kuze.Server in tests.
type fakeSecrets struct {
	stored map[string][]byte
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{stored: make(map[string][]byte)} }

func (f *fakeSecrets) Set(_ context.Context, name string, _ secrets.Type, value []byte) error {
	f.stored[name] = value
	return nil
}

func (f *fakeSecrets) Get(_ context.Context, name string) ([]byte, error) {
	v, ok := f.stored[name]
	if !ok {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	return v, nil
}

// newTestServer creates a kuze.Server backed by in-memory stores.
// The fakeSecrets instance satisfies both secretsSetter and secretsGetter, so
// SetSecretsGetter is always wired — tests can pre-populate ss.stored to make
// secrets available for redemption.
func newTestServer(t *testing.T, ttl time.Duration) (*kuze.Server, *fakeSecrets, *sql.DB) {
	t.Helper()
	db := testDB(t)
	ss := newFakeSecrets()
	srv := kuze.New(db, ss, kuze.Config{
		BaseURL: "https://example.com",
		TTL:     ttl,
	})
	srv.SetSecretsGetter(ss)
	return srv, ss, db
}

// --- TokenStore tests --------------------------------------------------------

func TestTokenStore_IssueValidateBurn(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	result, err := srv.IssueHumanToken(ctx, "openai_key", "api_key")
	if err != nil {
		t.Fatalf("IssueHumanToken: %v", err)
	}
	if result.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if result.SecretRef != "openai_key" {
		t.Errorf("SecretRef = %q, want %q", result.SecretRef, "openai_key")
	}
	if result.ExpiresAt.Before(time.Now()) {
		t.Error("ExpiresAt should be in the future")
	}
	if !strings.HasPrefix(result.Link, "https://example.com/s/") {
		t.Errorf("unexpected link format: %s", result.Link)
	}
}

func TestTokenStore_TokenUniqueness(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	r1, _ := srv.IssueHumanToken(ctx, "k1", "api_key")
	r2, _ := srv.IssueHumanToken(ctx, "k2", "api_key")
	if r1.Token == r2.Token {
		t.Error("two tokens should not be equal")
	}
}

// --- HTTP handler tests ------------------------------------------------------

func TestKuze_GetFormValid(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	result, err := srv.IssueHumanToken(ctx, "mykey", "api_key")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/s/"+result.Token, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /s/<token>: expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mykey") {
		t.Error("HTML form should contain the secret ref name")
	}
	if !strings.Contains(w.Body.String(), "secret_value") {
		t.Error("HTML form should contain input named secret_value")
	}
}

func TestKuze_GetFormUnknownToken(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/s/notavalidtoken", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should respond with 410 Gone (expired page).
	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for unknown token, got %d", w.Code)
	}
}

func TestKuze_PostSecretSuccess(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	result, _ := srv.IssueHumanToken(ctx, "mykey", "api_key")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"secret_value": {"supersecret123"}}
	req := httptest.NewRequest(http.MethodPost, "/s/"+result.Token,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /s/<token>: expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Secret stored") {
		t.Error("success page should contain 'Secret stored'")
	}
	if string(ss.stored["mykey"]) != "supersecret123" {
		t.Errorf("secret not stored correctly: got %q", ss.stored["mykey"])
	}
}

func TestKuze_TokenBurnAfterSubmit(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	result, _ := srv.IssueHumanToken(ctx, "mykey", "api_key")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// First submission — should succeed.
	form := url.Values{"secret_value": {"first_value"}}
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/s/"+result.Token,
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	if code := post(); code != http.StatusOK {
		t.Fatalf("first POST: expected 200, got %d", code)
	}
	// Second submission — token should be burned, return 410 Gone.
	if code := post(); code != http.StatusGone {
		t.Fatalf("second POST (burned token): expected 410, got %d", code)
	}
}

func TestKuze_PostEmptyValueReturnsForm(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	result, _ := srv.IssueHumanToken(ctx, "mykey", "api_key")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"secret_value": {""}}
	req := httptest.NewRequest(http.MethodPost, "/s/"+result.Token,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should re-render the form (200) with an error message.
	if w.Code != http.StatusOK {
		t.Fatalf("empty submit: expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "cannot be empty") {
		t.Error("response should contain 'cannot be empty' error")
	}
}

func TestKuze_IssueHumanHTTPEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost,
		"/kuze/issue/human?secret_ref=finnhub_key&type=api_key", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /kuze/issue/human: expected 200, got %d\nbody: %s",
			w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "link") {
		t.Error("response should contain 'link' field")
	}
	if !strings.Contains(body, "finnhub_key") {
		t.Error("response should echo back secret_ref")
	}
}

func TestKuze_IssueHumanGETNotAllowed(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/issue/human?secret_ref=x", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /kuze/issue/human: expected 405, got %d", w.Code)
	}
}

// --- R3.2: Matrix confirmation callback tests ---------------------------------

// TestKuze_OnSecretStoredCallback verifies that SetOnSecretStored fires with
// the correct secretRef after a successful form submission.
func TestKuze_OnSecretStoredCallback(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	var notified []string
	srv.SetOnSecretStored(func(_ context.Context, ref string) {
		notified = append(notified, ref)
	})

	result, err := srv.IssueHumanToken(ctx, "finnhub_key", "api_key")
	if err != nil {
		t.Fatalf("IssueHumanToken: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"secret_value": {"secret-value-here"}}
	req := httptest.NewRequest(http.MethodPost, "/s/"+result.Token,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /s/<token>: expected 200, got %d", w.Code)
	}
	if len(notified) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notified))
	}
	if notified[0] != "finnhub_key" {
		t.Errorf("notification secretRef = %q, want %q", notified[0], "finnhub_key")
	}
}

// TestKuze_OnSecretStoredCallbackNotFiredOnEmpty verifies that the callback is
// NOT fired when an empty value is submitted (form re-renders with error).
func TestKuze_OnSecretStoredCallbackNotFiredOnEmpty(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	notified := 0
	srv.SetOnSecretStored(func(_ context.Context, _ string) { notified++ })

	result, _ := srv.IssueHumanToken(ctx, "mykey", "api_key")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	form := url.Values{"secret_value": {""}}
	req := httptest.NewRequest(http.MethodPost, "/s/"+result.Token,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("empty submit: expected 200 (re-render), got %d", w.Code)
	}
	if notified != 0 {
		t.Errorf("expected 0 notifications for empty submit, got %d", notified)
	}
}

// TestKuze_PruneExpiredWithNotifyFires verifies that SetOnTokenExpired fires
// for each expired-but-unused token and that those tokens are deleted after
// PruneExpiredWithNotify returns.
func TestKuze_PruneExpiredWithNotifyFires(t *testing.T) {
	srv, _, db := newTestServer(t, time.Minute)
	ctx := context.Background()

	var expired []string
	srv.SetOnTokenExpired(func(_ context.Context, pt *kuze.PendingToken) {
		expired = append(expired, pt.SecretRef)
	})

	// Insert two tokens that already expired (expires_at in the past).
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	for _, ref := range []string{"old_key_1", "old_key_2"} {
		if _, err := db.ExecContext(ctx, `
INSERT INTO kuze_tokens (token, secret_ref, secret_type, created_at, expires_at, used)
VALUES (?, ?, 'api_key', ?, ?, 0)`, ref+"-tok", ref, past, past); err != nil {
			t.Fatalf("insert expired token: %v", err)
		}
	}

	if err := srv.PruneExpiredWithNotify(ctx); err != nil {
		t.Fatalf("PruneExpiredWithNotify: %v", err)
	}

	if len(expired) != 2 {
		t.Errorf("expected 2 expiry notifications, got %d: %v", len(expired), expired)
	}

	// Verify the rows were also deleted.
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM kuze_tokens").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after prune, got %d", count)
	}
}

// TestKuze_PruneExpiredWithNotifySkipsUsedTokens verifies that already-used
// tokens are pruned but do NOT trigger the expiry callback.
func TestKuze_PruneExpiredWithNotifySkipsUsedTokens(t *testing.T) {
	srv, _, db := newTestServer(t, time.Minute)
	ctx := context.Background()

	notified := 0
	srv.SetOnTokenExpired(func(_ context.Context, _ *kuze.PendingToken) { notified++ })

	// Insert one used token with an expired timestamp.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `
INSERT INTO kuze_tokens (token, secret_ref, secret_type, created_at, expires_at, used)
VALUES ('used-tok', 'used_key', 'api_key', ?, ?, 1)`, past, past); err != nil {
		t.Fatalf("insert used token: %v", err)
	}

	if err := srv.PruneExpiredWithNotify(ctx); err != nil {
		t.Fatalf("PruneExpiredWithNotify: %v", err)
	}

	if notified != 0 {
		t.Errorf("expected 0 expiry notifications for used token, got %d", notified)
	}

	// The row should still be deleted.
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM kuze_tokens").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after prune, got %d", count)
	}
}

// --- R4.1: Agent token issue + redemption tests --------------------------------

// TestKuze_IssueAgentToken verifies that IssueAgentToken returns a proper
// RedeemURL and short TTL (≤ AgentTTL) regardless of the server's configured TTL.
func TestKuze_IssueAgentToken(t *testing.T) {
	srv, _, _ := newTestServer(t, 10*time.Minute) // server TTL is long for human tokens
	ctx := context.Background()

	res, err := srv.IssueAgentToken(ctx, "warren", "finnhub_key", "api_key", "initial provisioning")
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}

	if res.AgentID != "warren" {
		t.Errorf("AgentID = %q, want %q", res.AgentID, "warren")
	}
	if res.SecretRef != "finnhub_key" {
		t.Errorf("SecretRef = %q, want %q", res.SecretRef, "finnhub_key")
	}
	if !strings.HasPrefix(res.RedeemURL, "https://example.com/kuze/redeem/") {
		t.Errorf("unexpected RedeemURL: %s", res.RedeemURL)
	}
	// Agent tokens must expire within kuze.AgentTTL (60 s).
	if res.ExpiresAt.After(time.Now().Add(kuze.AgentTTL + time.Second)) {
		t.Errorf("agent token TTL too long: expires in %v", time.Until(res.ExpiresAt))
	}
}

// TestKuze_IssueAgentToken_RequiresAgentID verifies that an empty agentID is
// rejected.
func TestKuze_IssueAgentToken_RequiresAgentID(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	_, err := srv.IssueAgentToken(context.Background(), "", "key", "api_key", "")
	if err == nil {
		t.Fatal("expected error for empty agentID, got nil")
	}
}

// TestKuze_IssueAgentHTTPEndpoint verifies the POST /kuze/issue/agent HTTP handler.
func TestKuze_IssueAgentHTTPEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost,
		"/kuze/issue/agent?agent_id=warren&secret_ref=finnhub_key&type=api_key&purpose=test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /kuze/issue/agent: expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	var resp struct {
		RedeemURL string    `json:"redeem_url"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		SecretRef string    `json:"secret_ref"`
		AgentID   string    `json:"agent_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AgentID != "warren" {
		t.Errorf("agent_id = %q, want %q", resp.AgentID, "warren")
	}
	if resp.SecretRef != "finnhub_key" {
		t.Errorf("secret_ref = %q, want %q", resp.SecretRef, "finnhub_key")
	}
	if resp.Token == "" {
		t.Error("token must not be empty")
	}
	if !strings.HasPrefix(resp.RedeemURL, "https://example.com/kuze/redeem/") {
		t.Errorf("unexpected redeem_url: %s", resp.RedeemURL)
	}
}

// TestKuze_IssueAgentHTTPEndpoint_GETNotAllowed verifies the method guard.
func TestKuze_IssueAgentHTTPEndpoint_GETNotAllowed(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/issue/agent?agent_id=x&secret_ref=y", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// TestKuze_RedeemSuccess verifies the full happy path:
//  1. Pre-store a secret in fakeSecrets.
//  2. Issue an agent token.
//  3. Redeem the token — response carries base64-encoded secret value.
func TestKuze_RedeemSuccess(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	// Pre-store the secret that the agent will receive after redemption.
	_ = ss.Set(ctx, "finnhub_key", secrets.TypeAPIKey, []byte("sk-test-value"))

	res, err := srv.IssueAgentToken(ctx, "warren", "finnhub_key", "api_key", "")
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/"+res.Token, nil)
	req.Header.Set("X-Agent-ID", "warren")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /kuze/redeem/<token>: expected 200, got %d\nbody: %s", w.Code, w.Body.String())
	}

	var resp struct {
		SecretRef  string `json:"secret_ref"`
		SecretType string `json:"secret_type"`
		Value      string `json:"value"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode redeem response: %v", err)
	}
	if resp.SecretRef != "finnhub_key" {
		t.Errorf("secret_ref = %q, want %q", resp.SecretRef, "finnhub_key")
	}
	// Decode the base64 value and compare.
	got, err := base64.StdEncoding.DecodeString(resp.Value)
	if err != nil {
		t.Fatalf("decode base64 value: %v", err)
	}
	if string(got) != "sk-test-value" {
		t.Errorf("redeemed value = %q, want %q", string(got), "sk-test-value")
	}
}

// TestKuze_RedeemOnlyOnce verifies that a token cannot be redeemed a second
// time (single-use semantics).
func TestKuze_RedeemOnlyOnce(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	_ = ss.Set(ctx, "some_key", secrets.TypeAPIKey, []byte("value"))
	res, _ := srv.IssueAgentToken(ctx, "brave", "some_key", "api_key", "")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	redeem := func() int {
		req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/"+res.Token, nil)
		req.Header.Set("X-Agent-ID", "brave")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	if code := redeem(); code != http.StatusOK {
		t.Fatalf("first redemption: expected 200, got %d", code)
	}
	// Second redemption must fail with 410 Gone.
	if code := redeem(); code != http.StatusGone {
		t.Fatalf("second redemption (burned token): expected 410, got %d", code)
	}
}

// TestKuze_RedeemWrongAgentID verifies that a token cannot be redeemed by a
// different agent than the one it was issued for.
func TestKuze_RedeemWrongAgentID(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	_ = ss.Set(ctx, "sec", secrets.TypeAPIKey, []byte("value"))
	res, _ := srv.IssueAgentToken(ctx, "warren", "sec", "api_key", "")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/"+res.Token, nil)
	req.Header.Set("X-Agent-ID", "evil-agent") // wrong agent
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for wrong agent, got %d\nbody: %s", w.Code, w.Body.String())
	}
}

// TestKuze_RedeemMissingAgentIDHeader verifies that omitting X-Agent-ID returns
// 401 Unauthorized.
func TestKuze_RedeemMissingAgentIDHeader(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	_ = ss.Set(ctx, "sec", secrets.TypeAPIKey, []byte("value"))
	res, _ := srv.IssueAgentToken(ctx, "warren", "sec", "api_key", "")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/"+res.Token, nil)
	// No X-Agent-ID header.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestKuze_RedeemUnknownToken verifies that an unknown token returns 410 Gone.
func TestKuze_RedeemUnknownToken(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Minute)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/definitely-not-a-valid-token", nil)
	req.Header.Set("X-Agent-ID", "warren")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 Gone for unknown token, got %d", w.Code)
	}
}

// TestKuze_IssueAgentToken_ShortTTLRegardlessOfServerTTL verifies that the
// server's configured human TTL (e.g. 24 h) does NOT bleed into agent tokens.
func TestKuze_IssueAgentToken_ShortTTLRegardlessOfServerTTL(t *testing.T) {
	// Create a server with a very long human token TTL.
	srv, _, _ := newTestServer(t, 24*time.Hour)
	ctx := context.Background()

	res, err := srv.IssueAgentToken(ctx, "tim", "matrix_token", "matrix_token", "")
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}

	// The agent token TTL must be at most AgentTTL (60 s), not 24 h.
	maxExpiry := time.Now().Add(kuze.AgentTTL + 2*time.Second)
	if res.ExpiresAt.After(maxExpiry) {
		t.Errorf("agent token expires too far in the future: %v (max allowed: %v)",
			res.ExpiresAt, maxExpiry)
	}
}

// TestKuze_HumanTokenCanNotBeRedeemed verifies that a human (non-agent) token
// produced by IssueHumanToken cannot be used via GET /kuze/redeem/<token>
// because its agent_id is empty — mismatching any supplied X-Agent-ID.
func TestKuze_HumanTokenCanNotBeRedeemed(t *testing.T) {
	srv, ss, _ := newTestServer(t, time.Minute)
	ctx := context.Background()

	_ = ss.Set(ctx, "key", secrets.TypeAPIKey, []byte("value"))
	res, _ := srv.IssueHumanToken(ctx, "key", "api_key")

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/kuze/redeem/"+res.Token, nil)
	req.Header.Set("X-Agent-ID", "any-agent")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Human tokens have agent_id = "" which won't match "any-agent".
	if w.Code != http.StatusForbidden {
		t.Fatalf("human token redeemed via agent endpoint: expected 403, got %d", w.Code)
	}
}
