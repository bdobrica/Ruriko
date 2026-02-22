-- Add gateway audit metadata columns to turn_log (R12.7 Observability)
--
-- These three columns allow gateway-triggered turns to carry their full
-- context in the audit table so that queries can distinguish them from
-- ordinary Matrix-message turns without parsing the sender_mxid string.
--
-- All columns are nullable so existing rows (and future Matrix-message turns
-- that call LogTurn instead of LogGatewayTurn) continue to work unchanged.

ALTER TABLE turn_log ADD COLUMN trigger      TEXT;     -- 'matrix' | 'gateway' (NULL for legacy rows)
ALTER TABLE turn_log ADD COLUMN gateway_name TEXT;     -- e.g. "scheduler"   (populated for gateway turns only)
ALTER TABLE turn_log ADD COLUMN event_type   TEXT;     -- e.g. "cron.tick"   (populated for gateway turns only)
ALTER TABLE turn_log ADD COLUMN duration_ms  INTEGER;  -- wall-clock duration of the full turn in milliseconds
