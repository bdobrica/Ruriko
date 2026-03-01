// Package acp defines the shared Agent Control Protocol (ACP) wire schema.
//
// These DTOs are used by both sides of the ACP boundary:
//   - Gitai ACP HTTP server (internal/gitai/control)
//   - Ruriko ACP HTTP client (internal/ruriko/runtime/acp)
//
// Keeping the request/response models in a common package prevents schema
// drift and keeps JSON contracts consistent.
package acp

import "time"

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status  string `json:"status"`
	AgentID string `json:"agent_id"`
}

// StatusResponse is returned by GET /status.
type StatusResponse struct {
	AgentID    string    `json:"agent_id"`
	Version    string    `json:"version"`
	GosutoHash string    `json:"gosuto_hash"`
	Uptime     float64   `json:"uptime_seconds"`
	StartedAt  time.Time `json:"started_at"`
	MCPs       []string  `json:"mcps"`
	// Gateways lists supervised gateway names (optional).
	Gateways []string `json:"gateways,omitempty"`
	// MessagesOutbound is the number of successful matrix.send_message calls
	// since process start.
	MessagesOutbound int64 `json:"messages_outbound,omitempty"`
}

// ConfigApplyRequest is the body for POST /config/apply.
type ConfigApplyRequest struct {
	YAML string `json:"yaml"`
	Hash string `json:"hash"`
}

// SecretsApplyRequest is the body for POST /secrets/apply.
type SecretsApplyRequest struct {
	Secrets map[string]string `json:"secrets"`
}

// SecretLease is one token-based secret lease delivered by Ruriko.
type SecretLease struct {
	SecretRef       string `json:"secret_ref"`
	RedemptionToken string `json:"redemption_token"`
	KuzeURL         string `json:"kuze_url"`
}

// SecretsTokenRequest is the body for POST /secrets/token.
type SecretsTokenRequest struct {
	Leases []SecretLease `json:"leases"`
}

// ApprovalDecisionRequest is the body for POST /approvals/decision.
type ApprovalDecisionRequest struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"` // approve|deny
	DecidedBy  string `json:"decided_by,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ErrorResponse is returned by ACP endpoints on errors.
type ErrorResponse struct {
	Error string `json:"error"`
}
