-- 0005_actions.sql: actions, action events, approvals, approval tokens, idempotency keys, the state guard.

-- actions: one tool call through the Gate. request/result are payloads that retention prunes;
-- their digests stay. Column names are fixed here for S06 and S13 (witness_verdict, witness_level,
-- shadow_version_id, …); later sessions add columns, they never rename these.
CREATE TABLE deedglass.actions (
  id deedglass.act_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  agent_id deedglass.agt_id NOT NULL,
  api_key_id deedglass.key_id,
  on_behalf_of text CHECK (pg_catalog.length(on_behalf_of) <= 256),
  session_ref text CHECK (pg_catalog.length(session_ref) <= 256),
  idempotency_key text CHECK (pg_catalog.length(idempotency_key) BETWEEN 1 AND 255),
  mode text NOT NULL CHECK (mode IN ('gateway', 'proxy')),
  tool text NOT NULL CHECK (pg_catalog.length(tool) BETWEEN 1 AND 200),
  connector_id deedglass.con_id,
  request jsonb,
  request_digest deedglass.digest NOT NULL,
  intent_hash deedglass.digest,
  intent_signed boolean NOT NULL DEFAULT false,
  state text NOT NULL DEFAULT 'received' CHECK (state IN ('received', 'authenticated', 'evaluated', 'denied',
    'pending_approval', 'executing', 'executed', 'failed', 'witnessing', 'verified', 'mismatch', 'unverifiable',
    'sealed', 'expired', 'cancelled')),
  decision text CHECK (decision IN ('allow', 'deny', 'require_approval')),
  decision_rule_id text CHECK (pg_catalog.length(decision_rule_id) <= 200),
  decision_reason text CHECK (pg_catalog.length(decision_reason) <= 2000),
  policy_version_id deedglass.pv_id,
  shadow_decision text CHECK (shadow_decision IN ('allow', 'deny', 'require_approval')),
  shadow_rule_id text CHECK (pg_catalog.length(shadow_rule_id) <= 200),
  shadow_version_id deedglass.pv_id,
  approval_id deedglass.apr_id,
  result jsonb,
  result_digest deedglass.digest,
  error_code text CHECK (error_code ~ '^[a-z][a-z0-9_]{0,63}$'),
  witness_level text CHECK (witness_level IN ('L0', 'L1', 'L2')),
  witness_verdict text CHECK (witness_verdict IN ('VERIFIED', 'MISMATCH', 'UNVERIFIABLE')),
  witness_method text CHECK (pg_catalog.length(witness_method) <= 100),
  witness_detail jsonb,
  witness_detail_digest deedglass.digest,
  cost_amount bigint CHECK (cost_amount >= 0),       -- integer minor units (contract §8)
  cost_currency deedglass.currency,
  latency_ms integer CHECK (latency_ms >= 0),
  deed_id deedglass.deed_id,                         -- foreign key added in 0006 (deeds)
  undoes_action_id deedglass.act_id,
  payload_pruned_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),   -- = received
  authenticated_at timestamptz,
  evaluated_at timestamptz,
  denied_at timestamptz,
  pending_approval_at timestamptz,
  executing_at timestamptz,
  executed_at timestamptz,
  failed_at timestamptz,
  witnessing_at timestamptz,
  verified_at timestamptz,
  mismatch_at timestamptz,
  unverifiable_at timestamptz,
  sealed_at timestamptz,
  expired_at timestamptz,
  cancelled_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, agent_id) REFERENCES deedglass.agents (org_id, id),
  FOREIGN KEY (org_id, api_key_id) REFERENCES deedglass.api_keys (org_id, id),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id),
  FOREIGN KEY (org_id, policy_version_id) REFERENCES deedglass.policy_versions (org_id, id),
  FOREIGN KEY (org_id, shadow_version_id) REFERENCES deedglass.policy_versions (org_id, id),
  FOREIGN KEY (org_id, undoes_action_id) REFERENCES deedglass.actions (org_id, id),
  CHECK ((cost_amount IS NULL) = (cost_currency IS NULL)),
  CHECK (undoes_action_id IS NULL OR undoes_action_id <> id),
  -- A row in a state carries that state's timestamp (received is created_at).
  CONSTRAINT actions_authenticated_at    CHECK (state <> 'authenticated'    OR authenticated_at IS NOT NULL),
  CONSTRAINT actions_evaluated_at        CHECK (state <> 'evaluated'        OR evaluated_at IS NOT NULL),
  CONSTRAINT actions_denied_at           CHECK (state <> 'denied'           OR denied_at IS NOT NULL),
  CONSTRAINT actions_pending_approval_at CHECK (state <> 'pending_approval' OR pending_approval_at IS NOT NULL),
  CONSTRAINT actions_executing_at        CHECK (state <> 'executing'        OR executing_at IS NOT NULL),
  CONSTRAINT actions_executed_at         CHECK (state <> 'executed'         OR executed_at IS NOT NULL),
  CONSTRAINT actions_failed_at           CHECK (state <> 'failed'           OR failed_at IS NOT NULL),
  CONSTRAINT actions_witnessing_at       CHECK (state <> 'witnessing'       OR witnessing_at IS NOT NULL),
  CONSTRAINT actions_verified_at         CHECK (state <> 'verified'         OR verified_at IS NOT NULL),
  CONSTRAINT actions_mismatch_at         CHECK (state <> 'mismatch'         OR mismatch_at IS NOT NULL),
  CONSTRAINT actions_unverifiable_at     CHECK (state <> 'unverifiable'     OR unverifiable_at IS NOT NULL),
  CONSTRAINT actions_sealed_at           CHECK (state <> 'sealed'           OR sealed_at IS NOT NULL),
  CONSTRAINT actions_expired_at          CHECK (state <> 'expired'          OR expired_at IS NOT NULL),
  CONSTRAINT actions_cancelled_at        CHECK (state <> 'cancelled'        OR cancelled_at IS NOT NULL)
);
CREATE UNIQUE INDEX actions_idem ON deedglass.actions (org_id, agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX actions_org_time   ON deedglass.actions (org_id, created_at DESC, id DESC);
CREATE INDEX actions_agent_time ON deedglass.actions (org_id, agent_id, created_at DESC, id DESC);
CREATE INDEX actions_open       ON deedglass.actions (state, updated_at) WHERE state <> 'sealed';
CREATE TRIGGER actions_touch BEFORE UPDATE ON deedglass.actions FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();

-- The state guard mirrors internal/gate/state.go: exactly 22 edges (contract §5). A new action
-- starts in received. An UPDATE that keeps the state is a no-op, not a transition. There is no
-- executing → unverifiable edge: an honest unknown goes executing → executed (unknown) →
-- witnessing → unverifiable.
CREATE FUNCTION deedglass.actions_state_guard() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'received' THEN
      RAISE EXCEPTION USING ERRCODE = 'DG004',
        MESSAGE = pg_catalog.format('deedglass: action %s must start in received, not %s', NEW.id, NEW.state);
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.state = OLD.state THEN RETURN NEW; END IF;
  IF (OLD.state, NEW.state) NOT IN (VALUES
      ('received', 'authenticated'), ('authenticated', 'evaluated'),
      ('evaluated', 'denied'), ('evaluated', 'pending_approval'), ('evaluated', 'executing'),
      ('pending_approval', 'executing'), ('pending_approval', 'denied'), ('pending_approval', 'expired'), ('pending_approval', 'cancelled'),
      ('denied', 'sealed'), ('expired', 'sealed'), ('cancelled', 'sealed'),
      ('executing', 'executed'), ('executing', 'failed'), ('executed', 'witnessing'),
      ('witnessing', 'verified'), ('witnessing', 'mismatch'), ('witnessing', 'unverifiable'),
      ('verified', 'sealed'), ('mismatch', 'sealed'), ('unverifiable', 'sealed'), ('failed', 'sealed')) THEN
    RAISE EXCEPTION USING ERRCODE = 'DG004',
      MESSAGE = pg_catalog.format('deedglass: action %s cannot move from %s to %s', OLD.id, OLD.state, NEW.state),
      HINT = 'The allowed edges are in internal/gate/state.go (contract §5).';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION deedglass.actions_state_guard() FROM PUBLIC;
CREATE TRIGGER actions_state_guard BEFORE INSERT OR UPDATE OF state ON deedglass.actions
  FOR EACH ROW EXECUTE FUNCTION deedglass.actions_state_guard();

ALTER TABLE deedglass.actions ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.actions FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.actions FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.actions TO deedglass_app;
-- Every column except the ones fixed at creation (who, what, which key, which request).
GRANT UPDATE (request, state, decision, decision_rule_id, decision_reason, policy_version_id, shadow_decision,
  shadow_rule_id, shadow_version_id, approval_id, result, result_digest, error_code, witness_level,
  witness_verdict, witness_method, witness_detail, witness_detail_digest, cost_amount, cost_currency, latency_ms,
  deed_id, undoes_action_id, payload_pruned_at, authenticated_at, evaluated_at, denied_at, pending_approval_at,
  executing_at, executed_at, failed_at, witnessing_at, verified_at, mismatch_at, unverifiable_at, sealed_at,
  expired_at, cancelled_at, updated_at) ON deedglass.actions TO deedglass_app;

-- action_events: append-only ----------------------------------------------------------------------
CREATE TABLE deedglass.action_events (
  id deedglass.evt_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  action_id deedglass.act_id NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  from_state text CHECK (from_state IN ('received', 'authenticated', 'evaluated', 'denied', 'pending_approval',
    'executing', 'executed', 'failed', 'witnessing', 'verified', 'mismatch', 'unverifiable', 'sealed', 'expired', 'cancelled')),
  to_state text NOT NULL CHECK (to_state IN ('received', 'authenticated', 'evaluated', 'denied', 'pending_approval',
    'executing', 'executed', 'failed', 'witnessing', 'verified', 'mismatch', 'unverifiable', 'sealed', 'expired', 'cancelled')),
  actor text NOT NULL CHECK (actor = 'system' OR actor ~ '^(usr|agt)_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  request_id deedglass.req_id,
  data jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(data) = 'object'),
  at timestamptz NOT NULL DEFAULT pg_catalog.clock_timestamp(),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (action_id, seq),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id)
);
CREATE INDEX action_events_org_time ON deedglass.action_events (org_id, at, id);
CREATE TRIGGER action_events_append_only BEFORE UPDATE OR DELETE ON deedglass.action_events
  FOR EACH ROW EXECUTE FUNCTION deedglass.forbid_mutation();
