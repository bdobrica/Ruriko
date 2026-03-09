// Package app wires all Gitai subsystems and implements the turn processing
// loop: Matrix message received → policy check → LLM → tool calls → reply.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"maunium.net/go/mautrix/event"

	commonmemory "github.com/bdobrica/Ruriko/common/memory"
	"github.com/bdobrica/Ruriko/common/spec/envelope"
	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/gitai/approvals"
	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/control"
	"github.com/bdobrica/Ruriko/internal/gitai/gateway"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/matrix"
	"github.com/bdobrica/Ruriko/internal/gitai/mcp"
	"github.com/bdobrica/Ruriko/internal/gitai/observability"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/secrets"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
	"github.com/bdobrica/Ruriko/internal/gitai/workflow"
)

const maxToolCallRounds = 10

const (
	dispatchCallerLLM      = "llm"
	dispatchCallerWorkflow = "workflow"
	dispatchCallerGateway  = "gateway"
	dispatchCallerControl  = "control"
)

// approvalGate is the subset of approvals.Gate used by the app dispatcher.
type approvalGate interface {
	Request(ctx context.Context, approvalsRoom, requestorMXID, action, target string, params map[string]interface{}, ttl time.Duration) error
	RecordDecision(approvalID string, status store.ApprovalStatus, decidedBy, reason string) error
}

// ToolDispatchRequest is the unified request shape for all tool execution
// paths (LLM, workflow, gateway, deterministic pipelines).
type ToolDispatchRequest struct {
	Caller string
	Sender string
	Name   string
	Args   map[string]interface{}
}

// eventMatrixSender abstracts the Matrix send operations needed by
// runEventTurn.  It is satisfied by *matrix.Client and can be replaced with
// a lightweight recording stub in unit tests without spinning up a real
// Matrix connection.
type eventMatrixSender interface {
	SendText(roomID, text string) error
}

// Config holds the Gitai application configuration. All values are typically
// loaded from environment variables by cmd/gitai/main.go.
type Config struct {
	// AgentID is this agent's stable identifier (matches the Ruriko agents.id).
	AgentID string

	// DatabasePath is the path to the SQLite database file.
	DatabasePath string

	// GosutoFile is an optional path to the initial gosuto.yaml to load.
	// When empty the agent starts with no config and waits for a push via ACP.
	GosutoFile string

	// Matrix holds the Matrix connection settings.
	Matrix matrix.Config

	// LLM holds the LLM provider settings.
	LLM LLMConfig

	// ACPAddr is the TCP address for the ACP (Agent Control Protocol) HTTP server.
	// Defaults to ":8765".
	ACPAddr string

	// ACPToken, when non-empty, is the bearer token that ACP clients must
	// supply in the Authorization header.  When empty, authentication is
	// disabled (dev/test mode).
	ACPToken string

	// DirectSecretPushEnabled re-enables the legacy POST /secrets/apply endpoint
	// that sends raw (base64-encoded) secret values directly in the ACP request
	// body.  This is DISABLED by default (production safe) — secrets must flow
	// via Kuze token redemption (POST /secrets/token).  Set to true only for
	// local dev or one-off migration scenarios.
	//
	// Environment variable: FEATURE_DIRECT_SECRET_PUSH (default: false)
	DirectSecretPushEnabled bool

	// LogLevel is "debug", "info", "warn", or "error". Defaults to "info".
	LogLevel string
	// LogFormat is "text" or "json". Defaults to "text".
	LogFormat string

	// LLMCallHardLimit is a strict upper bound on total LLM completion calls
	// made by this agent process. 0 disables the limiter.
	//
	// When exceeded, the process terminates immediately to prevent runaway cost.
	// Primarily intended for test and safety-constrained environments.
	LLMCallHardLimit int

	// MemoryContextEnabled enables shared memory-context assembly for prompt
	// injection (R18 prep). Disabled by default.
	MemoryContextEnabled bool
}

// LLMConfig configures the language model backend.
type LLMConfig struct {
	// Provider is the LLM backend to use. Currently only "openai" is supported.
	Provider string
	// APIKey is the API key (may come from a secret pushed by Ruriko).
	APIKey string
	// BaseURL overrides the API base URL (e.g. for local Ollama: "http://localhost:11434/v1").
	BaseURL string
	// Model is the default model identifier.
	Model string
	// MaxTokens caps the response length. 0 = provider default.
	MaxTokens int
}

// App is the main Gitai application.
type App struct {
	cfg        *Config
	db         *store.Store
	gosutoLdr  *gosuto.Loader
	secretsStr *secrets.Store
	secretsMgr *secrets.Manager
	policyEng  *policy.Engine
	supv       *supervisor.Supervisor
	cronMgr    *gateway.Manager
	extGWSupv  *supervisor.ExternalGatewaySupervisor
	// eventSender is used by runEventTurn to post gateway-event responses to
	// Matrix.  It defaults to matrixCli in New() and can be overridden in tests.
	eventSender eventMatrixSender
	llmProvMu   sync.RWMutex // guards llmProv
	llmProv     llm.Provider
	matrixCli   *matrix.Client
	approvalGt  approvalGate
	acpServer   *control.Server
	startedAt   time.Time
	restartCh   chan struct{}
	// cancelCh is signalled when Ruriko sends a POST /tasks/cancel request.
	// The currently running turn should watch this channel and abort early.
	cancelCh chan struct{}
	// builtinReg holds the registry of non-MCP built-in tools exposed to the
	// LLM. Currently contains matrix.send_message (R15.2).
	builtinReg *builtin.Registry
	// msgOutbound counts the total number of successful matrix.send_message
	// calls made by this agent (R15.5 audit/observability).
	msgOutbound atomic.Int64
	// llmCalls counts the total number of LLM completion calls made by this
	// process. Used by the hard limit kill-switch.
	llmCalls atomic.Int64
	// terminateProcess exits the current process; defaults to os.Exit.
	terminateProcess func(code int)
	memorySTM        *gitaiMemorySTM
	memoryAssembler  *commonmemory.ContextAssembler
	workflowEngine   *workflow.Engine
	workflowEngineMu sync.Once
}

