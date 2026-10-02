-- 0019_decoder.sql: what S20's decoder needs beyond 0007, plus the connector's execution evidence
-- on the action (ADR-044, the DL-031 carry-over).
--
-- decoded_txs (0007) becomes the store of frames: one row per committed transaction read from a
-- watched database's change stream, keyed by (connector, system identifier, timeline, commit LSN)
-- so a re-delivered transaction is stored once. The class says how the frame relates to a
-- Deedglass action (S20 §3.7; ADR-003). A frame is evidence: it is never updated, and only
-- maint_prune_decoded_txs deletes old frames. action_id loses its foreign key: a frame records
-- what the database said, and a forged or foreign ledger row can name an action this Deedglass
-- never created; S21 cross-checks every frame against the Gate's own record.

ALTER TABLE deedglass.decoded_txs
  DROP CONSTRAINT decoded_txs_org_id_action_id_fkey,
  DROP CONSTRAINT decoded_txs_connector_id_commit_lsn_key,
  ALTER COLUMN xid DROP NOT NULL,
  ALTER COLUMN commit_lsn DROP NOT NULL,
  ALTER COLUMN end_lsn DROP NOT NULL,
  ALTER COLUMN commit_time DROP NOT NULL,
  ADD COLUMN system_id text NOT NULL CHECK (system_id ~ '^[0-9]{1,20}$'),
  ADD COLUMN timeline_id integer NOT NULL CHECK (timeline_id >= 1),
  ADD COLUMN class text NOT NULL CHECK (class IN ('bound', 'unattributed', 'forged_marker', 'forged_marker_nontx',
    'marker_count', 'marker_mismatch', 'foreign_ledger', 'publication_gap', 'missing')),
  ADD COLUMN marker_lsn pg_lsn,
  ADD COLUMN ledger jsonb CHECK (ledger IS NULL OR pg_catalog.jsonb_typeof(ledger) = 'array'),
  ADD COLUMN markers text[] NOT NULL DEFAULT '{}' CHECK (pg_catalog.cardinality(markers) <= 64),
  ADD COLUMN tables jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(tables) = 'object'),
  ADD COLUMN counts jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(counts) = 'object'),
  ADD COLUMN changes_digest deedglass.digest,
  ADD COLUMN nontx boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT decoded_txs_missing_shape CHECK ((class = 'missing') = (commit_lsn IS NULL)),
  ADD CONSTRAINT decoded_txs_missing_action CHECK (class <> 'missing' OR action_id IS NOT NULL),
  ADD CONSTRAINT decoded_txs_flags_len CHECK (pg_catalog.cardinality(flags) <= 64),
  ADD CONSTRAINT decoded_txs_nontx_class CHECK (nontx = (class = 'forged_marker_nontx'));
COMMENT ON COLUMN deedglass.decoded_txs.marker_lsn IS 'Evidence only: never compared with commit_lsn (ADR-003 point 5).';
COMMENT ON COLUMN deedglass.decoded_txs.change_count IS 'Every row change in the frame, including those past the image cap.';
-- nontx: a non-transactional message's LSN is the END of its record, which can equal the next
-- record's start, including another transaction's commit LSN; the flag keeps the two apart.
CREATE UNIQUE INDEX decoded_txs_frame_uq ON deedglass.decoded_txs (connector_id, system_id, timeline_id, commit_lsn, nontx)
  WHERE commit_lsn IS NOT NULL;
CREATE UNIQUE INDEX decoded_txs_missing_uq ON deedglass.decoded_txs (connector_id, action_id) WHERE class = 'missing';
CREATE INDEX decoded_txs_recent ON deedglass.decoded_txs (org_id, connector_id, created_at DESC);
CREATE INDEX decoded_txs_prune ON deedglass.decoded_txs (created_at);
-- Frames are evidence: UPDATE and TRUNCATE raise DG001 (row and statement triggers), and the
-- runtime role has no DELETE; old frames go through maint_prune_decoded_txs only.
CREATE TRIGGER decoded_txs_no_update BEFORE UPDATE ON deedglass.decoded_txs
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER decoded_txs_no_update_stmt BEFORE UPDATE OR TRUNCATE ON deedglass.decoded_txs
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
REVOKE ALL ON deedglass.decoded_txs FROM PUBLIC;
REVOKE DELETE ON deedglass.decoded_txs FROM deedglass_app;
GRANT SELECT, INSERT ON deedglass.decoded_txs TO deedglass_app;

