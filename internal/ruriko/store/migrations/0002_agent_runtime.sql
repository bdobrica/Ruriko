-- Migration 0002: Add container runtime fields to agents table
-- Description: Store Docker container_id, control_url, and image per agent

ALTER TABLE agents ADD COLUMN container_id TEXT;
ALTER TABLE agents ADD COLUMN control_url TEXT;
ALTER TABLE agents ADD COLUMN image TEXT;

CREATE INDEX IF NOT EXISTS idx_agents_container_id ON agents(container_id);
