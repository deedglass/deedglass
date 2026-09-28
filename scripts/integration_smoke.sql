-- integration_smoke.sql: run by scripts/integration.sh as the installer (a superuser on a throwaway
-- cluster) right after sql/001-004. Expects psql variable fn_src_sha256 (from the bundle manifest).
-- Fixtures live in schema txv_it; nothing here is part of the product schema.
-- Signatures are 64 zero bytes: the function never checks signatures (the executor and verifier do).
\set ON_ERROR_STOP on
SET client_min_messages = warning;
\pset tuples_only on
\pset format unaligned

CREATE SCHEMA txv_it;
CREATE FUNCTION txv_it.check(ok boolean, what text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN
  IF ok IS NOT TRUE THEN RAISE EXCEPTION 'SMOKE FAIL: %', what; END IF;
  RETURN 'ok: ' || what;
END $$;

-- Builds a TRANSFER intent (not canonical JSON; canonical form is checked outside the database).
CREATE FUNCTION txv_it.intent(p_nonce text, p_amount text,
                              p_iat_offset_ms bigint DEFAULT 0, p_ttl_ms bigint DEFAULT 60000,
                              p_kid text DEFAULT '21fe31dfa154a261626bf854046fd227',
                              p_aud text DEFAULT '9b1c2d3e-0000-4000-8000-00000000d001')
RETURNS bytea LANGUAGE sql AS $$
  SELECT convert_to(jsonb_build_object(
           'v', 'txv.intent/1', 'op', 'TRANSFER', 'op_v', 1,
           'iss', 'ops-approver', 'kid', p_kid, 'aud', p_aud,
           'bundle', 'sha256:' || repeat('0', 63) || '1',
           'nonce', p_nonce, 'iat', t.now_ms + p_iat_offset_ms, 'exp', t.now_ms + p_iat_offset_ms + p_ttl_ms,
           'args', jsonb_build_object('amount', p_amount, 'currency', 'USD', 'from', 'alice', 'to', 'bob')
         )::text, 'UTF8')
  FROM (SELECT floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint AS now_ms) AS t
$$;
CREATE FUNCTION txv_it.edit(p_intent bytea, p_path text[], p_value jsonb) RETURNS bytea LANGUAGE sql AS $$
  SELECT convert_to(jsonb_set(convert_from(p_intent, 'UTF8')::jsonb, p_path, p_value, true)::text, 'UTF8')
$$;
CREATE FUNCTION txv_it.sig() RETURNS bytea LANGUAGE sql AS $$ SELECT decode(repeat('00', 64), 'hex') $$;
CREATE TABLE txv_it.fixture (name text PRIMARY KEY, intent bytea NOT NULL);
-- try_* return the SQLSTATE of the refusal, or NULL if the call succeeded (then the caller's check
-- fails and the whole statement, including any write, rolls back).
CREATE FUNCTION txv_it.try_call(p_name text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN
  PERFORM txv.execute_transfer_v1(f.intent, txv_it.sig()) FROM txv_it.fixture f WHERE f.name = p_name;
  RETURN NULL;
EXCEPTION WHEN OTHERS THEN RETURN SQLSTATE;
END $$;
CREATE FUNCTION txv_it.try_two(p_first text, p_second text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN
  PERFORM txv.execute_transfer_v1(f.intent, txv_it.sig()) FROM txv_it.fixture f WHERE f.name = p_first;
  PERFORM txv.execute_transfer_v1(f.intent, txv_it.sig()) FROM txv_it.fixture f WHERE f.name = p_second;
  RETURN NULL;
EXCEPTION WHEN OTHERS THEN RETURN SQLSTATE;
END $$;
CREATE FUNCTION txv_it.try_update_alice() RETURNS text LANGUAGE plpgsql AS $$
BEGIN
  UPDATE bank.accounts SET balance = 0 WHERE id = 'alice';
  RETURN NULL;
EXCEPTION WHEN OTHERS THEN RETURN SQLSTATE;
END $$;
CREATE FUNCTION txv_it.try_read_ledger() RETURNS text LANGUAGE plpgsql AS $$
BEGIN
  PERFORM 1 FROM txv.ledger;
  RETURN NULL;
EXCEPTION WHEN OTHERS THEN RETURN SQLSTATE;
END $$;
CREATE FUNCTION txv_it.expect(p_name text, p_want text, p_what text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE st text := txv_it.try_call(p_name);
BEGIN
  RETURN txv_it.check(st IS NOT DISTINCT FROM p_want, format('%s refused with %s (got %s)', p_what, p_want, st));
END $$;
CREATE ROLE txv_it_app NOLOGIN;   -- stands in for an application role: no grants on txv or bank
GRANT USAGE ON SCHEMA txv_it TO txv_executor, txv_verifier, txv_it_app;
GRANT SELECT ON txv_it.fixture TO txv_executor, txv_it_app;

\echo '== catalog checks'
SELECT txv_it.check(encode(sha256(convert_to(p.prosrc, 'UTF8')), 'hex') = :'fn_src_sha256',
                    'sha256(prosrc) equals manifest fn_src_sha256')
  FROM pg_proc p WHERE p.oid = 'txv.execute_transfer_v1(bytea,bytea)'::regprocedure;
SELECT txv_it.check(p.prosecdef AND p.proowner = 'txv_owner'::regrole
                    AND 'search_path=pg_catalog, pg_temp' = ANY (p.proconfig),
                    'function is SECURITY DEFINER, owned by txv_owner, search_path pinned')
  FROM pg_proc p WHERE p.oid = 'txv.execute_transfer_v1(bytea,bytea)'::regprocedure;
SELECT txv_it.check(NOT has_function_privilege('txv_it_app', 'txv.execute_transfer_v1(bytea,bytea)', 'EXECUTE')
                    AND has_function_privilege('txv_executor', 'txv.execute_transfer_v1(bytea,bytea)', 'EXECUTE'),
                    'EXECUTE only for txv_executor');
SELECT txv_it.check(bool_and(c.relreplident = 'f' AND c.relrowsecurity AND c.relforcerowsecurity
                             AND c.relpersistence = 'p') AND count(*) = 3,
                    'boundary tables: REPLICA IDENTITY FULL, RLS forced, permanent')
  FROM pg_class c WHERE c.oid IN ('bank.accounts'::regclass, 'bank.journal'::regclass, 'txv.ledger'::regclass);
SELECT txv_it.check(count(*) = 3, 'txv_pub publishes exactly the three boundary tables')
  FROM pg_publication_tables WHERE pubname = 'txv_pub';
SELECT txv_it.check(r.rolreplication AND NOT r.rolsuper AND NOT r.rolbypassrls,
                    'txv_verifier: REPLICATION, not superuser, no BYPASSRLS')
  FROM pg_roles r WHERE r.rolname = 'txv_verifier';
SELECT txv_it.check(NOT r.rolcanlogin AND NOT r.rolsuper AND NOT r.rolreplication AND NOT r.rolbypassrls,
                    'txv_owner: NOLOGIN, not superuser, no REPLICATION, no BYPASSRLS')
  FROM pg_roles r WHERE r.rolname = 'txv_owner';
SELECT txv_it.check(s.setconfig @> ARRAY['TimeZone=UTC', 'DateStyle=ISO, YMD', 'bytea_output=hex'],
                    'txv_verifier session settings pinned (plan §3.0 #5)')
  FROM pg_db_role_setting s WHERE s.setrole = 'txv_verifier'::regrole AND s.setdatabase = 0;
SELECT CASE WHEN coalesce(  -- PG17+ has a 4th argument (flush); PG16 has 3
              has_function_privilege('public', to_regprocedure('pg_logical_emit_message(boolean,text,text,boolean)'), 'EXECUTE'),
              has_function_privilege('public', to_regprocedure('pg_logical_emit_message(boolean,text,text)'), 'EXECUTE'))
            THEN 'note: pg_logical_emit_message is PUBLIC-executable here (recorded, not required)'
            ELSE 'note: pg_logical_emit_message is not PUBLIC-executable here' END AS emit_acl;

\echo '== seed (as txv_owner, before the slots exist)'
SET ROLE txv_owner;
INSERT INTO txv.config (deployment_id) VALUES ('9b1c2d3e-0000-4000-8000-00000000d001');
INSERT INTO txv.bundles VALUES ('TRANSFER', 1, 'sha256:' || repeat('0', 63) || '1', 'active', 100000000);
INSERT INTO txv.principal_keys VALUES   -- RFC 8032 §7.1 TEST 1 public key and its kid
  ('21fe31dfa154a261626bf854046fd227', 'ops-approver',
   decode('d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a', 'hex'),
   'active', now() - interval '1 day', NULL);
INSERT INTO txv.mandates VALUES ('ops-approver', 'alice', 1000000);
-- alice starts at 2^53+1 so exact integer text is exercised (a float64 would round it).
INSERT INTO bank.accounts (id, currency, balance) VALUES
  ('alice', 'USD', 9007199254740993), ('bob', 'USD', 100), ('carol', 'USD', 0);
RESET ROLE;

\echo '== slots (publication already exists)'
SET ROLE txv_verifier;
SELECT slot_name FROM pg_create_logical_replication_slot('txv_slot', 'pgoutput', false, false);
RESET ROLE;
SELECT slot_name FROM pg_create_logical_replication_slot('txv_it_td', 'test_decoding', false, false);

\echo '== one transfer as txv_executor'
INSERT INTO txv_it.fixture VALUES ('t1', txv_it.intent('00000000000000000000000000000001', '1000'));
SET ROLE txv_executor;
SELECT encode(r.intent_hash, 'hex') AS h1, r.xid8::text AS x1, r.marker_lsn::text AS m1
  FROM txv_it.fixture f, txv.execute_transfer_v1(f.intent, txv_it.sig()) AS r WHERE f.name = 't1' \gset
RESET ROLE;
\echo 'intent_hash=' :h1 ' xid8=' :x1 ' marker_lsn=' :m1
SELECT txv_it.check(balance = 9007199254739993 AND version = 1, 'alice debited exactly (2^53+1 - 1000)')
  FROM bank.accounts WHERE id = 'alice';
SELECT txv_it.check(balance = 1100 AND version = 1, 'bob credited') FROM bank.accounts WHERE id = 'bob';
SELECT txv_it.check(count(*) = 2 AND sum(amount) = 0 AND bool_and(encode(intent_hash, 'hex') = :'h1'),
                    'two journal legs, net zero') FROM bank.journal;
SELECT txv_it.check(count(*) = 1 AND bool_and(l.xid8::text = :'x1' AND l.isolation = 'read committed'
                                              AND l.intent_hash = sha256(l.intent)),
                    'one ledger row, intent_hash = sha256(intent), READ COMMITTED') FROM txv.ledger l;

\echo '== refusals (each must fail before commit with the expected SQLSTATE)'
INSERT INTO txv_it.fixture VALUES
  ('expired',   txv_it.intent('00000000000000000000000000000002', '5', -120000)),
  ('over',      txv_it.intent('00000000000000000000000000000003', '1000001')),
  ('num',       txv_it.edit(txv_it.intent('00000000000000000000000000000004', '5'), '{args,amount}', '5')),
  ('unknown',   txv_it.edit(txv_it.intent('00000000000000000000000000000005', '5'), '{memo}', '"x"')),
  ('badkey',    txv_it.intent('00000000000000000000000000000006', '5', 0, 60000, repeat('a', 32))),
  ('otheraud',  txv_it.intent('00000000000000000000000000000007', '5', 0, 60000,
                              '21fe31dfa154a261626bf854046fd227', '00000000-0000-4000-8000-000000000000')),
  ('longttl',   txv_it.intent('00000000000000000000000000000008', '5', 0, 300001)),
  ('first',     txv_it.intent('00000000000000000000000000000009', '5')),
  ('second',    txv_it.intent('0000000000000000000000000000000a', '5'));

-- Each refusal runs in its own top-level transaction (psql autocommit): a failed write inside a
-- subtransaction still leaves the top-level xid assigned, which would trip TX001 for the next case.
SET ROLE txv_executor;
SELECT txv_it.expect('t1', '23505', 'replay of the same signed intent');
SELECT txv_it.expect('expired', 'TX025', 'expired intent');
SELECT txv_it.expect('over', 'TX023', 'amount above mandate');
SELECT txv_it.expect('num', 'TX011', 'amount as a JSON number');
SELECT txv_it.expect('unknown', 'TX010', 'unknown top-level key');
SELECT txv_it.expect('badkey', 'TX024', 'unregistered kid');
SELECT txv_it.expect('otheraud', 'TX021', 'aud of another deployment');
SELECT txv_it.expect('longttl', 'TX026', 'exp - iat above max_ttl_ms');
SELECT txv_it.check(txv_it.try_two('first', 'second') IS NOT DISTINCT FROM 'TX001',
                    'second call in one transaction refused with TX001');
SELECT txv_it.check(txv_it.try_update_alice() IS NOT DISTINCT FROM '42501', 'executor direct DML refused with 42501');
RESET ROLE;

SET session_replication_role = replica;
SET ROLE txv_executor;
SELECT txv_it.expect('first', 'TX002', 'session_replication_role=replica');
RESET ROLE;
RESET session_replication_role;

SET ROLE txv_it_app;
SELECT txv_it.expect('first', '42501', 'app role EXECUTE');
SELECT txv_it.check(txv_it.try_update_alice() IS NOT DISTINCT FROM '42501', 'app role DML refused with 42501');
SELECT txv_it.check(txv_it.try_read_ledger() IS NOT DISTINCT FROM '42501', 'app role ledger read refused with 42501');
RESET ROLE;

SELECT txv_it.check(count(*) = 1, 'still exactly one ledger row after the refusals') FROM txv.ledger;
SELECT txv_it.check(sum(balance) = 9007199254740993 + 100, 'sum of balances conserved') FROM bank.accounts;

\echo '== decode checks'
SELECT txv_it.check(count(*) = 1 AND bool_and(position(:'h1' IN data) > 0),
                    'test_decoding: exactly one transactional txv/v1 marker, carrying intent_hash')
  FROM pg_logical_slot_peek_changes('txv_it_td', NULL, NULL)
 WHERE data LIKE 'message: transactional: 1 prefix: txv/v1,%';

SET ROLE txv_verifier;   -- the verifier path: REPLICATION role, SQL peek, pgoutput proto 1
CREATE TEMP TABLE frames AS
SELECT lsn, xid, data, row_number() OVER () AS pos
  FROM pg_logical_slot_peek_binary_changes('txv_slot', NULL, NULL,
         'proto_version', '1', 'publication_names', 'txv_pub', 'messages', 'true');
SELECT txv_it.check(
         count(*) FILTER (WHERE get_byte(data, 0) = ascii('B')) = 1
     AND count(*) FILTER (WHERE get_byte(data, 0) = ascii('C')) = 1
     AND count(*) FILTER (WHERE get_byte(data, 0) = ascii('I')) = 3
     AND count(*) FILTER (WHERE get_byte(data, 0) = ascii('U')) = 2
     AND count(*) FILTER (WHERE get_byte(data, 0) = ascii('M') AND get_byte(data, 1) = 1) = 1,
         'pgoutput: one frame with 3 inserts, 2 updates and 1 transactional marker')
  FROM frames;
SELECT txv_it.check(
         (SELECT pos FROM frames WHERE get_byte(data, 0) = ascii('M'))
       = (SELECT pos FROM frames WHERE get_byte(data, 0) = ascii('C')) - 1
     AND (SELECT position(convert_to(:'h1', 'UTF8') IN data) > 0 FROM frames WHERE get_byte(data, 0) = ascii('M')),
         'pgoutput: marker is the last change before COMMIT and carries intent_hash');
SELECT txv_it.check(
         (SELECT get_byte(data, 17)::bigint * 16777216 + get_byte(data, 18) * 65536
               + get_byte(data, 19) * 256 + get_byte(data, 20)
            FROM frames WHERE get_byte(data, 0) = ascii('B'))
       = (:'x1'::numeric % 4294967296),
         'BEGIN.xid = low32(ledger.xid8)');
DROP TABLE frames;
RESET ROLE;
\echo '== smoke done'
