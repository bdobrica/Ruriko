package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/ruriko/approvals"
	"github.com/bdobrica/Ruriko/internal/ruriko/audit"
	"github.com/bdobrica/Ruriko/internal/ruriko/config"
	"github.com/bdobrica/Ruriko/internal/ruriko/kuze"
	"github.com/bdobrica/Ruriko/internal/ruriko/memory"
	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
	"github.com/bdobrica/Ruriko/internal/ruriko/provisioning"
	"github.com/bdobrica/Ruriko/internal/ruriko/runtime"
	"github.com/bdobrica/Ruriko/internal/ruriko/secrets"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
	"github.com/bdobrica/Ruriko/internal/ruriko/templates"
)

// HandlersConfig holds the (mostly optional) dependencies for Handlers.
// Only Store and Secrets are required; the remaining fields may be nil when
// the corresponding subsystem is not enabled (e.g. no Docker runtime, no
// Matrix provisioner, etc.).
type HandlersConfig struct {
	Store       *store.Store
	Secrets     *secrets.Store
	Runtime     runtime.Runtime           // optional — enables agent lifecycle commands
	Provisioner *provisioning.Provisioner // optional — enables Matrix account provisioning
	Distributor *secrets.Distributor      // optional — enables secrets push
	Templates   *templates.Registry       // optional — enables Gosuto template commands
	Approvals   *approvals.Gate           // optional — enables approval gating
	Notifier    audit.Notifier            // optional — enables audit room notifications
	Kuze        *kuze.Server              // optional — enables one-time secret-entry links
	// RoomSender, when non-nil, is used by the async provisioning pipeline to
	// post Matrix breadcrumb notices back to the room where the operator
	// issued the create command.  The *matrix.Client satisfies this interface.
	RoomSender RoomSender // optional — enables provisioning breadcrumbs
	// DefaultAgentImage is the container image used when creating agents via
	// the natural-language provisioning wizard (R5.4).  When empty, the wizard
	// falls back to "ghcr.io/bdobrica/gitai:latest".
	DefaultAgentImage string // optional — default agent image for NL provisioning
	// MatrixHomeserver is the Matrix homeserver URL injected into agent
	// container environments as MATRIX_HOMESERVER so gitai can connect.
	MatrixHomeserver string // optional — injected into spawned agent containers

	// NLPProvider, when non-nil, is used by HandleNaturalLanguage to
	// classify free-form user messages via an LLM instead of the built-in
	// keyword matcher.  When nil, the deterministic keyword-based fallback
	// from R5.4 is used.
	NLPProvider nlp.Provider // optional — enables LLM-backed intent classification

	// NLPRateLimiter, when non-nil, enforces per-sender call limits on LLM
	// classification requests.  Should always be set alongside NLPProvider.
	NLPRateLimiter *nlp.RateLimiter // optional — rate limits NLP calls per sender

	// NLPTokenBudget, when non-nil, enforces per-sender daily token budgets
	// for LLM classification requests.  Should always be set alongside
	// NLPProvider.  Token usage is recorded after each successful Classify
	// call; when the budget is exhausted the handler replies with the
	// TokenBudgetExceededMessage without invoking the provider.
	NLPTokenBudget *nlp.TokenBudget // optional — token budget per sender per day

	// ConfigStore, when non-nil, is the runtime key/value configuration store.
	// It holds non-secret operator-tunable knobs (e.g. nlp.model, nlp.endpoint,
	// nlp.rate-limit) that take effect without a container restart.
	ConfigStore config.Store // optional — enables /ruriko config commands

	// NLPEnvAPIKey is the API key discovered from the environment variable at
	// startup (typically RURIKO_NLP_API_KEY).  When non-empty it is used as the
	// bootstrap API key until the operator stores an overriding value via
	// `/ruriko secrets set ruriko.nlp-api-key`.  Storing the env-var value here
	// lets tests inject a key without modifying the process environment.
	NLPEnvAPIKey string // optional — bootstrap NLP API key from env

	// Memory, when non-nil, is the context assembler that provides conversation
	// history (short-term + long-term) to the NLP classifier.  When nil,
	// classification calls proceed without conversation context.
	Memory *memory.ContextAssembler // optional — enables memory-aware NLP

	// SealPipeline, when non-nil, processes sealed conversations through the
	// summarise → embed → store pipeline.  When nil, sealed conversations are
	// logged at DEBUG level and discarded.
	SealPipeline *memory.SealPipeline // optional — enables conversation archival
}

