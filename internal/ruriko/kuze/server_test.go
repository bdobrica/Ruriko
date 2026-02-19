package kuze_test

import (
	"context"
	"database/sql"
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

// testDB opens an in-memory SQLite DB and creates the kuze_tokens table.
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
used        INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		t.Fatalf("create kuze_tokens: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeSecrets is a minimal in-memory implementation of the secretsSetter
// interface used by kuze.Server in tests.
type fakeSecrets struct {
	stored map[string][]byte
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{stored: make(map[string][]byte)} }

func (f *fakeSecrets) Set(_ context.Context, name string, _ secrets.Type, value []byte) error {
	f.stored[name] = value
	return nil
}

// newTestServer creates a kuze.Server backed by in-memory stores.
func newTestServer(t *testing.T, ttl time.Duration) (*kuze.Server, *fakeSecrets, *sql.DB) {
	t.Helper()
	db := testDB(t)
	ss := newFakeSecrets()
	srv := kuze.New(db, ss, kuze.Config{
		BaseURL: "https://example.com",
		TTL:     ttl,
	})
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
