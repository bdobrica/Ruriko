-- Migration 0006: Kuze one-time tokens for secure human secret entry.
--
-- Each row represents a pending link that a human can visit to submit a secret
-- value.  A token can be used at most once (used = 1) and is subject to a TTL
-- enforced via expires_at.  Expired and redeemed rows are pruned periodically.

CREATE TABLE IF NOT EXISTS kuze_tokens (
	token       TEXT    PRIMARY KEY,      -- base64url-encoded 32-byte random value
	secret_ref  TEXT    NOT NULL,         -- name of the secret to be stored
	secret_type TEXT    NOT NULL,         -- 'matrix_token', 'api_key', 'generic_json'
	created_at  TEXT    NOT NULL,         -- RFC-3339 UTC timestamp
	expires_at  TEXT    NOT NULL,         -- RFC-3339 UTC timestamp
	used        INTEGER NOT NULL DEFAULT 0 -- 0 = pending, 1 = redeemed / burned
);

CREATE INDEX IF NOT EXISTS idx_kuze_tokens_expires ON kuze_tokens(expires_at);
