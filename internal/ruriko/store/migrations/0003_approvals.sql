-- Migration 0003: Approval workflow tables
-- Description: Store approval requests and decisions for gated operations

CREATE TABLE IF NOT EXISTS approvals (
	id TEXT PRIMARY KEY,                      -- UUID
	action TEXT NOT NULL,                     -- e.g. "agents.delete", "secrets.delete"
	target TEXT NOT NULL,                     -- e.g. agent ID or secret name
	params_json TEXT NOT NULL DEFAULT '{}',   -- JSON-encoded args + flags for deferred execution
	requestor_mxid TEXT NOT NULL,             -- who requested the operation
	status TEXT NOT NULL DEFAULT 'pending',   -- pending | approved | denied | expired | cancelled
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	expires_at TIMESTAMP NOT NULL,
	resolved_at TIMESTAMP,
	resolved_by_mxid TEXT,
	resolve_reason TEXT
);

CREATE INDEX idx_approvals_status ON approvals(status);
CREATE INDEX idx_approvals_requestor ON approvals(requestor_mxid);
CREATE INDEX idx_approvals_action_target ON approvals(action, target);
