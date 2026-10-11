-- 0028_action_schedule.sql: S40's scheduled execution after approval.
--
-- POST /v1/actions may carry "schedule": {"not_before", "not_after"} (gateway mode, approval path
-- only). It is stored once, here, and never changes: the runtime role has no UPDATE grant on the
-- column, and the guard below refuses a change by any role (the same SQLSTATE as the state guard).
-- After the countersign, approval_resume runs at greatest(now, not_before); past not_after the
-- action is cancelled and sealed with schedule_missed. Nothing else reads it.
ALTER TABLE deedglass.actions ADD COLUMN schedule jsonb
  CHECK (schedule IS NULL OR (pg_catalog.jsonb_typeof(schedule) = 'object' AND pg_catalog.octet_length(schedule::text) <= 256));

CREATE FUNCTION deedglass.actions_schedule_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.schedule IS DISTINCT FROM OLD.schedule THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004',
      MESSAGE = pg_catalog.format('deedglass: action %s keeps the schedule it was created with', OLD.id),
      HINT = 'A schedule is fixed when the action is created (S40). Cancel it and submit a new action.';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.actions_schedule_guard() FROM PUBLIC;
CREATE TRIGGER actions_schedule_guard BEFORE UPDATE OF schedule ON deedglass.actions
  FOR EACH ROW EXECUTE FUNCTION deedglass.actions_schedule_guard();
