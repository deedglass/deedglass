-- 0027_purge_org.sql: deedglass.purge_org (S35, ADR-068), for a beta org's data-deletion request
-- (deploy/cloud/DATA_DELETION.md). It deletes every row of one org, in one transaction, and returns
-- the count per table. Run it as the schema owner (deedglass_migrator) from psql; the runtime role
-- can't call it, and nothing in the binary does.
--
-- The append-only tables (deeds, action_events, admin_events, policy_versions, policy_activations,
-- approval_votes, undo_plans) refuse every UPDATE, DELETE and TRUNCATE through forbid_mutation. Its
-- new body lets exactly one thing through: a DELETE by the schema owner of rows whose org_id equals
-- the transaction-local setting deedglass.purging_org, which only purge_org sets (and clears). The
-- runtime role holds no DELETE on these tables, so it can use neither path.
--
-- Every DELETE runs in one statement (data-modifying CTEs): the foreign keys are NO ACTION, checked
-- at the end of the statement, so the cycles (actions ⇄ deeds, policy_versions ⇄ suggestions) need
-- no ordering. Users who belonged only to this org, and are referenced nowhere else, go too.
-- Deeds the customer exported before still verify against the published keys.

CREATE OR REPLACE FUNCTION deedglass.forbid_mutation() RETURNS trigger LANGUAGE plpgsql
  SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE'
     AND coalesce(pg_catalog.current_setting('deedglass.purging_org', true), '') <> ''
     AND current_user = (SELECT pg_catalog.pg_get_userbyid(n.nspowner) FROM pg_catalog.pg_namespace AS n WHERE n.nspname = 'deedglass') THEN
    IF TG_LEVEL = 'STATEMENT' THEN
      RETURN NULL;
    END IF;
    IF OLD.org_id = pg_catalog.current_setting('deedglass.purging_org', true) THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION USING ERRCODE = 'DG001',
    MESSAGE = pg_catalog.format('deedglass: %I.%I is append-only (%s refused)', TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP),
    HINT = 'Write a new row. Deeds are corrected by amendment deeds, never edited.';
END $fn$;
REVOKE ALL ON FUNCTION deedglass.forbid_mutation() FROM PUBLIC;

CREATE FUNCTION deedglass.purge_org(p_org text, p_confirm text) RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $fn$
DECLARE
  v_slug  text;
  v_users text[];
  v_out   jsonb;
  v_gone  bigint;
