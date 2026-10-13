-- 0029_observe.sql: S50's observe mode (ADR-094). A watched session groups every unattributed frame
-- (S20: row changes with no Deedglass ledger row and no marker) that Postgres committed on a
-- connector's watched tables while the session was open. Nothing here is claimed about who made
-- those changes: Postgres's change log doesn't carry the role or the program.
--
-- observe_sessions: one row per session. The state machine is a trigger:
--   open → closing → sealed → undoing → undone, and undoing → sealed (a failed undo).
-- The identity columns never change; closed_at is set once (with closing); seal_deed_id once (with
-- sealed); undo_action_id only on the way into undoing. No DELETE except purge_org.
--
-- observe_frames: which frames a session holds. A frame is attached while the session is open or
-- closing; "excluded" (leave a transaction out of the undo) changes only while the session is
-- sealed, so never once an undo has started. There is no foreign key to decoded_txs: frames are
-- pruned after decoder retention, and a session whose frames are gone keeps its sealed summary.

CREATE DOMAIN deedglass.obs_id AS text CHECK (VALUE ~ '^obs_[0-7][0-9a-hjkmnp-tv-z]{25}$');

CREATE TABLE deedglass.observe_sessions (
  id deedglass.obs_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  connector_id deedglass.con_id NOT NULL,
  label text CHECK (label IS NULL OR (pg_catalog.char_length(label) BETWEEN 1 AND 80 AND label !~ '[[:cntrl:]]')),
  agent_id deedglass.agt_id,
  opened_by_type text NOT NULL CHECK (opened_by_type IN ('user', 'api_key', 'system')),
  opened_by_id text CHECK (opened_by_id IS NULL OR pg_catalog.length(opened_by_id) <= 64),
  opened_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  closed_at timestamptz,
  closed_by_type text CHECK (closed_by_type IS NULL OR closed_by_type IN ('user', 'api_key', 'system')),
  closed_by_id text CHECK (closed_by_id IS NULL OR pg_catalog.length(closed_by_id) <= 64),
  state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closing', 'sealed', 'undoing', 'undone')),
  flags text[] NOT NULL DEFAULT '{}' CHECK (pg_catalog.cardinality(flags) <= 16),
  summary jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(summary) = 'object' AND pg_catalog.octet_length(summary::text) <= 16384),
  seal_deed_id deedglass.deed_id,
  undo_action_id deedglass.act_id,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id),
  FOREIGN KEY (org_id, agent_id) REFERENCES deedglass.agents (org_id, id),
  CHECK ((state = 'open') = (closed_at IS NULL)),
  CHECK (closed_at IS NULL OR closed_at >= opened_at),
  CHECK ((seal_deed_id IS NULL) = (state IN ('open', 'closing')))
);
CREATE INDEX observe_sessions_list ON deedglass.observe_sessions (org_id, opened_at DESC, id DESC);
CREATE INDEX observe_sessions_live ON deedglass.observe_sessions (org_id, connector_id) WHERE state IN ('open', 'closing', 'undoing');

