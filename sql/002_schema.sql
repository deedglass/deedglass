-- 002_schema.sql: deedglass registry tables, the ledger, and the V0 reference schema bank (spec 20_ §4.3).
-- Run by the installer after 001_roles.sql, in the target database. The installer must be able to
-- SET ROLE deedglass_owner; that membership is a trusted principal listed in every statement.
-- Rules (CLAUDE.md): every object schema-qualified; REVOKE/GRANT in the same transaction as CREATE;
-- no sequences (deterministic keys only); narrow columns only (no TOAST on protected tables).
\set ON_ERROR_STOP on

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;   -- installer session: no search_path shadowing

CREATE SCHEMA deedglass  AUTHORIZATION deedglass_owner;
CREATE SCHEMA bank AUTHORIZATION deedglass_owner;           -- V0 reference ledger schema
REVOKE ALL ON SCHEMA deedglass, bank FROM PUBLIC;
GRANT USAGE ON SCHEMA deedglass  TO deedglass_agent, deedglass_witness;
GRANT USAGE ON SCHEMA bank TO deedglass_witness;
-- Hardening checklist (spec §4.5): applied where permitted (default since PG15; a no-op
-- WARNING when the installer does not own schema public). `deedglass connector postgres check` (S17) records the result.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

SET LOCAL ROLE deedglass_owner;

-- One-row deployment configuration; deployment_id is the UUID generated at install (= intent "aud").
CREATE TABLE deedglass.config (
  one           boolean PRIMARY KEY DEFAULT true CHECK (one),
  deployment_id text    NOT NULL,
  max_ttl_ms    bigint  NOT NULL DEFAULT 300000,
  skew_ms       bigint  NOT NULL DEFAULT 30000);

-- Activated Operation Bundles (spec §6). Activation is by hash; there is no in-place edit.
CREATE TABLE deedglass.bundles (
  op         text   NOT NULL,
  op_v       int    NOT NULL,
  hash       text   NOT NULL CHECK (hash ~ '^sha256:[0-9a-f]{64}$'),
  status     text   NOT NULL CHECK (status IN ('active', 'retired')),
  max_amount bigint NOT NULL CHECK (max_amount > 0),
  PRIMARY KEY (op, op_v, hash));

-- Principal keys used for prevention. The verifier keeps its own registry and never trusts this copy.
-- Small-order and non-canonical keys are refused before insert (internal/keys; attack case A13).
CREATE TABLE deedglass.principal_keys (
  kid        text        PRIMARY KEY CHECK (kid ~ '^[0-9a-f]{32}$'),
  iss        text        NOT NULL CHECK (iss ~ '^[a-z0-9._-]{1,64}$'),
  pubkey     bytea       NOT NULL CHECK (octet_length(pubkey) = 32),
  status     text        NOT NULL CHECK (status IN ('active', 'revoked')),
  not_before timestamptz NOT NULL,
  not_after  timestamptz);

-- Per-principal, per-source-account mandates (prevention; the verifier mirrors them in policy.json).
CREATE TABLE deedglass.mandates (
  iss        text   NOT NULL,
  account_id text   NOT NULL,
  max_amount bigint NOT NULL CHECK (max_amount > 0),
  PRIMARY KEY (iss, account_id));

-- The ledger: one row per executed intent. Only deedglass.execute_transfer_v1 (running as deedglass_owner)
-- can insert. PK (kid, nonce) blocks replay (SQLSTATE 23505 -> REPLAY).
CREATE TABLE deedglass.ledger (
  kid         text        NOT NULL,
  nonce       text        NOT NULL,
  iss         text        NOT NULL,
  intent_hash bytea       NOT NULL UNIQUE,
  intent      bytea       NOT NULL,
  sig         bytea       NOT NULL CHECK (octet_length(sig) = 64),
  bundle_hash text        NOT NULL,
  xid8        xid8        NOT NULL,
  exp_ms      bigint      NOT NULL,
  isolation   text        NOT NULL,
  created_at  timestamptz NOT NULL,
  PRIMARY KEY (kid, nonce));

CREATE TABLE bank.accounts (
  id       text   PRIMARY KEY CHECK (id ~ '^[a-z0-9_]{1,40}$'),
  currency text   NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  balance  bigint NOT NULL CHECK (balance >= 0),
  version  bigint NOT NULL DEFAULT 0);

-- INSERT-only, deterministic keys (no sequences): every expected row is predictable.
CREATE TABLE bank.journal (
  intent_hash bytea    NOT NULL,
  leg         smallint NOT NULL CHECK (leg IN (1, 2)),
  account_id  text     NOT NULL REFERENCES bank.accounts (id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  amount      bigint   NOT NULL,
  currency    text     NOT NULL,
  PRIMARY KEY (intent_hash, leg));

-- Protected (boundary) tables: full old images in the WAL, forced row-level security.
ALTER TABLE bank.accounts REPLICA IDENTITY FULL;
ALTER TABLE bank.journal  REPLICA IDENTITY FULL;
ALTER TABLE deedglass.ledger    REPLICA IDENTITY FULL;

ALTER TABLE bank.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE bank.journal  ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.journal  FORCE ROW LEVEL SECURITY;
ALTER TABLE deedglass.ledger    ENABLE ROW LEVEL SECURITY;
ALTER TABLE deedglass.ledger    FORCE ROW LEVEL SECURITY;

CREATE POLICY owner_all ON bank.accounts TO deedglass_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_all ON bank.journal  TO deedglass_owner USING (true) WITH CHECK (true);
CREATE POLICY owner_all ON deedglass.ledger    TO deedglass_owner USING (true) WITH CHECK (true);
-- Reconciliation reads (spec §8.4). Every other role is default-deny even if mis-granted.
CREATE POLICY verifier_read ON deedglass.ledger FOR SELECT TO deedglass_witness USING (true);

REVOKE ALL ON ALL TABLES IN SCHEMA deedglass, bank FROM PUBLIC;
GRANT SELECT ON deedglass.ledger, deedglass.bundles, deedglass.principal_keys, deedglass.config TO deedglass_witness;

COMMIT;