BEGIN
  SELECT o.slug INTO v_slug FROM deedglass.orgs AS o WHERE o.id = p_org;
  IF v_slug IS NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'DG007',
      MESSAGE = 'deedglass: purge_org found no org with that id',
      HINT = 'Copy the org id (org_…) from deedglass org list or the request, then run it again.';
  END IF;
  IF p_confirm IS DISTINCT FROM v_slug THEN
    RAISE EXCEPTION USING ERRCODE = 'DG007',
      MESSAGE = pg_catalog.format('deedglass: purge_org needs the org''s slug as its confirmation (%s); nothing was deleted', v_slug),
      HINT = 'Pass the slug exactly as the second argument.';
  END IF;
  SELECT pg_catalog.array_agg(DISTINCT m.user_id::text) INTO v_users FROM deedglass.memberships AS m WHERE m.org_id = p_org;
  PERFORM pg_catalog.set_config('deedglass.purging_org', p_org, true);

  WITH
    d01 AS (DELETE FROM deedglass.notification_messages AS t WHERE t.org_id = p_org RETURNING 1),
    d02 AS (DELETE FROM deedglass.webhook_deliveries AS t WHERE t.org_id = p_org RETURNING 1),
    d03 AS (DELETE FROM deedglass.webhooks AS t WHERE t.org_id = p_org RETURNING 1),
    d04 AS (DELETE FROM deedglass.witness_jobs AS t WHERE t.org_id = p_org RETURNING 1),
    d05 AS (DELETE FROM deedglass.undo_plans AS t WHERE t.org_id = p_org RETURNING 1),
    d06 AS (DELETE FROM deedglass.approval_votes AS t WHERE t.org_id = p_org RETURNING 1),
    d07 AS (DELETE FROM deedglass.approval_tokens AS t WHERE t.org_id = p_org RETURNING 1),
    d08 AS (DELETE FROM deedglass.approvals AS t WHERE t.org_id = p_org RETURNING 1),
    d09 AS (DELETE FROM deedglass.action_events AS t WHERE t.org_id = p_org RETURNING 1),
    d10 AS (DELETE FROM deedglass.deeds AS t WHERE t.org_id = p_org RETURNING 1),
    d11 AS (DELETE FROM deedglass.actions AS t WHERE t.org_id = p_org RETURNING 1),
    d12 AS (DELETE FROM deedglass.admin_events AS t WHERE t.org_id = p_org RETURNING 1),
    d13 AS (DELETE FROM deedglass.decoded_txs AS t WHERE t.org_id = p_org RETURNING 1),
    d14 AS (DELETE FROM deedglass.decoder_slots AS t WHERE t.org_id = p_org RETURNING 1),
    d15 AS (DELETE FROM deedglass.credentials AS t WHERE t.org_id = p_org RETURNING 1),
    d16 AS (DELETE FROM deedglass.connectors AS t WHERE t.org_id = p_org RETURNING 1),
    d17 AS (DELETE FROM deedglass.learn_runs AS t WHERE t.org_id = p_org RETURNING 1),
    d18 AS (DELETE FROM deedglass.anomalies AS t WHERE t.org_id = p_org RETURNING 1),
    d19 AS (DELETE FROM deedglass.suggestions AS t WHERE t.org_id = p_org RETURNING 1),
    d20 AS (DELETE FROM deedglass.replay_jobs AS t WHERE t.org_id = p_org RETURNING 1),
    d21 AS (DELETE FROM deedglass.policy_activations AS t WHERE t.org_id = p_org RETURNING 1),
    d22 AS (DELETE FROM deedglass.policy_versions AS t WHERE t.org_id = p_org RETURNING 1),
    d23 AS (DELETE FROM deedglass.policies AS t WHERE t.org_id = p_org RETURNING 1),
    d24 AS (DELETE FROM deedglass.usage_counters AS t WHERE t.org_id = p_org RETURNING 1),
    d25 AS (DELETE FROM deedglass.idempotency_keys AS t WHERE t.org_id = p_org RETURNING 1),
    d26 AS (DELETE FROM deedglass.intent_nonces AS t WHERE t.org_id = p_org RETURNING 1),
    d27 AS (DELETE FROM deedglass.api_keys AS t WHERE t.org_id = p_org RETURNING 1),
    d28 AS (DELETE FROM deedglass.agents AS t WHERE t.org_id = p_org RETURNING 1),
    d29 AS (DELETE FROM deedglass.login_tokens AS t WHERE t.org_id = p_org RETURNING 1),
    d30 AS (DELETE FROM deedglass.sessions AS t WHERE t.org_id = p_org RETURNING 1),
    d31 AS (DELETE FROM deedglass.memberships AS t WHERE t.org_id = p_org RETURNING 1),
    d32 AS (DELETE FROM deedglass.inbound_replay AS t WHERE t.org_id = p_org RETURNING 1),
    d33 AS (DELETE FROM deedglass.orgs AS t WHERE t.id = p_org RETURNING 1)
  SELECT pg_catalog.jsonb_build_object(
    'notification_messages', (SELECT pg_catalog.count(*) FROM d01), 'webhook_deliveries', (SELECT pg_catalog.count(*) FROM d02),
    'webhooks', (SELECT pg_catalog.count(*) FROM d03), 'witness_jobs', (SELECT pg_catalog.count(*) FROM d04),
    'undo_plans', (SELECT pg_catalog.count(*) FROM d05), 'approval_votes', (SELECT pg_catalog.count(*) FROM d06),
    'approval_tokens', (SELECT pg_catalog.count(*) FROM d07), 'approvals', (SELECT pg_catalog.count(*) FROM d08),
    'action_events', (SELECT pg_catalog.count(*) FROM d09), 'deeds', (SELECT pg_catalog.count(*) FROM d10),
    'actions', (SELECT pg_catalog.count(*) FROM d11), 'admin_events', (SELECT pg_catalog.count(*) FROM d12),
    'decoded_txs', (SELECT pg_catalog.count(*) FROM d13), 'decoder_slots', (SELECT pg_catalog.count(*) FROM d14),
    'credentials', (SELECT pg_catalog.count(*) FROM d15), 'connectors', (SELECT pg_catalog.count(*) FROM d16),
    'learn_runs', (SELECT pg_catalog.count(*) FROM d17), 'anomalies', (SELECT pg_catalog.count(*) FROM d18),
    'suggestions', (SELECT pg_catalog.count(*) FROM d19), 'replay_jobs', (SELECT pg_catalog.count(*) FROM d20),
    'policy_activations', (SELECT pg_catalog.count(*) FROM d21), 'policy_versions', (SELECT pg_catalog.count(*) FROM d22),
    'policies', (SELECT pg_catalog.count(*) FROM d23), 'usage_counters', (SELECT pg_catalog.count(*) FROM d24),
    'idempotency_keys', (SELECT pg_catalog.count(*) FROM d25), 'intent_nonces', (SELECT pg_catalog.count(*) FROM d26),
    'api_keys', (SELECT pg_catalog.count(*) FROM d27), 'agents', (SELECT pg_catalog.count(*) FROM d28),
    'login_tokens', (SELECT pg_catalog.count(*) FROM d29), 'sessions', (SELECT pg_catalog.count(*) FROM d30),
    'memberships', (SELECT pg_catalog.count(*) FROM d31), 'inbound_replay', (SELECT pg_catalog.count(*) FROM d32),
    'orgs', (SELECT pg_catalog.count(*) FROM d33))
  INTO v_out;

  -- People who belonged only to this org and appear nowhere else. A user still in another org stays.
  WITH g AS (
    DELETE FROM deedglass.users AS u
     WHERE u.id::text = ANY (coalesce(v_users, ARRAY[]::text[]))
       AND NOT EXISTS (SELECT 1 FROM deedglass.memberships AS m WHERE m.user_id = u.id OR m.invited_by = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.sessions AS s WHERE s.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.login_tokens AS l WHERE l.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.agents AS a WHERE a.owner_user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.approval_tokens AS t WHERE t.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.approval_votes AS v WHERE v.user_id = u.id)
       AND NOT EXISTS (SELECT 1 FROM deedglass.policy_versions AS p WHERE p.author_user_id = u.id)
    RETURNING 1)
  SELECT pg_catalog.count(*) INTO v_gone FROM g;

  PERFORM pg_catalog.set_config('deedglass.purging_org', '', true);
  RETURN v_out || pg_catalog.jsonb_build_object('users', v_gone, 'org', p_org, 'slug', v_slug);
END $fn$;
REVOKE ALL ON FUNCTION deedglass.purge_org(text, text) FROM PUBLIC;
