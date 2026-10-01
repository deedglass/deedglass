-- 0014_identity_intents.sql: what signed intents need beyond S02's tables (S09).
--
-- S02 created agents.intent_mode (off|optional|required), agents.intent_keys (a JSON array of at
-- most 2) and deedglass.intent_nonces (primary key (agent_id, nonce), RLS on org_id, the expiry
-- index) with S09's names. This file adds only the real gaps:
--   1. The shape of each intent_keys element: {kid, public_key, created_at, created_by}, with kid
--      32 lowercase hex and public_key "ed25519:" + 43 base64url characters (32 bytes).
--   2. deedglass.maint_prune_intent_nonces: deletes nonces whose intent can no longer be accepted.
--   3. deedglass.preauth_api_key also returns the agent's intent_mode and intent_keys, so the
--      principal cache (S08) carries them and the intent check costs no extra round trip.

-- 1. Intent key shape -------------------------------------------------------------------------------
CREATE FUNCTION deedglass.intent_keys_valid(p_keys jsonb)
  RETURNS boolean
  LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT pg_catalog.jsonb_typeof(p_keys) = 'array'
     AND pg_catalog.jsonb_array_length(p_keys) <= 2
     AND NOT EXISTS (
       SELECT 1 FROM pg_catalog.jsonb_array_elements(p_keys) AS e(k)
       WHERE pg_catalog.jsonb_typeof(e.k) <> 'object'
          OR NOT (e.k ?& ARRAY['kid', 'public_key', 'created_at', 'created_by'])
          OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(e.k)) <> 4
          OR pg_catalog.jsonb_typeof(e.k -> 'kid') <> 'string'
          OR (e.k ->> 'kid') !~ '^[0-9a-f]{32}$'
          OR pg_catalog.jsonb_typeof(e.k -> 'public_key') <> 'string'
          OR (e.k ->> 'public_key') !~ '^ed25519:[A-Za-z0-9_-]{43}$'
          OR pg_catalog.jsonb_typeof(e.k -> 'created_at') <> 'string'
          OR pg_catalog.jsonb_typeof(e.k -> 'created_by') <> 'string')
     AND (SELECT pg_catalog.count(DISTINCT e.k ->> 'kid') FROM pg_catalog.jsonb_array_elements(p_keys) AS e(k))
         = pg_catalog.jsonb_array_length(p_keys)
$fn$;
REVOKE ALL ON FUNCTION deedglass.intent_keys_valid(jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.intent_keys_valid(jsonb) TO deedglass_app;

ALTER TABLE deedglass.agents ADD CONSTRAINT agents_intent_keys_shape CHECK (deedglass.intent_keys_valid(intent_keys));

-- 2. Nonce pruning ----------------------------------------------------------------------------------
-- An intent is accepted until exp + 60 s of clock skew. A nonce row is kept until exp + 2 minutes,
-- so a pruned nonce's intent is always refused as expired before its nonce is looked at.
CREATE FUNCTION deedglass.maint_prune_intent_nonces(p_limit integer)
  RETURNS bigint
  LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH gone AS (
    DELETE FROM deedglass.intent_nonces AS n
    WHERE (n.agent_id, n.nonce) IN (
      SELECT x.agent_id, x.nonce FROM deedglass.intent_nonces AS x
      WHERE x.expires_at < pg_catalog.now() - interval '2 minutes'
      ORDER BY x.expires_at LIMIT LEAST(GREATEST(p_limit, 0), 100000))
    RETURNING 1
  )
  SELECT pg_catalog.count(*) FROM gone
$fn$;
REVOKE ALL ON FUNCTION deedglass.maint_prune_intent_nonces(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.maint_prune_intent_nonces(integer) TO deedglass_app;

-- 3. The key behind a bearer token, now with the agent's intent settings -----------------------------
-- The same contract as 0013's version (the prefix, never the secret), plus intent_mode and
-- intent_keys. A function's result type can't change in place, so it is dropped and created again in
-- this transaction; callers see one or the other, never neither.
DROP FUNCTION deedglass.preauth_api_key(text);
CREATE FUNCTION deedglass.preauth_api_key(p_prefix text)
  RETURNS TABLE (id text, org_id text, agent_id text, mode text, secret_hash bytea, scopes text[],
                 expires_at timestamptz, revoked_at timestamptz, agent_name text, agent_status text,
                 org_name text, org_status text, org_stopped_at timestamptz, org_api_version text,
                 agent_intent_mode text, agent_intent_keys jsonb)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT k.id, k.org_id, k.agent_id, k.mode, k.secret_hash, k.scopes, k.expires_at, k.revoked_at,
         a.name, a.status, o.name, o.status, o.stopped_at, o.api_version, a.intent_mode, a.intent_keys
  FROM deedglass.api_keys AS k
  JOIN deedglass.orgs AS o ON o.id = k.org_id
  LEFT JOIN deedglass.agents AS a ON a.org_id = k.org_id AND a.id = k.agent_id
  WHERE k.prefix = p_prefix AND p_prefix ~ '^[0-9A-Za-z]{8}$'
$fn$;
REVOKE ALL ON FUNCTION deedglass.preauth_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.preauth_api_key(text) TO deedglass_app;
