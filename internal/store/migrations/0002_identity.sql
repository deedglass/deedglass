-- 0002_identity.sql: orgs, users, memberships, sessions, login tokens, agents, API keys, intent nonces.
--
-- RLS keys on the transaction-local setting deedglass.org_id (store.OrgTx). With no org set the
-- policy compares with NULL: reads return no rows and writes fail with 42501. system_scope (the
-- setting deedglass.scope = 'system', store.SystemTx) is on the tables that need cross-org paths.
-- Every foreign key between org tables includes org_id, so a row can never point into another org.

-- orgs (global: the org is the boundary itself) ---------------------------------------------------
CREATE TABLE deedglass.orgs (
  id deedglass.org_id PRIMARY KEY,
  name text NOT NULL CHECK (pg_catalog.length(name) BETWEEN 1 AND 100),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$'),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  api_version text NOT NULL DEFAULT '2026-10-01' CHECK (api_version ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'),
  timezone text NOT NULL DEFAULT 'UTC' CHECK (pg_catalog.length(timezone) BETWEEN 1 AND 64),
  retention_days integer CHECK (retention_days BETWEEN 1 AND 3650),
  stopped_at timestamptz,
  stopped_by text CHECK (pg_catalog.length(stopped_by) <= 64),
  stop_reason text CHECK (pg_catalog.length(stop_reason) <= 500),
  settings jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(settings) = 'object'),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now()
);
CREATE TRIGGER orgs_touch BEFORE UPDATE ON deedglass.orgs FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.orgs ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_self ON deedglass.orgs FOR ALL TO deedglass_app
  USING      (id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.orgs FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.orgs FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.orgs TO deedglass_app;
GRANT UPDATE (name, slug, status, api_version, timezone, retention_days, stopped_at, stopped_by, stop_reason, settings, updated_at)
  ON deedglass.orgs TO deedglass_app;

-- users (global: one person, memberships in several orgs) ------------------------------------------
CREATE TABLE deedglass.users (
  id deedglass.usr_id PRIMARY KEY,
  email text NOT NULL UNIQUE CHECK (email = pg_catalog.lower(email) AND pg_catalog.length(email) BETWEEN 3 AND 254
    AND email ~ '^[^@[:space:]]+@[^@[:space:]]+$'),
  display_name text CHECK (pg_catalog.length(display_name) BETWEEN 1 AND 80),
  status text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'active', 'disabled')),
  email_verified_at timestamptz,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now()
);
CREATE TRIGGER users_touch BEFORE UPDATE ON deedglass.users FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();

-- memberships ---------------------------------------------------------------------------------------
CREATE TABLE deedglass.memberships (
  id deedglass.mem_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  user_id deedglass.usr_id NOT NULL REFERENCES deedglass.users (id),
  role text NOT NULL CHECK (role IN ('owner', 'admin', 'viewer')),
  status text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'active', 'removed')),
  invited_by deedglass.usr_id REFERENCES deedglass.users (id),
  invited_at timestamptz,
  joined_at timestamptz,
  removed_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (org_id, user_id),
  CHECK (status <> 'removed' OR removed_at IS NOT NULL)
);
CREATE INDEX memberships_user ON deedglass.memberships (user_id);
CREATE TRIGGER memberships_touch BEFORE UPDATE ON deedglass.memberships FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();

-- An org always keeps at least one active owner. Checked at commit, so a transaction may hand
-- ownership over in any order. SECURITY DEFINER: the check must see the whole org, whatever the
-- caller's RLS setting; it returns nothing and takes no input but the row.
CREATE FUNCTION deedglass.memberships_keep_owner() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM deedglass.memberships AS m
                 WHERE m.org_id = OLD.org_id AND m.role = 'owner' AND m.status = 'active')
     AND EXISTS (SELECT 1 FROM deedglass.orgs AS o WHERE o.id = OLD.org_id) THEN
    RAISE EXCEPTION USING ERRCODE = 'DG005',
      MESSAGE = pg_catalog.format('deedglass: org %s must keep at least one active owner', OLD.org_id),
      HINT = 'Make another member an owner first, then change or remove this one.';
  END IF;
  RETURN NULL;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.memberships_keep_owner() FROM PUBLIC;
