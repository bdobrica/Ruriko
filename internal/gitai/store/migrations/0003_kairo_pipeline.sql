-- Kairo analysis pipeline persistence (R6.2)
--
-- Stores the user-provided portfolio configuration and structured analysis
-- outputs for each triggered run.

CREATE TABLE IF NOT EXISTS kairo_portfolio_config (
	id             INTEGER PRIMARY KEY CHECK (id = 1),
	portfolio_json TEXT NOT NULL,
	updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS kairo_analysis_runs (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	trace_id       TEXT NOT NULL,
	trigger_source TEXT NOT NULL,
	room_id        TEXT NOT NULL,
	status         TEXT NOT NULL,
	summary        TEXT,
	commentary     TEXT,
	created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_kairo_analysis_runs_trace ON kairo_analysis_runs(trace_id);
CREATE INDEX idx_kairo_analysis_runs_created_at ON kairo_analysis_runs(created_at);

CREATE TABLE IF NOT EXISTS kairo_analysis_tickers (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id           INTEGER NOT NULL,
	ticker           TEXT NOT NULL,
	allocation       REAL NOT NULL,
	price            REAL,
	change_percent   REAL,
	open             REAL,
	high             REAL,
	low              REAL,
	previous_close   REAL,
	metrics_json     TEXT,
	commentary       TEXT,
	created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY(run_id) REFERENCES kairo_analysis_runs(id) ON DELETE CASCADE
);

CREATE INDEX idx_kairo_analysis_tickers_run_id ON kairo_analysis_tickers(run_id);
CREATE INDEX idx_kairo_analysis_tickers_ticker ON kairo_analysis_tickers(ticker);
