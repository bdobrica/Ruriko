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
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"maunium.net/go/mautrix/event"

	"github.com/bdobrica/Ruriko/common/trace"
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
)

// sessionTTL is how long a pending confirmation is kept without a user response.
const sessionTTL = 5 * time.Minute

// conversationSession holds the pending state for one room+sender pair.
type conversationSession struct {
	step           conversationStep
	intent         ParsedIntent
	missingSecrets []string // required secret names not yet present in the store
	image          string
	expiresAt      time.Time
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
// agent-creation intent.
//
// Returns ("", nil) when no intent was detected or the message is not a
// response to a pending confirmation — the caller should treat it as ordinary
// chat. Returns a non-empty string to be sent back to the user when an intent
// is recognised or a confirmation flow is in progress.
//
// This method is only meaningful when a template registry has been wired into
// the Handlers.  Callers should guard:
//
//	if h.templates == nil { return "", nil }
func (h *Handlers) HandleNaturalLanguage(ctx context.Context, text string, evt *event.Event) (string, error) {
	if h.templates == nil {
		return "", nil
	}

	roomID := evt.RoomID.String()
	senderMXID := evt.Sender.String()

	// --- Check for an active confirmation session ---------------------------
	if session, ok := h.conversations.get(roomID, senderMXID); ok {
		return h.handleConfirmationResponse(ctx, text, session, roomID, senderMXID, evt)
	}

	// --- Parse intent -------------------------------------------------------
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
