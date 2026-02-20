-- Migration 0007: Extend kuze_tokens to support agent redemption tokens.
--
-- Two nullable columns are added to kuze_tokens:
--   agent_id  — when set, the token may only be redeemed by that agent (matched
--               against the X-Agent-ID header on GET /kuze/redeem/<token>).
--               NULL means the token is a human one-time-link (existing behaviour).
--   purpose   — optional free-form label for audit / diagnostics
--               (e.g. "finnhub_api_key for warren initial provisioning").
--               NULL for tokens issued without an explicit purpose.
--
-- Human tokens (agent_id IS NULL) keep their existing semantics; the new columns
-- have no effect on the GET /s/<token> HTML-form flow.

ALTER TABLE kuze_tokens ADD COLUMN agent_id TEXT;
ALTER TABLE kuze_tokens ADD COLUMN purpose  TEXT;

-- Covering index: quickly find all pending tokens for a given agent.
CREATE INDEX IF NOT EXISTS idx_kuze_tokens_agent_id ON kuze_tokens(agent_id) WHERE agent_id IS NOT NULL;
