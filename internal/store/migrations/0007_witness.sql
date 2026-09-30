-- 0007_witness.sql: witness jobs, decoded transactions, decoder slots.

-- witness_jobs: work for the Witness and the approval workers. A bigint identity id, no prefix;
-- success deletes the row. Claimed across orgs only through S13's claim_witness_jobs function.
CREATE TABLE deedglass.witness_jobs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  action_id deedglass.act_id NOT NULL,
  kind text NOT NULL DEFAULT 'witness' CHECK (kind IN ('witness', 'l1_readback', 'l2_match',
    'approval_expire', 'approval_notify', 'approval_resume')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts integer NOT NULL DEFAULT 8 CHECK (max_attempts >= 1),
  run_after timestamptz NOT NULL DEFAULT pg_catalog.now(),
  locked_by text CHECK (pg_catalog.length(locked_by) <= 128),
  locked_until timestamptz,
  last_error text CHECK (pg_catalog.length(last_error) <= 2000),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (action_id, kind),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id)
);
CREATE INDEX witness_jobs_run_after ON deedglass.witness_jobs (run_after);
CREATE TRIGGER witness_jobs_touch BEFORE UPDATE ON deedglass.witness_jobs FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.witness_jobs ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.witness_jobs FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.witness_jobs FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.witness_jobs TO deedglass_app;

-- decoded_txs: transactions read from a watched database's change stream (S20) -------------------
CREATE TABLE deedglass.decoded_txs (
  id deedglass.dtx_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  connector_id deedglass.con_id NOT NULL,
  action_id deedglass.act_id,
  xid bigint NOT NULL CHECK (xid >= 0),
  commit_lsn pg_lsn NOT NULL,
  end_lsn pg_lsn NOT NULL,
  commit_time timestamptz NOT NULL,
  change_count integer NOT NULL CHECK (change_count >= 0),
  changes jsonb NOT NULL DEFAULT '[]' CHECK (pg_catalog.jsonb_typeof(changes) = 'array'),
  flags text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (connector_id, commit_lsn),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id)
);
CREATE INDEX decoded_txs_action ON deedglass.decoded_txs (org_id, action_id) WHERE action_id IS NOT NULL;
ALTER TABLE deedglass.decoded_txs ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.decoded_txs FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.decoded_txs FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.decoded_txs FROM PUBLIC;
GRANT SELECT, INSERT, DELETE ON deedglass.decoded_txs TO deedglass_app;

-- decoder_slots: one logical replication slot per Postgres connector (S20) -------------------------
CREATE TABLE deedglass.decoder_slots (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  connector_id deedglass.con_id NOT NULL,
  slot_name text NOT NULL UNIQUE CHECK (slot_name ~ '^deedglass_[a-z0-9_]{1,40}$'),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'connecting', 'running', 'degraded', 'waiting',
    'halted', 'dropped', 'disabled')),
  restart_lsn pg_lsn,
  lag_bytes bigint CHECK (lag_bytes >= 0),
  last_commit_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, connector_id),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id)
);
CREATE TRIGGER decoder_slots_touch BEFORE UPDATE ON deedglass.decoder_slots FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.decoder_slots ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.decoder_slots FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.decoder_slots FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.decoder_slots FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.decoder_slots TO deedglass_app;
