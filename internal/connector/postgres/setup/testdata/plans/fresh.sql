+ role deedglass_owner · NOLOGIN
+ role deedglass_agent · password generated at apply
+ role deedglass_witness · REPLICATION · password generated at apply
+ schema deedglass · ledger · begin_action (sha256:2f3f…7bd4)
+ grants · read public (5 tables) · write public.orders, public.refunds
+ replica identity full · 3 watched tables · brief lock on each
+ publication deedglass_pub · 4 tables
plan sha256:19e3debfa0653d3103635aafb572b97c19bd5384f696441fa7f63912b2204df0

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '5s';

-- + role deedglass_owner · NOLOGIN
CREATE ROLE deedglass_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

-- + role deedglass_agent · password generated at apply
CREATE ROLE deedglass_agent LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
ALTER ROLE deedglass_agent SET statement_timeout = '5s';
ALTER ROLE deedglass_agent SET lock_timeout = '2s';
ALTER ROLE deedglass_agent SET idle_in_transaction_session_timeout = '10s';
-- Credentials for deedglass_agent are generated at apply and never written to this file.

-- + role deedglass_witness · REPLICATION · password generated at apply
CREATE ROLE deedglass_witness LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT REPLICATION;
ALTER ROLE deedglass_witness SET TimeZone = 'UTC';
ALTER ROLE deedglass_witness SET DateStyle = 'ISO, YMD';
ALTER ROLE deedglass_witness SET IntervalStyle = 'postgres';
ALTER ROLE deedglass_witness SET bytea_output = 'hex';
ALTER ROLE deedglass_witness SET extra_float_digits = '3';
-- Credentials for deedglass_witness are generated at apply and never written to this file.

-- + schema deedglass · ledger · begin_action (sha256:2f3f…7bd4)
CREATE SCHEMA deedglass AUTHORIZATION deedglass_owner;
REVOKE ALL ON SCHEMA deedglass FROM PUBLIC;
GRANT USAGE ON SCHEMA deedglass TO deedglass_agent, deedglass_witness;
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

-- + grants · read public (5 tables) · write public.orders, public.refunds
GRANT USAGE ON SCHEMA "public" TO deedglass_agent;
GRANT SELECT ON ALL TABLES IN SCHEMA "public" TO deedglass_agent;
GRANT INSERT, UPDATE, DELETE ON TABLE "public"."orders" TO deedglass_agent;
GRANT INSERT, UPDATE, DELETE ON TABLE "public"."refunds" TO deedglass_agent;
GRANT EXECUTE ON FUNCTION "public"."ship_order"(bigint) TO deedglass_agent;
GRANT USAGE ON SCHEMA "public" TO deedglass_witness;
GRANT SELECT ON TABLE "public"."customers" TO deedglass_witness;
GRANT SELECT ON TABLE "public"."orders" TO deedglass_witness;
GRANT SELECT ON TABLE "public"."refunds" TO deedglass_witness;

-- + replica identity full · 3 watched tables · brief lock on each
ALTER TABLE "public"."customers" REPLICA IDENTITY FULL;
ALTER TABLE "public"."orders" REPLICA IDENTITY FULL;
ALTER TABLE "public"."refunds" REPLICA IDENTITY FULL;

-- + publication deedglass_pub · 4 tables
CREATE PUBLICATION deedglass_pub FOR TABLE deedglass.ledger, "public"."customers", "public"."orders", "public"."refunds" WITH (publish = 'insert, update, delete, truncate', publish_generated_columns = stored);

COMMIT;
