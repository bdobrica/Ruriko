package commands

// nlp_provider.go implements the lazy, cache-aware NLP provider resolution
// required by R9.7 (Runtime Configuration Store — NLP Key, model, and Tuning
// Knobs).
//
// # Provider lookup order (per Classify call)
//
//  1. "ruriko.nlp-api-key" secret from the encrypted secrets store (preferred)
//  2. RURIKO_NLP_API_KEY env var captured at startup (bootstrap fallback)
//  3. Neither present → returns nil; the NL layer stays in keyword-matching mode
//
// # Lazy provider rebuild
//
// The resolved (apiKey, model, endpoint) triple is compared against the
// memoised snapshot held in h.nlpProviderCache on every call.  A new
// http.Client wrapper is constructed only when at least one field differs.
// The rebuild is cheap (no network I/O) and guards with a sync.RWMutex so
// concurrent calls never race.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
)

// nlpSecretName is the name under which the operator stores the NLP API key
// in the Ruriko secrets store via `/ruriko secrets set ruriko.nlp-api-key`.
const nlpSecretName = "ruriko.nlp-api-key"

// nlpCache is the memoised NLP provider together with the exact configuration
// snapshot that was used to build it.  The zero value (provider == nil) means
// the cache is empty and the provider must be built on the next call.
type nlpCache struct {
	provider nlp.Provider
	apiKey   string
	model    string
	endpoint string
}

// resolveNLPProvider returns the NLP provider to use for the current request,
// rebuilding it lazily whenever the effective configuration has changed.
//
// The method is thread-safe: concurrent reads share a read lock on the cache;
// a rebuild takes a write lock for the shortest possible critical section
// (just pointer/string assignment after the provider object is fully
// constructed outside the lock).
//
// A nil return value means no API key is available from any source; the
// caller should degrade to keyword-matching mode.
func (h *Handlers) resolveNLPProvider(ctx context.Context) nlp.Provider {
	// Fast path: if neither a secrets store nor an env-var key is configured,
	// fall back to any pre-injected static provider (test / integration mode
	// where HandlersConfig.NLPProvider was set directly and no key-based
	// resolution is expected).
	if h.secrets == nil && h.nlpEnvAPIKey == "" {
		return h.nlpProvider
	}

	// -----------------------------------------------------------------------
	// 1. Resolve API key.
	// -----------------------------------------------------------------------
	secretKey := ""
	if h.secrets != nil {
		if raw, err := h.secrets.Get(ctx, nlpSecretName); err == nil {
			secretKey = string(raw)
		}
		// Errors (key not found, decrypt failure) are treated as "not present".
	}

	if secretKey != "" && h.nlpEnvAPIKey != "" {
		slog.Warn("nlp: API key present in both secrets store and environment variable; "+
			"secrets store takes precedence — remove the env var to avoid confusion",
			"secret", nlpSecretName,
			"hint", "unset RURIKO_NLP_API_KEY once the secret is stored")
	}

	apiKey := secretKey
	if apiKey == "" {
		apiKey = h.nlpEnvAPIKey
	}

	if apiKey == "" {
		// No key from any source — stay in keyword-matching mode silently.
		return nil
	}

	// -----------------------------------------------------------------------
	// 2. Resolve model and endpoint from the runtime config store.
	// -----------------------------------------------------------------------
	model, endpoint := resolveNLPConfig(ctx, h.configStore)

	// -----------------------------------------------------------------------
	// 3. Check the memoised cache under a read lock.
	// -----------------------------------------------------------------------
	h.nlpProviderMu.RLock()
	cached := h.nlpProviderCache
	h.nlpProviderMu.RUnlock()

	if cached.provider != nil &&
		cached.apiKey == apiKey &&
		cached.model == model &&
		cached.endpoint == endpoint {
		return cached.provider
	}

	// -----------------------------------------------------------------------
	// 4. Build the new provider outside any lock (pure CPU work, no I/O).
	// -----------------------------------------------------------------------
	built := nlp.New(nlp.Config{
		APIKey:  apiKey,
		BaseURL: endpoint,
		Model:   model,
	})

	logModel := model
	if logModel == "" {
		logModel = "gpt-4o-mini (default)"
	}
	logEndpoint := endpoint
	if logEndpoint == "" {
		logEndpoint = "https://api.openai.com/v1 (default)"
	}
	slog.Info("nlp: provider (re)built",
		"model", logModel,
		"endpoint", logEndpoint,
		"key_source", fmt.Sprintf("%s=%s", map[bool]string{true: "secret", false: "env"}[secretKey != ""], nlpSecretName),
	)

	// -----------------------------------------------------------------------
	// 5. Write the new cache entry under a write lock.
	// -----------------------------------------------------------------------
	h.nlpProviderMu.Lock()
	h.nlpProviderCache = nlpCache{
		provider: built,
		apiKey:   apiKey,
		model:    model,
		endpoint: endpoint,
	}
	h.nlpProviderMu.Unlock()

	return built
}

// resolveNLPConfig reads nlp.model and nlp.endpoint from the config store,
// returning empty strings when the store is nil or a key is not set (callers
// interpret empty as "use provider default").
func resolveNLPConfig(ctx context.Context, cs config.Store) (model, endpoint string) {
	if cs == nil {
		return "", ""
	}
	if m, err := cs.Get(ctx, "nlp.model"); err == nil {
		model = m
	}
	if e, err := cs.Get(ctx, "nlp.endpoint"); err == nil {
		endpoint = e
	}
	return model, endpoint
}
