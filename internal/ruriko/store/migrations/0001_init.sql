-- Initial schema for Ruriko control plane
-- Version: 0001
-- Description: Create core tables for agents, secrets, gosuto, and audit

-- Track schema migrations
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	description TEXT NOT NULL
);

-- Agents table: tracks all managed agents
CREATE TABLE IF NOT EXISTS agents (
	id TEXT PRIMARY KEY,
	mxid TEXT UNIQUE,
	display_name TEXT NOT NULL,
	template TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'stopped', -- stopped, starting, running, error
	last_seen TIMESTAMP,
	runtime_version TEXT,
	gosuto_version INTEGER,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_agents_status ON agents(status);
CREATE INDEX idx_agents_template ON agents(template);

-- Agent endpoints: how to reach agents (HTTP control API, rooms)
CREATE TABLE IF NOT EXISTS agent_endpoints (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id TEXT NOT NULL,
	endpoint_type TEXT NOT NULL, -- 'http', 'matrix_room'
	endpoint_value TEXT NOT NULL, -- URL or room_id
	pubkey TEXT, -- optional: for mTLS
	last_heartbeat TIMESTAMP,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);

CREATE INDEX idx_agent_endpoints_agent ON agent_endpoints(agent_id);

-- Secrets: encrypted credentials
CREATE TABLE IF NOT EXISTS secrets (
	name TEXT PRIMARY KEY,
	type TEXT NOT NULL, -- 'matrix_token', 'api_key', 'generic_json'
	encrypted_blob BLOB NOT NULL,
	rotation_version INTEGER NOT NULL DEFAULT 1,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Agent secret bindings: which agents have access to which secrets
CREATE TABLE IF NOT EXISTS agent_secret_bindings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id TEXT NOT NULL,
	secret_name TEXT NOT NULL,
	scope TEXT, -- optional: scope restriction within secret
	last_pushed_version INTEGER,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
	FOREIGN KEY (secret_name) REFERENCES secrets(name) ON DELETE CASCADE,
	UNIQUE(agent_id, secret_name)
);

CREATE INDEX idx_agent_secret_bindings_agent ON agent_secret_bindings(agent_id);
CREATE INDEX idx_agent_secret_bindings_secret ON agent_secret_bindings(secret_name);

-- Gosuto versions: versioned agent configurations
CREATE TABLE IF NOT EXISTS gosuto_versions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	hash TEXT NOT NULL, -- SHA-256 of yaml_blob
	yaml_blob TEXT NOT NULL,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	created_by_mxid TEXT NOT NULL,
	FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
	UNIQUE(agent_id, version)
);

CREATE INDEX idx_gosuto_versions_agent ON gosuto_versions(agent_id);
CREATE INDEX idx_gosuto_versions_hash ON gosuto_versions(hash);

-- Audit log: immutable record of all actions
CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	trace_id TEXT NOT NULL,
	actor_mxid TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT, -- agent_id, secret_name, or other resource
	payload_json TEXT, -- additional context
	result TEXT NOT NULL, -- 'success', 'error', 'denied'
	error_message TEXT
);

CREATE INDEX idx_audit_log_ts ON audit_log(ts DESC);
CREATE INDEX idx_audit_log_trace ON audit_log(trace_id);
CREATE INDEX idx_audit_log_actor ON audit_log(actor_mxid);
CREATE INDEX idx_audit_log_action ON audit_log(action);
CREATE INDEX idx_audit_log_target ON audit_log(target);