CREATE FUNCTION deedglass.observe_sessions_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.connector_id IS DISTINCT FROM OLD.connector_id
     OR NEW.label IS DISTINCT FROM OLD.label OR NEW.agent_id IS DISTINCT FROM OLD.agent_id
     OR NEW.opened_by_type IS DISTINCT FROM OLD.opened_by_type OR NEW.opened_by_id IS DISTINCT FROM OLD.opened_by_id
     OR NEW.opened_at IS DISTINCT FROM OLD.opened_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001',
      MESSAGE = pg_catalog.format('deedglass: watched session %s keeps the identity it was opened with', OLD.id),
      HINT = 'Open a new session instead.';
  END IF;
  IF OLD.closed_at IS NOT NULL AND (NEW.closed_at IS DISTINCT FROM OLD.closed_at OR NEW.closed_by_type IS DISTINCT FROM OLD.closed_by_type
     OR NEW.closed_by_id IS DISTINCT FROM OLD.closed_by_id) THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001',
      MESSAGE = pg_catalog.format('deedglass: watched session %s was already closed', OLD.id);
  END IF;
  IF OLD.seal_deed_id IS NOT NULL AND NEW.seal_deed_id IS DISTINCT FROM OLD.seal_deed_id THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001',
      MESSAGE = pg_catalog.format('deedglass: watched session %s is sealed by %s', OLD.id, OLD.seal_deed_id);
  END IF;
  IF OLD.state IN ('sealed', 'undoing', 'undone') AND NEW.summary IS DISTINCT FROM OLD.summary THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001',
      MESSAGE = pg_catalog.format('deedglass: watched session %s keeps its sealed summary', OLD.id);
  END IF;
  IF NEW.undo_action_id IS DISTINCT FROM OLD.undo_action_id AND NOT (OLD.state = 'sealed' AND NEW.state = 'undoing') THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001',
      MESSAGE = pg_catalog.format('deedglass: watched session %s names its undo only when the undo starts', OLD.id);
  END IF;
  IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
       (OLD.state = 'open' AND NEW.state = 'closing') OR
       (OLD.state = 'closing' AND NEW.state = 'sealed') OR
       (OLD.state = 'sealed' AND NEW.state = 'undoing') OR
       (OLD.state = 'undoing' AND NEW.state IN ('undone', 'sealed'))) THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004',
      MESSAGE = pg_catalog.format('deedglass: watched session %s can''t go from %s to %s', OLD.id, OLD.state, NEW.state),
      HINT = 'A session goes open → closing → sealed → undoing → undone (a failed undo returns to sealed).';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.observe_sessions_guard() FROM PUBLIC;
CREATE TRIGGER observe_sessions_guard BEFORE UPDATE ON deedglass.observe_sessions
  FOR EACH ROW EXECUTE FUNCTION deedglass.observe_sessions_guard();

-- A session starts open, with nothing sealed or undone.
CREATE FUNCTION deedglass.observe_sessions_insert_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.state <> 'open' OR NEW.closed_at IS NOT NULL OR NEW.seal_deed_id IS NOT NULL OR NEW.undo_action_id IS NOT NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004', MESSAGE = 'deedglass: a watched session starts open';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.observe_sessions_insert_guard() FROM PUBLIC;
CREATE TRIGGER observe_sessions_insert_guard BEFORE INSERT ON deedglass.observe_sessions
  FOR EACH ROW EXECUTE FUNCTION deedglass.observe_sessions_insert_guard();
CREATE TRIGGER observe_sessions_no_delete BEFORE DELETE ON deedglass.observe_sessions
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER observe_sessions_no_delete_stmt BEFORE DELETE OR TRUNCATE ON deedglass.observe_sessions
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();

ALTER TABLE deedglass.observe_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.observe_sessions FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.observe_sessions FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.observe_sessions TO deedglass_app;
GRANT UPDATE (closed_at, closed_by_type, closed_by_id, state, flags, summary, seal_deed_id, undo_action_id)
  ON deedglass.observe_sessions TO deedglass_app;

-- observe_frames ------------------------------------------------------------------------------------------
CREATE TABLE deedglass.observe_frames (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  session_id deedglass.obs_id NOT NULL,
  frame_id deedglass.dtx_id NOT NULL,
  excluded boolean NOT NULL DEFAULT false,
  excluded_by text CHECK (excluded_by IS NULL OR pg_catalog.length(excluded_by) <= 64),
  excluded_at timestamptz,
  attached_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, session_id, frame_id),
  FOREIGN KEY (org_id, session_id) REFERENCES deedglass.observe_sessions (org_id, id)
);
CREATE INDEX observe_frames_frame ON deedglass.observe_frames (org_id, frame_id);

