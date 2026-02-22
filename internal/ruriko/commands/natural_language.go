package commands

// natural_language.go implements R5.4 Chat-Driven Agent Creation (stretch goal).
//
// Ruriko listens for free-form Matrix messages in admin rooms and tries to
// detect a "create agent" intent using deterministic keyword and pattern
// matching — no LLM is used for control decisions, consistent with Ruriko's
// design principle of deterministic control.
//
// # Supported flow
//
//  1. User says "set up Saito" (or any recognised variant).
//  2. Ruriko parses intent, derives template + agent name.
//  3. Ruriko checks which required secrets for that template are missing.
//  4. Ruriko replies with a confirmation prompt listing missing secrets.
//  5. User replies "yes" (once secrets are stored) or "no" to cancel.
//  6. Ruriko calls HandleAgentsCreate with the synthesised parameters.
//
// # Conversation state
//
// Pending confirmations are held in an in-memory map keyed by roomID:senderMXID
// with a five-minute TTL. No database migration is required.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/ruriko/nlp"
	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

// ---------------------------------------------------------------------------
// Intent types and detection
// ---------------------------------------------------------------------------

// IntentType identifies the action the user wants Ruriko to take.
type IntentType string

const (
	// IntentUnknown means no recognisable intent was found.
	IntentUnknown IntentType = "unknown"
	// IntentCreateAgent means the user wants to create (provision) an agent.
	IntentCreateAgent IntentType = "create_agent"
)

// ParsedIntent is the result of natural-language intent extraction.
type ParsedIntent struct {
	Type         IntentType
	TemplateName string // canonical template name, e.g. "saito-agent"
	AgentName    string // derived lowercase agent ID, e.g. "saito"
	Raw          string // original message text
}

// templateKeywords maps lower-case keywords to canonical template names.
// Multiple keywords can map to the same template.
var templateKeywords = map[string]string{
	// saito-agent — cron/trigger/scheduling
	"saito":      "saito-agent",
	"cron":       "saito-agent",
	"scheduler":  "saito-agent",
	"scheduling": "saito-agent",
	"trigger":    "saito-agent",
	"periodic":   "saito-agent",
	"scheduled":  "saito-agent",
	"interval":   "saito-agent",

	// kumo-agent — news/search
	"kumo":   "kumo-agent",
	"news":   "kumo-agent",
	"search": "kumo-agent",
	"brave":  "kumo-agent",

	// browser-agent — headless browsing
	"browser":    "browser-agent",
	"playwright": "browser-agent",
	"browsing":   "browser-agent",

	// kairo-agent — finance/portfolio (template may not yet exist)
	"kairo":     "kairo-agent",
	"finance":   "kairo-agent",
	"portfolio": "kairo-agent",
	"trading":   "kairo-agent",
	"market":    "kairo-agent",
	"finnhub":   "kairo-agent",

	// research-agent — generic research
	"research": "research-agent",
}

// templateDefaultNames maps canonical template names to their default agent IDs.
var templateDefaultNames = map[string]string{
	"saito-agent":    "saito",
	"kumo-agent":     "kumo",
	"browser-agent":  "browser",
	"kairo-agent":    "kairo",
	"cron-agent":     "cron",
	"research-agent": "research",
}

// templateDisplayNames provides a human-friendly description for each template.
var templateDisplayNames = map[string]string{
	"saito-agent":    "Saito (scheduling / trigger)",
	"kumo-agent":     "Kumo (news / search)",
	"browser-agent":  "Browser (web automation)",
	"kairo-agent":    "Kairo (finance / portfolio)",
	"cron-agent":     "Cron (scheduled tasks)",
	"research-agent": "Research",
}

// setupVerbs is a list of words/phrases that signal an intent to create
// or deploy something.  Matching any one of these is sufficient.
var setupVerbs = []string{
	"create", "set up", "setup", "spin up", "spinup",
	"provision", "deploy", "start", "launch", "make", "build", "add",
	"initialise", "initialize", "configure",
	"can you", "could you", "please", "i need", "i want", "i'd like",
}