// New creates and initialises all Gitai subsystems. It does NOT start any
// goroutines; call Run() for that.
func New(cfg *Config) (*App, error) {
	observability.Setup(cfg.LogLevel, cfg.LogFormat)

	db, err := store.New(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	gosutoLdr := gosuto.New()

	// Load initial config from file (or previously applied config in DB).
	if cfg.GosutoFile != "" {
		if err := gosutoLdr.LoadFile(cfg.GosutoFile); err != nil {
			slog.Warn("could not load gosuto file; starting without config", "file", cfg.GosutoFile, "err", err)
		}
	} else {
		// Try to restore from last applied config in DB.
		hash, yaml, err := db.LoadAppliedConfig()
		if err != nil {
			slog.Warn("could not load applied config from DB", "err", err)
		} else if yaml != "" {
			if err := gosutoLdr.Apply([]byte(yaml)); err != nil {
				slog.Warn("stored config is invalid; starting without config", "hash", hash, "err", err)
			}
		}
	}

	secStore := secrets.New()
	secMgr := secrets.NewManager(secStore, 0) // uses DefaultCacheTTL (4 h)
	policyEng := policy.New(gosutoLdr)
	supv := supervisor.New()

	// Build LLM provider.
	llmProv := buildLLMProvider(cfg.LLM)

	// Matrix client.
	matrixCli, err := matrix.New(&cfg.Matrix)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init matrix: %w", err)
	}

	restartCh := make(chan struct{}, 1)
	cancelCh := make(chan struct{}, 1)

	// Built-in tool registry — populate before any turn can run.
	builtinReg := builtin.New()
	builtinReg.Register(builtin.NewMatrixSendTool(gosutoLdr, matrixCli))
	builtinReg.Register(builtin.NewScheduleUpsertTool(db))
	builtinReg.Register(builtin.NewScheduleDisableTool(db))
	builtinReg.Register(builtin.NewScheduleListTool(db))

	app := &App{
		cfg:              cfg,
		db:               db,
		gosutoLdr:        gosutoLdr,
		secretsStr:       secStore,
		secretsMgr:       secMgr,
		policyEng:        policyEng,
		supv:             supv,
		llmProv:          llmProv,
		matrixCli:        matrixCli,
		eventSender:      matrixCli,
		startedAt:        time.Now(),
		restartCh:        restartCh,
		cancelCh:         cancelCh,
		builtinReg:       builtinReg,
		terminateProcess: os.Exit,
	}

	if cfg.MemoryContextEnabled {
		app.memorySTM = newGitaiMemorySTM(50)
		app.memoryAssembler = &commonmemory.ContextAssembler{
			STM:       app.memorySTM,
			LTM:       gitaiNoopLTM{},
			Embedder:  gitaiNoopEmbedder{},
			MaxTokens: commonmemory.DefaultMaxTokens,
			LTMTopK:   commonmemory.DefaultLTMTopK,
		}
		slog.Info("gitai memory context enabled")
	}

	// Approval gate (needs matrix sender for posting to approvals room).
	app.approvalGt = approvals.New(db, matrixCli)

	// ACP server.
	acpAddr := cfg.ACPAddr
	if acpAddr == "" {
		acpAddr = ":8765"
	}
	// Cron gateway manager: connects to the ACP event ingress on localhost.
	cronMgr := gateway.NewManager(gateway.ACPBaseURL(acpAddr))
	app.cronMgr = cronMgr
	cronMgr.EnableDBSchedules(db, func(ctx context.Context, gatewayName, tool string, args map[string]interface{}) error {
		traceCtx := trace.WithTraceID(ctx, trace.GenerateID())
		_, err := app.DispatchToolCall(traceCtx, ToolDispatchRequest{
			Caller: dispatchCallerGateway,
			Sender: "gateway:" + gatewayName,
			Name:   tool,
			Args:   args,
		})
		return err
	})
	// External gateway supervisor: manages external gateway binaries (Command set in Gosuto).
	extGWSupv := supervisor.NewExternalGatewaySupervisor(gateway.ACPBaseURL(acpAddr))
	app.extGWSupv = extGWSupv
	app.acpServer = control.New(acpAddr, control.Handlers{
		AgentID:                 cfg.AgentID,
		Version:                 version.Version,
		StartedAt:               app.startedAt,
		Token:                   cfg.ACPToken,
		DirectSecretPushEnabled: cfg.DirectSecretPushEnabled,
		GosutoHash:              gosutoLdr.Hash,
		MCPNames:                supv.Names,
		ActiveConfig:            gosutoLdr.Config,
		// R15.5: expose outbound message count in the ACP /status response.
		MessagesOutbound: func() int64 { return app.msgOutbound.Load() },
		// GetSecret looks up an agent secret by ref name. Used by the
		// built-in webhook gateway to validate HMAC-SHA256 signatures.
		GetSecret: func(ref string) ([]byte, error) {
			return secStore.Get(ref)
		},
		ApplyConfig: func(yaml, hash string) error {
			if err := gosutoLdr.Apply([]byte(yaml)); err != nil {
				return err
			}
			// Persist to DB for restart recovery.
			_ = db.SaveAppliedConfig(hash, yaml)
			// Reconcile MCP servers, cron gateways, and external gateway processes.
			if c := gosutoLdr.Config(); c != nil {
				supv.Reconcile(c.MCPs)
				cronMgr.Reconcile(c.Gateways)
				extGWSupv.Reconcile(c.Gateways)
				if app.matrixCli != nil {
					app.matrixCli.EnsureJoinedRooms(roomsFromConfig(c))
				}
			}
			// Rebuild the LLM provider in case the new Gosuto specifies a
			// different APIKeySecretRef or model, and the matching secret is
			// already cached in the secret manager.
			app.rebuildLLMProvider()
			return nil
		},
		ApplySecrets: func(sec map[string]string) error {
			// Route through the Manager so TTL entries are recorded.
			// Manager.Apply calls secStore.Apply internally.
			if err := secMgr.Apply(sec, 0); err != nil {
				return err
			}
			// Re-inject secret env into MCP supervisor and external gateway supervisor
			// (new processes will pick up the updated credentials).
			if c := gosutoLdr.Config(); c != nil {
				supv.ApplySecrets(secStore.Env(buildSecretEnvMapping(c.Secrets)))
				extGWSupv.ApplySecrets(secStore.Env(buildSecretEnvMapping(c.Secrets)))
			}
			// Rebuild the LLM provider with the freshly redeemed API key if the
			// active Gosuto config specifies an APIKeySecretRef. This ensures the
			// provider always uses the most recently redeemed credential without
			// requiring an agent restart.
			app.rebuildLLMProvider()
			return nil
		},
		RequestRestart: func() { restartCh <- struct{}{} },
		// Signal the current turn to abort.  Non-blocking send: if no turn is
		// running the signal is silently dropped.
		RequestCancel: func() {
			select {
			case cancelCh <- struct{}{}:
			default:
			}
		},
		RecordApprovalDecision: func(approvalID, decision, decidedBy, reason string) error {
			status := store.ApprovalDenied
			if strings.EqualFold(decision, "approve") {
				status = store.ApprovalApproved
			}
			return app.approvalGt.RecordDecision(approvalID, status, decidedBy, reason)
		},
		ExecuteTool: func(ctx context.Context, sender, toolRef string, args map[string]interface{}) (string, error) {
			return app.DispatchToolCall(ctx, ToolDispatchRequest{
				Caller: dispatchCallerControl,
				Sender: sender,
				Name:   toolRef,
				Args:   args,
			})
		},
		// HandleEvent dispatches inbound gateway events to the turn engine.
		// The method must be non-blocking; the actual work runs in a goroutine.
		HandleEvent: func(ctx context.Context, evt *envelope.Event) {
			app.handleEvent(ctx, evt)
		},
	})

	return app, nil
}