CREATE CONSTRAINT TRIGGER memberships_keep_owner AFTER UPDATE OR DELETE ON deedglass.memberships
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.role = 'owner' AND OLD.status = 'active')
  EXECUTE FUNCTION deedglass.memberships_keep_owner();

ALTER TABLE deedglass.memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.memberships FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.memberships FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.memberships FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.memberships TO deedglass_app;

-- users' RLS reads memberships, so it is created after them. memberships' own RLS limits m to the
-- current org: a user row is visible when that user is a member of the current org.
ALTER TABLE deedglass.users ENABLE ROW LEVEL SECURITY;
CREATE POLICY member_read ON deedglass.users FOR SELECT TO deedglass_app
  USING (EXISTS (SELECT 1 FROM deedglass.memberships AS m WHERE m.user_id = users.id));
CREATE POLICY member_update ON deedglass.users FOR UPDATE TO deedglass_app
  USING      (EXISTS (SELECT 1 FROM deedglass.memberships AS m WHERE m.user_id = users.id))
  WITH CHECK (EXISTS (SELECT 1 FROM deedglass.memberships AS m WHERE m.user_id = users.id));
CREATE POLICY system_scope ON deedglass.users FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.users FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.users TO deedglass_app;
GRANT UPDATE (email, display_name, status, email_verified_at, last_login_at, updated_at) ON deedglass.users TO deedglass_app;

-- sessions: org_id is the session's active org ------------------------------------------------------
CREATE TABLE deedglass.sessions (
  id deedglass.ses_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  user_id deedglass.usr_id NOT NULL REFERENCES deedglass.users (id),
  token_hash bytea NOT NULL UNIQUE CHECK (pg_catalog.octet_length(token_hash) = 32),
  csrf_hash bytea NOT NULL CHECK (pg_catalog.octet_length(csrf_hash) = 32),
  idle_expires_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  last_seen_at timestamptz,
  revoked_at timestamptz,
  revoked_reason text CHECK (revoked_reason IN ('logout', 'logout_everywhere', 'org_switch', 'member_removed')),
  ip inet,
  user_agent text CHECK (pg_catalog.length(user_agent) <= 512),
  rotated_from deedglass.ses_id,   -- may name a session in another org (org switch), so no foreign key
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);
CREATE INDEX sessions_user ON deedglass.sessions (org_id, user_id);
CREATE INDEX sessions_expiry ON deedglass.sessions (expires_at);
ALTER TABLE deedglass.sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.sessions FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.sessions FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.sessions FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.sessions TO deedglass_app;

-- login_tokens: org_id is the org the token signs into; no email column ----------------------------
CREATE TABLE deedglass.login_tokens (
  id deedglass.ltk_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  user_id deedglass.usr_id NOT NULL REFERENCES deedglass.users (id),
  membership_id deedglass.mem_id,
  purpose text NOT NULL CHECK (purpose IN ('login', 'invite')),
  token_hash bytea NOT NULL UNIQUE CHECK (pg_catalog.octet_length(token_hash) = 32),
  code_hash bytea CHECK (pg_catalog.octet_length(code_hash) = 32),
  code_attempts integer NOT NULL DEFAULT 0 CHECK (code_attempts BETWEEN 0 AND 5),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  voided_at timestamptz,
  requested_ip inet,
  requested_ua text CHECK (pg_catalog.length(requested_ua) <= 512),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, membership_id) REFERENCES deedglass.memberships (org_id, id),
  CHECK (purpose <> 'invite' OR membership_id IS NOT NULL)
);
CREATE INDEX login_tokens_expiry ON deedglass.login_tokens (expires_at);
ALTER TABLE deedglass.login_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.login_tokens FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.login_tokens FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.login_tokens TO deedglass_app;

