-- 003_transfer_v1.sql: the controlled function deedglass.execute_transfer_v1 (spec 20_ §4.4; plan §3.0 #8).
-- Run by the installer after 002_schema.sql. The function body (the text between the $fn$
-- delimiters, i.e. pg_proc.prosrc) is hashed into bundles/transfer_v1/manifest.json as
-- fn_src_sha256; any edit to it needs a new manifest, a new bundle hash and an ADR.
--
-- Rules: no dynamic SQL; no EXCEPTION blocks (they open subtransactions and weaken the entry
-- guard); every object schema-qualified; search_path pinned to pg_catalog, pg_temp;
-- REVOKE and GRANT in the same transaction as CREATE.
--
-- The function never checks canonical form or the signature: the executor checks both before
-- BEGIN and the verifier checks both again after commit (spec §0 #4).
-- The entry guard shows that the call is the FIRST write of its transaction, not the ONLY one:
-- extra deedglass markers are caught by the verifier (MARKER_COUNT, FORGED_MARKER). Retries must use
-- a NEW transaction (a same-transaction retry trips TX001).
--
-- SQLSTATEs (listed again in docs/EVALUATION_RUNBOOK.md):
--   TX001 not the first write of the transaction   TX002 session_replication_role is not origin
--   TX003 read-only transaction                     TX004 argument missing or wrong size
--   TX010 not a closed deedglass.intent/1 object           TX011 field type or format
--   TX020 bundle not active                          TX021 aud is not this deployment
--   TX022 from = to                                  TX023 amount above bundle or mandate limit
--   TX024 key not active for iss at iat              TX025 expired
--   TX026 exp/iat window invalid (exp <= iat or exp - iat > max_ttl_ms)
--   TX027 iat in the future beyond skew              TX028 no mandate for (iss, from)
--   TX029 not installed (deedglass.config empty)           TX030 debit failed (funds, account, currency, RLS)
--   TX031 credit failed                              23505 duplicate (kid, nonce): REPLAY
\set ON_ERROR_STOP on

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL ROLE deedglass_owner;

CREATE FUNCTION deedglass.execute_transfer_v1(p_intent bytea, p_sig bytea)
RETURNS TABLE (intent_hash bytea, xid8 pg_catalog.xid8, marker_lsn pg_catalog.pg_lsn)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
SET TimeZone = 'UTC'
SET lock_timeout = '2s'
AS $fn$
#variable_conflict use_column
DECLARE
  v            pg_catalog.jsonb;
  v_args       pg_catalog.jsonb;
  v_h          bytea;
  v_xid        pg_catalog.xid8;
  v_lsn        pg_catalog.pg_lsn;
  v_kid        text;
  v_nonce      text;
  v_iss        text;
  v_aud        text;
  v_bundle     text;
  v_from       text;
  v_to         text;
  v_cur        text;
  v_amt        bigint;
  v_iat        bigint;
  v_exp        bigint;
  v_now_ms     bigint;
  v_deployment text;
  v_max_ttl    bigint;
  v_skew       bigint;
  v_bundle_max bigint;
  v_mandate    bigint;
  f_old        bigint;
  f_new        bigint;
  t_old        bigint;
  t_new        bigint;
  n            int;
  v_line       text;
BEGIN
  -- 1. Guards.
  IF pg_catalog.pg_current_xact_id_if_assigned() IS NOT NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'TX001',
      MESSAGE = 'deedglass: execute_transfer_v1 must be the first write of its transaction';
  END IF;
  IF pg_catalog.current_setting('session_replication_role') <> 'origin' THEN
    RAISE EXCEPTION USING ERRCODE = 'TX002', MESSAGE = 'deedglass: session_replication_role must be origin';
  END IF;
  IF pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION USING ERRCODE = 'TX003', MESSAGE = 'deedglass: read-only transaction';
  END IF;
  IF (p_intent IS NOT NULL AND p_sig IS NOT NULL
      AND pg_catalog.octet_length(p_intent) <= 4096
      AND pg_catalog.octet_length(p_sig) = 64) IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE = 'TX004', MESSAGE = 'deedglass: intent must be <= 4096 bytes and sig 64 bytes';
  END IF;

  -- 2. Parse: closed key set, types, fixed values and field formats (spec §5).
  v := pg_catalog.convert_from(p_intent, 'UTF8')::pg_catalog.jsonb;
  -- CASE (not AND) so jsonb_object_keys never sees a non-object.
  IF (CASE WHEN pg_catalog.jsonb_typeof(v) = 'object' THEN
             v ?& ARRAY['args', 'aud', 'bundle', 'exp', 'iat', 'iss', 'kid', 'nonce', 'op', 'op_v', 'v']
             AND NOT EXISTS (SELECT 1 FROM pg_catalog.jsonb_object_keys(v) AS k(key)
                              WHERE k.key <> ALL (ARRAY['args', 'aud', 'bundle', 'exp', 'iat', 'iss', 'kid',
                                                        'nonce', 'op', 'op_v', 'v', 'agent']))
           ELSE false END) IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE = 'TX010', MESSAGE = 'deedglass: not a closed deedglass.intent/1 object';
  END IF;
  v_args := v -> 'args';
  IF (CASE WHEN pg_catalog.jsonb_typeof(v_args) = 'object' THEN
             v_args ?& ARRAY['amount', 'currency', 'from', 'to']
             AND NOT EXISTS (SELECT 1 FROM pg_catalog.jsonb_object_keys(v_args) AS k(key)
                              WHERE k.key <> ALL (ARRAY['amount', 'currency', 'from', 'to']))
           ELSE false END) IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE = 'TX010', MESSAGE = 'deedglass: args is not a closed object';
  END IF;
  IF (    pg_catalog.jsonb_typeof(v -> 'v') = 'string'      AND (v ->> 'v') = 'deedglass.intent/1'
      AND pg_catalog.jsonb_typeof(v -> 'op') = 'string'     AND (v ->> 'op') = 'TRANSFER'
      AND pg_catalog.jsonb_typeof(v -> 'op_v') = 'number'   AND (v ->> 'op_v') = '1'
      AND pg_catalog.jsonb_typeof(v -> 'iss') = 'string'    AND (v ->> 'iss') ~ '^[a-z0-9._-]{1,64}$'
      AND pg_catalog.jsonb_typeof(v -> 'kid') = 'string'    AND (v ->> 'kid') ~ '^[0-9a-f]{32}$'
      AND pg_catalog.jsonb_typeof(v -> 'aud') = 'string'
      AND (v ->> 'aud') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
      AND pg_catalog.jsonb_typeof(v -> 'bundle') = 'string' AND (v ->> 'bundle') ~ '^sha256:[0-9a-f]{64}$'
      AND pg_catalog.jsonb_typeof(v -> 'nonce') = 'string'  AND (v ->> 'nonce') ~ '^[0-9a-f]{32}$'
      AND pg_catalog.jsonb_typeof(v -> 'iat') = 'number'    AND (v ->> 'iat') ~ '^(0|[1-9][0-9]{0,15})$'
      AND pg_catalog.jsonb_typeof(v -> 'exp') = 'number'    AND (v ->> 'exp') ~ '^(0|[1-9][0-9]{0,15})$'
      AND (CASE WHEN pg_catalog.jsonb_typeof(v -> 'iat') = 'number'
                THEN (v ->> 'iat')::numeric <= 9007199254740991 ELSE false END)
      AND (CASE WHEN pg_catalog.jsonb_typeof(v -> 'exp') = 'number'
                THEN (v ->> 'exp')::numeric <= 9007199254740991 ELSE false END)
      AND (NOT (v ? 'agent') OR pg_catalog.jsonb_typeof(v -> 'agent') = 'string')
      AND pg_catalog.jsonb_typeof(v_args -> 'amount') = 'string'   AND (v_args ->> 'amount') ~ '^[1-9][0-9]{0,17}$'
      AND pg_catalog.jsonb_typeof(v_args -> 'currency') = 'string' AND (v_args ->> 'currency') ~ '^[A-Z]{3}$'
      AND pg_catalog.jsonb_typeof(v_args -> 'from') = 'string'     AND (v_args ->> 'from') ~ '^[a-z0-9_]{1,40}$'
      AND pg_catalog.jsonb_typeof(v_args -> 'to') = 'string'       AND (v_args ->> 'to') ~ '^[a-z0-9_]{1,40}$'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION USING ERRCODE = 'TX011', MESSAGE = 'deedglass: intent field type or format';
  END IF;
  v_kid    := v ->> 'kid';
  v_nonce  := v ->> 'nonce';
  v_iss    := v ->> 'iss';
  v_aud    := v ->> 'aud';
  v_bundle := v ->> 'bundle';
  v_iat    := (v ->> 'iat')::bigint;
  v_exp    := (v ->> 'exp')::bigint;
  v_from   := v_args ->> 'from';
  v_to     := v_args ->> 'to';
  v_cur    := v_args ->> 'currency';
  v_amt    := (v_args ->> 'amount')::bigint;

  -- 3. Registry and policy (reads only; no xid is assigned yet).
  SELECT c.deployment_id, c.max_ttl_ms, c.skew_ms INTO v_deployment, v_max_ttl, v_skew
    FROM deedglass.config AS c WHERE c.one;
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = 'TX029', MESSAGE = 'deedglass: not installed (deedglass.config is empty)';
  END IF;
  SELECT b.max_amount INTO v_bundle_max
    FROM deedglass.bundles AS b
   WHERE b.op = 'TRANSFER' AND b.op_v = 1 AND b.hash = v_bundle AND b.status = 'active';
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = 'TX020', MESSAGE = 'deedglass: bundle not active';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM deedglass.principal_keys AS k
                  WHERE k.kid = v_kid AND k.iss = v_iss AND k.status = 'active'
                    AND k.not_before <= pg_catalog.to_timestamp(v_iat::double precision / 1000)
                    AND (k.not_after IS NULL
                         OR pg_catalog.to_timestamp(v_iat::double precision / 1000) < k.not_after)) THEN
    RAISE EXCEPTION USING ERRCODE = 'TX024', MESSAGE = 'deedglass: key not active for iss at iat';
  END IF;
  IF v_aud IS DISTINCT FROM v_deployment THEN
    RAISE EXCEPTION USING ERRCODE = 'TX021', MESSAGE = 'deedglass: aud is not this deployment';
  END IF;
  v_now_ms := pg_catalog.floor(EXTRACT(EPOCH FROM pg_catalog.clock_timestamp()) * 1000)::bigint;
  IF v_now_ms >= v_exp THEN
    RAISE EXCEPTION USING ERRCODE = 'TX025', MESSAGE = 'deedglass: intent expired';
  END IF;
  IF v_exp <= v_iat OR v_exp - v_iat > v_max_ttl THEN
    RAISE EXCEPTION USING ERRCODE = 'TX026', MESSAGE = 'deedglass: exp/iat window invalid';
  END IF;
  IF v_iat > v_now_ms + v_skew THEN
    RAISE EXCEPTION USING ERRCODE = 'TX027', MESSAGE = 'deedglass: iat in the future';
  END IF;
  IF v_from = v_to THEN
    RAISE EXCEPTION USING ERRCODE = 'TX022', MESSAGE = 'deedglass: from = to';
  END IF;
  SELECT m.max_amount INTO v_mandate
    FROM deedglass.mandates AS m WHERE m.iss = v_iss AND m.account_id = v_from;
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = 'TX028', MESSAGE = 'deedglass: no mandate for (iss, from)';
  END IF;
  IF v_amt > LEAST(v_bundle_max, v_mandate) THEN
    RAISE EXCEPTION USING ERRCODE = 'TX023', MESSAGE = 'deedglass: amount above bundle or mandate limit';
  END IF;

  -- 4. Nonce and ledger: the FIRST write; assigns the xid. Duplicate (kid, nonce) raises 23505.
  v_h   := pg_catalog.sha256(p_intent);
  v_xid := pg_catalog.pg_current_xact_id();
  INSERT INTO deedglass.ledger (kid, nonce, iss, intent_hash, intent, sig, bundle_hash, xid8, exp_ms, isolation, created_at)
  VALUES (v_kid, v_nonce, v_iss, v_h, p_intent, p_sig, v_bundle, v_xid, v_exp,
          pg_catalog.current_setting('transaction_isolation'), pg_catalog.clock_timestamp());

  -- 5. Lock order: both rows, by id, before any update (no A->B / B->A deadlock).
  PERFORM 1 FROM bank.accounts AS a WHERE a.id IN (v_from, v_to) ORDER BY a.id FOR UPDATE;

  -- 6. Debit (conditional; covers funds, missing account, currency mismatch, RLS zero-row filter).
  UPDATE bank.accounts SET balance = balance - v_amt, version = version + 1
   WHERE id = v_from AND currency = v_cur AND balance >= v_amt
   RETURNING balance + v_amt, balance INTO f_old, f_new;
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 1 THEN
    RAISE EXCEPTION USING ERRCODE = 'TX030', MESSAGE = 'deedglass: debit failed';
  END IF;

  -- 7. Credit.
  UPDATE bank.accounts SET balance = balance + v_amt, version = version + 1
   WHERE id = v_to AND currency = v_cur
   RETURNING balance - v_amt, balance INTO t_old, t_new;
  GET DIAGNOSTICS n = ROW_COUNT;
  IF n <> 1 THEN
    RAISE EXCEPTION USING ERRCODE = 'TX031', MESSAGE = 'deedglass: credit failed';
  END IF;

  -- 8. Journal: deterministic keys (intent_hash, leg).
  INSERT INTO bank.journal (intent_hash, leg, account_id, amount, currency)
  VALUES (v_h, 1, v_from, -v_amt, v_cur), (v_h, 2, v_to, v_amt, v_cur);

  -- 9. Marker, LAST. Correlation evidence only; authenticity comes from the same-transaction ledger
  --    row. The returned LSN is the END of the message record: evidence only, never compared with
  --    commit_lsn (plan §3.0 #1).
  v_line := 'v1|' || pg_catalog.encode(v_h, 'hex') || '|' || v_bundle || '|' || v_kid || '|' || v_nonce
         || '|' || v_xid::text || '|' || v_from || '|' || f_old::text || '|' || f_new::text
         || '|' || v_to || '|' || t_old::text || '|' || t_new::text || '|' || v_amt::text || '|' || v_cur;
  v_lsn := pg_catalog.pg_logical_emit_message(true, 'deedglass', v_line);

  -- 10.
  RETURN QUERY SELECT v_h, v_xid, v_lsn;
END
$fn$;

REVOKE ALL ON FUNCTION deedglass.execute_transfer_v1(bytea, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION deedglass.execute_transfer_v1(bytea, bytea) TO deedglass_agent;

COMMIT;
