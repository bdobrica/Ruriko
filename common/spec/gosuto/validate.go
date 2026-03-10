package gosuto

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes a Gosuto YAML document into a Config struct and validates it.
// It is the canonical entry point for loading Gosuto configurations.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("gosuto parse: %w", err)
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks a Config for structural correctness without executing it.
// It returns the first validation error encountered, or nil if the config is valid.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}

	// ── API version ──────────────────────────────────────────────────────────
	if cfg.APIVersion != SpecVersion {
		return fmt.Errorf("apiVersion must be %q, got %q", SpecVersion, cfg.APIVersion)
	}

	// ── Metadata ─────────────────────────────────────────────────────────────
	if strings.TrimSpace(cfg.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name must not be empty")
	}

	// ── Trust ────────────────────────────────────────────────────────────────
	if err := validateTrust(cfg.Trust); err != nil {
		return fmt.Errorf("trust: %w", err)
	}

	// ── Workflow ─────────────────────────────────────────────────────────────
	if err := validateWorkflow(cfg.Workflow); err != nil {
		return fmt.Errorf("workflow: %w", err)
	}

	// ── Limits ───────────────────────────────────────────────────────────────
	if err := validateLimits(cfg.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	// ── Capabilities ─────────────────────────────────────────────────────────
	for i, cap := range cfg.Capabilities {
		if err := validateCapability(cap); err != nil {
			return fmt.Errorf("capabilities[%d] (%q): %w", i, cap.Name, err)
		}
	}

	// ── MCP servers ──────────────────────────────────────────────────────────
	// supervisorNames tracks all names in the shared supervisor namespace
	// (MCPs + gateways) to detect cross-type collisions.
	supervisorNames := make(map[string]struct{}, len(cfg.MCPs)+len(cfg.Gateways))
	for i, mcp := range cfg.MCPs {
		if err := validateMCPServer(mcp); err != nil {
			return fmt.Errorf("mcps[%d] (%q): %w", i, mcp.Name, err)
		}
		if _, dup := supervisorNames[mcp.Name]; dup {
			return fmt.Errorf("mcps[%d]: duplicate name %q", i, mcp.Name)
		}
		supervisorNames[mcp.Name] = struct{}{}
	}

	// ── Gateways ─────────────────────────────────────────────────────────────
	for i, gw := range cfg.Gateways {
		if err := validateGateway(gw); err != nil {
			return fmt.Errorf("gateways[%d] (%q): %w", i, gw.Name, err)
		}
		if _, dup := supervisorNames[gw.Name]; dup {
			return fmt.Errorf("gateways[%d]: name %q already used by an MCP server or another gateway", i, gw.Name)
		}
		supervisorNames[gw.Name] = struct{}{}
	}

	// ── Secret refs ──────────────────────────────────────────────────────────
	for i, ref := range cfg.Secrets {
		if strings.TrimSpace(ref.Name) == "" {
			return fmt.Errorf("secrets[%d]: name must not be empty", i)
		}
	}

	// ── Persona ──────────────────────────────────────────────────────────────
	if err := validatePersona(cfg.Persona); err != nil {
		return fmt.Errorf("persona: %w", err)
	}

	// ── Instructions ─────────────────────────────────────────────────────────
	if err := validateInstructions(cfg.Instructions); err != nil {
		return fmt.Errorf("instructions: %w", err)
	}

	// ── Messaging ─────────────────────────────────────────────────────────────
	if err := validateMessaging(cfg.Messaging); err != nil {
		return fmt.Errorf("messaging: %w", err)
	}

	return nil
}

// ── Warnings ─────────────────────────────────────────────────────────────────

// Warning is a non-fatal advisory about a Gosuto configuration. Warnings
// indicate potential misconfigurations or logical inconsistencies that do not
// prevent the agent from running, but may indicate unintended behaviour.
//
// The canonical source of warnings is the three-layer authority model
// (Invariant §2 — Policy > Instructions > Persona): instructions are
// operational and advisory; they cannot grant capabilities outside policy.
// If a workflow step references an MCP server not covered by an allow rule,
// the agent will be denied access at runtime.
type Warning struct {
	// Field is the dotted path of the config field that triggered the warning
	// (e.g. "instructions.workflow[1].action").
	Field string

	// Message is a human-readable description of the issue.
	Message string
}

