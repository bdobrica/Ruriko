-- Migration 0008: Add provisioning_state to agents table
-- Description: Track the fine-grained state of the automated provisioning
-- pipeline introduced in R5.2 (container start → ACP health → Gosuto apply →
-- secrets push → healthy).
--
-- Possible values (empty string = legacy agent predating this migration):
--   'pending'     -- DB record created, nothing else started yet
--   'creating'    -- container spawn in progress
--   'configuring' -- container running, pushing Gosuto / secrets
--   'healthy'     -- full pipeline completed successfully
--   'error'       -- one or more pipeline steps failed

ALTER TABLE agents ADD COLUMN provisioning_state TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_agents_provisioning_state ON agents(provisioning_state);
