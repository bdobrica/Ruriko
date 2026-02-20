package kuze

import (
	"context"
	"database/sql"
	"encoding/base64"
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

// AgentIssueResult is returned by IssueAgentToken.
type AgentIssueResult struct {
	// RedeemURL is the fully-qualified URL the agent should call once to fetch
	// the secret value (GET /kuze/redeem/<token>).
	RedeemURL string
	// Token is the raw token value (for audit / logging; never log the value it
	// unlocks).
	Token string
	// ExpiresAt is the UTC time after which the token is invalid.
	ExpiresAt time.Time
	// SecretRef is the name of the secret this token is scoped to.
	SecretRef string
	// AgentID is the agent the token was issued for.
	AgentID string
}

// issueHumanResponse is the JSON body returned by POST /kuze/issue/human.
type issueHumanResponse struct {
	Link      string    `json:"link"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SecretRef string    `json:"secret_ref"`
}

// issueAgentResponse is the JSON body returned by POST /kuze/issue/agent.
type issueAgentResponse struct {
	RedeemURL string    `json:"redeem_url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	SecretRef string    `json:"secret_ref"`
	AgentID   string    `json:"agent_id"`
}

// redeemResponse is the JSON body returned by GET /kuze/redeem/<token>.
// value is the base64-standard-encoded plaintext secret so the response
// body is safe to transmit over HTTPS without binary encoding concerns.
type redeemResponse struct {
	SecretRef  string `json:"secret_ref"`
	SecretType string `json:"secret_type"`
	// Value is the base64-encoded plaintext secret value.  The agent MUST
	// decode this before use and MUST NOT log it.
	Value string `json:"value"`
}

// secretsSetter is the minimal interface Kuze needs from the secrets store.
// The production implementation is *secrets.Store.
type secretsSetter interface {
	Set(ctx context.Context, name string, secretType secrets.Type, value []byte) error
}

// secretsGetter is the interface Kuze needs to look up a stored secret value
// for delivery to an agent on redemption.  The production implementation is
// *secrets.Store.
type secretsGetter interface {
	Get(ctx context.Context, name string) ([]byte, error)
}

// Server handles Kuze HTTP routes and provides direct Go methods for the
// command layer.
type Server struct {
	tokens       *TokenStore
	secrets      secretsSetter
	getter       secretsGetter // optional; required for agent redemption
	baseURL      string
	storeNotify  func(ctx context.Context, secretRef string)
	expiryNotify func(ctx context.Context, pt *PendingToken)
}

// SetSecretsGetter registers fn as the secrets getter used by the
// GET /kuze/redeem/<token> endpoint.  It must be called before the route is
// used in production.  If not set, redemption requests will return 501.
func (srv *Server) SetSecretsGetter(g secretsGetter) {
	srv.getter = g
}

// SetOnSecretStored registers fn to be called each time a secret is
// successfully stored via the one-time HTML form.  fn receives the
// secret_ref so callers can send a confirmation notification.
func (srv *Server) SetOnSecretStored(fn func(ctx context.Context, secretRef string)) {
	srv.storeNotify = fn
}

// SetOnTokenExpired registers fn to be called for each expired-but-unused
// token when PruneExpiredWithNotify is executed.  Callers can use this to
// send an expiry notification to the user.
func (srv *Server) SetOnTokenExpired(fn func(ctx context.Context, pt *PendingToken)) {
	srv.expiryNotify = fn
}

// PruneExpiredWithNotify calls OnTokenExpired for every pending token that
// has expired without being used, then prunes all expired / used tokens.
// It is safe to call concurrently; notifications are best-effort.
func (srv *Server) PruneExpiredWithNotify(ctx context.Context) error {
	if srv.expiryNotify != nil {
		expired, err := srv.tokens.ListExpiredUnused(ctx)
		if err != nil {
			slog.Warn("kuze: list expired tokens for notification", "err", err)
		} else {
			for _, pt := range expired {
				srv.expiryNotify(ctx, pt)
			}
		}
	}
	return srv.tokens.PruneExpired(ctx)
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
//   - POST /kuze/issue/human  — internal: generate and return a human one-time link.
//   - POST /kuze/issue/agent  — internal: generate and return an agent redemption token.
//   - GET  /kuze/redeem/<tok> — agent: redeem a token to obtain the secret value.
//   - GET  /s/<token>         — serve the HTML secret-entry form.
//   - POST /s/<token>         — accept the submitted value, encrypt+store, burn.
func (srv *Server) RegisterRoutes(r RouteRegistrar) {
	r.Handle("/kuze/issue/human", http.HandlerFunc(srv.handleIssueHuman))
	r.Handle("/kuze/issue/agent", http.HandlerFunc(srv.handleIssueAgent))
	r.Handle("/kuze/redeem/", http.HandlerFunc(srv.handleRedeem))
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

// IssueAgentToken is a direct Go method (used by the secret distributor) that
// creates a short-lived, agent-scoped redemption token.  The returned
// AgentIssueResult carries the RedeemURL that the agent must call (once, within
// AgentTTL) to obtain the plaintext secret value.
//
// agentID must be the canonical agent identifier (e.g. "kairo").
// secretType defaults to "api_key" when empty.
// purpose is optional; pass "" to omit.
func (srv *Server) IssueAgentToken(ctx context.Context, agentID, secretRef, secretType, purpose string) (*AgentIssueResult, error) {
	if agentID == "" {
		return nil, fmt.Errorf("kuze: agentID must not be empty")
	}
	if secretRef == "" {
		return nil, fmt.Errorf("kuze: secret_ref must not be empty")
	}
	if secretType == "" {
		secretType = string(secrets.TypeAPIKey)
	}

	token, expiresAt, err := srv.tokens.IssueAgent(ctx, secretRef, secretType, agentID, purpose)
	if err != nil {
		return nil, fmt.Errorf("kuze: issue agent token: %w", err)
	}

	return &AgentIssueResult{
		RedeemURL: fmt.Sprintf("%s/kuze/redeem/%s", srv.baseURL, token),
		Token:     token,
		ExpiresAt: expiresAt,
		SecretRef: secretRef,
		AgentID:   agentID,
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

// handleIssueAgent handles POST /kuze/issue/agent
//
// Query params:
//   - agent_id   (required) — the canonical agent identifier.
//   - secret_ref (required) — the name of the secret the agent needs.
//   - type       (optional) — secret type; defaults to "api_key".
//   - purpose    (optional) — free-form audit label.
//
// This endpoint is internal and must not be exposed to the public internet.
// The returned token is valid for AgentTTL (60 s) and can be redeemed exactly
// once via GET /kuze/redeem/<token>.
func (srv *Server) handleIssueAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentID := r.URL.Query().Get("agent_id")
	secretRef := r.URL.Query().Get("secret_ref")
	secretType := r.URL.Query().Get("type")
	purpose := r.URL.Query().Get("purpose")

	result, err := srv.IssueAgentToken(r.Context(), agentID, secretRef, secretType, purpose)
	if err != nil {
		slog.Error("kuze: issue agent token via HTTP", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("kuze: issued agent redemption token",
		"agent", result.AgentID,
		"ref", result.SecretRef,
		"token_prefix", safePrefix(result.Token, 8),
		"expires_at", result.ExpiresAt,
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issueAgentResponse{
		RedeemURL: result.RedeemURL,
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
		SecretRef: result.SecretRef,
		AgentID:   result.AgentID,
	})
}

// handleRedeem handles GET /kuze/redeem/<token>
//
// The agent sends:
//   - The token in the URL path.
//   - Its identity in the X-Agent-ID header (must match the token's agent_id).
//
// On success the token is atomically burned and the response carries the
// base64-encoded plaintext secret value.  Any subsequent attempt with the
// same token returns 410 Gone.
//
// This endpoint should be reachable by agents inside the Docker network.
// It does NOT return HTML — responses are always JSON.
func (srv *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := strings.TrimPrefix(r.URL.Path, "/kuze/redeem/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}

	claimedAgentID := r.Header.Get("X-Agent-ID")
	if claimedAgentID == "" {
		http.Error(w, `{"error":"X-Agent-ID header is required"}`, http.StatusUnauthorized)
		return
	}

	// Check the getter is wired (misconfiguration guard).
	if srv.getter == nil {
		slog.Error("kuze: redeem called but no secrets getter is configured")
		http.Error(w, `{"error":"service not fully initialised"}`, http.StatusNotImplemented)
		return
	}

	// Atomically validate, enforce agent identity, and burn the token.
	pt, err := srv.tokens.Redeem(r.Context(), token, claimedAgentID)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenUsed), errors.Is(err, ErrTokenExpired), errors.Is(err, ErrTokenNotFound):
			// Return 410 for all "token no longer valid" variants — do not
			// distinguish between them to avoid oracle behaviour.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "token not valid or already used"})
		case errors.Is(err, ErrAgentIDMismatch):
			slog.Warn("kuze: agent identity mismatch on redeem",
				"claimed_agent", claimedAgentID,
				"token_prefix", safePrefix(token, 8),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "agent identity mismatch"})
		default:
			slog.Error("kuze: redeem token", "err", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		}
		return
	}

	// Fetch the plaintext secret value.
	raw, err := srv.getter.Get(r.Context(), pt.SecretRef)
	if err != nil {
		// The token is already burned; log the failure but return an error so
		// the agent knows to request a new token.
		slog.Error("kuze: fetch secret after redeem",
			"ref", pt.SecretRef,
			"agent", claimedAgentID,
			"err", err,
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "secret unavailable; request a new token"})
		return
	}

	slog.Info("kuze: secret redeemed by agent",
		"agent", claimedAgentID,
		"ref", pt.SecretRef,
		"token_prefix", safePrefix(token, 8),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(redeemResponse{
		SecretRef:  pt.SecretRef,
		SecretType: pt.SecretType,
		Value:      base64.StdEncoding.EncodeToString(raw),
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

	// Notify the Matrix room (or any registered listener) that the secret
	// was stored successfully.  This is best-effort; errors are logged but
	// do not affect the HTTP response.
	if srv.storeNotify != nil {
		srv.storeNotify(r.Context(), pt.SecretRef)
	}

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