// RoomSender is the subset of the Matrix client needed for posting breadcrumb
// notices back to the operator's Matrix room during the provisioning pipeline.
type RoomSender interface {
	SendNotice(roomID, message string) error
}

// Handlers holds all command handlers and dependencies.
type Handlers struct {
	store             *store.Store
	secrets           *secrets.Store
	runtime           runtime.Runtime
	provisioner       *provisioning.Provisioner
	distributor       *secrets.Distributor
	templates         *templates.Registry
	approvals         *approvals.Gate
	notifier          audit.Notifier
	kuze              *kuze.Server
	roomSender        RoomSender
	dispatch          DispatchFunc
	conversations     *conversationStore
	defaultAgentImage string
	matrixHomeserver  string
	nlpProvider       nlp.Provider
	nlpRateLimiter    *nlp.RateLimiter
	nlpTokenBudget    *nlp.TokenBudget
	// nlpHealthState tracks the health of the NLP provider based on recent
	// call outcomes.  Written by handleNLClassify; read by NLPProviderStatus.
	// Values: 0 = ok, 1 = degraded, 2 = unavailable.
	nlpHealthState atomic.Int32

	// configStore is the runtime key/value configuration store for non-secret
	// operator-tunable knobs (e.g. nlp.model, nlp.endpoint, nlp.rate-limit).
	configStore config.Store

	// nlpEnvAPIKey is the API key captured from the environment at startup.
	// It is the bootstrap fallback used when the secrets store has no
	// "ruriko.nlp-api-key" entry yet.
	nlpEnvAPIKey string

	// memory is the context assembler that provides conversation history
	// (STM + LTM) to the NLP classifier.  Nil when memory is disabled.
	memory *memory.ContextAssembler

	// sealPipeline processes sealed conversations through the archive flow.
	// Nil when memory archival is disabled.
	sealPipeline *memory.SealPipeline

	// nlHistoryFallback stores short-term conversation history per room+sender
	// for NLP calls when the R10 memory assembler is not configured.
	nlHistoryFallback *nlHistoryStore

	// nlpProviderMu guards nlpProviderCache for concurrent-safe lazy rebuilds.
	nlpProviderMu sync.RWMutex

	// nlpProviderCache is the memoised provider and the config snapshot that
	// was used to build it.  Rebuilt whenever apiKey / model / endpoint change.
	nlpProviderCache nlpCache
}

// nlp health-state constants (stored in Handlers.nlpHealthState).
const (
	nlpHealthOK          int32 = 0
	nlpHealthDegraded    int32 = 1
	nlpHealthUnavailable int32 = 2
)

// NewHandlers creates a new Handlers instance from the given config.
func NewHandlers(cfg HandlersConfig) *Handlers {
	n := cfg.Notifier
	if n == nil {
		n = audit.Noop{}
	}
	return &Handlers{
		store:             cfg.Store,
		secrets:           cfg.Secrets,
		runtime:           cfg.Runtime,
		provisioner:       cfg.Provisioner,
		distributor:       cfg.Distributor,
		templates:         cfg.Templates,
		approvals:         cfg.Approvals,
		notifier:          n,
		kuze:              cfg.Kuze,
		roomSender:        cfg.RoomSender,
		conversations:     newConversationStore(),
		defaultAgentImage: cfg.DefaultAgentImage,
		matrixHomeserver:  cfg.MatrixHomeserver,
		nlpProvider:       cfg.NLPProvider,
		nlpRateLimiter:    cfg.NLPRateLimiter,
		nlpTokenBudget:    cfg.NLPTokenBudget,
		configStore:       cfg.ConfigStore,
		nlpEnvAPIKey:      cfg.NLPEnvAPIKey,
		memory:            cfg.Memory,
		sealPipeline:      cfg.SealPipeline,
		nlHistoryFallback: newNLHistoryStore(),
	}
}

// SetDispatch sets the dispatch callback used to re-execute approved operations.
// This is intentionally a setter rather than a config field because the dispatch
// callback typically references the Router, which in turn holds references to the
// Handlers — creating a circular dependency at construction time.
func (h *Handlers) SetDispatch(fn DispatchFunc) {
	h.dispatch = fn
}