-- agents -------------------------------------------------------------------------------------------
CREATE TABLE deedglass.agents (
  id deedglass.agt_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$'),   -- immutable: no UPDATE grant on it
  description text CHECK (pg_catalog.length(description) <= 500),
  owner_user_id deedglass.usr_id REFERENCES deedglass.users (id),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'stopped')),
  status_reason text CHECK (pg_catalog.length(status_reason) <= 500),
  status_changed_at timestamptz,
  status_changed_by text CHECK (pg_catalog.length(status_changed_by) <= 64),
  labels jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(labels) = 'object'),
  budgets jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(budgets) = 'object'),
  intent_mode text NOT NULL DEFAULT 'off' CHECK (intent_mode IN ('off', 'optional', 'required')),
  intent_keys jsonb NOT NULL DEFAULT '[]' CHECK (pg_catalog.jsonb_typeof(intent_keys) = 'array'
    AND pg_catalog.jsonb_array_length(intent_keys) <= 2),
  created_by text CHECK (pg_catalog.length(created_by) <= 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (org_id, name)
);
CREATE INDEX agents_org_time ON deedglass.agents (org_id, created_at DESC, id DESC);
CREATE INDEX agents_labels ON deedglass.agents USING gin (labels);
CREATE TRIGGER agents_touch BEFORE UPDATE ON deedglass.agents FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.agents ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.agents FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.agents FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.agents FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.agents TO deedglass_app;
GRANT UPDATE (description, owner_user_id, status, status_reason, status_changed_at, status_changed_by, labels, budgets,
  intent_mode, intent_keys, updated_at) ON deedglass.agents TO deedglass_app;

-- api_keys: agent_id NULL = an org admin key --------------------------------------------------------
CREATE TABLE deedglass.api_keys (
  id deedglass.key_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  agent_id deedglass.agt_id,
  name text CHECK (pg_catalog.length(name) BETWEEN 1 AND 100),
  mode text NOT NULL CHECK (mode IN ('live', 'test')),
  prefix text NOT NULL UNIQUE CHECK (prefix ~ '^[0-9A-Za-z]{8}$'),   -- globally unique: the lookup key
  secret_hash bytea NOT NULL CHECK (pg_catalog.octet_length(secret_hash) = 32),
  scopes text[] NOT NULL CHECK (pg_catalog.cardinality(scopes) >= 1
    AND scopes <@ ARRAY['actions:execute', 'actions:read', 'deeds:read', 'actions:undo', 'admin']::text[]),
  expires_at timestamptz,
  revoked_at timestamptz,
  revoked_by text CHECK (pg_catalog.length(revoked_by) <= 64),
  revoke_reason text CHECK (pg_catalog.length(revoke_reason) <= 500),
  rolled_from deedglass.key_id,
  last_used_at timestamptz,
  created_by text CHECK (pg_catalog.length(created_by) <= 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, agent_id) REFERENCES deedglass.agents (org_id, id),
  FOREIGN KEY (org_id, rolled_from) REFERENCES deedglass.api_keys (org_id, id),
  -- Admin keys carry exactly {admin}; agent keys never carry admin (S08).
  CHECK ((agent_id IS NULL AND scopes = ARRAY['admin']::text[])
      OR (agent_id IS NOT NULL AND NOT ('admin' = ANY (scopes))))
);
CREATE INDEX api_keys_agent ON deedglass.api_keys (org_id, agent_id);
ALTER TABLE deedglass.api_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.api_keys FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.api_keys FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.api_keys FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.api_keys TO deedglass_app;
GRANT UPDATE (name, expires_at, revoked_at, revoked_by, revoke_reason, last_used_at) ON deedglass.api_keys TO deedglass_app;

-- intent_nonces: replay memory for signed intents (S09) ---------------------------------------------
CREATE TABLE deedglass.intent_nonces (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  agent_id deedglass.agt_id NOT NULL,
  nonce text NOT NULL CHECK (nonce ~ '^[0-9a-f]{32}$'),
  intent_hash deedglass.digest NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (agent_id, nonce),
  FOREIGN KEY (org_id, agent_id) REFERENCES deedglass.agents (org_id, id)
);
CREATE INDEX intent_nonces_expiry ON deedglass.intent_nonces (expires_at);
ALTER TABLE deedglass.intent_nonces ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.intent_nonces FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.intent_nonces FROM PUBLIC;
GRANT SELECT, INSERT, DELETE ON deedglass.intent_nonces TO deedglass_app;
