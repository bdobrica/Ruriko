// Package app wires all Gitai subsystems and implements the turn processing
// loop: Matrix message received → policy check → LLM → tool calls → reply.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"maunium.net/go/mautrix/event"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/common/version"
	"github.com/bdobrica/Ruriko/internal/gitai/approvals"
	"github.com/bdobrica/Ruriko/internal/gitai/control"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/matrix"
	"github.com/bdobrica/Ruriko/internal/gitai/mcp"
	"github.com/bdobrica/Ruriko/internal/gitai/observability"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/secrets"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

const maxToolCallRounds = 10

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
	llmProvMu  sync.RWMutex // guards llmProv
	llmProv    llm.Provider
	matrixCli  *matrix.Client
	approvalGt *approvals.Gate
	acpServer  *control.Server
	startedAt  time.Time
	restartCh  chan struct{}
	// cancelCh is signalled when Ruriko sends a POST /tasks/cancel request.
	// The currently running turn should watch this channel and abort early.
	cancelCh chan struct{}
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

	app := &App{
		cfg:        cfg,
		db:         db,
		gosutoLdr:  gosutoLdr,
		secretsStr: secStore,
		secretsMgr: secMgr,
		policyEng:  policyEng,
		supv:       supv,
		llmProv:    llmProv,
		matrixCli:  matrixCli,
		startedAt:  time.Now(),
		restartCh:  restartCh,
		cancelCh:   cancelCh,
	}

	// Approval gate (needs matrix sender for posting to approvals room).
	app.approvalGt = approvals.New(db, matrixCli)

	// ACP server.
	acpAddr := cfg.ACPAddr
	if acpAddr == "" {
		acpAddr = ":8765"
	}
	app.acpServer = control.New(acpAddr, control.Handlers{
		AgentID:                 cfg.AgentID,
		Version:                 version.Version,
		StartedAt:               app.startedAt,
		Token:                   cfg.ACPToken,
		DirectSecretPushEnabled: cfg.DirectSecretPushEnabled,
		GosutoHash:              gosutoLdr.Hash,
		MCPNames:                supv.Names,
		ApplyConfig: func(yaml, hash string) error {
			if err := gosutoLdr.Apply([]byte(yaml)); err != nil {
				return err
			}
			// Persist to DB for restart recovery.
			_ = db.SaveAppliedConfig(hash, yaml)
			// Reconcile MCP servers with new config.
			if c := gosutoLdr.Config(); c != nil {
				supv.Reconcile(c.MCPs)
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
			// Re-inject secret env into supervisor (new processes will pick it up).
			if c := gosutoLdr.Config(); c != nil {
				supv.ApplySecrets(secStore.Env(buildSecretEnvMapping(c.Secrets)))
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

	// Start MCP supervisor if config is available.
	if c := a.gosutoLdr.Config(); c != nil {
		a.supv.Reconcile(c.MCPs)
	}

	// Start Matrix sync.
	var rooms []string
	if c := a.gosutoLdr.Config(); c != nil {
		rooms = c.Trust.AllowedRooms
		if c.Trust.AdminRoom != "" {
			rooms = append(rooms, c.Trust.AdminRoom)
		}
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
	text := msgContent.Body
	roomID := evt.RoomID.String()
	sender := evt.Sender.String()

	// --- Approval decision handling (from an approver replying in the approvals room) ---
	if approvalID, decision, reason, ok := approvals.ParseDecision(text); ok {
		if err := a.approvalGt.RecordDecision(approvalID, decision, sender, reason); err != nil {
			slog.Warn("could not record approval decision", "err", err)
		}
		return
	}

	// --- Policy: check room and sender ---
	if !a.policyEng.IsRoomAllowed(roomID) {
		slog.Debug("message from disallowed room; ignoring", "room", roomID)
		return
	}
	if !a.policyEng.IsSenderAllowed(sender) {
		slog.Debug("message from disallowed sender; ignoring", "sender", sender)
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

	result, toolCalls, err := a.runTurn(ctx, roomID, sender, text, evt.ID.String())
	if err != nil {
		log.Error("turn failed", "err", err)
		_ = a.matrixCli.SendReply(roomID, evt.ID.String(), fmt.Sprintf("❌ %s", err))
		if turnID > 0 {
			_ = a.db.FinishTurn(turnID, toolCalls, "error", err.Error())
		}
		return
	}
	if result != "" {
		if err := a.matrixCli.SendReply(roomID, evt.ID.String(), result); err != nil {
			log.Error("could not send reply", "err", err)
		}
	}
	if turnID > 0 {
		_ = a.db.FinishTurn(turnID, toolCalls, "success", "")
	}
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

	// Build system prompt from persona.
	systemPrompt := cfg.Persona.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = fmt.Sprintf("You are %s. %s", cfg.Metadata.Name, cfg.Metadata.Description)
	}

	// Gather available tools across all running MCP servers.
	toolDefs, _ := a.gatherTools(ctx)

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
		resp, err := prov.Complete(ctx, llm.CompletionRequest{
			Model:     "",
			Messages:  messages,
			Tools:     toolDefs,
			MaxTokens: maxTokens,
		})
		if err != nil {
			return "", totalToolCalls, fmt.Errorf("LLM call failed: %w", err)
		}

		// Append assistant message to history.
		messages = append(messages, resp.Message)

		if resp.FinishReason != "tool_calls" || len(resp.Message.ToolCalls) == 0 {
			// Done — return the text response.
			return resp.Message.Content, totalToolCalls, nil
		}

		// Process tool calls.
		for _, tc := range resp.Message.ToolCalls {
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

// executeToolCall performs policy evaluation and invokes a tool.
func (a *App) executeToolCall(ctx context.Context, roomID, sender string, tc llm.ToolCall) (string, error) {
	log := observability.WithTrace(ctx)

	// Parse tool name: "mcpServer__toolName"
	mcpName, toolName := splitToolName(tc.Function.Name)

	// Parse args.
	var args map[string]interface{}
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}

	// Policy evaluation.
	result := a.policyEng.Evaluate(mcpName, toolName, args)
	log.Info("policy evaluation",
		"mcp", mcpName,
		"tool", toolName,
		"decision", result.Decision,
		"rule", result.MatchedRule,
	)

	switch result.Decision {
	case policy.DecisionDeny:
		return "", fmt.Errorf("policy denied: %s", result.Violation.Message)

	case policy.DecisionRequireApproval:
		cfg := a.gosutoLdr.Config()
		if !cfg.Approvals.Enabled || cfg.Approvals.Room == "" {
			return "", fmt.Errorf("approval required but approvals not configured")
		}
		ttl := time.Duration(cfg.Approvals.TTLSeconds) * time.Second
		log.Info("requesting approval", "mcp", mcpName, "tool", toolName)
		if err := a.approvalGt.Request(ctx, cfg.Approvals.Room, sender, "mcp.call", tc.Function.Name, args, ttl); err != nil {
			return "", fmt.Errorf("approval: %w", err)
		}
		// Approved — fall through to execution.

	case policy.DecisionAllow:
		// Proceed immediately.
	}

	// Execute via MCP client.
	client := a.supv.Get(mcpName)
	if client == nil {
		return "", fmt.Errorf("MCP server %q is not running", mcpName)
	}

	// Resolve any {{secret:ref}} placeholders in tool arguments before the
	// call reaches the MCP server. This allows Gosuto-defined capabilities to
	// include secret references in tool argument defaults without embedding
	// plaintext credentials in the config.
	args, err := a.resolveSecretArgs(args)
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

// gatherTools collects ToolDefinitions from all running MCP servers and returns
// them along with a lookup map of composed tool name → (mcp, tool).
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
	return defs, toolMap
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

// buildLLMProvider creates the LLM provider from config.
func buildLLMProvider(cfg LLMConfig) llm.Provider {
	switch cfg.Provider {
	case "openai", "":
		return llm.NewOpenAI(llm.OpenAIConfig{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
		})
	default:
		slog.Warn("unknown LLM provider; defaulting to OpenAI", "provider", cfg.Provider)
		return llm.NewOpenAI(llm.OpenAIConfig{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
		})
	}
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