CREATE FUNCTION deedglass.observe_frames_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  v_state text;
BEGIN
  SELECT s.state INTO v_state FROM deedglass.observe_sessions AS s WHERE s.org_id = NEW.org_id AND s.id = NEW.session_id;
  IF TG_OP = 'INSERT' THEN
    IF v_state IS DISTINCT FROM 'open' AND v_state IS DISTINCT FROM 'closing' THEN
      RAISE EXCEPTION USING ERRCODE = 'DG004',
        MESSAGE = pg_catalog.format('deedglass: watched session %s is %s; frames attach only while it is open or closing', NEW.session_id, v_state);
    END IF;
    IF NEW.excluded THEN
      RAISE EXCEPTION USING ERRCODE = 'DG004', MESSAGE = 'deedglass: a frame is attached included';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.session_id IS DISTINCT FROM OLD.session_id OR NEW.frame_id IS DISTINCT FROM OLD.frame_id
     OR NEW.attached_at IS DISTINCT FROM OLD.attached_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION USING ERRCODE = 'DG001', MESSAGE = 'deedglass: an attached frame keeps its session and id';
  END IF;
  IF v_state IS DISTINCT FROM 'sealed' THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004',
      MESSAGE = pg_catalog.format('deedglass: watched session %s is %s; a transaction can be left out only while it is sealed and before an undo starts', NEW.session_id, v_state);
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.observe_frames_guard() FROM PUBLIC;
CREATE TRIGGER observe_frames_guard BEFORE INSERT OR UPDATE ON deedglass.observe_frames
  FOR EACH ROW EXECUTE FUNCTION deedglass.observe_frames_guard();
CREATE TRIGGER observe_frames_no_delete BEFORE DELETE ON deedglass.observe_frames
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER observe_frames_no_delete_stmt BEFORE DELETE OR TRUNCATE ON deedglass.observe_frames
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();

ALTER TABLE deedglass.observe_frames ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.observe_frames FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.observe_frames FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.observe_frames TO deedglass_app;
GRANT UPDATE (excluded, excluded_by, excluded_at) ON deedglass.observe_frames TO deedglass_app;

