-- 0024_learn_v2.sql: what S27's Insights needs (replay jobs, anomaly episodes, learn runs).
--
-- 0008 created replay_jobs, suggestions and anomalies with row-level security. Nothing wrote to
-- them before S27, so this migration tightens their checks in place. It first refuses, with a
-- clear message, if replay_jobs or anomalies already hold rows: a forward-only migration never
-- guesses what to do with data it didn't expect.
DO $refuse$
BEGIN
  IF EXISTS (SELECT 1 FROM deedglass.replay_jobs) OR EXISTS (SELECT 1 FROM deedglass.anomalies) THEN
    RAISE EXCEPTION 'migration 0024 expects replay_jobs and anomalies to be empty (nothing wrote to them before S27)'
      USING HINT = 'Move the rows aside, apply the migration, and write them back in the new shape.';
  END IF;
END
$refuse$;

-- replay_jobs: the candidate is a stored version or a draft source; a lease; exact progress -------
ALTER TABLE deedglass.replay_jobs
  ALTER COLUMN version_id DROP NOT NULL,                       -- NULL for a draft (source) replay
  ADD COLUMN source text CHECK (pg_catalog.octet_length(source) <= 262144),
  ADD COLUMN source_hash text CHECK (source_hash ~ '^sha256:[0-9a-f]{64}$'),
  ADD COLUMN baseline jsonb NOT NULL DEFAULT '{"type":"recorded"}' CHECK (pg_catalog.jsonb_typeof(baseline) = 'object'),
  ADD COLUMN purpose text NOT NULL DEFAULT 'manual' CHECK (purpose IN ('manual', 'suggestion')),
  ADD COLUMN sqlguard_version text CHECK (pg_catalog.length(sqlguard_version) <= 32),
  ADD COLUMN total bigint CHECK (total >= 0),
  ADD COLUMN processed bigint NOT NULL DEFAULT 0 CHECK (processed >= 0),
  ADD COLUMN result_digest text CHECK (result_digest ~ '^sha256:[0-9a-f]{64}$'),
  ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  ADD COLUMN locked_by text CHECK (pg_catalog.length(locked_by) <= 128),
  ADD COLUMN locked_until timestamptz,
  ADD COLUMN cancel_requested_at timestamptz,
  ADD COLUMN requested_by_type text NOT NULL CHECK (requested_by_type IN ('user', 'api_key', 'system')),
  ADD COLUMN requested_by_id text NOT NULL CHECK (pg_catalog.length(requested_by_id) BETWEEN 1 AND 64),
  ADD CONSTRAINT replay_candidate CHECK ((version_id IS NOT NULL) <> (source IS NOT NULL)),
  ADD CONSTRAINT replay_result_size CHECK (result IS NULL OR pg_catalog.octet_length(result::text) <= 1048576);
-- status: queued → running → succeeded | failed | cancelled (replaces 0008's done).
ALTER TABLE deedglass.replay_jobs DROP CONSTRAINT replay_jobs_status_check;
ALTER TABLE deedglass.replay_jobs ADD CONSTRAINT replay_jobs_status_check
  CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled'));
CREATE INDEX replay_jobs_claim_idx ON deedglass.replay_jobs (created_at) WHERE status IN ('queued', 'running');
CREATE INDEX replay_jobs_org_time ON deedglass.replay_jobs (org_id, created_at DESC, id DESC);

-- anomalies: episodes, the review lifecycle, and the opt-in facts --------------------------------
-- An episode is the unresolved series of findings for one agent|kind|tool. active_until feeds
-- anomaly.active and expires on its own; status (open → acknowledged → resolved) is the review
-- inbox, and only a person moves it (or the 14-day close, by the system, with no deed).
ALTER TABLE deedglass.anomalies
  ADD COLUMN tool text CHECK (pg_catalog.length(tool) <= 200),
  ADD COLUMN score double precision NOT NULL,
  ADD COLUMN episode_key text NOT NULL CHECK (pg_catalog.length(episode_key) BETWEEN 3 AND 300),
  ADD COLUMN occurrences integer NOT NULL DEFAULT 1 CHECK (occurrences >= 1),
  ADD COLUMN active_until timestamptz NOT NULL,
  ADD COLUMN detector_version text NOT NULL CHECK (pg_catalog.length(detector_version) <= 16),
  ADD COLUMN title text NOT NULL DEFAULT '' CHECK (pg_catalog.length(title) <= 280),
  ADD COLUMN acknowledged_by text CHECK (pg_catalog.length(acknowledged_by) <= 64),
  ADD COLUMN acknowledged_at timestamptz,
  ADD COLUMN resolved_by text CHECK (pg_catalog.length(resolved_by) <= 64),
  ADD COLUMN resolution_reason text CHECK (pg_catalog.char_length(resolution_reason) <= 280),
  ADD COLUMN resolution_deed_id text,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  ADD CONSTRAINT anomalies_evidence_size CHECK (pg_catalog.octet_length(evidence::text) <= 32768),
  ADD CONSTRAINT anomalies_resolution CHECK (status <> 'resolved' OR resolved_by IS NOT NULL);
CREATE TRIGGER anomalies_touch BEFORE UPDATE ON deedglass.anomalies FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
CREATE UNIQUE INDEX anomalies_open_episode_uq ON deedglass.anomalies (org_id, episode_key) WHERE status <> 'resolved';
CREATE INDEX anomalies_active_idx ON deedglass.anomalies (org_id, agent_id, active_until) WHERE status <> 'resolved';

-- learn_runs: idempotency and health of Insights' background work (S27 detect; S28 suggest, digest).
CREATE TABLE deedglass.learn_runs (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  kind text NOT NULL CHECK (kind IN ('detect', 'suggest', 'digest')),
  period_key text NOT NULL CHECK (pg_catalog.length(period_key) BETWEEN 1 AND 32), -- detect: 2026-10-09T14:05Z · suggest: 2026-10-09 · digest: 2026-W41
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  stats jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(stats) = 'object' AND pg_catalog.octet_length(stats::text) <= 4096),
  error text CHECK (pg_catalog.length(error) <= 2000),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, kind, period_key)
);
CREATE INDEX learn_runs_recent ON deedglass.learn_runs (org_id, kind, started_at DESC);
ALTER TABLE deedglass.learn_runs ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.learn_runs FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.learn_runs FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.learn_runs TO deedglass_app;

