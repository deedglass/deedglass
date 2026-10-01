-- 0013_identity_agents.sql: what agents, API keys and Stop need beyond S02's tables (S08).
--
-- S02 created agents, api_keys and the orgs stop columns with S08's names, CHECKs and grants
-- (names have no UPDATE grant, so they can't change; admin keys carry exactly {admin}). This file
-- adds only the real gaps:
--   1. api_keys_agent_live: the active keys of one agent (the 10-key limit and the key list).
--   2. admin_events.actor_type gains 'api_key': an org admin key is the actor of what it changes.
--   3. deedglass.preauth_api_key: the one lookup that happens before the org is known (a bearer
--      key arrives with no org), following 0012's pattern.

-- 1. Active keys per agent ------------------------------------------------------------------------
CREATE INDEX api_keys_agent_live ON deedglass.api_keys (org_id, agent_id) WHERE revoked_at IS NULL;

-- 2. Admin keys as actors ---------------------------------------------------------------------------
ALTER TABLE deedglass.admin_events DROP CONSTRAINT admin_events_actor_type_check;
ALTER TABLE deedglass.admin_events ADD CONSTRAINT admin_events_actor_type_check
  CHECK (actor_type IN ('user', 'agent', 'api_key', 'system'));

-- 3. The key behind a bearer token ------------------------------------------------------------------
-- Takes the 8-character public prefix, never the secret: the caller compares sha256(pepper ‖ secret)
-- with secret_hash in constant time. Returns the key, its agent's name and status, and its org's
-- name, stop overlay and pinned API version, in one round trip. The hash leaves the database only
-- to be compared; it is never logged or returned to a client.
CREATE FUNCTION deedglass.preauth_api_key(p_prefix text)
  RETURNS TABLE (id text, org_id text, agent_id text, mode text, secret_hash bytea, scopes text[],
                 expires_at timestamptz, revoked_at timestamptz, agent_name text, agent_status text,
                 org_name text, org_status text, org_stopped_at timestamptz, org_api_version text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT k.id, k.org_id, k.agent_id, k.mode, k.secret_hash, k.scopes, k.expires_at, k.revoked_at,
         a.name, a.status, o.name, o.status, o.stopped_at, o.api_version
  FROM deedglass.api_keys AS k
  JOIN deedglass.orgs AS o ON o.id = k.org_id
  LEFT JOIN deedglass.agents AS a ON a.org_id = k.org_id AND a.id = k.agent_id
  WHERE k.prefix = p_prefix AND p_prefix ~ '^[0-9A-Za-z]{8}$'
$fn$;

REVOKE ALL ON FUNCTION deedglass.preauth_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.preauth_api_key(text) TO deedglass_app;
