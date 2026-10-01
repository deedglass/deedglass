-- 20_schema.sql: what Deedglass keeps inside a customer database (S17, contract §4). Embedded in the
-- binary; deedglass connector postgres init emits each section whose objects are missing, inside its
-- one transaction, after SET LOCAL search_path = pg_catalog, pg_temp and SET LOCAL lock_timeout = '5s'.
-- The roles come first (the planner creates deedglass_owner, deedglass_agent and deedglass_witness).
--
-- Rules (CLAUDE.md): every object schema-qualified; REVOKE and GRANT in the same transaction as
-- CREATE; no EXCEPTION blocks; the function's search_path pinned to pg_catalog, pg_temp. The body of
-- deedglass.begin_action (the text between the $fn$ delimiters, as pg_proc.prosrc stores it) is pinned
-- in Go as setup.BeginActionBodySHA256; check fails when the live body differs.

-- @section schema
CREATE SCHEMA deedglass AUTHORIZATION deedglass_owner;
REVOKE ALL ON SCHEMA deedglass FROM PUBLIC;
GRANT USAGE ON SCHEMA deedglass TO deedglass_agent, deedglass_witness;

-- @section ledger
SET LOCAL ROLE deedglass_owner;
CREATE TABLE deedglass.ledger (
  action_id   text        PRIMARY KEY CHECK (action_id ~ '^act_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  org_id      text        NOT NULL CHECK (org_id ~ '^org_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  agent_id    text        NOT NULL CHECK (agent_id ~ '^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  intent_hash text        CHECK (intent_hash ~ '^sha256:[0-9a-f]{64}$'),
  sql_digest  text        NOT NULL CHECK (sql_digest ~ '^sha256:[0-9a-f]{64}$'),
  xid8        xid8        NOT NULL,
  created_at  timestamptz NOT NULL);
ALTER TABLE deedglass.ledger REPLICA IDENTITY FULL;
ALTER TABLE deedglass.ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE deedglass.ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY owner_all ON deedglass.ledger TO deedglass_owner USING (true) WITH CHECK (true);
CREATE POLICY witness_read ON deedglass.ledger FOR SELECT TO deedglass_witness USING (true);
REVOKE ALL ON deedglass.ledger FROM PUBLIC;
GRANT SELECT ON deedglass.ledger TO deedglass_witness;
RESET ROLE;

-- @section begin_action
SET LOCAL ROLE deedglass_owner;
CREATE OR REPLACE FUNCTION deedglass.begin_action(p_action_id text, p_org_id text, p_agent_id text,
                                                  p_intent_hash text, p_sql_digest text)
RETURNS TABLE (xid8 pg_catalog.xid8, marker_lsn pg_catalog.pg_lsn)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
SET TimeZone = 'UTC'
AS $fn$
#variable_conflict use_column
DECLARE v_xid pg_catalog.xid8; v_lsn pg_catalog.pg_lsn;
BEGIN
  IF pg_catalog.pg_current_xact_id_if_assigned() IS NOT NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'DG101', MESSAGE = 'deedglass: begin_action must be the first write of its transaction';
  END IF;
  IF pg_catalog.current_setting('session_replication_role') <> 'origin' THEN
    RAISE EXCEPTION USING ERRCODE = 'DG102', MESSAGE = 'deedglass: session_replication_role must be origin';
  END IF;
  IF pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION USING ERRCODE = 'DG103', MESSAGE = 'deedglass: read-only transaction';
  END IF;
  v_xid := pg_catalog.pg_current_xact_id();
  INSERT INTO deedglass.ledger (action_id, org_id, agent_id, intent_hash, sql_digest, xid8, created_at)
  VALUES (p_action_id, p_org_id, p_agent_id, p_intent_hash, p_sql_digest, v_xid, pg_catalog.clock_timestamp());
  v_lsn := pg_catalog.pg_logical_emit_message(true, 'deedglass', p_action_id);
  RETURN QUERY SELECT v_xid, v_lsn;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.begin_action(text, text, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.begin_action(text, text, text, text, text) TO deedglass_agent;
RESET ROLE;
