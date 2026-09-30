-- 0008_learn.sql: Insights: replay jobs, suggestions, anomalies.

-- replay_jobs: replay past actions against a policy version (S27) --------------------------------
CREATE TABLE deedglass.replay_jobs (
  id deedglass.rpl_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  policy_id deedglass.pol_id NOT NULL,
  version_id deedglass.pv_id NOT NULL,
  from_ts timestamptz NOT NULL,
  to_ts timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  result jsonb,
  error text CHECK (pg_catalog.length(error) <= 2000),
  requested_by text CHECK (pg_catalog.length(requested_by) <= 64),
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, policy_id) REFERENCES deedglass.policies (org_id, id),
  FOREIGN KEY (org_id, policy_id, version_id) REFERENCES deedglass.policy_versions (org_id, policy_id, id),
  CHECK (to_ts > from_ts)
);
CREATE INDEX replay_jobs_open ON deedglass.replay_jobs (status, created_at) WHERE status IN ('queued', 'running');
ALTER TABLE deedglass.replay_jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.replay_jobs FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.replay_jobs FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.replay_jobs FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON deedglass.replay_jobs TO deedglass_app;

-- suggestions: policy changes a person accepts or rejects (S28) ----------------------------------
CREATE TABLE deedglass.suggestions (
  id deedglass.sug_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  kind text NOT NULL CHECK (kind IN ('loosen', 'tighten', 'budget', 'explicit_rule', 'advisory')),
  title text NOT NULL CHECK (pg_catalog.length(title) BETWEEN 1 AND 200),
  rationale text CHECK (pg_catalog.length(rationale) <= 4000),
  evidence jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(evidence) = 'object'),
  diff text CHECK (pg_catalog.length(diff) <= 65536),
  replay_job_id deedglass.rpl_id,
  replay_result jsonb,
  risk_note text CHECK (pg_catalog.length(risk_note) <= 2000),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'rejected', 'superseded')),
  decided_by text CHECK (pg_catalog.length(decided_by) <= 64),
  decided_at timestamptz,
  decision_reason text CHECK (pg_catalog.length(decision_reason) <= 2000),
  resulting_version_id deedglass.pv_id,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, replay_job_id) REFERENCES deedglass.replay_jobs (org_id, id),
  FOREIGN KEY (org_id, resulting_version_id) REFERENCES deedglass.policy_versions (org_id, id)
);
CREATE INDEX suggestions_open ON deedglass.suggestions (org_id, created_at DESC) WHERE status = 'open';
ALTER TABLE deedglass.suggestions ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.suggestions FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.suggestions FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON deedglass.suggestions TO deedglass_app;

-- A policy version made from a suggestion names it (the column exists since 0003).
ALTER TABLE deedglass.policy_versions ADD CONSTRAINT policy_versions_suggestion_fk
  FOREIGN KEY (org_id, suggestion_id) REFERENCES deedglass.suggestions (org_id, id);

-- anomalies (S27) ------------------------------------------------------------------------------
CREATE TABLE deedglass.anomalies (
  id deedglass.anm_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  agent_id deedglass.agt_id,                        -- NULL for an org-wide anomaly
  kind text NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,63}$'),
  severity text NOT NULL CHECK (severity IN ('info', 'warn', 'high')),
  window_start timestamptz NOT NULL,
  window_end timestamptz NOT NULL,
  evidence jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(evidence) = 'object'),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved')),
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, agent_id) REFERENCES deedglass.agents (org_id, id),
  CHECK (window_end >= window_start),
  CHECK (status <> 'resolved' OR resolved_at IS NOT NULL)
);
CREATE INDEX anomalies_org_time ON deedglass.anomalies (org_id, created_at DESC, id DESC);
ALTER TABLE deedglass.anomalies ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.anomalies FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.anomalies FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON deedglass.anomalies TO deedglass_app;
