-- 0018_witness.sql: what S19's Witness needs beyond 0005, 0007 and 0016. The action records the
-- level its policy requires (verify:, recorded with the decision), and a witness job carries the
-- evidence a read-back needs after a restart (the statement's transaction id, the result's meta),
-- because the connector's evidence never goes into the actions row. 0007 already allows the job
-- kinds l1_readback and l2_match, and 0010 already indexes actions by verdict
-- (actions_verdict_time), so neither is repeated here.

ALTER TABLE deedglass.actions
  ADD COLUMN witness_required text CHECK (witness_required IN ('L0', 'L1', 'L2'));
GRANT UPDATE (witness_required) ON deedglass.actions TO deedglass_app;

-- At most 16 KiB of JSON: ids, digests and small numbers, never a payload or a secret.
ALTER TABLE deedglass.witness_jobs
  ADD COLUMN payload jsonb CHECK (payload IS NULL OR (pg_catalog.jsonb_typeof(payload) = 'object'
    AND pg_catalog.pg_column_size(payload) <= 16384));
REVOKE ALL ON deedglass.witness_jobs FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.witness_jobs TO deedglass_app;
