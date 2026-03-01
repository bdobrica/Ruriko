// Package secrets — distributor sends bound secrets to agent ACP endpoints.
package secrets

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/bdobrica/Ruriko/common/retry"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime/acp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// TokenLeaseResult holds the result of issuing an agent redemption token.
// It carries only the metadata the distributor needs to build the ACP
// payload; the plaintext secret is never exposed here.
type TokenLeaseResult struct {
	// RedeemURL is the fully-qualified Kuze URL the agent calls to retrieve
	// the plaintext value (e.g. "https://ruriko.example.com/kuze/redeem/<tok>").
	RedeemURL string
	// SecretRef is the secret name this lease covers.
	SecretRef string
	// Token is the raw one-time token value (for audit logging only — never
	// log the secret it unlocks).
	Token string
}

// TokenIssuer creates short-lived, agent-scoped redemption tokens via Kuze.
// The production implementation is *kuze.Server (wrapped in an adapter
// inside app.go to avoid a circular import between the secrets and kuze
// packages).
type TokenIssuer interface {
	IssueAgentToken(ctx context.Context, agentID, secretRef, secretType, purpose string) (*TokenLeaseResult, error)
}

// Distributor pushes bound secrets to agent ACP endpoints.
//
// When a TokenIssuer (Kuze) is configured, secrets are distributed via
// one-time Kuze tokens so that plaintext values never traverse the ACP
// payload.  When no TokenIssuer is present, the Distributor falls back to
// the legacy direct-push path (POST /secrets/apply with base64 values).
type Distributor struct {
	secrets *Store
	store   *store.Store
	kuze    TokenIssuer // nil → fall back to direct push
}

// NewDistributor creates a Distributor that uses the legacy raw-push path.
// Prefer NewDistributorWithKuze for production deployments.
func NewDistributor(secrets *Store, db *store.Store) *Distributor {
	return &Distributor{secrets: secrets, store: db}
}

// NewDistributorWithKuze creates a Distributor that issues Kuze redemption
// tokens instead of sending raw secrets over ACP. This is the recommended
// constructor for production.
func NewDistributorWithKuze(secrets *Store, db *store.Store, kuze TokenIssuer) *Distributor {
	return &Distributor{secrets: secrets, store: db, kuze: kuze}
}

// PushToAgent distributes all bound secrets for agentID to its ACP endpoint.
//
// When a TokenIssuer is configured the token-based path is used; otherwise
// the legacy direct-push path is active. Returns the number of secrets
// delivered and any aggregate error.
func (d *Distributor) PushToAgent(ctx context.Context, agentID string) (int, error) {
	if d.kuze != nil {
		return d.distributeViaTokens(ctx, agentID)
	}
	return d.pushRaw(ctx, agentID)
}

// --- token-based distribution (R4.2) ----------------------------------------

// distributeViaTokens issues a Kuze token per bound secret and sends the
// list of {secret_ref, redemption_token, kuze_url} leases to the agent via
// POST /secrets/token. The agent redeems each token from Kuze to obtain
// the plaintext value.
func (d *Distributor) distributeViaTokens(ctx context.Context, agentID string) (int, error) {
	traceID := trace.FromContext(ctx)

	agent, err := d.store.GetAgent(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("agent not found: %w", err)
	}
	if !agent.ControlURL.Valid || agent.ControlURL.String == "" {
		return 0, fmt.Errorf("agent %q has no control URL; is it running?", agentID)
	}
	controlURL := agent.ControlURL.String
	acpToken := agent.ACPToken.String

	bindings, err := d.secrets.ListBindings(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("list bindings: %w", err)
	}
	if len(bindings) == 0 {
		return 0, nil
	}

	// Issue a Kuze token for each bound secret.
	var leases []acp.SecretLease
	var errs []error
	issuedRefs := make(map[string]bool) // track which refs were issued

	for _, b := range bindings {
		// Resolve secret type for proper token scoping.
		meta, err := d.secrets.GetMetadata(ctx, b.SecretName)
		if err != nil {
			slog.Warn("distributor: failed to get secret metadata",
				"agent", agentID, "secret", b.SecretName, "err", err)
			errs = append(errs, fmt.Errorf("metadata %q: %w", b.SecretName, err))
			continue
		}

		result, err := d.kuze.IssueAgentToken(ctx, agentID, b.SecretName, string(meta.Type), "distribution")
		if err != nil {
			slog.Warn("distributor: failed to issue Kuze token",
				"agent", agentID, "secret", b.SecretName, "err", err)
			errs = append(errs, fmt.Errorf("issue token %q: %w", b.SecretName, err))
			continue
		}

		leases = append(leases, acp.SecretLease{
			SecretRef:       result.SecretRef,
			RedemptionToken: result.Token,
			KuzeURL:         result.RedeemURL,
		})
		issuedRefs[b.SecretName] = true
	}

	if len(leases) == 0 {
		if len(errs) > 0 {
			return 0, fmt.Errorf("all secrets failed to issue tokens: %v", errs[0])
		}
		return 0, nil
	}

	slog.Info("distributing secrets via Kuze tokens",
		"agent", agentID, "count", len(leases), "trace", traceID)

	client := acp.New(controlURL, acp.Options{Token: acpToken})
	sendErr := retry.Do(ctx, retry.DefaultConfig, func() error {
		return client.ApplySecretsToken(ctx, acp.SecretsTokenRequest{Leases: leases})
	})
	if sendErr != nil {
		return 0, fmt.Errorf("ACP /secrets/token: %w", sendErr)
	}

	// Mark pushed for each issued secret.
	pushed := 0
	for _, b := range bindings {
		if !issuedRefs[b.SecretName] {
			continue
		}
		if err := d.secrets.MarkPushed(ctx, agentID, b.SecretName); err != nil {
			slog.Warn("distributor: MarkPushed failed",
				"agent", agentID, "secret", b.SecretName, "err", err)
		} else {
			pushed++
		}
	}

	if len(errs) > 0 {
		return pushed, fmt.Errorf("%d secret(s) failed to issue tokens: %v", len(errs), errs[0])
	}
	return pushed, nil
}

