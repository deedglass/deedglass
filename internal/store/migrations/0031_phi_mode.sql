-- 0031_phi_mode.sql: HC-P09's PHI mode (orgs.settings.phi_mode). In a PHI-mode org, once an action
-- is sealed and its checks are done, its stored request, result, error message, policy input and
-- witness detail are replaced by ids and digests (internal/phi); the deed already holds digests
-- only. The digest columns (request_digest, result_digest, witness_detail_digest) keep the digests
-- of what was sent and seen, so the deeds stay checkable.
--
-- actions.phi: set at insert from the org's setting (actions_phi, below), so only PHI-mode actions
-- ever enter the small partial index the serve job reads. Turning PHI mode on also marks the org's
-- earlier actions (identity.UpdateOrg). actions.payload_minimized_at: when the payloads were
-- replaced.

ALTER TABLE deedglass.actions
  ADD COLUMN phi boolean NOT NULL DEFAULT false,
  ADD COLUMN payload_minimized_at timestamptz;

CREATE FUNCTION deedglass.actions_phi() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  NEW.phi := COALESCE((SELECT (o.settings ->> 'phi_mode') = 'true' FROM deedglass.orgs AS o WHERE o.id = NEW.org_id), false);
  RETURN NEW;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.actions_phi() FROM PUBLIC;
CREATE TRIGGER actions_phi BEFORE INSERT ON deedglass.actions FOR EACH ROW EXECUTE FUNCTION deedglass.actions_phi();

-- The serve job's queue: PHI-mode actions whose payloads are still stored.
CREATE INDEX actions_phi_pending ON deedglass.actions (org_id, sealed_at)
  WHERE phi AND payload_minimized_at IS NULL AND payload_pruned_at IS NULL;

GRANT UPDATE (phi, payload_minimized_at) ON deedglass.actions TO deedglass_app;

-- claim_phi_orgs: the orgs with a sealed PHI-mode action still holding its payloads (the serve
-- job's only cross-org read; ids only, like claim_observe_orgs).
CREATE FUNCTION deedglass.claim_phi_orgs(p_limit integer) RETURNS TABLE (org_id text) LANGUAGE sql STABLE SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT DISTINCT a.org_id::text FROM deedglass.actions AS a
   WHERE a.phi AND a.payload_minimized_at IS NULL AND a.payload_pruned_at IS NULL AND a.state = 'sealed'
   ORDER BY 1 LIMIT LEAST(GREATEST(p_limit, 1), 1000)
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_phi_orgs(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_phi_orgs(integer) TO deedglass_app;