// ParseIntent attempts to extract a creation intent from free-form text.
// Returns nil when no recognisable intent is found.
//
// The function is deterministic: no LLM is involved.
func ParseIntent(text string) *ParsedIntent {
	lower := strings.ToLower(text)

	// Must contain at least one "setup verb" to be considered an intent.
	hasSetupVerb := false
	for _, v := range setupVerbs {
		if strings.Contains(lower, v) {
			hasSetupVerb = true
			break
		}
	}
	if !hasSetupVerb {
		return nil
	}

	// Tokenise and look for template keywords (single words and bigrams).
	words := tokenise(lower)
	templateName := ""
	for i, w := range words {
		if t, ok := templateKeywords[w]; ok {
			templateName = t
			break
		}
		// Also check two-word phrases (e.g. "news agent", "cron job").
		if i+1 < len(words) {
			if t, ok := templateKeywords[words[i]+" "+words[i+1]]; ok {
				templateName = t
				break
			}
		}
	}
	if templateName == "" {
		return nil
	}

	// Derive agent name: prefer an explicit hint in the message; fall back
	// to the template's canonical default.
	agentName := extractNameHint(lower, templateName)
	if agentName == "" {
		if def, ok := templateDefaultNames[templateName]; ok {
			agentName = def
		} else {
			agentName = strings.TrimSuffix(templateName, "-agent")
		}
	}

	return &ParsedIntent{
		Type:         IntentCreateAgent,
		TemplateName: templateName,
		AgentName:    agentName,
		Raw:          text,
	}
}

// tokenise splits text into lowercase tokens consisting of letters, digits,
// and hyphens (matching valid agent ID characters).
func tokenise(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// extractNameHint looks for explicit naming patterns inside the message such
// as "called <name>", "named <name>", or "name it <name>".
// Returns "" if no hint is found.
func extractNameHint(lower, templateName string) string {
	namingPhrases := []string{"called ", "named ", "name it ", "name: ", "call it "}
	for _, p := range namingPhrases {
		idx := strings.Index(lower, p)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(lower[idx+len(p):])
		words := strings.Fields(rest)
		if len(words) == 0 {
			continue
		}
		candidate := sanitiseAgentID(words[0])
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// sanitiseAgentID converts an arbitrary string to a valid Ruriko agent ID:
// lowercase letters, digits, and hyphens only; must not start or end with a
// hyphen; maximum 63 characters.
func sanitiseAgentID(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 63 {
		result = result[:63]
	}
	return result
}

// inferSecretTypeHint returns a short description of a secret from its name.
func inferSecretTypeHint(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "openai"):
		return "OpenAI API key"
	case strings.Contains(lower, "anthropic"):
		return "Anthropic API key"
	case strings.Contains(lower, "brave"):
		return "Brave Search API key"
	case strings.Contains(lower, "finnhub"):
		return "Finnhub API key"
	case strings.Contains(lower, "matrix") || strings.Contains(lower, "token"):
		return "Matrix access token"
	default:
		return "API key / secret"
	}
}

// ---------------------------------------------------------------------------
// Conversation state
// ---------------------------------------------------------------------------

// conversationStep identifies which step of a multi-turn flow is active.
type conversationStep string

const (
	stepAwaitingConfirmation conversationStep = "awaiting_confirmation"
	// stepNLAwaitingConfirmation is the step used by the LLM-backed NL
	// dispatch path (R9.4).  Sessions in this state hold one or more pending
	// command steps that are awaiting the operator's yes/no confirmation.
	stepNLAwaitingConfirmation conversationStep = "nl_awaiting_confirmation"
)

// sessionTTL is how long a pending confirmation is kept without a user response.
const sessionTTL = 5 * time.Minute

// nlStep is one pending command in the NL dispatch queue.
// It is stored in conversationSession.nlPendingSteps.
type nlStep struct {
	action      string   // Ruriko action key, e.g. "agents.create"
	command     *Command // pre-built Command ready for Dispatch
	explanation string   // human-readable description used in confirmation prompt
}

// conversationSession holds the pending state for one room+sender pair.
type conversationSession struct {
	step           conversationStep
	intent         ParsedIntent
	missingSecrets []string // required secret names not yet present in the store
	image          string
	expiresAt      time.Time

	// NL dispatch fields (populated when step == stepNLAwaitingConfirmation).
	nlPendingSteps []nlStep // remaining command steps awaiting confirmation
	nlTotalSteps   int      // total number of steps (for display)
	nlRawIntent    string   // LLM explanation string, included in audit logs
}

// conversationStore manages in-memory per-room conversation sessions.
// It is safe for concurrent use.
type conversationStore struct {
	mu       sync.Mutex
	sessions map[string]*conversationSession // key: roomID+":"+senderMXID
}

func newConversationStore() *conversationStore {
	return &conversationStore{
		sessions: make(map[string]*conversationSession),
	}
}

func (cs *conversationStore) sessionKey(roomID, senderMXID string) string {
	return roomID + ":" + senderMXID
}

func (cs *conversationStore) get(roomID, senderMXID string) (*conversationSession, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	k := cs.sessionKey(roomID, senderMXID)
	s, ok := cs.sessions[k]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.expiresAt) {
		delete(cs.sessions, k)
		return nil, false
	}
	return s, true
}