-- decoder_slots (0007): one row per witnessed Postgres connector, its lease, positions and health.
ALTER TABLE deedglass.decoder_slots
  ADD COLUMN publication text NOT NULL DEFAULT 'deedglass_pub' CHECK (publication ~ '^[a-z_][a-z0-9_]{0,62}$'),
  ADD COLUMN slot_state text NOT NULL DEFAULT 'none' CHECK (slot_state IN ('none', 'created')),
  ADD COLUMN status_reason text CHECK (status_reason ~ '^[a-z_]{1,48}$'),
  ADD COLUMN status_detail jsonb NOT NULL DEFAULT '{}'
    CHECK (pg_catalog.jsonb_typeof(status_detail) = 'object' AND pg_catalog.pg_column_size(status_detail) <= 4096),
  ADD COLUMN status_since timestamptz NOT NULL DEFAULT pg_catalog.now(),
  ADD COLUMN system_id text CHECK (system_id ~ '^[0-9]{1,20}$'),
  ADD COLUMN timeline_id integer CHECK (timeline_id >= 1),
  ADD COLUMN server_version_num integer CHECK (server_version_num >= 0),
  ADD COLUMN checkpoint_lsn pg_lsn,
  ADD COLUMN confirmed_flush_lsn pg_lsn,
  ADD COLUMN current_wal_lsn pg_lsn,
  ADD COLUMN retained_bytes bigint CHECK (retained_bytes >= 0),
  ADD COLUMN wal_status text CHECK (wal_status ~ '^[a-z]{1,16}$'),
  ADD COLUMN last_commit_lsn pg_lsn,
  ADD COLUMN last_heartbeat_at timestamptz,
  ADD COLUMN lag_seconds double precision CHECK (lag_seconds >= 0),
  ADD COLUMN frames jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(frames) = 'object'),
  ADD COLUMN shapes jsonb NOT NULL DEFAULT '{}'
    CHECK (pg_catalog.jsonb_typeof(shapes) = 'object' AND pg_catalog.pg_column_size(shapes) <= 65536),
  ADD COLUMN watching text[] NOT NULL DEFAULT '{}',
  ADD COLUMN reconcile_watermark timestamptz,
  ADD COLUMN slot_created_at timestamptz,
  ADD COLUMN leased_by text CHECK (pg_catalog.length(leased_by) <= 128),
  ADD COLUMN lease_until timestamptz;
CREATE UNIQUE INDEX decoder_slots_connector ON deedglass.decoder_slots (connector_id);
REVOKE ALL ON deedglass.decoder_slots FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.decoder_slots TO deedglass_app;

