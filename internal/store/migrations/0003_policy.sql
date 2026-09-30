-- 0003_policy.sql: policies, immutable policy versions, activations, usage counters.

-- policies -----------------------------------------------------------------------------------------
CREATE TABLE deedglass.policies (
  id deedglass.pol_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,99}$'),
  description text CHECK (pg_catalog.length(description) <= 500),
  created_by text CHECK (pg_catalog.length(created_by) <= 64),
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (org_id, name)
);
CREATE TRIGGER policies_touch BEFORE UPDATE ON deedglass.policies FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.policies ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.policies FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.policies FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON deedglass.policies TO deedglass_app;

-- policy_versions: append-only ---------------------------------------------------------------------
CREATE TABLE deedglass.policy_versions (
  id deedglass.pv_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  policy_id deedglass.pol_id NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  yaml text NOT NULL CHECK (pg_catalog.octet_length(yaml) <= 262144),
  compiled_hash deedglass.digest NOT NULL,
  source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'suggestion', 'starter')),
  suggestion_id deedglass.sug_id,                   -- foreign key added in 0008 (suggestions)
  author_user_id deedglass.usr_id REFERENCES deedglass.users (id),
  notes text CHECK (pg_catalog.length(notes) <= 2000),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (org_id, policy_id, id),                   -- lets an activation name a version of *its* policy
  UNIQUE (policy_id, version),
  FOREIGN KEY (org_id, policy_id) REFERENCES deedglass.policies (org_id, id),
  CHECK ((source = 'suggestion') = (suggestion_id IS NOT NULL))
);
CREATE TRIGGER policy_versions_append_only BEFORE UPDATE OR DELETE ON deedglass.policy_versions
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER policy_versions_append_only_stmt BEFORE UPDATE OR DELETE OR TRUNCATE ON deedglass.policy_versions
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
ALTER TABLE deedglass.policy_versions ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.policy_versions FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.policy_versions FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.policy_versions TO deedglass_app;

-- policy_activations: at most one active enforce and one active shadow version per org -------------
CREATE TABLE deedglass.policy_activations (
  id deedglass.pa_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  policy_id deedglass.pol_id NOT NULL,
  version_id deedglass.pv_id NOT NULL,
  mode text NOT NULL CHECK (mode IN ('enforce', 'shadow')),
  activated_by text CHECK (pg_catalog.length(activated_by) <= 64),
  activated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  deactivated_at timestamptz,
  deed_id deedglass.deed_id,                        -- foreign key added in 0006 (deeds)
  reason text CHECK (pg_catalog.length(reason) <= 500),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, policy_id) REFERENCES deedglass.policies (org_id, id),
  FOREIGN KEY (org_id, policy_id, version_id) REFERENCES deedglass.policy_versions (org_id, policy_id, id),
  CHECK (deactivated_at IS NULL OR deactivated_at >= activated_at)
);
CREATE UNIQUE INDEX policy_activations_active ON deedglass.policy_activations (org_id, mode) WHERE deactivated_at IS NULL;
ALTER TABLE deedglass.policy_activations ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.policy_activations FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.policy_activations FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.policy_activations TO deedglass_app;
GRANT UPDATE (deactivated_at) ON deedglass.policy_activations TO deedglass_app;

-- usage_counters (S11): the column is window_kind because WINDOW is a reserved word -----------------
CREATE TABLE deedglass.usage_counters (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  scope text NOT NULL CHECK (scope = 'org' OR scope ~ '^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  counter text NOT NULL CHECK (counter = 'calls' OR counter ~ '^spend:[a-z]{3}$' OR counter ~ '^budget:[a-z0-9_-]{1,64}$'),
  window_kind text NOT NULL CHECK (window_kind IN ('minute', 'hour', 'day')),
  window_start timestamptz NOT NULL,
  value bigint NOT NULL DEFAULT 0 CHECK (value >= 0),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, scope, counter, window_kind, window_start)
);
CREATE INDEX usage_counters_window ON deedglass.usage_counters (window_start);
CREATE TRIGGER usage_counters_touch BEFORE UPDATE ON deedglass.usage_counters FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.usage_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.usage_counters FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.usage_counters FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.usage_counters FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.usage_counters TO deedglass_app;