func (cs *conversationStore) set(roomID, senderMXID string, s *conversationSession) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.sessions[cs.sessionKey(roomID, senderMXID)] = s
}

func (cs *conversationStore) delete(roomID, senderMXID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.sessions, cs.sessionKey(roomID, senderMXID))
}

// ---------------------------------------------------------------------------
// Handler implementation
// ---------------------------------------------------------------------------

// confirmationPositiveWords are replies that mean "yes, proceed".
var confirmationPositiveWords = []string{
	"yes", "y", "ok", "okay", "confirm", "proceed",
	"go ahead", "go", "create it", "do it", "continue",
	"sure", "yep", "yup", "affirmative",
}

// confirmationNegativeWords are replies that mean "no, cancel".
var confirmationNegativeWords = []string{
	"no", "n", "cancel", "abort", "stop", "nope",
	"nevermind", "never mind", "forget it", "nah",
}

// HandleNaturalLanguage processes a free-form Matrix message for natural-language
// command or agent-creation intent.
//
// Routing logic:
//  1. If the text starts with "/ruriko", return ("", nil) immediately — these
//     are handled by the command router and must never be re-interpreted by the
//     NL layer.
//  2. If there is an active LLM session (stepNLAwaitingConfirmation), delegate
//     to handleNLConfirmationResponse.
//  3. If there is an active keyword session (stepAwaitingConfirmation), delegate
//     to handleConfirmationResponse.
//  4. If h.nlpProvider is configured, call the LLM classifier (R9.4).
//  5. Otherwise, fall back to the deterministic keyword-based ParseIntent (R5.4).
//
// Returns ("", nil) when no intent was detected or the message is not a
// response to a pending confirmation.
func (h *Handlers) HandleNaturalLanguage(ctx context.Context, text string, evt *event.Event) (string, error) {
	roomID := evt.RoomID.String()
	senderMXID := evt.Sender.String()

	// --- Guard: /ruriko-prefixed messages are owned by the command router ----
	// This is a defense-in-depth check; the app-level dispatcher already
	// routes these before reaching HandleNaturalLanguage.  The guard ensures
	// that if HandleNaturalLanguage is called directly (e.g. in tests) with a
	// /ruriko prefix it is always a no-op.
	if strings.HasPrefix(strings.TrimSpace(text), "/ruriko") {
		return "", nil
	}

	// --- Check for an active confirmation session ---------------------------
	if session, ok := h.conversations.get(roomID, senderMXID); ok {
		if session.step == stepNLAwaitingConfirmation {
			return h.handleNLConfirmationResponse(ctx, text, session, roomID, senderMXID, evt)
		}
		return h.handleConfirmationResponse(ctx, text, session, roomID, senderMXID, evt)
	}

	// --- LLM path (R9.4) — takes precedence when a provider is available ----
	// resolveNLPProvider performs the R9.7 lookup order:
	//   1. "ruriko.nlp-api-key" secret (preferred)
	//   2. RURIKO_NLP_API_KEY env var (bootstrap fallback)
	//   3. Nil → degrade to keyword matching below
	// It also rebuilds the underlying http.Client wrapper lazily whenever the
	// effective (apiKey, model, endpoint) triple changes.
	if provider := h.resolveNLPProvider(ctx); provider != nil {
		return h.handleNLClassify(ctx, text, roomID, senderMXID, provider, evt)
	}

	// --- Keyword path (R5.4) — requires template registry ------------------
	if h.templates == nil {
		return "", nil
	}
	return h.handleKeywordIntent(ctx, text, evt)
}

