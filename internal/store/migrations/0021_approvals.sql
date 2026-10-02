-- 0021_approvals.sql: what S22's approvals and webhooks need beyond 0005, 0007 and 0009.
--
-- An approval is a person's countersign on one action. Its context snapshot (what the person saw)
-- and the snapshot's digest are written once and never change; its status leaves 'pending' exactly
-- once. Votes live one row per person in approval_votes, so four-eyes ("two different people")
-- is a primary key, not an array. Email links are one token per (approval, person), purpose
-- 'decide'. Webhooks keep their signing secret in a vault envelope (S16's A256GCM-ENV-1) and an
-- outbox row per (endpoint, event) holding the exact payload, so every retry sends the same bytes.
-- 0007 already allows the job kinds approval_notify and approval_resume.

-- approvals ------------------------------------------------------------------------------------------
ALTER TABLE deedglass.approvals
  ADD COLUMN agent_id deedglass.agt_id,
  ADD COLUMN tool text CHECK (pg_catalog.length(tool) <= 200),
  ADD COLUMN on_behalf_of text CHECK (pg_catalog.length(on_behalf_of) <= 256),
  ADD COLUMN policy_version_id deedglass.pv_id,
  ADD COLUMN notified_count integer NOT NULL DEFAULT 0 CHECK (notified_count >= 0),
  ADD COLUMN notified_at timestamptz,
  ADD CONSTRAINT approvals_context_shape CHECK (context IS NULL
    OR (pg_catalog.jsonb_typeof(context) = 'object' AND pg_catalog.octet_length(context::text) <= 65536)),
  ADD CONSTRAINT approvals_context_digest CHECK ((context IS NULL) = (context_digest IS NULL));

-- The pending inbox of one org, oldest expiry first (GET /v1/approvals?status=pending).
CREATE INDEX approvals_org_pending ON deedglass.approvals (org_id, expires_at, id) WHERE status = 'pending';

-- approvals_guard: status leaves 'pending' once; context and context_digest never change; a decided
-- approval never changes again (only the notification counters may move). While pending, the
-- approvers list may only grow (a non-final countersign of a four-eyes rule). DG006.
CREATE FUNCTION deedglass.approvals_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF NEW.context IS DISTINCT FROM OLD.context OR NEW.context_digest IS DISTINCT FROM OLD.context_digest THEN
    RAISE EXCEPTION 'an approval''s context never changes' USING ERRCODE = 'DG006';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.action_id IS DISTINCT FROM OLD.action_id
     OR NEW.rule_id IS DISTINCT FROM OLD.rule_id OR NEW.reason IS DISTINCT FROM OLD.reason
     OR NEW.approvers_required IS DISTINCT FROM OLD.approvers_required OR NEW.approver_spec IS DISTINCT FROM OLD.approver_spec
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.agent_id IS DISTINCT FROM OLD.agent_id
     OR NEW.tool IS DISTINCT FROM OLD.tool OR NEW.on_behalf_of IS DISTINCT FROM OLD.on_behalf_of
     OR NEW.policy_version_id IS DISTINCT FROM OLD.policy_version_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'an approval''s request never changes' USING ERRCODE = 'DG006';
  END IF;
  IF OLD.status <> 'pending' THEN
    IF NEW.status IS DISTINCT FROM OLD.status OR NEW.approved_by IS DISTINCT FROM OLD.approved_by
       OR NEW.decided_by IS DISTINCT FROM OLD.decided_by OR NEW.decided_at IS DISTINCT FROM OLD.decided_at
       OR NEW.decision_reason IS DISTINCT FROM OLD.decision_reason OR NEW.deed_id IS DISTINCT FROM OLD.deed_id THEN
      RAISE EXCEPTION 'approval % was already decided (%)', OLD.id, OLD.status USING ERRCODE = 'DG006';
    END IF;
    RETURN NEW;
  END IF;
  -- Pending: the approvers list only grows, keeping its order.
  IF pg_catalog.cardinality(NEW.approved_by) < pg_catalog.cardinality(OLD.approved_by)
     OR NEW.approved_by[1:pg_catalog.cardinality(OLD.approved_by)] IS DISTINCT FROM OLD.approved_by THEN
    RAISE EXCEPTION 'an approval''s countersigns are append-only' USING ERRCODE = 'DG006';
  END IF;
  RETURN NEW;
END
$fn$;
REVOKE ALL ON FUNCTION deedglass.approvals_guard() FROM PUBLIC;
CREATE TRIGGER approvals_guard BEFORE UPDATE ON deedglass.approvals FOR EACH ROW EXECUTE FUNCTION deedglass.approvals_guard();

-- approval_votes: one row per person per approval; four-eyes integrity is the primary key ---------------
CREATE TABLE deedglass.approval_votes (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  approval_id deedglass.apr_id NOT NULL,
  user_id deedglass.usr_id NOT NULL REFERENCES deedglass.users (id),
  vote text NOT NULL CHECK (vote IN ('approve', 'deny')),
  reason text CHECK (pg_catalog.char_length(reason) BETWEEN 1 AND 280),
  via text NOT NULL CHECK (via IN ('console', 'email_link', 'slack', 'cli_dev')),
  context_digest deedglass.digest NOT NULL,
  deed_id deedglass.deed_id NOT NULL,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, approval_id, user_id),
  FOREIGN KEY (org_id, approval_id) REFERENCES deedglass.approvals (org_id, id)
);
CREATE TRIGGER approval_votes_append_only BEFORE UPDATE OR DELETE ON deedglass.approval_votes
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER approval_votes_append_only_stmt BEFORE UPDATE OR DELETE OR TRUNCATE ON deedglass.approval_votes
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
ALTER TABLE deedglass.approval_votes ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.approval_votes FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.approval_votes FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.approval_votes TO deedglass_app;