CREATE TRIGGER action_events_append_only_stmt BEFORE UPDATE OR DELETE OR TRUNCATE ON deedglass.action_events
  FOR EACH STATEMENT EXECUTE FUNCTION deedglass.forbid_mutation();
ALTER TABLE deedglass.action_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.action_events FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.action_events FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.action_events TO deedglass_app;

-- approvals ----------------------------------------------------------------------------------------
CREATE TABLE deedglass.approvals (
  id deedglass.apr_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  action_id deedglass.act_id NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')),
  rule_id text CHECK (pg_catalog.length(rule_id) <= 200),
  reason text CHECK (pg_catalog.length(reason) <= 2000),
  approvers_required integer NOT NULL DEFAULT 1 CHECK (approvers_required BETWEEN 1 AND 5),
  approver_spec jsonb NOT NULL DEFAULT '{}' CHECK (pg_catalog.jsonb_typeof(approver_spec) = 'object'),
  approved_by text[] NOT NULL DEFAULT '{}',
  decided_by text CHECK (pg_catalog.length(decided_by) <= 64),
  decided_at timestamptz,
  decision_reason text CHECK (pg_catalog.length(decision_reason) <= 2000),
  expires_at timestamptz NOT NULL,
  context jsonb,
  context_digest deedglass.digest,
  deed_id deedglass.deed_id,                         -- foreign key added in 0006 (deeds)
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id),
  CHECK ((status = 'pending') = (decided_at IS NULL))
);
CREATE INDEX approvals_pending_expiry ON deedglass.approvals (expires_at) WHERE status = 'pending';
CREATE INDEX approvals_org_time ON deedglass.approvals (org_id, created_at DESC, id DESC);
CREATE TRIGGER approvals_touch BEFORE UPDATE ON deedglass.approvals FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.approvals ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.approvals FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.approvals FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.approvals FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON deedglass.approvals TO deedglass_app;