// handleKeywordIntent runs the R5.4 deterministic keyword-matching flow.
func (h *Handlers) handleKeywordIntent(ctx context.Context, text string, evt *event.Event) (string, error) {
	roomID := evt.RoomID.String()
	senderMXID := evt.Sender.String()

	intent := ParseIntent(text)
	if intent == nil {
		return "", nil
	}

	// Verify the template exists in the registry.
	available, err := h.templates.List()
	if err != nil {
		return "", nil
	}
	templateExists := false
	for _, t := range available {
		if t == intent.TemplateName {
			templateExists = true
			break
		}
	}
	if !templateExists {
		// Template not installed — don't raise a false-positive error.
		return "", nil
	}

	// Validate the derived agent ID before committing to the flow.
	if err := validateAgentID(intent.AgentName); err != nil {
		// Attempt a safe fallback name from the template defaults.
		if def, ok := templateDefaultNames[intent.TemplateName]; ok {
			intent.AgentName = def
		} else {
			return "", nil
		}
	}

	// Determine the container image to use.
	image := h.defaultAgentImage
	if image == "" {
		image = "ghcr.io/bdobrica/gitai:latest"
	}

	// Discover required secrets and check which are missing.
	requiredSecrets, err := h.templates.RequiredSecrets(intent.TemplateName, intent.AgentName)
	if err != nil {
		// Non-fatal: proceed without secret guidance.
		requiredSecrets = nil
	}

	missingSecrets := make([]string, 0, len(requiredSecrets))
	for _, ref := range requiredSecrets {
		if _, err := h.secrets.GetMetadata(ctx, ref); err != nil {
			missingSecrets = append(missingSecrets, ref)
		}
	}

	// Store the pending session.
	session := &conversationSession{
		step:           stepAwaitingConfirmation,
		intent:         *intent,
		missingSecrets: missingSecrets,
		image:          image,
		expiresAt:      time.Now().Add(sessionTTL),
	}
	h.conversations.set(roomID, senderMXID, session)

	// Build the confirmation prompt.
	return buildConfirmationPrompt(*intent, missingSecrets, image), nil
}

