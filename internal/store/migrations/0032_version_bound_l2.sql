-- 0032_version_bound_l2.sql: HC-P05's Proven path for FHIR servers on Postgres (ADR-108). The
-- decoder reads a FHIR server's own database and binds a transaction to an action by the version
-- the server reported to that action (the action's execution evidence, version_key), not by a
-- ledger row and marker, which a FHIR server's transactions can never hold.
--
-- 1. sync_decoder_slots also keeps a row for an active fhir connector with witness.enabled.
-- 2. decoded_txs.class also allows request_id_reused (a version carrying an action's binding token
--    that the action never reported: reported, never bound) and version_tag_mismatch (the reported
--    version's row carries another request's tag: a gap, never bound). Frames stay immutable.
-- 3. Two partial indexes find an action of a connector (actions.connector, its name) by its
--    execution evidence: version_key ("Type/id/version") and binding_token (the 16-character
--    token the fhir connector sends as X-Request-ID).

ALTER TABLE deedglass.decoded_txs
  DROP CONSTRAINT decoded_txs_class_check,
  ADD CONSTRAINT decoded_txs_class_check CHECK (class IN ('bound', 'unattributed', 'forged_marker', 'forged_marker_nontx',
    'marker_count', 'marker_mismatch', 'foreign_ledger', 'publication_gap', 'missing', 'request_id_reused', 'version_tag_mismatch'));

CREATE INDEX actions_version_key ON deedglass.actions (org_id, connector, (execution_evidence ->> 'version_key'))
  WHERE execution_evidence ? 'version_key';
CREATE INDEX actions_binding_token ON deedglass.actions (org_id, connector, (execution_evidence ->> 'binding_token'))
  WHERE execution_evidence ? 'binding_token';

CREATE OR REPLACE FUNCTION deedglass.sync_decoder_slots() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  v_n integer := 0;
  v_k integer;
BEGIN
  INSERT INTO deedglass.decoder_slots AS s (org_id, connector_id, slot_name, status, status_since)
  SELECT c.org_id, c.id, 'deedglass_' || pg_catalog.lower(pg_catalog.substr(c.id, 5)), 'pending', pg_catalog.now()
    FROM deedglass.connectors AS c
   WHERE c.type IN ('postgres', 'fhir') AND c.deleted_at IS NULL AND c.status = 'active'
     AND (c.config -> 'witness' ->> 'enabled') = 'true'
  ON CONFLICT (org_id, connector_id) DO UPDATE
     SET status = 'pending', status_reason = NULL, status_detail = '{}', status_since = pg_catalog.now(),
         checkpoint_lsn = NULL
   WHERE s.status IN ('disabled', 'dropped') AND s.status_reason IS DISTINCT FROM 'dropping';
  GET DIAGNOSTICS v_k = ROW_COUNT;
  v_n := v_n + v_k;
  UPDATE deedglass.decoder_slots AS s
     SET status = 'disabled', status_reason = 'not_witnessed', status_detail = '{}', status_since = pg_catalog.now()
   WHERE s.status <> 'disabled'
     AND NOT EXISTS (SELECT 1 FROM deedglass.connectors AS c
                      WHERE c.org_id = s.org_id AND c.id = s.connector_id AND c.type IN ('postgres', 'fhir')
                        AND c.deleted_at IS NULL AND c.status = 'active'
                        AND (c.config -> 'witness' ->> 'enabled') = 'true');
  GET DIAGNOSTICS v_k = ROW_COUNT;
  RETURN v_n + v_k;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.sync_decoder_slots() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.sync_decoder_slots() TO deedglass_app;