// Run starts all subsystems and blocks until a shutdown signal is received.
func (a *App) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start ACP server.
	if err := a.acpServer.Start(ctx); err != nil {
		return fmt.Errorf("start acp server: %w", err)
	}

	// Start MCP supervisor, cron gateways, and external gateway processes.
	if c := a.gosutoLdr.Config(); c != nil {
		a.supv.Reconcile(c.MCPs)
		a.cronMgr.Reconcile(c.Gateways)
		a.extGWSupv.Reconcile(c.Gateways)
	}

	// Start Matrix sync.
	var rooms []string
	if c := a.gosutoLdr.Config(); c != nil {
		rooms = roomsFromConfig(c)
	}
	if err := a.matrixCli.Start(ctx, rooms, a.handleMessage); err != nil {
		return fmt.Errorf("start matrix: %w", err)
	}

	slog.Info("Gitai agent started",
		"agent_id", a.cfg.AgentID,
		"version", version.Version,
	)

	// Start secret eviction goroutine — sweeps expired cached secrets every
	// minute to reduce the in-memory credential exposure window.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := a.secretsMgr.Evict(); n > 0 {
					slog.Debug("secrets eviction sweep", "evicted", n)
				}
			}
		}
	}()

	// Wait for stop signal or restart request.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		slog.Info("received shutdown signal")
	case <-a.restartCh:
		slog.Info("restart requested via ACP")
	}

	slog.Info("shutting down")
	cancel()
	a.Stop()
	return nil
}

// Stop shuts down all subsystems cleanly.
func (a *App) Stop() {
	a.matrixCli.Stop()
	a.supv.Stop()
	a.cronMgr.Stop()
	a.extGWSupv.Stop()
	a.acpServer.Stop()
	a.db.Close()
}

// GetSecret retrieves a named secret value from the Manager cache.
//
// It returns secrets.ErrSecretNotFound when the ref was never applied and
// secrets.ErrSecretExpired when the TTL has elapsed. Callers MUST NOT log
// the returned value.
//
// This is the canonical point for tool implementations, LLM provider
// rebuilds, or any other subsystem that needs to read a secret at call time.
func (a *App) GetSecret(ref string) (string, error) {
	return a.secretsMgr.GetSecret(ref)
}

// GetSecretBytes is the raw-byte variant of GetSecret. See GetSecret for the
// full semantics.
func (a *App) GetSecretBytes(ref string) ([]byte, error) {
	return a.secretsMgr.GetSecretBytes(ref)
}

// provider returns the current LLM provider under its read lock. Callers must
// use this rather than reading a.llmProv directly so that concurrent secret
// refreshes (which may rebuild the provider) remain safe.
func (a *App) provider() llm.Provider {
	a.llmProvMu.RLock()
	defer a.llmProvMu.RUnlock()
	return a.llmProv
}

// setProvider atomically replaces the current LLM provider. Called by
// rebuildLLMProvider after obtaining fresh credentials.
func (a *App) setProvider(p llm.Provider) {
	a.llmProvMu.Lock()
	a.llmProv = p
	a.llmProvMu.Unlock()
}