// NLPProviderStatus returns a string representing the current health of the
// NLP provider as seen by recent Classify calls:
//   - "unavailable" — no NLP provider is configured, or the provider is
//     configured but the last call failed with a hard network error.
//   - "degraded"    — the provider is configured but the last call hit an
//     API-side rate limit or returned malformed output.
//   - "ok"          — the provider is configured and the last call succeeded
//     (or no call has been made yet since startup).
//
// This method is used by the health/status endpoint.
func (h *Handlers) NLPProviderStatus() string {
	if h.nlpProvider == nil {
		return "unavailable"
	}
	switch h.nlpHealthState.Load() {
	case nlpHealthDegraded:
		return "degraded"
	case nlpHealthUnavailable:
		return "unavailable"
	default:
		return "ok"
	}
}

// MemoryEnabled reports whether the conversation memory subsystem is wired.
// Returns true when a non-nil ContextAssembler was provided via
// HandlersConfig.Memory; false when the memory layer is disabled.
func (h *Handlers) MemoryEnabled() bool {
	return h.memory != nil
}

// HandleHelp shows available commands
func (h *Handlers) HandleHelp(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	help := `**Ruriko Control Plane**

**General Commands:**
• /ruriko help - Show this help message
• /ruriko version - Show version information
• /ruriko ping - Health check

**Agent Commands:**
• /ruriko agents list - List all agents
• /ruriko agents show <name> - Show agent details
• /ruriko agents create --name <id> --template <tmpl> --image <image> [--mxid <existing>] - Create agent
• /ruriko agents stop <name> - Stop agent
• /ruriko agents start <name> - Start agent
• /ruriko agents respawn <name> - Force respawn agent
• /ruriko agents status <name> - Show agent runtime status
• /ruriko agents cancel <name> - Cancel in-flight task on agent
• /ruriko agents delete <name> - Delete agent
• /ruriko agents matrix register <name> [--mxid <existing>] - Provision Matrix account
• /ruriko agents disable <name> [--erase] - Soft-disable agent (deactivates Matrix account)

**Secrets Commands (admin only):**
• /ruriko secrets list - List secret names and metadata
• /ruriko secrets set <name> --type <type> - Issue one-time Kuze link to store/update a secret
• /ruriko secrets info <name> - Show secret metadata
• /ruriko secrets rotate <name> - Issue one-time Kuze link to rotate an existing secret
• /ruriko secrets delete <name> - Delete a secret
• /ruriko secrets bind <agent> <secret> --scope <scope> - Grant agent access
• /ruriko secrets unbind <agent> <secret> - Revoke agent access
• /ruriko secrets push <agent> - Push all bound secrets to running agent

🔐 **Secret values are never accepted in Matrix commands.** Use Kuze one-time links issued by /ruriko secrets set and /ruriko secrets rotate.

**Audit Commands:**
• /ruriko audit tail [n] - Show recent audit entries
• /ruriko trace <trace_id> - Show all events for a trace

**Gosuto Commands:**
• /ruriko gosuto show <agent> [--version <n>] - Show current (or specific) Gosuto config with persona and instructions sections clearly labelled
• /ruriko gosuto versions <agent> - List all stored versions
• /ruriko gosuto diff <agent> --from <v1> --to <v2> - Diff between two versions (annotates which sections changed)
• /ruriko gosuto set <agent> --content <base64yaml> - Store new Gosuto version (full config)
• /ruriko gosuto set-instructions <agent> --content <base64yaml> - Update only the instructions section (persona unchanged)
• /ruriko gosuto set-persona <agent> --content <base64yaml> - Update only the persona section (instructions unchanged)
• /ruriko gosuto rollback <agent> --to <version> - Revert to previous version
• /ruriko gosuto push <agent> - Push current config to running agent

**Approvals Commands:**
• /ruriko approvals list [--status pending|approved|denied|expired|cancelled] - List approvals
• /ruriko approvals show <id> - Show approval details
• approve <id> [reason] - Approve a pending operation
• deny <id> reason="<text>" - Deny a pending operation

**Config Commands:**
• /ruriko config set <key> <value> - Set a runtime config value (keys: nlp.model, nlp.endpoint, nlp.rate-limit)
• /ruriko config get <key> - Show a config value
• /ruriko config list - Show all non-default config values
• /ruriko config unset <key> - Revert a config value to its default
`
	return help, nil
}