// Warnings inspects a validated Config for non-fatal advisory issues and
// returns any warnings found. It assumes cfg has already passed Validate —
// call Validate (or Parse) first; Warnings does not re-validate structure.
//
// Currently emitted warnings:
//   - Instructions workflow steps whose Action text references an MCP server
//     name that has no allow:true capability rule. Per Invariant §2
//     (Policy > Instructions > Persona), instructions cannot grant access to
//     tools outside the capability rules — requests will be denied at runtime.
func Warnings(cfg *Config) []Warning {
	if cfg == nil {
		return nil
	}

	var ws []Warning

	// Build the set of MCP server names covered by at least one allow:true rule.
	allowed := make(map[string]bool, len(cfg.MCPs))
	wildcardAllow := false
	for _, cap := range cfg.Capabilities {
		if !cap.Allow {
			continue
		}
		if cap.MCP == "*" {
			wildcardAllow = true
			break
		}
		allowed[cap.MCP] = true
	}

	// If a wildcard allow rule covers all MCPs there is nothing to warn about.
	if wildcardAllow {
		return ws
	}

	// For each workflow step, check whether the action text mentions MCP server
	// names that are not covered by an allow rule.
	for i, step := range cfg.Instructions.Workflow {
		actionLower := strings.ToLower(step.Action)
		for _, mcp := range cfg.MCPs {
			if allowed[mcp.Name] {
				continue
			}
			if strings.Contains(actionLower, strings.ToLower(mcp.Name)) {
				ws = append(ws, Warning{
					Field: fmt.Sprintf("instructions.workflow[%d].action", i),
					Message: fmt.Sprintf(
						"references MCP server %q which has no allow:true capability rule; "+
							"the agent will be denied access at runtime "+
							"(Invariant §2: Policy > Instructions > Persona)",
						mcp.Name,
					),
				})
			}
		}
	}

	return ws
}

// ── helpers ──────────────────────────────────────────────────────────────────

func validateTrust(t Trust) error {
	if len(t.AllowedRooms) == 0 {
		return fmt.Errorf("allowedRooms must not be empty")
	}
	for _, room := range t.AllowedRooms {
		if room != "*" && !strings.HasPrefix(room, "!") {
			return fmt.Errorf("allowedRooms entry %q must start with '!' or be \"*\"", room)
		}
	}

	if len(t.AllowedSenders) == 0 {
		return fmt.Errorf("allowedSenders must not be empty")
	}
	for _, sender := range t.AllowedSenders {
		if sender != "*" && !strings.HasPrefix(sender, "@") {
			return fmt.Errorf("allowedSenders entry %q must start with '@' or be \"*\"", sender)
		}
	}

	seen := make(map[string]struct{})
	for i, peer := range t.TrustedPeers {
		if !strings.HasPrefix(peer.MXID, "@") {
			return fmt.Errorf("trustedPeers[%d]: mxid %q must start with '@'", i, peer.MXID)
		}
		if !strings.HasPrefix(peer.RoomID, "!") {
			return fmt.Errorf("trustedPeers[%d]: roomId %q must start with '!'", i, peer.RoomID)
		}
		if len(peer.Protocols) == 0 {
			return fmt.Errorf("trustedPeers[%d]: protocols must not be empty", i)
		}
		for j, protocol := range peer.Protocols {
			protocol = strings.TrimSpace(protocol)
			if protocol == "" {
				return fmt.Errorf("trustedPeers[%d]: protocols[%d] must not be empty", i, j)
			}
			tuple := peer.MXID + "|" + peer.RoomID + "|" + protocol
			if _, dup := seen[tuple]; dup {
				return fmt.Errorf("trustedPeers[%d]: duplicate trusted peer tuple (%s, %s, %s)", i, peer.MXID, peer.RoomID, protocol)
			}
			seen[tuple] = struct{}{}
		}
	}
	return nil
}