-- claim_decoder_leases: the supervisors' one cross-org read of decoder_slots. It leases (or renews
-- for the same worker) the rows that need a runner: every witnessed connector, and an unwitnessed
-- one whose slot still exists (its runner drops it). Rows locked by a runner's checkpoint write are
-- skipped this round (SKIP LOCKED) and renewed on the next; a runner also checks its lease in that
-- very write, so an expired lease can never move a checkpoint. Ids only.
CREATE FUNCTION deedglass.claim_decoder_leases(p_worker text, p_lease interval, p_limit integer)
RETURNS TABLE (org_id text, connector_id text, slot_name text, status text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH c AS (
    SELECT s.org_id, s.connector_id FROM deedglass.decoder_slots AS s
     WHERE (s.status NOT IN ('disabled', 'dropped') OR (s.status = 'disabled' AND s.slot_state = 'created'))
       AND (s.leased_by = p_worker OR s.lease_until IS NULL OR s.lease_until < pg_catalog.now())
     ORDER BY s.connector_id
     LIMIT LEAST(GREATEST(p_limit, 1), 1000)
     FOR UPDATE SKIP LOCKED)
  UPDATE deedglass.decoder_slots AS s
     SET leased_by = p_worker, lease_until = pg_catalog.now() + p_lease
    FROM c WHERE s.org_id = c.org_id AND s.connector_id = c.connector_id
  RETURNING s.org_id::text, s.connector_id::text, s.slot_name, s.status;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_decoder_leases(text, interval, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_decoder_leases(text, interval, integer) TO deedglass_app;

-- sync_decoder_slots: keeps one row per witnessed Postgres connector. A connector that is active,
-- live and has witness.enabled = true gets a pending row (a disabled or dropped row becomes
-- pending again: a new slot, a new baseline; the reconcile watermark stays, so actions in the gap
-- are found and recorded as missing); every other row becomes disabled. It returns how many rows
-- changed. The slot name is decided here once and never recomputed.
CREATE FUNCTION deedglass.sync_decoder_slots() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  v_n integer := 0;
  v_k integer;
BEGIN
  INSERT INTO deedglass.decoder_slots AS s (org_id, connector_id, slot_name, status, status_since)
  SELECT c.org_id, c.id, 'deedglass_' || pg_catalog.lower(pg_catalog.substr(c.id, 5)), 'pending', pg_catalog.now()
    FROM deedglass.connectors AS c
   WHERE c.type = 'postgres' AND c.deleted_at IS NULL AND c.status = 'active'
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
                      WHERE c.org_id = s.org_id AND c.id = s.connector_id AND c.type = 'postgres'
                        AND c.deleted_at IS NULL AND c.status = 'active'
                        AND (c.config -> 'witness' ->> 'enabled') = 'true');
  GET DIAGNOSTICS v_k = ROW_COUNT;
  RETURN v_n + v_k;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.sync_decoder_slots() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.sync_decoder_slots() TO deedglass_app;

-- maint_prune_decoded_txs: decoder.retention. Deletes at most p_limit frames created before
-- p_before, except unattributed and forged_* frames, which stay until p_keep_before (30 days), and
-- returns how many it deleted. The one way a frame leaves the table.
CREATE FUNCTION deedglass.maint_prune_decoded_txs(p_before timestamptz, p_keep_before timestamptz, p_limit integer)
RETURNS integer
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH d AS (
    DELETE FROM deedglass.decoded_txs AS t
     WHERE t.id IN (SELECT x.id FROM deedglass.decoded_txs AS x
                     WHERE x.created_at < LEAST(p_before, pg_catalog.now() - interval '1 hour')
                       AND (x.class NOT IN ('unattributed', 'forged_marker', 'forged_marker_nontx')
                            OR x.created_at < LEAST(p_keep_before, pg_catalog.now() - interval '1 hour'))
                     ORDER BY x.created_at
                     LIMIT LEAST(GREATEST(p_limit, 1), 10000))
    RETURNING 1)
  SELECT pg_catalog.count(*)::integer FROM d;
$fn$;
REVOKE ALL ON FUNCTION deedglass.maint_prune_decoded_txs(timestamptz, timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.maint_prune_decoded_txs(timestamptz, timestamptz, integer) TO deedglass_app;

-- The connector's execution evidence (ADR-044): what the connector reported about where its
-- write landed, for the Witness to cross-check against the decoded frame (Postgres: xid8,
-- marker_lsn, sql_digest, command_tag). Written once, with the execution; ids, positions and
-- digests only, at most 1 KiB, never a value from the customer's rows.
ALTER TABLE deedglass.actions
  ADD COLUMN execution_evidence jsonb CHECK (execution_evidence IS NULL
    OR (pg_catalog.jsonb_typeof(execution_evidence) = 'object' AND pg_catalog.pg_column_size(execution_evidence) <= 1024));
GRANT UPDATE (execution_evidence) ON deedglass.actions TO deedglass_app;