-- approval_tokens: one emailed link per (approval, person); S22 issues only purpose 'decide' ----------
ALTER TABLE deedglass.approval_tokens
  ADD COLUMN voided_at timestamptz,
  ADD COLUMN previewed_at timestamptz,
  DROP CONSTRAINT approval_tokens_purpose_check,
  ADD CONSTRAINT approval_tokens_purpose_check CHECK (purpose IN ('approve', 'deny', 'decide'));
CREATE INDEX approval_tokens_open ON deedglass.approval_tokens (org_id, approval_id, user_id)
  WHERE used_at IS NULL AND voided_at IS NULL;

-- The only lookup of a link before its org is known (POST /v1/approval-links/*). Everything after it
-- runs in an org transaction with row-level security on. Same rules as 0012's preauth_* functions.
CREATE FUNCTION deedglass.preauth_approval_token(p_hash bytea)
  RETURNS TABLE (org_id text, token_id text, approval_id text, user_id text, expires_at timestamptz,
                 used_at timestamptz, voided_at timestamptz)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT t.org_id, t.id, t.approval_id, t.user_id, t.expires_at, t.used_at, t.voided_at
  FROM deedglass.approval_tokens AS t
  WHERE t.token_hash = p_hash AND pg_catalog.octet_length(p_hash) = 32
$fn$;
REVOKE ALL ON FUNCTION deedglass.preauth_approval_token(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.preauth_approval_token(bytea) TO deedglass_app;

-- actions: find the action an approval paused (deedglass.check_approval, DL-029) -------------------
CREATE INDEX actions_approval ON deedglass.actions (org_id, approval_id) WHERE approval_id IS NOT NULL;

-- webhooks: the S16 vault envelope for the secret, the version, health ------------------------------
ALTER TABLE deedglass.webhooks
  ADD COLUMN secret_envelope bytea CHECK (secret_envelope IS NULL OR pg_catalog.octet_length(secret_envelope) BETWEEN 30 AND 4096),
  ADD COLUMN secret_hint text CHECK (pg_catalog.length(secret_hint) <= 16),
  ADD COLUMN kek_id text CHECK (pg_catalog.length(kek_id) <= 64),
  ADD COLUMN api_version text NOT NULL DEFAULT '2026-10-01' CHECK (api_version ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'),
  ADD COLUMN failing_since timestamptz,
  ADD COLUMN last_success_at timestamptz,
  ALTER COLUMN secret_ciphertext DROP NOT NULL,
  ALTER COLUMN secret_nonce DROP NOT NULL,
  ADD CONSTRAINT webhooks_secret_present CHECK (secret_envelope IS NOT NULL OR (secret_ciphertext IS NOT NULL AND secret_nonce IS NOT NULL));

-- webhook_deliveries: the outbox. One row per (endpoint, event), the payload stored once ---------------
ALTER TABLE deedglass.webhook_deliveries
  ADD COLUMN payload jsonb CHECK (payload IS NULL OR pg_catalog.pg_column_size(payload) <= 16384),
  ADD COLUMN locked_by text CHECK (pg_catalog.length(locked_by) <= 128),
  ADD COLUMN locked_until timestamptz,
  ADD CONSTRAINT webhook_deliveries_event_uq UNIQUE (webhook_id, event_id);

-- The one cross-org read of the outbox: lease due deliveries to one worker (FOR UPDATE SKIP LOCKED),
-- at most 4 in flight per endpoint, ids only. Delivery then runs in an org transaction.
CREATE FUNCTION deedglass.claim_webhook_deliveries(p_worker text, p_limit integer, p_lease interval)
RETURNS TABLE (id text, org_id text, webhook_id text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH busy AS (
    SELECT b.webhook_id, pg_catalog.count(*) AS n FROM deedglass.webhook_deliveries AS b
     WHERE b.status = 'pending' AND b.locked_until > pg_catalog.now()
     GROUP BY b.webhook_id),
  due AS (
    SELECT d.id, d.webhook_id, d.next_attempt_at,
           pg_catalog.row_number() OVER (PARTITION BY d.webhook_id ORDER BY d.next_attempt_at, d.id) AS rn
      FROM deedglass.webhook_deliveries AS d
     WHERE d.status = 'pending' AND d.next_attempt_at <= pg_catalog.now()
       AND (d.locked_until IS NULL OR d.locked_until < pg_catalog.now())),
  c AS (
    SELECT d.id FROM deedglass.webhook_deliveries AS d
      JOIN due ON due.id = d.id
      LEFT JOIN busy ON busy.webhook_id = due.webhook_id
     WHERE due.rn <= 4 - COALESCE(busy.n, 0)
     ORDER BY due.next_attempt_at, d.id
     LIMIT LEAST(GREATEST(p_limit, 1), 100)
     FOR UPDATE OF d SKIP LOCKED)
  UPDATE deedglass.webhook_deliveries AS d
     SET locked_by = p_worker, locked_until = pg_catalog.now() + p_lease
    FROM c WHERE d.id = c.id
  RETURNING d.id::text, d.org_id::text, d.webhook_id::text;
$fn$;
REVOKE ALL ON FUNCTION deedglass.claim_webhook_deliveries(text, integer, interval) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.claim_webhook_deliveries(text, integer, interval) TO deedglass_app;
