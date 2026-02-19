package kuze

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
)

// Config holds options for creating a Kuze Server.
type Config struct {
	// BaseURL is the externally reachable base URL of the Ruriko HTTP server
	// (e.g. "https://ruriko.example.com").  It is used to construct the
	// one-time link that is sent to the user over Matrix.
	//
	// The URL must NOT end with a trailing slash.
	BaseURL string

	// TTL is the lifetime of a one-time token before it expires automatically.
	// When zero, DefaultTTL (10 minutes) is used.
	TTL time.Duration
}

// RouteRegistrar is satisfied by *http.ServeMux and by app.HealthServer's
// Handle method, so Kuze can register its routes without importing the app
// package directly.
type RouteRegistrar interface {
	Handle(pattern string, handler http.Handler)
}

// IssueResult is returned by IssueHumanToken.
type IssueResult struct {
	// Link is the complete one-time URL to send to the user.
	Link string
	// Token is the raw token value (useful for tests / audit).
	Token string
	// ExpiresAt is the UTC time after which the link can no longer be used.
	ExpiresAt time.Time
	// SecretRef is the name of the secret this token is scoped to.
	SecretRef string
}

// issueHumanResponse is the JSON body returned by POST /kuze/issue/human.
type issueHumanResponse struct {
	Link      string    `json:"link"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SecretRef string    `json:"secret_ref"`
}

// secretsSetter is the minimal interface Kuze needs from the secrets store.
// The production implementation is *secrets.Store.
type secretsSetter interface {
	Set(ctx context.Context, name string, secretType secrets.Type, value []byte) error
}

// Server handles Kuze HTTP routes and provides direct Go methods for the
// command layer.
type Server struct {
	tokens  *TokenStore
	secrets secretsSetter
	baseURL string
}

// New creates a new Kuze Server.
//
//   - db must be the same *sql.DB used by the Ruriko store (so that the
//     kuze_tokens table is in the same SQLite file).
//   - secretsStore must implement Set (a *secrets.Store satisfies this).
//   - cfg.BaseURL must be set; cfg.TTL defaults to DefaultTTL when zero.
func New(db *sql.DB, secretsStore secretsSetter, cfg Config) *Server {
	return &Server{
		tokens:  newTokenStore(db, cfg.TTL),
		secrets: secretsStore,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
	}
}

// RegisterRoutes adds the Kuze HTTP routes to the given RouteRegistrar (e.g.
// the application's *http.ServeMux or HealthServer):
//
//   - POST /kuze/issue/human — internal: generate and return a one-time link.
//   - GET  /s/<token>        — serve the HTML secret-entry form.
//   - POST /s/<token>        — accept the submitted value, encrypt+store, burn.
func (srv *Server) RegisterRoutes(r RouteRegistrar) {
	r.Handle("/kuze/issue/human", http.HandlerFunc(srv.handleIssueHuman))
	r.Handle("/s/", http.HandlerFunc(srv.handleForm))
}

// IssueHumanToken is a direct Go method (used by Matrix command handlers) that
// creates a one-time token for entering a secret referred to by secretRef.
// secretType must be one of the values recognised by the secrets.Store
// (e.g. "api_key", "matrix_token", "generic_json").
func (srv *Server) IssueHumanToken(ctx context.Context, secretRef, secretType string) (*IssueResult, error) {
	if secretRef == "" {
		return nil, fmt.Errorf("kuze: secret_ref must not be empty")
	}
	if secretType == "" {
		secretType = string(secrets.TypeAPIKey)
	}

	token, expiresAt, err := srv.tokens.Issue(ctx, secretRef, secretType)
	if err != nil {
		return nil, fmt.Errorf("kuze: issue token: %w", err)
	}

	return &IssueResult{
		Link:      fmt.Sprintf("%s/s/%s", srv.baseURL, token),
		Token:     token,
		ExpiresAt: expiresAt,
		SecretRef: secretRef,
	}, nil
}

// PruneExpired delegates to the underlying TokenStore. Intended to be called
// from a background goroutine or a periodic task.
func (srv *Server) PruneExpired(ctx context.Context) error {
	return srv.tokens.PruneExpired(ctx)
}

// --- Internal HTTP handlers ---------------------------------------------------

// handleIssueHuman handles POST /kuze/issue/human
//
// Query params:
//   - secret_ref (required) — the name of the secret to be entered.
//   - type       (optional) — secret type; defaults to "api_key".
//
// This endpoint is internal and must not be exposed to the public internet.
// Access control is expected to come from network topology (Docker network /
// firewall rules), not from this handler.
func (srv *Server) handleIssueHuman(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	secretRef := r.URL.Query().Get("secret_ref")
	secretType := r.URL.Query().Get("type")

	result, err := srv.IssueHumanToken(r.Context(), secretRef, secretType)
	if err != nil {
		slog.Error("kuze: issue human token via HTTP", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issueHumanResponse{
		Link:      result.Link,
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		SecretRef: result.SecretRef,
	})
}

// handleForm dispatches GET and POST requests for /s/<token>.
func (srv *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/s/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		srv.serveForm(w, r, token)
	case http.MethodPost:
		srv.acceptSecret(w, r, token)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveForm renders the HTML entry form for a valid pending token.
func (srv *Server) serveForm(w http.ResponseWriter, r *http.Request, token string) {
	pt, err := srv.tokens.Validate(r.Context(), token)
	if err != nil {
		srv.handleTokenError(w, err, "validate token for GET")
		return
	}
	renderForm(w, pt.SecretRef, token)
}

// acceptSecret handles the form POST submission.
func (srv *Server) acceptSecret(w http.ResponseWriter, r *http.Request, token string) {
	pt, err := srv.tokens.Validate(r.Context(), token)
	if err != nil {
		srv.handleTokenError(w, err, "validate token for POST")
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	value := r.FormValue("secret_value")
	if value == "" {
		renderFormWithError(w, pt.SecretRef, token, "Secret value cannot be empty.")
		return
	}

	// Persist the secret value.
	if err := srv.secrets.Set(r.Context(), pt.SecretRef, secrets.Type(pt.SecretType), []byte(value)); err != nil {
		slog.Error("kuze: store secret via form", "ref", pt.SecretRef, "err", err)
		http.Error(w, "failed to store secret; please try again", http.StatusInternalServerError)
		return
	}

	// Burn the token so it cannot be reused.
	if err := srv.tokens.Burn(r.Context(), token); err != nil {
		// Non-fatal: secret is already stored.  Log and continue so the user
		// sees the success page rather than an error.
		slog.Warn("kuze: burn token after successful store",
			"token_prefix", safePrefix(token, 8), "err", err)
	}

	slog.Info("kuze: secret stored via one-time form", "ref", pt.SecretRef)
	renderSuccessPage(w, pt.SecretRef)
}

// handleTokenError maps token validation errors to appropriate HTTP responses.
func (srv *Server) handleTokenError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, ErrTokenNotFound),
		errors.Is(err, ErrTokenExpired),
		errors.Is(err, ErrTokenUsed):
		renderExpiredPage(w)
	default:
		slog.Error("kuze: "+op, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