// --- legacy direct push (pre-R4.2, will be gated/removed via R4.4) ----------

// pushRaw is the legacy direct-push path: decrypts each secret and sends
// the base64-encoded plaintext via POST /secrets/apply. Secrets appear in
// the ACP request body, which is why this path is being superseded by
// token-based distribution.
func (d *Distributor) pushRaw(ctx context.Context, agentID string) (int, error) {
	traceID := trace.FromContext(ctx)

	// Resolve the agent's control endpoint.
	agent, err := d.store.GetAgent(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("agent not found: %w", err)
	}
	if !agent.ControlURL.Valid || agent.ControlURL.String == "" {
		return 0, fmt.Errorf("agent %q has no control URL; is it running?", agentID)
	}
	controlURL := agent.ControlURL.String
	acpToken := agent.ACPToken.String

	// Fetch all bindings for this agent.
	bindings, err := d.secrets.ListBindings(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("list bindings: %w", err)
	}
	if len(bindings) == 0 {
		return 0, nil
	}

	// Decrypt each secret value.
	payload := make(map[string]string, len(bindings))
	var errs []error

	for _, b := range bindings {
		raw, err := d.secrets.Get(ctx, b.SecretName)
		if err != nil {
			slog.Warn("distributor: failed to decrypt secret",
				"agent", agentID, "secret", b.SecretName, "err", err)
			errs = append(errs, fmt.Errorf("decrypt %q: %w", b.SecretName, err))
			continue
		}
		payload[b.SecretName] = base64.StdEncoding.EncodeToString(raw)
	}

	if len(payload) == 0 {
		if len(errs) > 0 {
			return 0, fmt.Errorf("all secrets failed to decrypt: %v", errs[0])
		}
		return 0, nil
	}

	// Push the bundle to the agent (with retry for transient failures).
	slog.Info("pushing secrets to agent", "agent", agentID, "count", len(payload), "trace", traceID)
	client := acp.New(controlURL, acp.Options{Token: acpToken})
	pushErr := retry.Do(ctx, retry.DefaultConfig, func() error {
		return client.ApplySecrets(ctx, acp.SecretsApplyRequest{Secrets: payload})
	})
	if pushErr != nil {
		return 0, fmt.Errorf("ACP /secrets/apply: %w", pushErr)
	}

	// Update last_pushed_version for each successfully included secret.
	pushed := 0
	for _, b := range bindings {
		if _, ok := payload[b.SecretName]; !ok {
			continue // was skipped due to decrypt error
		}
		if err := d.secrets.MarkPushed(ctx, agentID, b.SecretName); err != nil {
			slog.Warn("distributor: failed to mark pushed",
				"agent", agentID, "secret", b.SecretName, "err", err)
		} else {
			pushed++
		}
	}

	if len(errs) > 0 {
		return pushed, fmt.Errorf("%d secret(s) failed: %v", len(errs), errs[0])
	}
	return pushed, nil
}
