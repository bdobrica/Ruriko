-- DB-backed cron schedules for Saito-style deterministic scheduling (R19)
--
-- This table stores schedule rows managed via built-in tools and executed by
-- the built-in cron gateway runner.

CREATE TABLE IF NOT EXISTS cron_schedules (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	gateway_name    TEXT NOT NULL,
	cron_expression TEXT NOT NULL,
	tool            TEXT NOT NULL,
	payload_json    TEXT NOT NULL,
	enabled         INTEGER NOT NULL DEFAULT 1,
	last_trigger_at TIMESTAMP,
	next_trigger_at TIMESTAMP NOT NULL,
	is_bootstrap    INTEGER NOT NULL DEFAULT 0,
	created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_cron_schedules_due
	ON cron_schedules(gateway_name, enabled, next_trigger_at);

CREATE INDEX idx_cron_schedules_bootstrap
	ON cron_schedules(gateway_name, is_bootstrap);
