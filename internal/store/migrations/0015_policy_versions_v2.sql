-- 0015_policy_versions_v2.sql: what policy versions and activations need beyond S02's tables (S12).
--
-- S02's 0003_policy.sql created policies, policy_versions (append-only) and policy_activations
-- (pa_ ids, the partial unique index that allows one live enforce and one live shadow per org).
-- That design stays. This file only:
--   1. renames policy_versions.compiled_hash to version_hash (the deed and API name) and
--      policy_versions.source to origin (the API's "source" is the YAML text), and lets origin be
--      'onboarding' (default-deny for a new org);
--   2. adds what the API shows of a version: source_hash, lang, rules_count, budgets_count,
--      warnings, and its author as admin_events records actors (user, api_key or system);
--   3. adds who ended an activation and the deed that sealed it, and widens the column-level
--      UPDATE grant to exactly those columns.
-- policy_versions is empty before S12 (nothing wrote it), so the new NOT NULL columns need no
-- backfill: on a database where it isn't empty this migration fails, and that is the report.

-- 1. Renames ------------------------------------------------------------------------------------------
ALTER TABLE deedglass.policy_versions RENAME COLUMN compiled_hash TO version_hash;
ALTER TABLE deedglass.policy_versions RENAME COLUMN source TO origin;
ALTER TABLE deedglass.policy_versions DROP CONSTRAINT policy_versions_source_check;
ALTER TABLE deedglass.policy_versions ADD CONSTRAINT policy_versions_origin_check
  CHECK (origin IN ('manual', 'suggestion', 'starter', 'onboarding'));

-- 2. Version details ----------------------------------------------------------------------------------
ALTER TABLE deedglass.policy_versions
  ADD COLUMN source_hash deedglass.digest NOT NULL,
  ADD COLUMN lang text NOT NULL DEFAULT 'deedglass.policy/v1' CHECK (lang = 'deedglass.policy/v1'),
  ADD COLUMN rules_count integer NOT NULL DEFAULT 0 CHECK (rules_count BETWEEN 0 AND 1000),
  ADD COLUMN budgets_count integer NOT NULL DEFAULT 0 CHECK (budgets_count BETWEEN 0 AND 1000),
  ADD COLUMN warnings jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (pg_catalog.jsonb_typeof(warnings) = 'array' AND pg_catalog.octet_length(warnings::text) <= 65536),
  ADD COLUMN author_type text NOT NULL CHECK (author_type IN ('user', 'api_key', 'system')),
  ADD COLUMN author_id text CHECK (author_id ~ '^(usr|key)_[0-7][0-9a-hjkmnp-tv-z]{25}$');
ALTER TABLE deedglass.policy_versions ADD CONSTRAINT policy_versions_author_check
  CHECK ((author_type = 'system') = (author_id IS NULL)
     AND (author_type <> 'user' OR author_id LIKE 'usr\_%')
     AND (author_type <> 'api_key' OR author_id LIKE 'key\_%'));
-- At most one version per policy with the same normalized content: pushing it again is a no-op.
CREATE UNIQUE INDEX policy_versions_content ON deedglass.policy_versions (policy_id, version_hash);

-- 3. Who ended an activation, and its deed ---------------------------------------------------------------
ALTER TABLE deedglass.policy_activations
  ADD COLUMN deactivated_by text CHECK (pg_catalog.length(deactivated_by) <= 64),
  ADD COLUMN deactivation_deed_id deedglass.deed_id;
ALTER TABLE deedglass.policy_activations ADD CONSTRAINT policy_activations_deactivation_deed_fk
  FOREIGN KEY (org_id, deactivation_deed_id) REFERENCES deedglass.deeds (org_id, id);
ALTER TABLE deedglass.policy_activations ADD CONSTRAINT policy_activations_ended_check
  CHECK ((deactivated_at IS NULL) = (deactivated_by IS NULL)
     AND (deactivated_at IS NULL) = (deactivation_deed_id IS NULL));
-- A live activation is never edited except to end it, and an ended one never changes again.
CREATE FUNCTION deedglass.policy_activation_end_only()
  RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF OLD.deactivated_at IS NOT NULL THEN
    RAISE EXCEPTION 'policy activation % has already ended', OLD.id USING ERRCODE = 'DG001';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.org_id IS DISTINCT FROM OLD.org_id
     OR NEW.policy_id IS DISTINCT FROM OLD.policy_id OR NEW.version_id IS DISTINCT FROM OLD.version_id
     OR NEW.mode IS DISTINCT FROM OLD.mode OR NEW.activated_by IS DISTINCT FROM OLD.activated_by
     OR NEW.activated_at IS DISTINCT FROM OLD.activated_at OR NEW.deed_id IS DISTINCT FROM OLD.deed_id
     OR NEW.reason IS DISTINCT FROM OLD.reason OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.deactivated_at IS NULL THEN
    RAISE EXCEPTION 'policy activation % can only be ended', OLD.id USING ERRCODE = 'DG001';
  END IF;
  RETURN NEW;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.policy_activation_end_only() FROM PUBLIC;
CREATE TRIGGER policy_activations_end_only BEFORE UPDATE ON deedglass.policy_activations
  FOR EACH ROW EXECUTE FUNCTION deedglass.policy_activation_end_only();
CREATE TRIGGER policy_activations_no_delete BEFORE DELETE OR TRUNCATE ON deedglass.policy_activations
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();

REVOKE UPDATE ON deedglass.policy_activations FROM deedglass_app;
GRANT UPDATE (deactivated_at, deactivated_by, deactivation_deed_id) ON deedglass.policy_activations TO deedglass_app;