// handleConfirmationResponse processes a user reply to a pending confirmation.
func (h *Handlers) handleConfirmationResponse(
	ctx context.Context,
	text string,
	session *conversationSession,
	roomID, senderMXID string,
	evt *event.Event,
) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(text))

	isYes := false
	for _, w := range confirmationPositiveWords {
		if lower == w || strings.HasPrefix(lower, w+" ") {
			isYes = true
			break
		}
	}

	isNo := false
	for _, w := range confirmationNegativeWords {
		if lower == w || strings.HasPrefix(lower, w+" ") {
			isNo = true
			break
		}
	}

	if isNo {
		h.conversations.delete(roomID, senderMXID)
		return fmt.Sprintf("❌ Cancelled. Agent **%s** will not be created.", session.intent.AgentName), nil
	}

	if !isYes {
		// Not a recognised response — leave the session active and stay silent
		// so the user can keep using the room normally (e.g. continue typing).
		return "", nil
	}

	// User confirmed — re-check missing secrets in case they were just stored.
	stillMissing := make([]string, 0, len(session.missingSecrets))
	for _, ref := range session.missingSecrets {
		if _, err := h.secrets.GetMetadata(ctx, ref); err != nil {
			stillMissing = append(stillMissing, ref)
		}
	}

	if len(stillMissing) > 0 {
		// Some secrets are still missing — refresh and remind.
		session.missingSecrets = stillMissing
		session.expiresAt = time.Now().Add(sessionTTL)
		h.conversations.set(roomID, senderMXID, session)

		var sb strings.Builder
		sb.WriteString("⚠️ Still missing required secrets before I can create the agent:\n\n")
		for _, s := range stillMissing {
			sb.WriteString(fmt.Sprintf("• `%s` (%s)\n  → `/ruriko secrets set %s`\n",
				s, inferSecretTypeHint(s), s))
		}
		sb.WriteString("\nOnce stored, reply **yes** to proceed or **no** to cancel.")
		return sb.String(), nil
	}

	// All secrets present — clear session and run the provisioning pipeline.
	h.conversations.delete(roomID, senderMXID)

	intent := session.intent

	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)

	syntheticCmd := &Command{
		Name:       "agents",
		Subcommand: "create",
		Flags: map[string]string{
			"name":         intent.AgentName,
			"template":     intent.TemplateName,
			"image":        session.image,
			"display-name": intent.AgentName,
		},
		Args: []string{},
		RawText: fmt.Sprintf(
			"agents create --name %s --template %s --image %s",
			intent.AgentName, intent.TemplateName, session.image,
		),
	}

	return h.HandleAgentsCreate(ctx, syntheticCmd, evt)
}

// buildConfirmationPrompt generates the Matrix message Ruriko sends when an
// intent has been detected.
func buildConfirmationPrompt(intent ParsedIntent, missingSecrets []string, image string) string {
	displayName := templateDisplayNames[intent.TemplateName]
	if displayName == "" {
		displayName = intent.TemplateName
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🤖 I can create a **%s** agent named **%s**.\n\n",
		displayName, intent.AgentName))
	sb.WriteString(fmt.Sprintf("Template: `%s`\nImage: `%s`\n\n", intent.TemplateName, image))

	if len(missingSecrets) == 0 {
		sb.WriteString("✅ All required secrets are already stored.\n\n")
		sb.WriteString("Reply **yes** to provision the agent, or **no** to cancel.")
		return sb.String()
	}

	sb.WriteString("⚠️ **Missing secrets** — these must be stored before provisioning:\n\n")
	for _, s := range missingSecrets {
		sb.WriteString(fmt.Sprintf("• `%s` (%s)\n  → `/ruriko secrets set %s`\n",
			s, inferSecretTypeHint(s), s))
	}
	sb.WriteString("\nStore the missing secrets above, then reply **yes** to create the agent or **no** to cancel.")
	return sb.String()
}

// ---------------------------------------------------------------------------
// R9.4 LLM-backed NL dispatch
// ---------------------------------------------------------------------------

