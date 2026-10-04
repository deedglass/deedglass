-- 0026_slack_approvals.sql: approvals in Slack (S29).
--
-- notification_messages: one row per (approval, Slack channel) the approval card was posted to, so
-- the card can be edited in place until the deed is sealed. version is the rank of the last state
-- the card showed (1 pending, 2 partly countersigned, 3 approved, 4 final): an update only moves it
-- up, so a stale job can never turn an approved card back into a pending one.
-- inbound_replay: the digest of every verified Slack signature until its 5-minute window has
-- passed, so a replayed click does nothing.
-- Routing: the one cross-org read is deedglass.slack_route(team, app), which answers which org and
-- connector a Slack request names. It only picks whose signing secret to check; the signature
-- decides. A Slack app (team id and app id) routes to at most one live connector with approvals
-- enabled, and an org has at most one such connector.
-- The job kind notify_update edits a card after approval.decided, expiry, cancel and deed.sealed.

-- notification_messages -----------------------------------------------------------------------------
CREATE TABLE deedglass.notification_messages (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  subject_type text NOT NULL CHECK (subject_type IN ('approval')),
  subject_id deedglass.apr_id NOT NULL,
  action_id deedglass.act_id NOT NULL,
  channel_kind text NOT NULL CHECK (channel_kind IN ('slack')),
  connector_id deedglass.con_id NOT NULL,
  channel_id text NOT NULL CHECK (channel_id ~ '^[CGD][A-Z0-9]{2,30}$'),
  message_ts text NOT NULL CHECK (message_ts ~ '^[0-9]{1,12}\.[0-9]{1,9}$'),
  state text NOT NULL CHECK (state IN ('posted', 'updated', 'delivery_failed')),
  version integer NOT NULL DEFAULT 1 CHECK (version BETWEEN 1 AND 100),
  last_error text CHECK (pg_catalog.length(last_error) <= 500),
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  updated_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  UNIQUE (org_id, id),
  UNIQUE (org_id, subject_id, channel_kind, channel_id),
  FOREIGN KEY (org_id, subject_id) REFERENCES deedglass.approvals (org_id, id),
  FOREIGN KEY (org_id, action_id) REFERENCES deedglass.actions (org_id, id),
  FOREIGN KEY (org_id, connector_id) REFERENCES deedglass.connectors (org_id, id)
);
CREATE INDEX notification_messages_action ON deedglass.notification_messages (org_id, action_id);
CREATE TRIGGER notification_messages_touch BEFORE UPDATE ON deedglass.notification_messages
  FOR EACH ROW EXECUTE FUNCTION deedglass.touch_updated_at();
ALTER TABLE deedglass.notification_messages ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.notification_messages FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.notification_messages FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.notification_messages TO deedglass_app;
GRANT UPDATE (state, version, last_error, updated_at) ON deedglass.notification_messages TO deedglass_app;

-- inbound_replay ------------------------------------------------------------------------------------
CREATE TABLE deedglass.inbound_replay (
  org_id deedglass.org_id NOT NULL REFERENCES deedglass.orgs (id),
  source text NOT NULL CHECK (source IN ('slack')),
  digest deedglass.digest NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT pg_catalog.now(),
  PRIMARY KEY (org_id, source, digest)
);
CREATE INDEX inbound_replay_expires ON deedglass.inbound_replay (expires_at);
ALTER TABLE deedglass.inbound_replay ENABLE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON deedglass.inbound_replay FOR ALL TO deedglass_app
  USING      (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')))
  WITH CHECK (org_id = (SELECT NULLIF(pg_catalog.current_setting('deedglass.org_id', true), '')));
REVOKE ALL ON deedglass.inbound_replay FROM PUBLIC;
GRANT SELECT, INSERT ON deedglass.inbound_replay TO deedglass_app;

-- Expired replay digests leave only through this function (the S09 pruner schedule runs it).
CREATE FUNCTION deedglass.maint_prune_inbound_replay(p_limit integer)
  RETURNS bigint
  LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  WITH gone AS (
    DELETE FROM deedglass.inbound_replay AS r
    WHERE (r.org_id, r.source, r.digest) IN (
      SELECT x.org_id, x.source, x.digest FROM deedglass.inbound_replay AS x
      WHERE x.expires_at < pg_catalog.now()
      ORDER BY x.expires_at LIMIT LEAST(GREATEST(p_limit, 0), 100000))
    RETURNING 1
  )
  SELECT pg_catalog.count(*) FROM gone
$fn$;
REVOKE ALL ON FUNCTION deedglass.maint_prune_inbound_replay(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.maint_prune_inbound_replay(integer) TO deedglass_app;

-- Slack routing ------------------------------------------------------------------------------------
-- A Slack app routes to one live connector with approvals on, anywhere; and an org has at most one.
CREATE UNIQUE INDEX connectors_slack_route_uq ON deedglass.connectors ((config ->> 'team_id'), (config ->> 'app_id'))
  WHERE type = 'slack' AND deleted_at IS NULL AND (config -> 'approvals' ->> 'enabled') = 'true';
CREATE UNIQUE INDEX connectors_slack_approvals_org_uq ON deedglass.connectors (org_id)
  WHERE type = 'slack' AND deleted_at IS NULL AND (config -> 'approvals' ->> 'enabled') = 'true';

-- The only lookup before the org is known (POST /v1/slack/interact). Constant SQL, ids only; a
-- malformed team or app id finds nothing. The caller then checks the signature with that org's
-- secret, in that org's transaction.
CREATE FUNCTION deedglass.slack_route(p_team text, p_app text)
  RETURNS TABLE (org_id text, connector_id text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $fn$
  SELECT c.org_id::text, c.id::text
  FROM deedglass.connectors AS c
  JOIN deedglass.orgs AS o ON o.id = c.org_id
  WHERE p_team ~ '^T[A-Z0-9]{2,20}$' AND p_app ~ '^A[A-Z0-9]{2,20}$'
    AND c.type = 'slack' AND c.deleted_at IS NULL AND c.status = 'active'
    AND (c.config -> 'approvals' ->> 'enabled') = 'true'
    AND (c.config ->> 'team_id') = p_team AND (c.config ->> 'app_id') = p_app
    AND o.status = 'active'
  LIMIT 1
$fn$;
REVOKE ALL ON FUNCTION deedglass.slack_route(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.slack_route(text, text) TO deedglass_app;

-- witness_jobs: the job kind notify_update ----------------------------------------------------------
ALTER TABLE deedglass.witness_jobs
  DROP CONSTRAINT witness_jobs_kind_check,
  ADD CONSTRAINT witness_jobs_kind_check CHECK (kind IN ('witness', 'l1_readback', 'l2_match',
    'approval_expire', 'approval_notify', 'approval_resume', 'notify_update'));
