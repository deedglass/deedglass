-- 0022_undo.sql: what S23's undo needs.
--
-- A person starts an undo; actions need an agent, so every org gets a system agent named console
-- (agents.kind = 'system'), created on demand. An undo is an action like any other (tool
-- <connector>.undo, actions.undoes_action_id from 0005); at most one live undo per action is a
-- unique index, and an undo that ends without applying (denied, expired, cancelled, failed)
-- releases its slot with undo_released_at, set once at its seal. The plan an undo ran is kept in
-- undo_plans: written once by the executor, and only its payload is ever cleared (pruned with the
-- other payloads; the digest stays).

-- agents.kind --------------------------------------------------------------------------------------------
ALTER TABLE deedglass.agents ADD COLUMN kind text NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'system'));

-- actions: the one-live-undo slot ------------------------------------------------------------------------
ALTER TABLE deedglass.actions ADD COLUMN undo_released_at timestamptz;
GRANT UPDATE (undo_released_at) ON deedglass.actions TO deedglass_app;
CREATE UNIQUE INDEX actions_one_live_undo ON deedglass.actions (org_id, undoes_action_id)
  WHERE undoes_action_id IS NOT NULL AND undo_released_at IS NULL;

-- An undo's exact footprint (S21's witness_expect) holds every planned row with its values: up to
-- undo.max_rows rows, so its cap rises from 64 KiB to 8 MiB, the plan's own cap.
ALTER TABLE deedglass.actions DROP CONSTRAINT actions_witness_expect_check;
ALTER TABLE deedglass.actions ADD CONSTRAINT actions_witness_expect_check CHECK (witness_expect IS NULL
  OR (pg_catalog.jsonb_typeof(witness_expect) = 'object' AND pg_catalog.pg_column_size(witness_expect) <= 8388608));

-- undo_released_at is set once, never cleared or moved (DG001), and only on an undo action.
CREATE FUNCTION deedglass.actions_undo_release_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF OLD.undo_released_at IS NOT NULL AND NEW.undo_released_at IS DISTINCT FROM OLD.undo_released_at THEN
    RAISE EXCEPTION 'action %''s undo slot was already released', OLD.id USING ERRCODE = 'DG001';
  END IF;
  IF NEW.undo_released_at IS NOT NULL AND NEW.undoes_action_id IS NULL THEN
    RAISE EXCEPTION 'action % is not an undo', OLD.id USING ERRCODE = 'DG001';
  END IF;
  RETURN NEW;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.actions_undo_release_guard() FROM PUBLIC;
CREATE TRIGGER actions_undo_release_guard BEFORE UPDATE OF undo_released_at ON deedglass.actions
  FOR EACH ROW EXECUTE FUNCTION deedglass.actions_undo_release_guard();

-- undo_plans: the plan each undo ran ---------------------------------------------------------------------
CREATE TABLE deedglass.undo_plans (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  action_id deedglass.act_id NOT NULL,         -- the undo action
  undoes_action_id deedglass.act_id NOT NULL,  -- the action it undoes
  plan jsonb CHECK (plan IS NULL OR (pg_catalog.jsonb_typeof(plan) = 'object' AND pg_catalog.pg_column_size(plan) <= 8388608)),
  plan_digest deedglass.digest NOT NULL,
  row_count integer NOT NULL CHECK (row_count BETWEEN 1 AND 100000),
  tables text[] NOT NULL CHECK (pg_catalog.cardinality(tables) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, action_id),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id),
  FOREIGN KEY (org_id, undoes_action_id) REFERENCES deedglass.actions (org_id, id),
  CHECK (action_id <> undoes_action_id)
);
CREATE INDEX undo_plans_undoes ON deedglass.undo_plans (org_id, undoes_action_id);

-- Only the payload is ever cleared (pruning); every other change, and any delete, is DG001.
CREATE FUNCTION deedglass.undo_plans_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.plan IS NOT NULL
     OR NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.action_id IS DISTINCT FROM OLD.action_id
     OR NEW.undoes_action_id IS DISTINCT FROM OLD.undoes_action_id OR NEW.plan_digest IS DISTINCT FROM OLD.plan_digest
     OR NEW.row_count IS DISTINCT FROM OLD.row_count OR NEW.tables IS DISTINCT FROM OLD.tables
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'an undo plan never changes; only its payload is pruned' USING ERRCODE = 'DG001';
  END IF;
  RETURN NEW;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.undo_plans_guard() FROM PUBLIC;
CREATE TRIGGER undo_plans_guard BEFORE UPDATE ON deedglass.undo_plans FOR EACH ROW EXECUTE FUNCTION deedglass.undo_plans_guard();
CREATE TRIGGER undo_plans_no_delete BEFORE DELETE ON deedglass.undo_plans FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER undo_plans_no_delete_stmt BEFORE DELETE OR TRUNCATE ON deedglass.undo_plans
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();

ALTER TABLE deedglass.undo_plans ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.undo_plans FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.undo_plans FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.undo_plans TO deedglass_app;
GRANT UPDATE (plan) ON deedglass.undo_plans TO deedglass_app;