// handleNLClassify is called when an NLP provider is available.  It enforces
// the rate limit and daily token budget, builds the ClassifyRequest from live
// context, calls the LLM via provider, records token usage, and routes the
// response to the appropriate sub-handler.
//
// provider is the resolved nlp.Provider from resolveNLPProvider (R9.7).  It
// is passed as an argument rather than read from h.nlpProvider so that the
// value used for this request always matches the one that caused the path to
// be taken in HandleNaturalLanguage.
func (h *Handlers) handleNLClassify(ctx context.Context, text, roomID, senderMXID string, provider nlp.Provider, evt *event.Event) (string, error) {
	// Rate-limit check.
	if h.nlpRateLimiter != nil && !h.nlpRateLimiter.Allow(senderMXID) {
		return nlp.RateLimitMessage, nil
	}

	// Daily token-budget check.
	if h.nlpTokenBudget != nil && !h.nlpTokenBudget.Allow(senderMXID) {
		slog.Info("nlp: daily token budget exhausted; rejecting request",
			"sender", senderMXID,
			"budget", nlp.DefaultTokenBudget,
		)
		return nlp.TokenBudgetExceededMessage, nil
	}

	// Collect live context for the classifier.
	var knownAgents []string
	if h.store != nil {
		if agents, err := h.store.ListAgents(ctx); err == nil {
			for _, a := range agents {
				knownAgents = append(knownAgents, a.ID)
			}
		}
	}

	var knownTemplates []string
	if h.templates != nil {
		if ts, err := h.templates.List(); err == nil {
			knownTemplates = ts
		}
	}

	req := nlp.ClassifyRequest{
		Message:          text,
		CommandCatalogue: h.buildCommandCatalogue(ctx, evt),
		KnownAgents:      knownAgents,
		KnownTemplates:   knownTemplates,
		SenderMXID:       senderMXID,
	}

	resp, err := provider.Classify(ctx, req)
	if err != nil {
		switch {
		case errors.Is(err, nlp.ErrRateLimit):
			// The upstream LLM API is rate-limiting us globally.  Surface a
			// user-visible message and mark the provider as degraded; do NOT
			// fall back to keyword matching because the user's message was
			// understood.
			slog.Warn("nlp: upstream API rate limit; notifying user", "sender", senderMXID)
			h.nlpHealthState.Store(nlpHealthDegraded)
			return nlp.APIRateLimitMessage, nil

		case errors.Is(err, nlp.ErrMalformedOutput):
			// The LLM returned something we couldn't parse.  Show a friendly
			// clarification prompt rather than silently falling back.
			slog.Warn("nlp: malformed LLM output; prompting user to rephrase", "err", err)
			h.nlpHealthState.Store(nlpHealthDegraded)
			return nlp.MalformedOutputMessage, nil

		default:
			// Generic connectivity / server error → degrade health status and
			// fall back to the deterministic keyword path so the operator is
			// not left in the dark when the LLM is unreachable.
			slog.Warn("nlp.classify failed; falling back to keyword path", "err", err)
			h.nlpHealthState.Store(nlpHealthUnavailable)
			if h.templates != nil {
				return h.handleKeywordIntent(ctx, text, evt)
			}
			return "", nil
		}
	}
	// Successful call — restore health state.
	h.nlpHealthState.Store(nlpHealthOK)

	// Record token usage and write an audit-trail entry so cost can be
	// tracked per operator after the fact.
	if resp.Usage != nil {
		if h.nlpTokenBudget != nil {
			h.nlpTokenBudget.RecordUsage(senderMXID, resp.Usage.TotalTokens)
		}
		slog.Info("nlp: token usage",
			"sender", senderMXID,
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens,
			"total_tokens", resp.Usage.TotalTokens,
			"model", resp.Usage.Model,
			"latency_ms", resp.Usage.LatencyMS,
		)
	}

	switch resp.Intent {
	case nlp.IntentConversational:
		if len(resp.ReadQueries) > 0 {
			return h.handleNLReadQueries(ctx, resp, senderMXID, evt)
		}
		// Pure conversational answer — return the LLM response directly.
		return resp.Response, nil

	case nlp.IntentCommand:
		return h.handleNLCommandIntent(ctx, resp, roomID, senderMXID)

	default:
		// IntentUnknown or low-confidence — surface the clarification prompt.
		return resp.Response, nil
	}
}

