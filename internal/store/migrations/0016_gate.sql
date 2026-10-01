-- 0016_gate.sql: what the Gate (S13) needs beyond 0005, 0007 and 0010: the request context, the
-- idempotency fingerprint, the persisted policy decision (input, enforced and shadow), the
-- execution deadline and the edge-reported times, plus the indexes, the two worker claim functions
-- and the idempotency-key release. Schema diff against S13.md §3.3 in reports/S13_REPORT.md.

-- actions: new columns. context, connector, tool_digest, request_fingerprint and livemode are
-- fixed at creation; the rest are set by one transition each (ledger.Transition's Patch).
ALTER TABLE deedglass.actions
  -- identity and request
  ADD COLUMN context jsonb CHECK (context IS NULL OR (pg_catalog.jsonb_typeof(context) = 'object'
    AND pg_catalog.pg_column_size(context) <= 4096)),
  ADD COLUMN connector text CHECK (connector ~ '^[a-z][a-z0-9-]{1,31}$'),   -- the slug before the first dot
  ADD COLUMN tool_digest deedglass.digest,                                  -- sent by MCP edges (S14)
  ADD COLUMN request_fingerprint deedglass.digest,                          -- request_digest + on-behalf-of + session
  ADD COLUMN livemode boolean NOT NULL DEFAULT false,                       -- dg_live_ vs dg_test_ key
  -- lifecycle
  ADD COLUMN executing_deadline timestamptz,
  -- decision: S11's Input, canonical (raw facts only, so S27 can replay it), and the decisions
  ADD COLUMN policy_input jsonb CHECK (policy_input IS NULL OR pg_catalog.jsonb_typeof(policy_input) = 'object'),
  ADD COLUMN policy_input_digest deedglass.digest,
  ADD COLUMN decision_code text CHECK (decision_code ~ '^[a-z][a-z0-9_]{0,63}$'),
  ADD COLUMN decision_source text CHECK (decision_source IN ('stop', 'budget', 'rule', 'default', 'builtin')),
  ADD COLUMN decision_message text CHECK (pg_catalog.length(decision_message) <= 2000),
  ADD COLUMN decision_detail jsonb CHECK (decision_detail IS NULL OR (pg_catalog.jsonb_typeof(decision_detail) = 'object'
    AND pg_catalog.pg_column_size(decision_detail) <= 16384)),
  ADD COLUMN policy_id deedglass.pol_id,
  ADD COLUMN policy_version_hash deedglass.digest,
  ADD COLUMN policy_mode text CHECK (policy_mode IN ('enforce', 'shadow')),
  ADD COLUMN shadow_detail jsonb CHECK (shadow_detail IS NULL OR (pg_catalog.jsonb_typeof(shadow_detail) = 'object'
    AND pg_catalog.pg_column_size(shadow_detail) <= 16384)),
  ADD COLUMN reservations jsonb CHECK (reservations IS NULL OR (pg_catalog.jsonb_typeof(reservations) = 'array'
    AND pg_catalog.pg_column_size(reservations) <= 16384)),
  -- execution: the tool's own times (the edge's, in proxy mode), and a redacted error message
  ADD COLUMN error_message text CHECK (pg_catalog.length(error_message) <= 1024),
  ADD COLUMN started_at timestamptz,
  ADD COLUMN finished_at timestamptz,
  ADD CONSTRAINT actions_policy_fk FOREIGN KEY (org_id, policy_id) REFERENCES deedglass.policies (org_id, id),
  ADD CONSTRAINT actions_exec_times CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at);

