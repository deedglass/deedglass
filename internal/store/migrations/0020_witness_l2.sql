-- 0020_witness_l2.sql: what S21's L2 witness needs beyond 0007, 0018 and 0019.
--
-- An action may carry an explicit footprint (witness_expect): the rows a Gate-planned statement is
-- expected to touch, which S23's undo writes before it runs. A sealed action's later L2 verdict is
-- an amendment deed (contract §5, §7: a deed is never edited); witness_amendments counts them, at
-- most three per action. 0007 already allows the job kind l2_match, and 0019 already gives the
-- decoded frames and the execution evidence, so neither is repeated here.

ALTER TABLE deedglass.actions
  ADD COLUMN witness_expect jsonb CHECK (witness_expect IS NULL
    OR (pg_catalog.jsonb_typeof(witness_expect) = 'object' AND pg_catalog.pg_column_size(witness_expect) <= 65536)),
  ADD COLUMN witness_amendments integer NOT NULL DEFAULT 0 CHECK (witness_amendments BETWEEN 0 AND 3);
GRANT UPDATE (witness_expect, witness_amendments) ON deedglass.actions TO deedglass_app;

-- The L2 waits of one connector, found when its slot is lost (SlotLost): pending l2_match jobs.
CREATE INDEX witness_jobs_kind ON deedglass.witness_jobs (org_id, kind);