// handleNLReadQueries dispatches a set of read-only action keys that the LLM
// needs to compose a conversational answer.  Results are concatenated and
// returned to the caller.  A single "nl.read" audit entry is written.
func (h *Handlers) handleNLReadQueries(ctx context.Context, resp *nlp.ClassifyResponse, senderMXID string, evt *event.Event) (string, error) {
	if h.dispatch == nil {
		// No dispatch wired — return whatever the LLM produced as-is.
		return resp.Response, nil
	}

	var sb strings.Builder
	if resp.Response != "" {
		sb.WriteString(resp.Response)
		sb.WriteString("\n\n")
	}

	for _, query := range resp.ReadQueries {
		cmd := actionKeyToCommand(query, nil, nil)
		result, err := h.dispatch(ctx, query, cmd, evt)
		if err != nil {
			slog.Warn("nl read-query dispatch failed", "action", query, "err", err)
			continue
		}
		if result != "" {
			sb.WriteString(result)
			sb.WriteString("\n\n")
		}
	}

	// Audit annotation.
	if h.store != nil {
		traceID := trace.GenerateID()
		ctx = trace.WithTraceID(ctx, traceID)
		if err := h.store.WriteAudit(ctx, traceID, senderMXID, "nl.read",
			strings.Join(resp.ReadQueries, ","), "success",
			store.AuditPayload{
				"source":       "nl",
				"llm_intent":   resp.Explanation,
				"read_queries": resp.ReadQueries,
			}, ""); err != nil {
			slog.Warn("audit write failed", "op", "nl.read", "err", err)
		}
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

// handleNLCommandIntent builds the pending-step session for a mutation command
// and returns the first step's confirmation prompt.
func (h *Handlers) handleNLCommandIntent(ctx context.Context, resp *nlp.ClassifyResponse, roomID, senderMXID string) (string, error) {
	if h.dispatch == nil {
		return "", nil
	}

	var steps []nlStep
	if len(resp.Steps) > 0 {
		// Multi-step mutation — decompose into individual confirmations.
		for _, s := range resp.Steps {
			cmd := actionKeyToCommand(s.Action, s.Args, s.Flags)
			steps = append(steps, nlStep{
				action:      s.Action,
				command:     cmd,
				explanation: s.Explanation,
			})
		}
	} else {
		// Single command.
		cmd := actionKeyToCommand(resp.Action, resp.Args, resp.Flags)
		steps = []nlStep{{
			action:      resp.Action,
			command:     cmd,
			explanation: resp.Explanation,
		}}
	}

	session := &conversationSession{
		step:           stepNLAwaitingConfirmation,
		nlPendingSteps: steps,
		nlTotalSteps:   len(steps),
		nlRawIntent:    resp.Explanation,
		expiresAt:      time.Now().Add(sessionTTL),
	}
	h.conversations.set(roomID, senderMXID, session)

	return buildNLStepPrompt(steps[0], 1, len(steps)), nil
}

// handleNLConfirmationResponse handles the operator's yes/no reply to a
// pending NL command step.
func (h *Handlers) handleNLConfirmationResponse(
	ctx context.Context,
	text string,
	session *conversationSession,
	roomID, senderMXID string,
	evt *event.Event,
) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(text))

	isYes := false
	for _, w := range confirmationPositiveWords {
		if lower == w || strings.HasPrefix(lower, w+" ") {
			isYes = true
			break
		}
	}

	isNo := false
	for _, w := range confirmationNegativeWords {
		if lower == w || strings.HasPrefix(lower, w+" ") {
			isNo = true
			break
		}
	}

	if isNo {
		h.conversations.delete(roomID, senderMXID)
		return "❌ Cancelled. No changes were made.", nil
	}

	if !isYes {
		// Not a recognised response — leave the session active.
		return "", nil
	}

	if len(session.nlPendingSteps) == 0 {
		h.conversations.delete(roomID, senderMXID)
		return "", nil
	}

	step := session.nlPendingSteps[0]
	remaining := session.nlPendingSteps[1:]
	doneIndex := session.nlTotalSteps - len(session.nlPendingSteps)

	// Write NL dispatch audit before executing so the reasoning chain is
	// captured even if the command subsequently fails or is denied.
	traceID := trace.GenerateID()
	ctx = trace.WithTraceID(ctx, traceID)
	if h.store != nil {
		if err := h.store.WriteAudit(ctx, traceID, senderMXID,
			"nl.dispatch", step.action, "dispatching",
			store.AuditPayload{
				"source":     "nl",
				"llm_intent": session.nlRawIntent,
				"action":     step.action,
				"step":       doneIndex + 1,
				"total":      session.nlTotalSteps,
			}, ""); err != nil {
			slog.Warn("audit write failed", "op", "nl.dispatch", "err", err)
		}
	}

	result, err := h.dispatch(ctx, step.action, step.command, evt)
	if err != nil {
		// Leave the session active so the operator can retry.
		return fmt.Sprintf("❌ Failed to run `%s`: %v\n\nReply **yes** to retry or **no** to cancel.", step.action, err), nil
	}

	if len(remaining) > 0 {
		// Advance to the next step.
		session.nlPendingSteps = remaining
		session.expiresAt = time.Now().Add(sessionTTL)
		h.conversations.set(roomID, senderMXID, session)

		nextStep := remaining[0]
		var sb strings.Builder
		if result != "" {
			sb.WriteString(result)
			sb.WriteString("\n\n")
		}
		sb.WriteString(buildNLStepPrompt(nextStep, doneIndex+2, session.nlTotalSteps))
		return sb.String(), nil
	}

	// All steps complete — tidy up and return the final result.
	h.conversations.delete(roomID, senderMXID)
	return result, nil
}