-- Rows written without a fingerprint (before the Gate, and S06's dev fake-action and test seeds)
-- take their request digest: the fingerprint of a request with no on-behalf-of and no session.
UPDATE deedglass.actions SET request_fingerprint = request_digest WHERE request_fingerprint IS NULL;
ALTER TABLE deedglass.actions ALTER COLUMN request_fingerprint SET NOT NULL;
CREATE FUNCTION deedglass.actions_fingerprint_default() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.request_fingerprint IS NULL THEN
    NEW.request_fingerprint := NEW.request_digest;
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.actions_fingerprint_default() FROM PUBLIC;
CREATE TRIGGER actions_fingerprint_default BEFORE INSERT ON deedglass.actions
  FOR EACH ROW EXECUTE FUNCTION deedglass.actions_fingerprint_default();

-- The worker's scans. (org_id, agent_id, idempotency_key) is unique since 0005 (actions_idem);
-- (org_id, created_at DESC, id DESC) is actions_org_time; executing past its deadline and idle
-- unsealed actions are read through actions_open (state, updated_at) WHERE state <> 'sealed' (few
-- rows: S13.md's extra (state, executing_deadline) index isn't built, because every index on actions
-- costs every transition). Keys to release after 24 h need their own small partial index.
CREATE INDEX actions_idem_release ON deedglass.actions (created_at) WHERE idempotency_key IS NOT NULL;

-- Grants: the new mutable columns, and idempotency_key (released after 24 h, or when a request
-- reuses a key whose window has closed).
GRANT UPDATE (idempotency_key, executing_deadline, policy_input, policy_input_digest, decision_code, decision_source,
  decision_message, decision_detail, policy_id, policy_version_hash, policy_mode, shadow_detail, reservations,
  error_message, started_at, finished_at) ON deedglass.actions TO deedglass_app;

-- claim_witness_jobs: the one cross-org read of witness_jobs. It leases due jobs to one worker
-- (FOR UPDATE SKIP LOCKED, so two workers never take the same job) and returns ids only; all real
-- work then runs in an org transaction with row-level security on.
CREATE FUNCTION deedglass.claim_witness_jobs(p_worker text, p_limit integer, p_lease interval)
RETURNS TABLE (id bigint, org_id text, action_id text, kind text, attempts integer)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH c AS (
    SELECT j.id FROM deedglass.witness_jobs AS j
     WHERE j.run_after <= pg_catalog.now()
       AND (j.locked_until IS NULL OR j.locked_until < pg_catalog.now())
     ORDER BY j.run_after, j.id
     LIMIT LEAST(GREATEST(p_limit, 1), 100)
     FOR UPDATE SKIP LOCKED)
  UPDATE deedglass.witness_jobs AS j
     SET locked_by = p_worker, locked_until = pg_catalog.now() + p_lease, attempts = j.attempts + 1
    FROM c WHERE j.id = c.id
  RETURNING j.id, j.org_id::text, j.action_id::text, j.kind, j.attempts;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_witness_jobs(text, integer, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_witness_jobs(text, integer, interval) TO deedglass_app;

-- claim_stale_actions: the sweepers' one cross-org read of actions. It returns (org, action, state)
-- for at most p_limit actions of each kind: executing past its deadline (crash recovery), idle in a
-- pre-seal state for 30 s (finish sealing after a crash), and pending approval past the approval's
-- expiry. Rows are locked SKIP LOCKED only for the call; processing is a compare-and-set on state,
-- so two instances that get the same id race safely and exactly one wins.
CREATE FUNCTION deedglass.claim_stale_actions(p_limit integer)
RETURNS TABLE (org_id text, action_id text, state text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH lost AS (
    SELECT a.org_id, a.id, a.state FROM deedglass.actions AS a
     WHERE a.state = 'executing' AND a.executing_deadline < pg_catalog.now()
     ORDER BY a.executing_deadline
     LIMIT LEAST(GREATEST(p_limit, 1), 1000)
     FOR UPDATE SKIP LOCKED),
  unsealed AS (
    SELECT a.org_id, a.id, a.state FROM deedglass.actions AS a
     WHERE a.state IN ('denied', 'failed', 'verified', 'mismatch', 'unverifiable', 'expired', 'cancelled', 'executed')
       AND a.updated_at < pg_catalog.now() - interval '30 seconds'
     ORDER BY a.updated_at
     LIMIT LEAST(GREATEST(p_limit, 1), 1000)
     FOR UPDATE SKIP LOCKED),
  expired AS (
    SELECT a.org_id, a.id, a.state FROM deedglass.approvals AS ap
      JOIN deedglass.actions AS a ON a.org_id = ap.org_id AND a.id = ap.action_id
     WHERE ap.status = 'pending' AND ap.expires_at < pg_catalog.now() AND a.state = 'pending_approval'
     ORDER BY ap.expires_at
     LIMIT LEAST(GREATEST(p_limit, 1), 1000)
     FOR UPDATE OF a SKIP LOCKED)
  SELECT org_id::text, id::text, state FROM lost
  UNION ALL SELECT org_id::text, id::text, state FROM unsealed
  UNION ALL SELECT org_id::text, id::text, state FROM expired;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_stale_actions(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_stale_actions(integer) TO deedglass_app;

-- maint_release_action_keys: the 24-hour Idempotency-Key window of contract §8 on POST /v1/actions.
-- It clears idempotency_key on at most p_limit actions created more than 24 h ago and returns how
-- many it cleared. The action and its deed are untouched; only the key's reservation ends.
CREATE FUNCTION deedglass.maint_release_action_keys(p_limit integer)
RETURNS integer
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH c AS (
    SELECT a.id FROM deedglass.actions AS a
     WHERE a.idempotency_key IS NOT NULL AND a.created_at < pg_catalog.now() - interval '24 hours'
     ORDER BY a.created_at
     LIMIT LEAST(GREATEST(p_limit, 1), 10000)
     FOR UPDATE SKIP LOCKED),
  u AS (
    UPDATE deedglass.actions AS a SET idempotency_key = NULL FROM c WHERE a.id = c.id RETURNING 1)
  SELECT pg_catalog.count(*)::integer FROM u;
$fn$;
REVOKE ALL ON FUNCTION deedglass.maint_release_action_keys(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.maint_release_action_keys(integer) TO deedglass_app;

-- Several edges in one UPDATE (S13). Within one transaction the Gate often takes several edges in a
-- row with no I/O between them (received → authenticated → evaluated → executing; executing →
-- executed → witnessing → verified). They are written as one UPDATE that names the intermediate
-- states in hops; the guard checks every hop against the edge table, then clears hops, so nothing is
-- stored and a skipped edge is refused exactly as before (DG004). Each hop still gets its own
-- timestamp column and its own action_events row (ledger.Advance). With hops NULL the guard is the
-- 0005 guard, unchanged.
ALTER TABLE deedglass.actions ADD COLUMN hops text[];
GRANT UPDATE (hops) ON deedglass.actions TO deedglass_app;

CREATE OR REPLACE FUNCTION deedglass.actions_state_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  prev text;
  hop text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'received' OR NEW.hops IS NOT NULL THEN
      RAISE EXCEPTION USING ERRCODE = 'DG004',
        MESSAGE = pg_catalog.format('deedglass: action %s must start in received, not %s', NEW.id, NEW.state);
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.state = OLD.state AND NEW.hops IS NULL THEN RETURN NEW; END IF;
  prev := OLD.state;
  IF NEW.hops IS NOT NULL THEN
    FOREACH hop IN ARRAY NEW.hops LOOP
      IF (prev, hop) NOT IN (VALUES
          ('received', 'authenticated'), ('authenticated', 'evaluated'),
          ('evaluated', 'denied'), ('evaluated', 'pending_approval'), ('evaluated', 'executing'),
          ('pending_approval', 'executing'), ('pending_approval', 'denied'), ('pending_approval', 'expired'), ('pending_approval', 'cancelled'),
          ('denied', 'sealed'), ('expired', 'sealed'), ('cancelled', 'sealed'),
          ('executing', 'executed'), ('executing', 'failed'), ('executed', 'witnessing'),
          ('witnessing', 'verified'), ('witnessing', 'mismatch'), ('witnessing', 'unverifiable'),
          ('verified', 'sealed'), ('mismatch', 'sealed'), ('unverifiable', 'sealed'), ('failed', 'sealed')) THEN
        RAISE EXCEPTION USING ERRCODE = 'DG004',
          MESSAGE = pg_catalog.format('deedglass: action %s cannot move from %s to %s', OLD.id, prev, hop),
          HINT = 'The allowed edges are in internal/gate/lifecycle (contract §5).';
      END IF;
      prev := hop;
    END LOOP;
    NEW.hops := NULL;
  END IF;
  IF (prev, NEW.state) NOT IN (VALUES
      ('received', 'authenticated'), ('authenticated', 'evaluated'),
      ('evaluated', 'denied'), ('evaluated', 'pending_approval'), ('evaluated', 'executing'),
      ('pending_approval', 'executing'), ('pending_approval', 'denied'), ('pending_approval', 'expired'), ('pending_approval', 'cancelled'),
      ('denied', 'sealed'), ('expired', 'sealed'), ('cancelled', 'sealed'),
      ('executing', 'executed'), ('executing', 'failed'), ('executed', 'witnessing'),
      ('witnessing', 'verified'), ('witnessing', 'mismatch'), ('witnessing', 'unverifiable'),
      ('verified', 'sealed'), ('mismatch', 'sealed'), ('unverifiable', 'sealed'), ('failed', 'sealed')) THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004',
      MESSAGE = pg_catalog.format('deedglass: action %s cannot move from %s to %s', OLD.id, prev, NEW.state),
      HINT = 'The allowed edges are in internal/gate/lifecycle (contract §5).';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.actions_state_guard() FROM PUBLIC;
DROP TRIGGER actions_state_guard ON deedglass.actions;
CREATE TRIGGER actions_state_guard BEFORE INSERT OR UPDATE OF state, hops ON deedglass.actions
  FOR EACH ROW EXECUTE FUNCTION deedglass.actions_state_guard();
