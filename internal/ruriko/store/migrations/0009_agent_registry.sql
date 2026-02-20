-- Migration 0009: Agent registry fields for drift detection (R5.3)
-- Description: Track desired vs actual Gosuto hash, enabled flag, and last
-- health-check timestamp.  These fields are used by the reconciler to detect
-- configuration drift (desired != actual) and stale health checks.
--
-- Field semantics:
--   desired_gosuto_hash  -- SHA-256 of the Gosuto version last *pushed* to the agent
--                           (set by HandleGosutoPush / provisioning pipeline).
--                           NULL means no Gosuto has been pushed yet.
--   actual_gosuto_hash   -- SHA-256 the agent reported via ACP GET /status.
--                           Updated by the reconciler on each pass.
--                           NULL means the agent has never responded to a status query.
--   enabled              -- 1 = agent is active; 0 = agent is administratively disabled.
--                           Disabled agents are skipped by the reconciler.
--   last_health_check    -- Timestamp of the last *successful* ACP GET /health response.
--                           NULL means the agent has never responded to a health check.

ALTER TABLE agents ADD COLUMN desired_gosuto_hash TEXT;
ALTER TABLE agents ADD COLUMN actual_gosuto_hash TEXT;
ALTER TABLE agents ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE agents ADD COLUMN last_health_check TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_agents_enabled      ON agents(enabled);
CREATE INDEX IF NOT EXISTS idx_agents_desired_hash ON agents(desired_gosuto_hash);
CREATE INDEX IF NOT EXISTS idx_agents_actual_hash  ON agents(actual_gosuto_hash);