func validateWorkflow(w Workflow) error {
	for _, key := range w.Schemas.Duplicates {
		return fmt.Errorf("workflow schemas contains duplicate key %s", key)
	}

	schemas := w.Schemas.Definitions

	validateSchemaRef := func(protocolID, fieldName, ref string, stepIndex int) error {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" {
			return fmt.Errorf("workflow protocol %s: schema ref cannot be empty", protocolID)
		}
		if isExternalSchemaRef(trimmed) {
			return fmt.Errorf("external schema references are not supported; use workflow.schemas")
		}
		if len(schemas) == 0 {
			return fmt.Errorf("workflow schema ref requires workflow.schemas to be defined")
		}
		if _, ok := schemas[trimmed]; !ok {
			switch fieldName {
			case "inputSchemaRef":
				return fmt.Errorf("workflow protocol %s: input schema ref %s not found", protocolID, trimmed)
			case "outputSchemaRef":
				return fmt.Errorf("workflow protocol %s, step %d: output schema ref %s not found", protocolID, stepIndex, trimmed)
			default:
				return fmt.Errorf("workflow protocol %s: schema ref %s not found", protocolID, trimmed)
			}
		}
		return nil
	}

	for name, schema := range schemas {
		if err := validateWorkflowSchemaObject(name, schema); err != nil {
			return err
		}
	}

	validStepTypes := map[string]struct{}{
		"parse_input":  {},
		"tool":         {},
		"branch":       {},
		"summarize":    {},
		"plan":         {},
		"send_message": {},
		"persist":      {},
		"for_each":     {},
		"collect":      {},
	}

	var validateStep func(protocolID string, step WorkflowProtocolStep, stepPath string, topLevelStepIndex int) error
	validateStep = func(protocolID string, step WorkflowProtocolStep, stepPath string, topLevelStepIndex int) error {
		if _, ok := validStepTypes[step.Type]; !ok {
			return fmt.Errorf("workflow protocol %s, %s: unknown step type %q", protocolID, stepPath, step.Type)
		}
		if step.Retries < 0 {
			return fmt.Errorf("workflow protocol %s, %s: retries must be >= 0", protocolID, stepPath)
		}
		if step.MaxOutputItems < 0 {
			return fmt.Errorf("workflow protocol %s, %s: maxOutputItems must be >= 0", protocolID, stepPath)
		}
		if ref := step.InputSchemaRef; ref != "" {
			if err := validateSchemaRef(protocolID, "inputSchemaRef", ref, topLevelStepIndex); err != nil {
				return err
			}
		}
		if ref := step.OutputSchemaRef; ref != "" {
			if err := validateSchemaRef(protocolID, "outputSchemaRef", ref, topLevelStepIndex); err != nil {
				return err
			}
		}
		if ref := step.ForEachIterationSchemaRef; ref != "" {
			if step.Type != "for_each" {
				return fmt.Errorf("workflow protocol %s, %s: forEachIterationSchemaRef is only valid for for_each", protocolID, stepPath)
			}
			if err := validateSchemaRef(protocolID, "outputSchemaRef", ref, topLevelStepIndex); err != nil {
				return err
			}
		}
		if ref := step.ForEachResultSchemaRef; ref != "" {
			if step.Type != "for_each" {
				return fmt.Errorf("workflow protocol %s, %s: forEachResultSchemaRef is only valid for for_each", protocolID, stepPath)
			}
			if err := validateSchemaRef(protocolID, "outputSchemaRef", ref, topLevelStepIndex); err != nil {
				return err
			}
		}

		switch step.Type {
		case "plan":
			if strings.TrimSpace(step.Prompt) == "" {
				return fmt.Errorf("workflow protocol %s, %s: plan requires prompt", protocolID, stepPath)
			}
			if strings.TrimSpace(step.OutputSchemaRef) == "" {
				return fmt.Errorf("workflow protocol %s, %s: plan requires outputSchemaRef", protocolID, stepPath)
			}
		case "for_each":
			if strings.TrimSpace(step.ItemsExpr) == "" {
				return fmt.Errorf("workflow protocol %s, %s: for_each requires itemsExpr", protocolID, stepPath)
			}
			if step.MaxIterations < 0 {
				return fmt.Errorf("workflow protocol %s, %s: maxIterations must be >= 0", protocolID, stepPath)
			}
			if len(step.Steps) == 0 {
				return fmt.Errorf("workflow protocol %s, %s: for_each requires nested steps", protocolID, stepPath)
			}
			for i, nested := range step.Steps {
				nestedPath := stepPath + "." + strconv.Itoa(i)
				if err := validateStep(protocolID, nested, nestedPath, topLevelStepIndex); err != nil {
					return err
				}
			}
		case "collect":
			if strings.TrimSpace(step.CollectFrom) == "" {
				return fmt.Errorf("workflow protocol %s, %s: collect requires collectFrom", protocolID, stepPath)
			}
			if mode := strings.TrimSpace(step.CollectMode); mode != "" {
				switch mode {
				case "result", "entry", "outputs", "item":
					// supported
				default:
					return fmt.Errorf("workflow protocol %s, %s: collectMode %q is invalid", protocolID, stepPath, mode)
				}
			}
		}

		return nil
	}

	for i, protocol := range w.Protocols {
		if strings.TrimSpace(protocol.ID) == "" {
			return fmt.Errorf("protocols[%d]: id must not be empty", i)
		}
		if err := validateWorkflowTrigger(protocol.ID, protocol.Trigger); err != nil {
			return err
		}
		if protocol.Retries < 0 {
			return fmt.Errorf("workflow protocol %s: retries must be >= 0", protocol.ID)
		}
		if ref := protocol.InputSchemaRef; ref != "" {
			if err := validateSchemaRef(protocol.ID, "inputSchemaRef", ref, -1); err != nil {
				return err
			}
		}
		for stepIndex, step := range protocol.Steps {
			if err := validateStep(protocol.ID, step, fmt.Sprintf("step %d", stepIndex), stepIndex); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateWorkflowTrigger(protocolID string, trigger WorkflowTrigger) error {
	triggerType := strings.TrimSpace(trigger.Type)
	if triggerType == "" {
		return fmt.Errorf("workflow protocol %s: trigger.type must not be empty", protocolID)
	}

	rawPrefix := trigger.Prefix
	prefix := strings.TrimSpace(trigger.Prefix)
	if rawPrefix != "" && strings.ContainsAny(rawPrefix, " \t\n\r") {
		return fmt.Errorf("workflow protocol %s: trigger.prefix %q must not contain whitespace", protocolID, trigger.Prefix)
	}

	switch triggerType {
	case "matrix.protocol_message":
		if prefix == "" {
			return fmt.Errorf("workflow protocol %s: trigger.prefix is required for trigger.type %q", protocolID, triggerType)
		}
	case "gateway.event":
		// Prefix is optional for gateway.event; when set it is matched against event type.
	default:
		return fmt.Errorf(
			"workflow protocol %s: trigger.type %q is invalid; valid values are %q and %q",
			protocolID,
			triggerType,
			"matrix.protocol_message",
			"gateway.event",
		)
	}

	return nil
}

func validateWorkflowSchemaObject(name string, schema interface{}) error {
	obj, ok := schema.(map[string]interface{})
	if !ok {
		return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
	}

	if typeValue, hasType := obj["type"]; hasType {
		if _, ok := typeValue.(string); !ok {
			return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
		}
	}

	if requiredValue, hasRequired := obj["required"]; hasRequired {
		requiredList, ok := requiredValue.([]interface{})
		if !ok {
			return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
		}
		seen := make(map[string]struct{}, len(requiredList))
		for _, raw := range requiredList {
			requiredField, ok := raw.(string)
			if !ok {
				return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
			}
			if _, dup := seen[requiredField]; dup {
				return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
			}
			seen[requiredField] = struct{}{}
		}
	}

	if propertiesValue, hasProperties := obj["properties"]; hasProperties {
		if _, ok := propertiesValue.(map[string]interface{}); !ok {
			return fmt.Errorf("workflow schema %s: invalid JSON Schema", name)
		}
	}

	return nil
}

func isExternalSchemaRef(ref string) bool {
	if strings.Contains(ref, "://") {
		return true
	}
	if strings.HasPrefix(ref, "#") {
		return true
	}
	if strings.ContainsAny(ref, `/\\`) {
		return true
	}
	return false
}

func validateLimits(l Limits) error {
	if l.MaxRequestsPerMinute < 0 {
		return fmt.Errorf("maxRequestsPerMinute must be >= 0")
	}
	if l.MaxTokensPerRequest < 0 {
		return fmt.Errorf("maxTokensPerRequest must be >= 0")
	}
	if l.MaxConcurrentRequests < 0 {
		return fmt.Errorf("maxConcurrentRequests must be >= 0")
	}
	if l.MaxMonthlyCostUSD < 0 {
		return fmt.Errorf("maxMonthlyCostUSD must be >= 0")
	}
	if l.MaxEventsPerMinute < 0 {
		return fmt.Errorf("maxEventsPerMinute must be >= 0")
	}
	return nil
}

func validateCapability(c Capability) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name must not be empty")
	}
	return nil
}

