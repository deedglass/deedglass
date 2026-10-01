-- 0012_preauth.sql: the reads that happen before the org is known (S07).
--
-- A session cookie, a sign-in token and the email typed at sign-in all arrive before Deedglass
-- knows which org a request belongs to. Row-level security stays on for the runtime role; these
-- narrow SECURITY DEFINER functions are the only way across it. Each one:
--   - is owned by the schema owner (the migrator), whose tables have no FORCE ROW LEVEL SECURITY,
--   - pins search_path to pg_catalog, pg_temp and schema-qualifies everything,
--   - takes a hash or an id, never a pattern, and returns the fewest columns its caller needs,
--   - is executable by deedglass_app only (REVOKE ALL FROM PUBLIC, then GRANT EXECUTE).
-- Writes (marking a token used, creating a session) always happen afterwards in a normal org-scoped
-- transaction. store.PreAuth is the only Go caller (a test greps for these names).

-- The session behind a cookie (session middleware).
CREATE FUNCTION deedglass.preauth_session(p_token_hash bytea)
  RETURNS TABLE (id text, org_id text, user_id text, csrf_hash bytea, last_seen_at timestamptz,
                 idle_expires_at timestamptz, expires_at timestamptz, revoked_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT s.id, s.org_id, s.user_id, s.csrf_hash, s.last_seen_at, s.idle_expires_at, s.expires_at, s.revoked_at
  FROM deedglass.sessions AS s
  WHERE s.token_hash = p_token_hash AND pg_catalog.octet_length(p_token_hash) = 32
$fn$;

-- The token behind a sign-in or invite link (preview and callback).
CREATE FUNCTION deedglass.preauth_login_token(p_token_hash bytea)
  RETURNS TABLE (id text, org_id text, user_id text, membership_id text, purpose text,
                 expires_at timestamptz, used_at timestamptz, voided_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT t.id, t.org_id, t.user_id, t.membership_id, t.purpose, t.expires_at, t.used_at, t.voided_at
  FROM deedglass.login_tokens AS t
  WHERE t.token_hash = p_token_hash AND pg_catalog.octet_length(p_token_hash) = 32
$fn$;

-- The person behind an email address (the sign-in worker, code redemption, logout everywhere, the
-- org list in GET /v1/me): one row per membership that isn't removed, or one row with null
-- membership columns when there is none. The org's name and status come with each membership, so
-- a person can see the names of the orgs they belong to. tokens_last_hour is the global per-user
-- cap on new sign-in tokens (S07 §3.12).
CREATE FUNCTION deedglass.preauth_user_by_email(p_email text)
  RETURNS TABLE (user_id text, user_status text, org_id text, org_name text, org_status text, role text,
                 membership_status text, last_used_at timestamptz, joined_at timestamptz, tokens_last_hour integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT u.id, u.status, m.org_id, o.name, o.status, m.role, m.status, m.last_used_at, m.joined_at,
         (SELECT pg_catalog.count(*)::integer FROM deedglass.login_tokens AS t
          WHERE t.user_id = u.id AND t.created_at > pg_catalog.now() - interval '1 hour')
  FROM deedglass.users AS u
  LEFT JOIN deedglass.memberships AS m ON m.user_id = u.id AND m.status <> 'removed'
  LEFT JOIN deedglass.orgs AS o ON o.id = m.org_id
  WHERE u.email = p_email AND pg_catalog.length(p_email) BETWEEN 3 AND 254
$fn$;

-- The one sign-in token a typed code can match: the user's newest unused, unvoided, unexpired
-- login token that has a code.
CREATE FUNCTION deedglass.preauth_code_token(p_user_id text)
  RETURNS TABLE (id text, org_id text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT t.id, t.org_id
  FROM deedglass.login_tokens AS t
  WHERE t.user_id = p_user_id AND t.purpose = 'login' AND t.code_hash IS NOT NULL
    AND t.used_at IS NULL AND t.voided_at IS NULL AND t.expires_at > pg_catalog.now()
  ORDER BY t.created_at DESC, t.id DESC
  LIMIT 1
$fn$;

-- Retention for personal data (IP addresses, user agents): sign-in tokens 7 days after they
-- expire, sessions 30 days after they expire or are revoked. At most p_limit rows of each per call.
CREATE FUNCTION deedglass.maint_prune_identity(p_limit integer)
  RETURNS bigint
  LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH lt AS (
    DELETE FROM deedglass.login_tokens AS t
    WHERE t.id IN (SELECT x.id FROM deedglass.login_tokens AS x
                   WHERE x.expires_at < pg_catalog.now() - interval '7 days'
                   ORDER BY x.expires_at LIMIT LEAST(GREATEST(p_limit, 0), 100000))
    RETURNING 1
  ), se AS (
    DELETE FROM deedglass.sessions AS s
    WHERE s.id IN (SELECT x.id FROM deedglass.sessions AS x
                   WHERE x.expires_at < pg_catalog.now() - interval '30 days'
                      OR x.revoked_at < pg_catalog.now() - interval '30 days'
                   ORDER BY x.expires_at LIMIT LEAST(GREATEST(p_limit, 0), 100000))
    RETURNING 1
  )
  SELECT (SELECT pg_catalog.count(*) FROM lt) + (SELECT pg_catalog.count(*) FROM se)
$fn$;

REVOKE ALL ON FUNCTION deedglass.preauth_session(bytea), deedglass.preauth_login_token(bytea),
  deedglass.preauth_user_by_email(text), deedglass.preauth_code_token(text),
  deedglass.maint_prune_identity(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.preauth_session(bytea), deedglass.preauth_login_token(bytea),
  deedglass.preauth_user_by_email(text), deedglass.preauth_code_token(text),
  deedglass.maint_prune_identity(integer) TO deedglass_app;
