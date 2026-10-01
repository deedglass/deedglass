~ begin_action · replace (body changed: expected sha256:2f3f…7bd4, found sha256:abab…abab · owner postgres)
plan sha256:1faee0dcce3b7d68e9354e2017a32d4dd3179b1d89f234c8e502ca70956112c8

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- ~ begin_action · replace (body changed: expected sha256:2f3f…7bd4, found sha256:abab…abab · owner postgres)
ALTER FUNCTION deedglass.begin_action(text,text,text,text,text) OWNER TO deedglass_owner;
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

COMMIT;