-- The action's approval link: deferred, so the action and its approval can be written in either order.
ALTER TABLE deedglass.actions ADD CONSTRAINT actions_approval_fk
  FOREIGN KEY (org_id, approval_id) REFERENCES deedglass.approvals (org_id, id) DEFERRABLE INITIALLY DEFERRED;

-- approval_tokens: one-time approval links (S22) ---------------------------------------------------
CREATE TABLE deedglass.approval_tokens (
  id deedglass.atk_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  approval_id deedglass.apr_id NOT NULL,
  user_id deedglass.usr_id NOT NULL REFERENCES deedglass.users (id),
  token_hash bytea NOT NULL UNIQUE CHECK (pg_catalog.octet_length(token_hash) = 32),
  purpose text NOT NULL CHECK (purpose IN ('approve', 'deny')),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, approval_id) REFERENCES deedglass.approvals (org_id, id)
);
CREATE INDEX approval_tokens_expiry ON deedglass.approval_tokens (expires_at);
ALTER TABLE deedglass.approval_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.approval_tokens FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.approval_tokens FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.approval_tokens FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.approval_tokens TO deedglass_app;

-- idempotency_keys: stored responses for Idempotency-Key (S03), 24-hour window ----------------------
CREATE TABLE deedglass.idempotency_keys (
  id deedglass.idk_id PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  principal text NOT NULL CHECK (principal ~ '^(agt|usr|key)_[0-7][0-9a-hjkmnp-tv-z]{25}$'),
  key text NOT NULL CHECK (pg_catalog.length(key) BETWEEN 1 AND 255),
  method text NOT NULL CHECK (method ~ '^[A-Z]{3,7}$'),
  route text NOT NULL CHECK (pg_catalog.length(route) BETWEEN 1 AND 200),   -- the route pattern, e.g. "POST /v1/agents"
  request_hash deedglass.digest NOT NULL,
  status text NOT NULL CHECK (status IN ('in_progress', 'complete')),
  response_status smallint CHECK (response_status BETWEEN 100 AND 599),
  response_headers jsonb CHECK (pg_catalog.jsonb_typeof(response_headers) = 'object'),
  response_body bytea CHECK (pg_catalog.octet_length(response_body) <= 1048576),
  locked_until timestamptz NOT NULL,
  completed_at timestamptz,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, principal, key),
  UNIQUE (org_id, id),
  CHECK ((status = 'complete') = (completed_at IS NOT NULL AND response_status IS NOT NULL))
);
CREATE INDEX idempotency_keys_expiry ON deedglass.idempotency_keys (expires_at);
ALTER TABLE deedglass.idempotency_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.idempotency_keys FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
CREATE POLICY system_scope ON deedglass.idempotency_keys FOR ALL TO deedglass_app
  USING      ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system')
  WITH CHECK ((SELECT pg_catalog.current_setting('deedglass.scope', true)) = 'system');
REVOKE ALL ON deedglass.idempotency_keys FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON deedglass.idempotency_keys TO deedglass_app;
