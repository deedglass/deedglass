-- 004_publication.sql: the observation boundary publication (spec 20_ §8.2, §9.1).
-- Run by the installer after 003_transfer_v1.sql and BEFORE the slot is created: on PG18+ a
-- missing publication only WARNs and silently skips changes, so order matters.
-- Explicit table list (no FOR ALL TABLES, no row filters, no column lists, no partitions).
-- The slot itself is created afterwards by `txv install`, connected as txv_verifier:
--   SELECT * FROM pg_create_logical_replication_slot('txv_slot', 'pgoutput', false, false);
--   (PG17+: failover => true for HA pilots; not tested in V0)
-- Optional self-hosted hardening (`txv install --harden`, spec §4.5) revokes EXECUTE on
-- pg_logical_emit_message from PUBLIC; it is defence in depth only and is not applied here.
\set ON_ERROR_STOP on

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;

CREATE PUBLICATION txv_pub FOR TABLE bank.accounts, bank.journal, txv.ledger
  WITH (publish = 'insert, update, delete, truncate');

COMMIT;