func validateMCPServer(m MCPServer) error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name must not be empty")
	}
	if strings.TrimSpace(m.Command) == "" {
		return fmt.Errorf("command must not be empty")
	}
	return nil
}

func validateGateway(g Gateway) error {
	if strings.TrimSpace(g.Name) == "" {
		return fmt.Errorf("name must not be empty")
	}

	hasType := strings.TrimSpace(g.Type) != ""
	hasCommand := strings.TrimSpace(g.Command) != ""

	switch {
	case hasType && hasCommand:
		return fmt.Errorf("type and command are mutually exclusive; set exactly one")
	case !hasType && !hasCommand:
		return fmt.Errorf("exactly one of type or command must be set")
	}

	if hasType {
		switch g.Type {
		case "cron":
			source := strings.TrimSpace(g.Config["source"])
			if source == "" {
				source = "static"
			}
			switch source {
			case "static":
				if strings.TrimSpace(g.Config["expression"]) == "" {
					return fmt.Errorf("type %q with source %q requires config.expression to be set", g.Type, source)
				}
			case "db":
				if strings.TrimSpace(g.Config["expression"]) != "" {
					if strings.TrimSpace(g.Config["target"]) == "" {
						return fmt.Errorf("type %q with source %q and bootstrap expression requires config.target", g.Type, source)
					}
					if strings.TrimSpace(g.Config["payload"]) == "" {
						return fmt.Errorf("type %q with source %q and bootstrap expression requires config.payload", g.Type, source)
					}
				}
			default:
				return fmt.Errorf("type %q has unknown config.source %q; valid values are \"static\" and \"db\"", g.Type, source)
			}
		case "webhook":
			if g.Config["authType"] == "hmac-sha256" {
				if strings.TrimSpace(g.Config["hmacSecretRef"]) == "" {
					return fmt.Errorf("type %q with authType hmac-sha256 requires config.hmacSecretRef to be set", g.Type)
				}
			}
		default:
			return fmt.Errorf("unknown built-in type %q; valid values are \"cron\" and \"webhook\"", g.Type)
		}
	}

	return nil
}