// ---------------------------------------------------------------------------
// R9.4 helpers
// ---------------------------------------------------------------------------

// actionKeyToCommand builds a synthetic *Command from a dot-separated action
// key (e.g. "agents.list"), positional args, and a flag map.  The resulting
// Command is safe to pass directly to a DispatchFunc.
func actionKeyToCommand(action string, args []string, flags map[string]string) *Command {
	parts := strings.SplitN(action, ".", 2)
	cmd := &Command{
		Name:    parts[0],
		Args:    args,
		Flags:   flags,
		RawText: buildNLRawText(action, args, flags),
	}
	if len(parts) == 2 {
		cmd.Subcommand = parts[1]
	}
	if cmd.Args == nil {
		cmd.Args = []string{}
	}
	if cmd.Flags == nil {
		cmd.Flags = map[string]string{}
	}
	return cmd
}

// buildNLRawText produces the human-readable command string used in prompts
// and Command.RawText.
func buildNLRawText(action string, args []string, flags map[string]string) string {
	var sb strings.Builder
	sb.WriteString("/ruriko ")
	sb.WriteString(strings.ReplaceAll(action, ".", " "))
	for _, a := range args {
		sb.WriteString(" ")
		sb.WriteString(a)
	}
	// Deterministic flag ordering.
	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf(" --%s %s", k, flags[k]))
	}
	return sb.String()
}

// buildNLStepPrompt returns the Matrix message shown when a NL command step
// is pending confirmation.
func buildNLStepPrompt(step nlStep, stepNum, totalSteps int) string {
	var sb strings.Builder
	if totalSteps > 1 {
		sb.WriteString(fmt.Sprintf("**Step %d of %d**\n\n", stepNum, totalSteps))
	}
	if step.explanation != "" {
		sb.WriteString(step.explanation)
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("I'll run:\n```\n%s\n```\n\n",
		buildNLRawText(step.action, step.command.Args, step.command.Flags)))
	sb.WriteString("Reply **yes** to proceed or **no** to cancel.")
	return sb.String()
}

// buildCommandCatalogue returns the help text used as the LLM's command
// catalogue.  Returns an empty string on error (non-fatal — the LLM will
// still attempt to classify using its training data).
func (h *Handlers) buildCommandCatalogue(ctx context.Context, evt *event.Event) string {
	helpCmd := &Command{Name: "help", Args: []string{}, Flags: map[string]string{}}
	text, err := h.HandleHelp(ctx, helpCmd, evt)
	if err != nil {
		return ""
	}
	return text
}
