-- Initial schema for Gitai agent runtime
-- Version: 0001
-- Description: Applied config, turn log, and approval cache

-- Tracks the currently applied Gosuto configuration (single row)
CREATE TABLE IF NOT EXISTS applied_config (
	id       INTEGER PRIMARY KEY CHECK (id = 1),
	gosuto_hash TEXT NOT NULL,
	yaml_blob   TEXT NOT NULL,
	applied_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Turn log: each conversation turn handled by this agent
CREATE TABLE IF NOT EXISTS turn_log (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	trace_id    TEXT NOT NULL,
	room_id     TEXT NOT NULL,
	sender_mxid TEXT NOT NULL,
	message     TEXT NOT NULL,
	tool_calls  INTEGER NOT NULL DEFAULT 0,
	result      TEXT,        -- 'success', 'error', 'approval_required'
	error_msg   TEXT,
	started_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	finished_at TIMESTAMP
);

CREATE INDEX idx_turn_log_trace  ON turn_log(trace_id);
CREATE INDEX idx_turn_log_room   ON turn_log(room_id);
CREATE INDEX idx_turn_log_sender ON turn_log(sender_mxid);

-- Approval cache: pending/decided approval requests posted by this agent
CREATE TABLE IF NOT EXISTS approvals (
	approval_id TEXT PRIMARY KEY,
	trace_id    TEXT NOT NULL,
	room_id     TEXT NOT NULL,             -- approvals room
	action      TEXT NOT NULL,             -- e.g. "mcp.call"
	target      TEXT NOT NULL,             -- tool name
	params_json TEXT,
	status      TEXT NOT NULL DEFAULT 'pending',  -- pending, approved, denied, expired
	requestor_mxid TEXT NOT NULL,
	expires_at  TIMESTAMP NOT NULL,
	created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	decided_at  TIMESTAMP,
	decided_by  TEXT,
	decision_reason TEXT
);

CREATE INDEX idx_approvals_status ON approvals(status);
CREATE INDEX idx_approvals_trace  ON approvals(trace_id);
