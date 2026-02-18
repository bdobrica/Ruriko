package gosuto

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse decodes a Gosuto YAML document into a Config struct and validates it.
// It is the canonical entry point for loading Gosuto configurations.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
	seen := make(map[string]struct{}, len(cfg.MCPs))
	for i, mcp := range cfg.MCPs {
		if err := validateMCPServer(mcp); err != nil {
			return fmt.Errorf("mcps[%d] (%q): %w", i, mcp.Name, err)
		}
		if _, dup := seen[mcp.Name]; dup {
			return fmt.Errorf("mcps[%d]: duplicate name %q", i, mcp.Name)
		}
		seen[mcp.Name] = struct{}{}
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

	return nil
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
	return nil
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

func validatePersona(p Persona) error {
	if p.Temperature < 0 || p.Temperature > 2.0 {
		return fmt.Errorf("temperature %.2f is outside valid range [0.0, 2.0]", p.Temperature)
	}
	return nil
}
