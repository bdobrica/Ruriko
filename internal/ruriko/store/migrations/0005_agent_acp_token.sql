-- Migration 0005: Add acp_token column to agents table.
--
-- Ruriko generates a per-agent bearer token at provisioning time, stores it
-- here, and passes it to both the Docker runtime (as GITAI_ACP_TOKEN) and
-- every outgoing ACP client call so that the agent can authenticate all
-- control-plane requests.

ALTER TABLE agents ADD COLUMN acp_token TEXT;