// handleMessage is called by the Matrix client for every incoming text message.
func (a *App) handleMessage(ctx context.Context, evt *event.Event) {
	msgContent := evt.Content.AsMessage()
	if msgContent == nil {
		return
	}

	// Skip stale messages that were sent before this agent started.
	// During initial sync the homeserver replays recent timeline events;
	// processing all of them would cause an LLM call storm that exhausts
	// rate limits. Only messages arriving after startup are actionable.
	evtTime := time.UnixMilli(evt.Timestamp)
	if evtTime.Before(a.startedAt) {
		slog.Debug("skipping stale message from before startup",
			"event_id", evt.ID,
			"event_ts", evtTime.Format(time.RFC3339),
			"started_at", a.startedAt.Format(time.RFC3339),
		)
		return
	}

	text := msgContent.Body
	roomID := evt.RoomID.String()
	sender := evt.Sender.String()

	// --- Policy: check room and sender ---
	if !a.policyEng.IsRoomAllowed(roomID) {
		slog.Debug("message from disallowed room; ignoring", "room", roomID)
		return
	}
	if !a.policyEng.IsSenderAllowed(sender) {
		slog.Debug("message from disallowed sender; ignoring", "sender", sender)
		return
	}

	cfg := a.gosutoLdr.Config()
	directedToSelf := false
	var protocolMatch *workflow.InboundProtocolMatch
	if cfg != nil {
		if target, directedBody, directed := parseDirectedAgentMessage(text); directed {
			fallbackAgentID := ""
			if a.cfg != nil {
				fallbackAgentID = a.cfg.AgentID
			}
			self := agentIdentity(cfg, fallbackAgentID)
			if !isDirectedAtSelf(target, self) {
				slog.Debug("ignoring directed message intended for another agent",
					"room", roomID,
					"sender", sender,
					"target", target,
					"self", self,
				)
				return
			}
			directedToSelf = true
			text = directedBody
		}

		match, werr := workflow.MatchInboundProtocol(cfg, roomID, sender, text)
		if werr != nil {
			protocolID := ""
			if match != nil {
				protocolID = match.Protocol.ID
			}
			if workflow.HasCode(werr, workflow.CodeTrustMismatch) {
				slog.Warn("workflow protocol message rejected by trust gate",
					"room", roomID,
					"sender", sender,
					"protocol", protocolID,
					"err", werr,
				)
			} else {
				slog.Warn("workflow protocol message rejected",
					"room", roomID,
					"sender", sender,
					"protocol", protocolID,
					"err", werr,
				)
			}
			return
		}
		protocolMatch = match
	}

	if protocolMatch == nil && !directedToSelf {
		slog.Debug("ignoring undirected non-protocol message",
			"room", roomID,
			"sender", sender,
		)
		return
	}

	// Generate trace ID for this turn.
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)
	log := observability.WithTrace(ctx)

	turnID, err := a.db.LogTurn(traceID, roomID, sender, text)
	if err != nil {
		log.Warn("could not log turn", "err", err)
	}

	var (
		result    string
		toolCalls int
	)
	if protocolMatch != nil && len(protocolMatch.Protocol.Steps) > 0 {
		result, toolCalls, err = a.runWorkflowTurn(ctx, roomID, sender, protocolMatch)
	} else {
		result, toolCalls, err = a.runTurn(ctx, roomID, sender, text, evt.ID.String())
	}
	if err != nil {
		log.Error("turn failed", "err", err)
		if a.matrixCli != nil && shouldSendTurnErrorReply(cfg, sender, err) {
			_ = a.matrixCli.SendReply(roomID, evt.ID.String(), fmt.Sprintf("❌ %s", err))
		}
		if turnID > 0 {
			_ = a.db.FinishTurn(turnID, toolCalls, "error", err.Error())
		}
		return
	}
	if result != "" && a.matrixCli != nil {
		if err := a.matrixCli.SendReply(roomID, evt.ID.String(), result); err != nil {
			log.Error("could not send reply", "err", err)
		}
	}
	if turnID > 0 {
		_ = a.db.FinishTurn(turnID, toolCalls, "success", "")
	}
}

func parseDirectedAgentMessage(text string) (target, body string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", false
	}
	remainder := ""
	if len(trimmed) >= 4 && strings.EqualFold(trimmed[:4], "hey ") {
		remainder = strings.TrimSpace(trimmed[4:])
	} else if strings.HasPrefix(trimmed, "@") {
		remainder = trimmed
	} else {
		return "", "", false
	}
	if remainder == "" {
		return "", "", false
	}
	comma := strings.Index(remainder, ",")
	if comma <= 0 {
		return "", "", false
	}
	target = strings.TrimSpace(remainder[:comma])
	target = strings.TrimPrefix(target, "@")
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", false
	}
	body = strings.TrimSpace(remainder[comma+1:])
	if body == "" {
		return "", "", false
	}
	return target, body, true
}

func isDirectedAtSelf(target, self string) bool {
	normalizedTarget := normalizeDirectedTarget(target)
	normalizedSelf := normalizeDirectedTarget(self)
	if normalizedTarget == "" || normalizedSelf == "" {
		return false
	}
	return strings.EqualFold(normalizedTarget, normalizedSelf)
}

func normalizeDirectedTarget(v string) string {
	v = strings.TrimSpace(strings.TrimPrefix(v, "@"))
	if v == "" {
		return ""
	}
	if at := strings.Index(v, ":"); at > 0 {
		v = v[:at]
	}
	return strings.TrimSpace(v)
}

func agentIdentity(cfg *gosutospec.Config, fallback string) string {
	if cfg != nil {
		if canonical := strings.TrimSpace(cfg.Metadata.CanonicalName); canonical != "" {
			return canonical
		}
		if name := strings.TrimSpace(cfg.Metadata.Name); name != "" {
			return name
		}
	}
	return strings.TrimSpace(fallback)
}

func isTrustedPeerSender(cfg *gosutospec.Config, sender string) bool {
	if cfg == nil {
		return false
	}
	for _, peer := range cfg.Trust.TrustedPeers {
		if strings.EqualFold(strings.TrimSpace(peer.MXID), strings.TrimSpace(sender)) {
			return true
		}
	}
	return false
}

func shouldSendTurnErrorReply(cfg *gosutospec.Config, sender string, err error) bool {
	if err == nil {
		return false
	}
	if !isTrustedPeerSender(cfg, sender) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "llm provider not configured") || strings.Contains(msg, "llm call failed") {
		return false
	}
	return true
}