// HandleVersion shows version information
func (h *Handlers) HandleVersion(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	return fmt.Sprintf("**Ruriko Control Plane**\nVersion: %s\nCommit: %s\nBuild Time: %s",
		version.Version, version.GitCommit, version.BuildTime), nil
}

// HandlePing responds with a health check
func (h *Handlers) HandlePing(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	// Write audit log — failure is non-fatal; the primary operation already succeeded.
	if err := h.store.WriteAudit(
		ctx,
		traceID,
		evt.Sender.String(),
		"ping",
		"",
		"success",
		nil,
		"",
	); err != nil {
		slog.Warn("audit write failed", "op", "ping", "err", err)
	}

	return fmt.Sprintf("🏓 Pong! (trace: %s)", traceID), nil
}

// HandleAgentsList lists all agents
func (h *Handlers) HandleAgentsList(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	// Query agents
	agents, err := h.store.ListAgents(ctx)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.list", "", "error", nil, err.Error())
		return "", fmt.Errorf("failed to list agents: %w", err)
	}

	// Write audit log — failure is non-fatal; the primary operation already succeeded.
	if err = h.store.WriteAudit(
		ctx,
		traceID,
		evt.Sender.String(),
		"agents.list",
		"",
		"success",
		store.AuditPayload{"count": len(agents)},
		"",
	); err != nil {
		slog.Warn("audit write failed", "op", "agents.list", "err", err)
	}

	// Format response
	if len(agents) == 0 {
		return fmt.Sprintf("No agents found. (trace: %s)\n\nCreate your first agent with:\n/ruriko agents create --template cron --name myagent", traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Agents (%d)**\n\n", len(agents)))

	for _, agent := range agents {
		statusEmoji := "❓"
		switch agent.Status {
		case "running":
			statusEmoji = "✅"
		case "stopped":
			statusEmoji = "⏹️"
		case "starting":
			statusEmoji = "🔄"
		case "error":
			statusEmoji = "❌"
		}

		sb.WriteString(fmt.Sprintf("%s **%s** (%s)\n", statusEmoji, agent.ID, agent.Status))
		sb.WriteString(fmt.Sprintf("  Template: %s\n", agent.Template))
		if agent.MXID.Valid {
			sb.WriteString(fmt.Sprintf("  MXID: %s\n", agent.MXID.String))
		}
		if agent.LastSeen.Valid {
			sb.WriteString(fmt.Sprintf("  Last Seen: %s\n", agent.LastSeen.Time.Format(time.RFC3339)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleAgentsShow shows details for a specific agent
func (h *Handlers) HandleAgentsShow(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	// Get agent name from arguments
	agentName, ok := cmd.GetArg(0)
	if !ok {
		return "", fmt.Errorf("usage: /ruriko agents show <name>")
	}

	// Query agent
	agent, err := h.store.GetAgent(ctx, agentName)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "agents.show", agentName, "error", nil, err.Error())
		return "", fmt.Errorf("failed to get agent: %w", err)
	}

	// Write audit log — failure is non-fatal; the primary operation already succeeded.
	if err = h.store.WriteAudit(
		ctx,
		traceID,
		evt.Sender.String(),
		"agents.show",
		agentName,
		"success",
		nil,
		"",
	); err != nil {
		slog.Warn("audit write failed", "op", "agents.show", "err", err)
	}

	// Format response
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Agent: %s**\n\n", agent.ID))
	sb.WriteString(fmt.Sprintf("**Display Name:** %s\n", agent.DisplayName))
	sb.WriteString(fmt.Sprintf("**Template:** %s\n", agent.Template))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", agent.Status))

	if agent.MXID.Valid {
		sb.WriteString(fmt.Sprintf("**Matrix ID:** %s\n", agent.MXID.String))
	}

	if agent.RuntimeVersion.Valid {
		sb.WriteString(fmt.Sprintf("**Runtime Version:** %s\n", agent.RuntimeVersion.String))
	}

	if agent.GosutoVersion.Valid {
		sb.WriteString(fmt.Sprintf("**Gosuto Version:** %d\n", agent.GosutoVersion.Int64))
	}

	if agent.LastSeen.Valid {
		sb.WriteString(fmt.Sprintf("**Last Seen:** %s\n", agent.LastSeen.Time.Format(time.RFC3339)))
	}

	sb.WriteString(fmt.Sprintf("**Created:** %s\n", agent.CreatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("**Updated:** %s\n", agent.UpdatedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("\n(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleAuditTail shows recent audit entries
func (h *Handlers) HandleAuditTail(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	// Get limit from arguments
	limit := 10
	if limitStr, ok := cmd.GetArg(0); ok {
		fmt.Sscanf(limitStr, "%d", &limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	// Query audit log
	entries, err := h.store.GetAuditLog(ctx, limit)
	if err != nil {
		return "", fmt.Errorf("failed to get audit log: %w", err)
	}

	// Write audit log — failure is non-fatal; the primary operation already succeeded.
	if err = h.store.WriteAudit(
		ctx,
		traceID,
		evt.Sender.String(),
		"audit.tail",
		"",
		"success",
		store.AuditPayload{"limit": limit},
		"",
	); err != nil {
		slog.Warn("audit write failed", "op", "audit.tail", "err", err)
	}

	// Format response
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Recent Audit Entries (last %d)**\n\n", limit))

	for _, entry := range entries {
		resultEmoji := "✅"
		if entry.Result == "error" {
			resultEmoji = "❌"
		} else if entry.Result == "denied" {
			resultEmoji = "🚫"
		}

		sb.WriteString(fmt.Sprintf("%s `%s` **%s** by %s\n",
			resultEmoji,
			entry.Timestamp.Format("15:04:05"),
			entry.Action,
			entry.ActorMXID,
		))

		if entry.Target.Valid {
			sb.WriteString(fmt.Sprintf("   Target: %s\n", entry.Target.String))
		}
		if entry.ErrorMessage.Valid {
			sb.WriteString(fmt.Sprintf("   Error: %s\n", entry.ErrorMessage.String))
		}

		sb.WriteString(fmt.Sprintf("   Trace: %s\n\n", entry.TraceID))
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}

// HandleTrace shows all audit entries for a trace ID
func (h *Handlers) HandleTrace(ctx context.Context, cmd *Command, evt *event.Event) (string, error) {
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	// Get trace ID from subcommand position (e.g. /ruriko trace t_abc123).
	// The router may place the argument in either Subcommand or Args[0] depending
	// on whether a matching registered key exists, so check both.
	searchTraceID := cmd.Subcommand
	if searchTraceID == "" {
		searchTraceID, _ = cmd.GetArg(0)
	}
	if searchTraceID == "" {
		return "", fmt.Errorf("usage: /ruriko trace <trace_id>")
	}

	// Query audit log
	entries, err := h.store.GetAuditByTrace(ctx, searchTraceID)
	if err != nil {
		h.store.WriteAudit(ctx, traceID, evt.Sender.String(), "trace", searchTraceID, "error", nil, err.Error())
		return "", fmt.Errorf("failed to get trace: %w", err)
	}

	// Write audit log — failure is non-fatal; the primary operation already succeeded.
	if err = h.store.WriteAudit(
		ctx,
		traceID,
		evt.Sender.String(),
		"trace",
		searchTraceID,
		"success",
		store.AuditPayload{"entries": len(entries)},
		"",
	); err != nil {
		slog.Warn("audit write failed", "op", "trace", "err", err)
	}

	// Format response
	if len(entries) == 0 {
		return fmt.Sprintf("No entries found for trace: %s\n\n(trace: %s)", searchTraceID, traceID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Trace: %s** (%d entries)\n\n", searchTraceID, len(entries)))

	for i, entry := range entries {
		resultEmoji := "✅"
		if entry.Result == "error" {
			resultEmoji = "❌"
		} else if entry.Result == "denied" {
			resultEmoji = "🚫"
		}

		sb.WriteString(fmt.Sprintf("%d. %s `%s` **%s** by %s\n",
			i+1,
			resultEmoji,
			entry.Timestamp.Format("15:04:05.000"),
			entry.Action,
			entry.ActorMXID,
		))

		if entry.Target.Valid {
			sb.WriteString(fmt.Sprintf("   Target: %s\n", entry.Target.String))
		}
		if entry.PayloadJSON.Valid {
			sb.WriteString(fmt.Sprintf("   Payload: %s\n", entry.PayloadJSON.String))
		}
		if entry.ErrorMessage.Valid {
			sb.WriteString(fmt.Sprintf("   Error: %s\n", entry.ErrorMessage.String))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("(trace: %s)", traceID))

	return sb.String(), nil
}