-- claim_observe_orgs: the orgs with a session to attach to, close or seal (the serve job's only
-- cross-org read; ids only, like claim_learn_orgs).
CREATE FUNCTION deedglass.claim_observe_orgs(p_limit integer) RETURNS TABLE (org_id text) LANGUAGE sql STABLE SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT DISTINCT s.org_id::text FROM deedglass.observe_sessions AS s
   WHERE s.state IN ('open', 'closing', 'undoing') ORDER BY 1 LIMIT LEAST(GREATEST(p_limit, 1), 1000)
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_observe_orgs(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_observe_orgs(integer) TO deedglass_app;

-- purge_org (0027) also empties the two new tables.
CREATE OR REPLACE FUNCTION deedglass.purge_org(p_org text, p_confirm text) RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  v_slug  text;
  v_users text[];
  v_out   jsonb;
  v_gone  bigint;
BEGIN
  SELECT o.slug INTO v_slug FROM deedglass.orgs AS o WHERE o.id = p_org;
  IF v_slug IS NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'DG007',
      MESSAGE = 'deedglass: purge_org found no org with that id',
      HINT = 'Copy the org id (org_…) from deedglass org list or the request, then run it again.';
  END IF;
  IF p_confirm IS DISTINCT FROM v_slug THEN
    RAISE EXCEPTION USING ERRCODE = 'DG007',
      MESSAGE = pg_catalog.format('deedglass: purge_org needs the org''s slug as its confirmation (%s); nothing was deleted', v_slug),
      HINT = 'Pass the slug exactly as the second argument.';
  END IF;
  SELECT pg_catalog.array_agg(DISTINCT m.user_id::text) INTO v_users FROM deedglass.memberships AS m WHERE m.org_id = p_org;
  PERFORM pg_catalog.set_config('deedglass.purging_org', p_org, true);

  WITH
    d34 AS (DELETE FROM deedglass.observe_frames AS t WHERE t.org_id = p_org RETURNING 1),
    d35 AS (DELETE FROM deedglass.observe_sessions AS t WHERE t.org_id = p_org RETURNING 1),
    d01 AS (DELETE FROM deedglass.notification_messages AS t WHERE t.org_id = p_org RETURNING 1),
    d02 AS (DELETE FROM deedglass.webhook_deliveries AS t WHERE t.org_id = p_org RETURNING 1),
    d03 AS (DELETE FROM deedglass.webhooks AS t WHERE t.org_id = p_org RETURNING 1),
    d04 AS (DELETE FROM deedglass.witness_jobs AS t WHERE t.org_id = p_org RETURNING 1),
    d05 AS (DELETE FROM deedglass.undo_plans AS t WHERE t.org_id = p_org RETURNING 1),
    d06 AS (DELETE FROM deedglass.approval_votes AS t WHERE t.org_id = p_org RETURNING 1),
    d07 AS (DELETE FROM deedglass.approval_tokens AS t WHERE t.org_id = p_org RETURNING 1),
    d08 AS (DELETE FROM deedglass.approvals AS t WHERE t.org_id = p_org RETURNING 1),
    d09 AS (DELETE FROM deedglass.action_events AS t WHERE t.org_id = p_org RETURNING 1),
    d10 AS (DELETE FROM deedglass.deeds AS t WHERE t.org_id = p_org RETURNING 1),
    d11 AS (DELETE FROM deedglass.actions AS t WHERE t.org_id = p_org RETURNING 1),
    d12 AS (DELETE FROM deedglass.admin_events AS t WHERE t.org_id = p_org RETURNING 1),
    d13 AS (DELETE FROM deedglass.decoded_txs AS t WHERE t.org_id = p_org RETURNING 1),
    d14 AS (DELETE FROM deedglass.decoder_slots AS t WHERE t.org_id = p_org RETURNING 1),
    d15 AS (DELETE FROM deedglass.credentials AS t WHERE t.org_id = p_org RETURNING 1),
    d16 AS (DELETE FROM deedglass.connectors AS t WHERE t.org_id = p_org RETURNING 1),
    d17 AS (DELETE FROM deedglass.learn_runs AS t WHERE t.org_id = p_org RETURNING 1),
    d18 AS (DELETE FROM deedglass.anomalies AS t WHERE t.org_id = p_org RETURNING 1),
    d19 AS (DELETE FROM deedglass.suggestions AS t WHERE t.org_id = p_org RETURNING 1),
    d20 AS (DELETE FROM deedglass.replay_jobs AS t WHERE t.org_id = p_org RETURNING 1),
    d21 AS (DELETE FROM deedglass.policy_activations AS t WHERE t.org_id = p_org RETURNING 1),
    d22 AS (DELETE FROM deedglass.policy_versions AS t WHERE t.org_id = p_org RETURNING 1),
    d23 AS (DELETE FROM deedglass.policies AS t WHERE t.org_id = p_org RETURNING 1),
    d24 AS (DELETE FROM deedglass.usage_counters AS t WHERE t.org_id = p_org RETURNING 1),
    d25 AS (DELETE FROM deedglass.idempotency_keys AS t WHERE t.org_id = p_org RETURNING 1),
    d26 AS (DELETE FROM deedglass.intent_nonces AS t WHERE t.org_id = p_org RETURNING 1),
    d27 AS (DELETE FROM deedglass.api_keys AS t WHERE t.org_id = p_org RETURNING 1),
    d28 AS (DELETE FROM deedglass.agents AS t WHERE t.org_id = p_org RETURNING 1),
    d29 AS (DELETE FROM deedglass.login_tokens AS t WHERE t.org_id = p_org RETURNING 1),
    d30 AS (DELETE FROM deedglass.sessions AS t WHERE t.org_id = p_org RETURNING 1),
    d31 AS (DELETE FROM deedglass.memberships AS t WHERE t.org_id = p_org RETURNING 1),
    d32 AS (DELETE FROM deedglass.inbound_replay AS t WHERE t.org_id = p_org RETURNING 1),
    d33 AS (DELETE FROM deedglass.orgs AS t WHERE t.id = p_org RETURNING 1)
  SELECT pg_catalog.jsonb_build_object(
    'notification_messages', (SELECT pg_catalog.count(*) FROM d01), 'webhook_deliveries', (SELECT pg_catalog.count(*) FROM d02),
    'webhooks', (SELECT pg_catalog.count(*) FROM d03), 'witness_jobs', (SELECT pg_catalog.count(*) FROM d04),
    'undo_plans', (SELECT pg_catalog.count(*) FROM d05), 'approval_votes', (SELECT pg_catalog.count(*) FROM d06),
    'approval_tokens', (SELECT pg_catalog.count(*) FROM d07), 'approvals', (SELECT pg_catalog.count(*) FROM d08),
    'action_events', (SELECT pg_catalog.count(*) FROM d09), 'deeds', (SELECT pg_catalog.count(*) FROM d10),
    'actions', (SELECT pg_catalog.count(*) FROM d11), 'admin_events', (SELECT pg_catalog.count(*) FROM d12),
    'decoded_txs', (SELECT pg_catalog.count(*) FROM d13), 'decoder_slots', (SELECT pg_catalog.count(*) FROM d14),
    'credentials', (SELECT pg_catalog.count(*) FROM d15), 'connectors', (SELECT pg_catalog.count(*) FROM d16),
    'learn_runs', (SELECT pg_catalog.count(*) FROM d17), 'anomalies', (SELECT pg_catalog.count(*) FROM d18),
    'suggestions', (SELECT pg_catalog.count(*) FROM d19), 'replay_jobs', (SELECT pg_catalog.count(*) FROM d20),
    'policy_activations', (SELECT pg_catalog.count(*) FROM d21), 'policy_versions', (SELECT pg_catalog.count(*) FROM d22),
    'policies', (SELECT pg_catalog.count(*) FROM d23), 'usage_counters', (SELECT pg_catalog.count(*) FROM d24),
    'idempotency_keys', (SELECT pg_catalog.count(*) FROM d25), 'intent_nonces', (SELECT pg_catalog.count(*) FROM d26),
    'api_keys', (SELECT pg_catalog.count(*) FROM d27), 'agents', (SELECT pg_catalog.count(*) FROM d28),
    'login_tokens', (SELECT pg_catalog.count(*) FROM d29), 'sessions', (SELECT pg_catalog.count(*) FROM d30),
    'memberships', (SELECT pg_catalog.count(*) FROM d31), 'inbound_replay', (SELECT pg_catalog.count(*) FROM d32),
    'orgs', (SELECT pg_catalog.count(*) FROM d33),
    'observe_frames', (SELECT pg_catalog.count(*) FROM d34), 'observe_sessions', (SELECT pg_catalog.count(*) FROM d35))
  INTO v_out;

  -- People who belonged only to this org and appear nowhere else. A user still in another org stays.
  WITH g AS (
    DELETE FROM deedglass.users AS u
     WHERE u.id::text = ANY (coalesce(v_users, ARRAY[]::text[]))
       AND NOT EXISTS (SELECT 1 FROM deedglass.memberships AS m WHERE m.user_id = u.id OR m.invited_by = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.sessions AS s WHERE s.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.login_tokens AS l WHERE l.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.agents AS a WHERE a.owner_user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.approval_tokens AS t WHERE t.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.approval_votes AS v WHERE v.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.policy_versions AS p WHERE p.author_user_id = u.id)
    RETURNING 1)
  SELECT pg_catalog.count(*) INTO v_gone FROM g;

  PERFORM pg_catalog.set_config('deedglass.purging_org', '', true);
  RETURN v_out || pg_catalog.jsonb_build_object('users', v_gone, 'org', p_org, 'slug', v_slug);
END $fn$;
REVOKE ALL ON FUNCTION deedglass.purge_org(text, text) FROM PUBLIC;