// runTurn executes the full turn loop: prompt → LLM → tool calls → response.
func (a *App) runTurn(ctx context.Context, roomID, sender, userText, replyToEventID string) (string, int, error) {
	cfg := a.gosutoLdr.Config()
	if cfg == nil {
		return "", 0, fmt.Errorf("no Gosuto config loaded; cannot process messages")
	}
	prov := a.provider()
	if prov == nil {
		return "", 0, fmt.Errorf("LLM provider not configured")
	}

	// Build messaging targets summary for the system prompt (R15.2).
	messagingTargets := buildMessagingTargets(cfg)
	memoryContext := ""
	if a.memorySTM != nil {
		a.memorySTM.RecordMessage(roomID, sender, "user", userText)
	}
	if a.memoryAssembler != nil {
		msgs, err := a.memoryAssembler.Assemble(ctx, roomID, sender, userText)
		if err != nil {
			slog.Warn("memory context assembly failed", "err", err, "room_id", roomID, "sender", sender)
		} else {
			memoryContext = formatMemoryContext(msgs)
		}
	}

	// Build system prompt from persona + instructions + messaging targets (R14.3, R15.2).
	systemPrompt := buildSystemPrompt(cfg, messagingTargets, memoryContext)

	// Gather available tools: MCP servers + built-in tools (R15.2).
	toolDefs, _ := a.gatherTools(ctx)
	providerName := strings.ToLower(strings.TrimSpace(cfg.Persona.LLMProvider))
	if providerName == "" {
		providerName = strings.ToLower(strings.TrimSpace(a.cfg.LLM.Provider))
	}
	toolDefsForLLM, llmToolNameMap := normalizeToolDefinitionsForProvider(providerName, toolDefs)

	// Initial message history.
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userText},
	}

	totalToolCalls := 0
	maxTokens := 0
	if cfg.Limits.MaxTokensPerRequest > 0 {
		maxTokens = cfg.Limits.MaxTokensPerRequest
	}

	for round := 0; round < maxToolCallRounds; round++ {
		if err := a.enforceLLMCallHardLimit(); err != nil {
			return "", totalToolCalls, err
		}
		resp, err := prov.Complete(ctx, llm.CompletionRequest{
			Model:     "",
			Messages:  messages,
			Tools:     toolDefsForLLM,
			MaxTokens: maxTokens,
		})
		if err != nil {
			return "", totalToolCalls, fmt.Errorf("LLM call failed: %w", err)
		}

		// Append assistant message to history.
		messages = append(messages, resp.Message)

		if resp.FinishReason != "tool_calls" || len(resp.Message.ToolCalls) == 0 {
			// Done — return the text response.
			if a.memorySTM != nil && resp.Message.Content != "" {
				a.memorySTM.RecordMessage(roomID, sender, "assistant", resp.Message.Content)
			}
			return resp.Message.Content, totalToolCalls, nil
		}

		// Process tool calls.
		for _, tc := range resp.Message.ToolCalls {
			if canonical, ok := llmToolNameMap[tc.Function.Name]; ok {
				tc.Function.Name = canonical
			}
			totalToolCalls++
			result, err := a.executeToolCall(ctx, roomID, sender, tc)
			toolResultMsg := llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			}
			if err != nil {
				toolResultMsg.Content = fmt.Sprintf("error: %s", err)
			} else {
				toolResultMsg.Content = result
			}
			messages = append(messages, toolResultMsg)
		}
	}

	return "", totalToolCalls, fmt.Errorf("exceeded maximum tool call rounds (%d)", maxToolCallRounds)
}

func (a *App) enforceLLMCallHardLimit() error {
	if a == nil || a.cfg == nil || a.cfg.LLMCallHardLimit <= 0 {
		return nil
	}
	count := a.llmCalls.Add(1)
	limit := int64(a.cfg.LLMCallHardLimit)
	if count <= limit {
		return nil
	}
	slog.Error("LLM hard call limit exceeded; terminating process",
		"agent_id", a.cfg.AgentID,
		"llm_calls", count,
		"limit", limit,
	)
	if a.terminateProcess != nil {
		a.terminateProcess(1)
	} else {
		os.Exit(1)
	}
	return fmt.Errorf("LLM hard call limit exceeded (%d)", limit)
}

var nonOpenAIToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func normalizeToolDefinitionsForProvider(provider string, defs []llm.ToolDefinition) ([]llm.ToolDefinition, map[string]string) {
	if strings.ToLower(strings.TrimSpace(provider)) != "openai" {
		return defs, map[string]string{}
	}

	normalizedDefs := make([]llm.ToolDefinition, 0, len(defs))
	llmToCanonical := make(map[string]string, len(defs))
	used := make(map[string]struct{}, len(defs))

	for _, def := range defs {
		canonical := def.Function.Name
		normalized := nonOpenAIToolNameChars.ReplaceAllString(canonical, "_")
		if normalized == "" {
			normalized = "tool"
		}
		candidate := normalized
		suffix := 2
		for {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s_%d", normalized, suffix)
			suffix++
		}
		used[candidate] = struct{}{}

		copyDef := def
		copyDef.Function.Name = candidate
		normalizedDefs = append(normalizedDefs, copyDef)
		llmToCanonical[candidate] = canonical
	}

	return normalizedDefs, llmToCanonical
}

// executeToolCall performs policy evaluation and invokes a tool.
// Built-in tools (registered in a.builtinReg) are dispatched to
// executeBuiltinTool; all other tool calls route through MCP clients.
func (a *App) executeToolCall(ctx context.Context, roomID, sender string, tc llm.ToolCall) (string, error) {
	var args map[string]interface{}
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}

	_ = roomID
	return a.DispatchToolCall(ctx, ToolDispatchRequest{
		Caller: dispatchCallerLLM,
		Sender: sender,
		Name:   tc.Function.Name,
		Args:   args,
	})
}