-- Insights reads actions by decision time (evaluated_at = policy_input.time, S27 §3.2).
CREATE INDEX actions_org_evaluated_idx ON deedglass.actions (org_id, evaluated_at, id);
CREATE INDEX actions_org_shadow_idx ON deedglass.actions (org_id, shadow_version_id, evaluated_at)
  WHERE shadow_version_id IS NOT NULL;

-- claim_replay_jobs: the one cross-org read of replay_jobs. Under one transaction-level advisory
-- lock (claims are short), it leases at most p_limit jobs to one worker: queued jobs, and running
-- jobs whose lease ran out (a lost runner; the engine is deterministic, so it restarts from zero),
-- at most one per org, and never for an org that already has a job with a live lease. Ids only.
CREATE FUNCTION deedglass.claim_replay_jobs(p_worker text, p_limit integer, p_lease interval)
RETURNS TABLE (org_id text, id text, attempts integer)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH l AS (SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('deedglass/learn/replay-claim', 0))),
  c AS (
    SELECT DISTINCT ON (j.org_id) j.id, j.org_id, j.created_at FROM deedglass.replay_jobs AS j, l
     WHERE (j.status = 'queued' OR (j.status = 'running' AND (j.locked_until IS NULL OR j.locked_until < pg_catalog.now())))
       AND NOT EXISTS (SELECT 1 FROM deedglass.replay_jobs AS r
                        WHERE r.org_id = j.org_id AND r.status = 'running' AND r.id <> j.id
                          AND r.locked_until >= pg_catalog.now())
     ORDER BY j.org_id, j.created_at, j.id),
  pick AS (
    SELECT x.id FROM deedglass.replay_jobs AS x JOIN c ON c.id = x.id
     ORDER BY c.created_at, c.id
     LIMIT LEAST(GREATEST(p_limit, 1), 16)
     FOR UPDATE OF x SKIP LOCKED)
  UPDATE deedglass.replay_jobs AS j
     SET status = 'running', locked_by = p_worker, locked_until = pg_catalog.now() + p_lease,
         attempts = j.attempts + 1, processed = 0, started_at = pg_catalog.now()
    FROM pick WHERE j.id = pick.id
  RETURNING j.org_id::text, j.id::text, j.attempts;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_replay_jobs(text, integer, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_replay_jobs(text, integer, interval) TO deedglass_app;

-- claim_learn_orgs: the detect loop's one cross-org read (S27 §3.7). The orgs with actions decided
-- in the last p_recent, or with anomaly facts switched on (an opted-in org is never stale just
-- because it was quiet). Org ids only; the work runs in each org's own transaction.
CREATE FUNCTION deedglass.claim_learn_orgs(p_recent interval)
RETURNS TABLE (org_id text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT o.id::text FROM deedglass.orgs AS o
   WHERE o.status = 'active'
     AND ((o.settings -> 'insights' ->> 'anomaly_facts') = 'true'
          OR EXISTS (SELECT 1 FROM deedglass.actions AS a
                      WHERE a.org_id = o.id AND a.evaluated_at > pg_catalog.now() - p_recent))
   ORDER BY o.id
   LIMIT 10000;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_learn_orgs(interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_learn_orgs(interval) TO deedglass_app;
