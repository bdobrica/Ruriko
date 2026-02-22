-- Migration 0011: Runtime configuration store
-- Description: Key/value table for non-secret operator-tunable knobs such as
-- NLP model, endpoint, and rate-limit.  Intentionally separate from the
-- secrets table so that non-credential config does not blur the security
-- boundary enforced by Kuze.

CREATE TABLE IF NOT EXISTS config (
    key        TEXT     PRIMARY KEY,
    value      TEXT     NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