// DispatchToolCall is the single deterministic tool execution boundary used by
// both LLM and non-LLM execution paths (workflow, gateway, deterministic flows).
func (a *App) DispatchToolCall(ctx context.Context, req ToolDispatchRequest) (string, error) {
	log := observability.WithTrace(ctx)

	isBuiltin := a.builtinReg != nil && a.builtinReg.IsBuiltin(req.Name)
	namespace := ""
	mcpName := ""
	toolName := req.Name
	approvalAction := "mcp.call"

	if isBuiltin {
		namespace = builtin.BuiltinMCPNamespace
		approvalAction = "builtin.call"
	} else {
		mcpName, toolName = splitToolName(req.Name)
		namespace = mcpName
		if strings.TrimSpace(mcpName) == "" || strings.TrimSpace(toolName) == "" {
			return "", fmt.Errorf("invalid MCP tool name %q: expected mcp__tool", req.Name)
		}
	}

	result := a.policyEng.Evaluate(namespace, toolName, req.Args)
	log.Info("policy evaluation",
		"caller", req.Caller,
		"mcp", namespace,
		"tool", toolName,
		"decision", result.Decision,
		"rule", result.MatchedRule,
	)

	switch result.Decision {
	case policy.DecisionDeny:
		return "", fmt.Errorf("policy denied: %s", result.Violation.Message)

	case policy.DecisionRequireApproval:
		cfg := a.gosutoLdr.Config()
		if cfg == nil || !cfg.Approvals.Enabled || cfg.Approvals.Room == "" {
			return "", fmt.Errorf("approval required but approvals not configured")
		}
		if a.approvalGt == nil {
			return "", fmt.Errorf("approval required but approval gate is not configured")
		}
		ttl := time.Duration(cfg.Approvals.TTLSeconds) * time.Second
		approvalPayload := map[string]interface{}{
			"trace_id":            trace.FromContext(ctx),
			"tool_ref":            req.Name,
			"normalized_arg_hash": normalizedArgsHash(req.Args),
			"caller_context":      req.Caller,
			"args":                req.Args,
		}
		log.Info("requesting approval",
			"caller", req.Caller,
			"mcp", namespace,
			"tool", req.Name,
		)
		if err := a.approvalGt.Request(ctx, cfg.Approvals.Room, req.Sender, approvalAction, req.Name, approvalPayload, ttl); err != nil {
			return "", fmt.Errorf("approval: %w", err)
		}

	case policy.DecisionAllow:
		// Continue.
	}

	if isBuiltin {
		tool := a.builtinReg.Get(req.Name)
		if tool == nil {
			return "", fmt.Errorf("built-in tool %q not found in registry", req.Name)
		}

		toolResult, execErr := tool.Execute(ctx, req.Args)
		if req.Name == builtin.MatrixSendToolName {
			a.auditMessagingSend(ctx, req.Args, execErr)
		}
		return toolResult, execErr
	}

	client := a.supv.Get(mcpName)
	if client == nil {
		return "", fmt.Errorf("MCP server %q is not running", mcpName)
	}

	args, err := a.resolveSecretArgs(req.Args)
	if err != nil {
		return "", fmt.Errorf("resolving secret args for %s.%s: %w", mcpName, toolName, err)
	}

	callResult, err := client.CallTool(ctx, toolName, args)
	if err != nil {
		return "", fmt.Errorf("tool call %s.%s: %w", mcpName, toolName, err)
	}
	if callResult.IsError {
		return "", fmt.Errorf("tool %s.%s returned error: %v", mcpName, toolName, callResult.Content)
	}

	return formatToolResult(callResult), nil
}

// resolveSecretArgs returns a copy of args where any string value matching
// the placeholder pattern "{{secret:ref_name}}" has been replaced with the
// plaintext value obtained from the secret manager.
//
// Security contract:
//   - The method NEVER logs resolved secret values; only the ref name is logged
//     at debug level.
//   - The caller (executeToolCall) must also ensure values are not logged after
//     substitution.
//   - If a placeholder references an unknown or expired secret the entire call
//     fails — the agent should request a fresh Kuze token before retrying.
//
// Non-string argument values and strings that do not follow the placeholder
// syntax are returned unchanged.
func (a *App) resolveSecretArgs(args map[string]interface{}) (map[string]interface{}, error) {
	if len(args) == 0 {
		return args, nil
	}
	resolved := make(map[string]interface{}, len(args))
	for k, v := range args {
		sv, ok := v.(string)
		if !ok {
			resolved[k] = v
			continue
		}
		val, wasRef, err := a.interpolateSecretString(sv)
		if err != nil {
			return nil, fmt.Errorf("arg %q references secret that could not be resolved: %w", k, err)
		}
		if wasRef {
			// Log the ref name (never the value) so operators can trace which
			// secret was used without exposing the credential.
			slog.Debug("secrets: resolved secret arg placeholder",
				"arg_key", k,
				"secret_ref", sv[len("{{secret:"):len(sv)-2],
			)
		}
		resolved[k] = val
	}
	return resolved, nil
}

// interpolateSecretString checks whether s is a well-formed secret placeholder
// of the form "{{secret:ref_name}}" (whole string, not embedded substring) and
// resolves it via the secret manager.
//
// Returns (resolved, true, nil) when s was a placeholder and resolution
// succeeded; (s, false, nil) when s is a plain string; ("", true, err) when s
// was a placeholder but the secret could not be retrieved.
//
// The returned value MUST NOT be logged when wasRef is true.
func (a *App) interpolateSecretString(s string) (value string, wasRef bool, err error) {
	const prefix = "{{secret:"
	const suffix = "}}"
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) || len(s) <= len(prefix)+len(suffix) {
		return s, false, nil
	}
	ref := s[len(prefix) : len(s)-len(suffix)]
	if ref == "" {
		return s, false, nil
	}
	val, err := a.GetSecret(ref)
	if err != nil {
		return "", true, err
	}
	return val, true, nil
}

// rebuildLLMProvider rebuilds the LLM provider using the API key from the
// secret manager when the active Gosuto config specifies an APIKeySecretRef.
//
// This is called after every successful secret refresh (POST /secrets/token)
// and after a new Gosuto config is applied, so that the provider always uses
// the most recently redeemed API key.
//
// If no APIKeySecretRef is configured, or the secret is not yet available,
// the existing provider is left in place and a warning is logged.
func (a *App) rebuildLLMProvider() {
	cfg := a.gosutoLdr.Config()
	if cfg == nil || cfg.Persona.APIKeySecretRef == "" {
		return
	}
	ref := cfg.Persona.APIKeySecretRef
	apiKey, err := a.GetSecret(ref)
	if err != nil {
		slog.Warn("secrets: cannot rebuild LLM provider — API key secret unavailable",
			"ref", ref, "err", err)
		return
	}
	llmCfg := LLMConfig{
		Provider:  cfg.Persona.LLMProvider,
		APIKey:    apiKey, // value is never logged
		BaseURL:   a.cfg.LLM.BaseURL,
		Model:     cfg.Persona.Model,
		MaxTokens: a.cfg.LLM.MaxTokens,
	}
	if llmCfg.Provider == "" {
		llmCfg.Provider = a.cfg.LLM.Provider
	}
	if llmCfg.Model == "" {
		llmCfg.Model = a.cfg.LLM.Model
	}
	a.setProvider(buildLLMProvider(llmCfg))
	slog.Info("secrets: LLM provider rebuilt with refreshed API key", "ref", ref)
}