func validateInstructions(ins Instructions) error {
	for i, step := range ins.Workflow {
		if strings.TrimSpace(step.Trigger) == "" {
			return fmt.Errorf("workflow[%d]: trigger must not be empty", i)
		}
		if strings.TrimSpace(step.Action) == "" {
			return fmt.Errorf("workflow[%d]: action must not be empty", i)
		}
	}
	for i, peer := range ins.Context.Peers {
		if strings.TrimSpace(peer.Name) == "" {
			return fmt.Errorf("context.peers[%d]: name must not be empty", i)
		}
		if strings.TrimSpace(peer.Role) == "" {
			return fmt.Errorf("context.peers[%d]: role must not be empty", i)
		}
	}
	return nil
}

func validateMessaging(m Messaging) error {
	if m.MaxMessagesPerMinute < 0 {
		return fmt.Errorf("maxMessagesPerMinute must be >= 0")
	}
	aliases := make(map[string]struct{}, len(m.AllowedTargets))
	for i, target := range m.AllowedTargets {
		if strings.TrimSpace(target.RoomID) == "" {
			return fmt.Errorf("allowedTargets[%d]: roomId must not be empty", i)
		}
		if !strings.HasPrefix(target.RoomID, "!") {
			return fmt.Errorf("allowedTargets[%d]: roomId %q must start with '!'", i, target.RoomID)
		}
		if strings.TrimSpace(target.Alias) == "" {
			return fmt.Errorf("allowedTargets[%d]: alias must not be empty", i)
		}
		if strings.ContainsAny(target.Alias, " \t\n\r") {
			return fmt.Errorf("allowedTargets[%d]: alias %q must not contain whitespace", i, target.Alias)
		}
		if _, dup := aliases[target.Alias]; dup {
			return fmt.Errorf("allowedTargets[%d]: duplicate alias %q", i, target.Alias)
		}
		aliases[target.Alias] = struct{}{}
	}
	return nil
}

func validatePersona(p Persona) error {
	if p.Temperature != nil {
		if *p.Temperature < 0 || *p.Temperature > 2.0 {
			return fmt.Errorf("temperature %.2f is outside valid range [0.0, 2.0]", *p.Temperature)
		}
	}
	return nil
}
