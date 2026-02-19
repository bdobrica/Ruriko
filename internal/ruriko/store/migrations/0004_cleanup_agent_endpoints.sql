-- Migration 0004: Remove unused agent_endpoints table
-- Description: Drop agent_endpoints which was superseded by container_id / control_url columns on agents

-- agent_endpoints was originally intended to track multiple endpoints per agent
-- (HTTP and Matrix room). In practice the ACP control URL is stored directly on
-- the agents row (added in migration 0002). The table has no Go references and
-- can be safely removed.

DROP TABLE IF EXISTS agent_endpoints;