// gatherTools collects ToolDefinitions from all running MCP servers plus
// every registered built-in tool, and returns them along with a lookup map
// of composed MCP tool name → (mcp, tool).  Built-in tools are added after
// MCP tools; they are not included in the lookup map (dispatched separately).
func (a *App) gatherTools(ctx context.Context) ([]llm.ToolDefinition, map[string][2]string) {
	toolMap := make(map[string][2]string)
	var defs []llm.ToolDefinition

	for _, mcpName := range a.supv.Names() {
		client := a.supv.Get(mcpName)
		if client == nil {
			continue
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			slog.Warn("could not list tools", "mcp", mcpName, "err", err)
			continue
		}
		for _, t := range tools {
			composed := mcpName + "__" + t.Name
			toolMap[composed] = [2]string{mcpName, t.Name}
			defs = append(defs, llm.ToolDefinition{
				Type: "function",
				Function: llm.FunctionDef{
					Name:        composed,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	// Append built-in tool definitions (R15.2). They are identified by their
	// canonical name (e.g. "matrix.send_message") and dispatched via
	// executeBuiltinTool, not through the MCP client.
	//
	// R15.3: matrix.send_message is excluded from the tool list when no
	// messaging targets are configured in Gosuto (default-deny: the tool is
	// unavailable rather than visible-but-always-denied, which would cause the
	// LLM to attempt calls that always fail).
	if a.builtinReg != nil {
		messagingConfigured := a.policyEng.IsMessagingConfigured()
		for _, def := range a.builtinReg.Definitions() {
			if def.Function.Name == builtin.MatrixSendToolName && !messagingConfigured {
				continue
			}
			defs = append(defs, def)
		}
	}

	return defs, toolMap
}

// executeBuiltinTool evaluates policy and dispatches a tool call to the
// appropriate built-in handler.
//
// Policy is evaluated using the "builtin" pseudo-MCP server namespace so that
// Gosuto capability rules of the form (mcp: builtin, tool: <name>) apply with
// the same first-match-wins semantics as MCP tool rules.
func (a *App) executeBuiltinTool(ctx context.Context, sender string, tc llm.ToolCall) (string, error) {
	var args map[string]interface{}
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}

	return a.DispatchToolCall(ctx, ToolDispatchRequest{
		Caller: dispatchCallerLLM,
		Sender: sender,
		Name:   tc.Function.Name,
		Args:   args,
	})
}

// auditMessagingSend logs a matrix.send_message call at INFO and, on success,
// posts an audit breadcrumb to the agent's admin room and increments the
// outbound message counter (R15.5).
//
// Message content is never logged at INFO.
func (a *App) auditMessagingSend(ctx context.Context, args map[string]interface{}, execErr error) {
	log := observability.WithTrace(ctx)

	targetAlias, _ := args["target"].(string)

	// Resolve the room ID for the target alias from the active Gosuto config.
	// This is a read-only lookup — it does not re-validate or re-check policy.
	roomID := ""
	if cfg := a.gosutoLdr.Config(); cfg != nil {
		for _, t := range cfg.Messaging.AllowedTargets {
			if t.Alias == targetAlias {
				roomID = t.RoomID
				break
			}
		}
	}

	agentID := ""
	if a.cfg != nil {
		agentID = a.cfg.AgentID
	}

	status := "success"
	if execErr != nil {
		status = "error"
	}

	log.Info("matrix.send_message",
		"agent_id", agentID,
		"target", targetAlias,
		"room_id", roomID,
		"status", status,
	)

	if execErr != nil {
		return
	}

	// R15.5: Increment outbound message counter.
	a.msgOutbound.Add(1)

	// R15.5: Post audit breadcrumb to admin room.
	// Only attempt when the Matrix sender is available and adminRoom is configured.
	cfg := a.gosutoLdr.Config()
	if cfg == nil || cfg.Trust.AdminRoom == "" || a.eventSender == nil {
		return
	}
	traceID := trace.FromContext(ctx)
	breadcrumb := fmt.Sprintf("📨 Sent message to %s (trace=%s)", targetAlias, traceID)
	if err := a.eventSender.SendText(cfg.Trust.AdminRoom, breadcrumb); err != nil {
		log.Warn("audit: could not post messaging breadcrumb to admin room",
			"admin_room", cfg.Trust.AdminRoom,
			"err", err,
		)
	}
}

// buildMessagingTargets returns a slice of human-readable target strings
// ("alias (roomID)") derived from cfg.Messaging.AllowedTargets, suitable for
// injection into the LLM system prompt via buildSystemPrompt.
func buildMessagingTargets(cfg *gosutospec.Config) []string {
	if cfg == nil || len(cfg.Messaging.AllowedTargets) == 0 {
		return nil
	}
	targets := make([]string, 0, len(cfg.Messaging.AllowedTargets))
	for _, t := range cfg.Messaging.AllowedTargets {
		targets = append(targets, fmt.Sprintf("%s (%s)", t.Alias, t.RoomID))
	}
	return targets
}

// splitToolName splits a composed tool name "mcpServer__toolName" into
// its components. When there is no separator the whole string is returned as
// the tool name with an empty MCP name.
func splitToolName(composed string) (mcpName, toolName string) {
	for i := 0; i < len(composed)-1; i++ {
		if composed[i] == '_' && composed[i+1] == '_' {
			return composed[:i], composed[i+2:]
		}
	}
	return "", composed
}

// formatToolResult converts an MCP CallToolResult into a string for the LLM.
func formatToolResult(result *mcp.CallToolResult) string {
	var parts []string
	for _, item := range result.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	if len(parts) == 0 {
		return "(empty result)"
	}
	out := ""
	for _, p := range parts {
		out += p + "\n"
	}
	return out
}

func normalizedArgsHash(args map[string]interface{}) string {
	if len(args) == 0 {
		sum := sha256.Sum256([]byte("{}"))
		return hex.EncodeToString(sum[:])
	}
	b, err := json.Marshal(args)
	if err != nil {
		sum := sha256.Sum256([]byte("{}"))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildLLMProvider creates the LLM provider from config.
func buildLLMProvider(cfg LLMConfig) llm.Provider {
	apiKey := strings.TrimSpace(cfg.APIKey)
	switch cfg.Provider {
	case "openai", "":
		if apiKey == "" {
			slog.Warn("LLM provider disabled: missing OpenAI API key")
			return nil
		}
		return llm.NewOpenAI(llm.OpenAIConfig{
			APIKey:  apiKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
		})
	default:
		slog.Warn("unknown LLM provider; defaulting to OpenAI", "provider", cfg.Provider)
		if apiKey == "" {
			slog.Warn("LLM provider disabled: missing OpenAI API key")
			return nil
		}
		return llm.NewOpenAI(llm.OpenAIConfig{
			APIKey:  apiKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
		})
	}
}

// handleEvent is the HandleEvent callback wired into the ACP server (R12.2).
// It MUST return quickly — the full turn runs in a background goroutine so that
// the HTTP 202 is returned to the gateway before the LLM call completes.
func (a *App) handleEvent(ctx context.Context, evt *envelope.Event) {
	go a.runEventTurn(context.Background(), evt)
}

// runEventTurn executes the full turn pipeline for an inbound gateway event.
// It mirrors handleMessage but uses the admin room as the output destination
// and a "gateway:<source>" label as the sender identifier.
func (a *App) runEventTurn(ctx context.Context, evt *envelope.Event) {
	cfg := a.gosutoLdr.Config()
	if cfg == nil {
		slog.Warn("event dropped: no Gosuto config loaded",
			"source", evt.Source, "type", evt.Type, "reason", "no_config")
		return
	}

	adminRoom := cfg.Trust.AdminRoom
	if adminRoom == "" {
		slog.Warn("event dropped: no adminRoom configured in Gosuto trust block",
			"source", evt.Source, "type", evt.Type, "reason", "no_admin_room")
		return
	}

	// Build the user-facing text for this event turn.
	userText := buildEventMessage(evt)

	// Assign a stable trace ID for the turn so every log line and DB record
	// can be correlated back to this specific event.
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)
	log := observability.WithTrace(ctx)

	// Log the turn in the DB. LogGatewayTurn stores trigger="gateway",
	// gateway_name, and event_type so that gateway turns are distinguishable
	// from Matrix-message turns without parsing the sender_mxid string.
	senderLabel := "gateway:" + evt.Source
	turnID, err := a.db.LogGatewayTurn(traceID, adminRoom, senderLabel, userText, evt.Source, evt.Type)
	if err != nil {
		log.Warn("could not log event turn", "err", err)
	}

	// "event received" — source, type, timestamp (payload content never logged at INFO).
	log.Info("event received",
		"trigger", "gateway",
		"gateway_name", evt.Source,
		"event_type", evt.Type,
		"ts", evt.TS,
	)

	startedAt := time.Now()
	result := ""
	toolCalls := 0
	match, werr := workflow.MatchGatewayProtocol(cfg, evt)
	if werr != nil {
		err = werr
	} else if match != nil && len(match.Protocol.Steps) > 0 {
		result, toolCalls, err = a.runWorkflowTurn(ctx, adminRoom, senderLabel, match)
	} else {
		result, toolCalls, err = a.runTurn(ctx, adminRoom, senderLabel, userText, "")
	}
	durationMS := time.Since(startedAt).Milliseconds()

	if err != nil {
		// "event processed" with status=error.
		log.Error("event processed",
			"trigger", "gateway",
			"gateway_name", evt.Source,
			"event_type", evt.Type,
			"status", "error",
			"duration_ms", durationMS,
			"tool_calls", toolCalls,
			"err", err,
		)
		if a.eventSender != nil {
			_ = a.eventSender.SendText(adminRoom,
				fmt.Sprintf("⚡ Event: %s/%s\n❌ %s", evt.Source, evt.Type, err))
		}
		if turnID > 0 {
			_ = a.db.FinishTurnWithDuration(turnID, toolCalls, durationMS, "error", err.Error())
		}
		return
	}

	// Post the formatted response to the admin room.  The raw event payload
	// is intentionally NOT forwarded — only the LLM-processed response is
	// sent to Matrix (R12.6 safety requirement).
	if result != "" && a.eventSender != nil {
		header := fmt.Sprintf("⚡ Event: %s/%s", evt.Source, evt.Type)
		_ = a.eventSender.SendText(adminRoom, header+"\n"+result)
	}

	if turnID > 0 {
		_ = a.db.FinishTurnWithDuration(turnID, toolCalls, durationMS, "success", "")
	}

	// "event processed" — source, type, duration, tool_calls, status.
	log.Info("event processed",
		"trigger", "gateway",
		"gateway_name", evt.Source,
		"event_type", evt.Type,
		"status", "success",
		"duration_ms", durationMS,
		"tool_calls", toolCalls,
	)
}

// buildEventMessage returns the user-facing text for an event turn.
// When the event's Payload.Message is non-empty it is returned verbatim.
// When it is empty a descriptive prompt is auto-generated from the event
// metadata and any structured data in the payload — matching the pattern
// described in R12.2: "Event received from {source} (type: {type}). Data: {json}"
func buildEventMessage(evt *envelope.Event) string {
	if evt.Payload.Message != "" {
		return evt.Payload.Message
	}
	if len(evt.Payload.Data) == 0 {
		return fmt.Sprintf("Event received from %s (type: %s).", evt.Source, evt.Type)
	}
	dataJSON, err := json.Marshal(evt.Payload.Data)
	if err != nil {
		return fmt.Sprintf("Event received from %s (type: %s).", evt.Source, evt.Type)
	}
	return fmt.Sprintf("Event received from %s (type: %s). Data: %s", evt.Source, evt.Type, dataJSON)
}

// buildSecretEnvMapping creates an envVar → secretName mapping from the Gosuto
// SecretRef list so the supervisor can inject secrets into MCP environments.
// Only refs with a non-empty EnvVar are included.
func buildSecretEnvMapping(secrets []gosutospec.SecretRef) map[string]string {
	out := make(map[string]string, len(secrets))
	for _, s := range secrets {
		if s.EnvVar != "" {
			out[s.EnvVar] = s.Name
		}
	}
	return out
}

func roomsFromConfig(cfg *gosutospec.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(cfg.Trust.AllowedRooms)+1)
	rooms := make([]string, 0, len(cfg.Trust.AllowedRooms)+1)
	for _, room := range cfg.Trust.AllowedRooms {
		room = strings.TrimSpace(room)
		if room == "" {
			continue
		}
		if _, ok := seen[room]; ok {
			continue
		}
		seen[room] = struct{}{}
		rooms = append(rooms, room)
	}
	adminRoom := strings.TrimSpace(cfg.Trust.AdminRoom)
	if adminRoom != "" {
		if _, ok := seen[adminRoom]; !ok {
			rooms = append(rooms, adminRoom)
		}
	}
	return rooms
}
