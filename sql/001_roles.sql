-- 001_roles.sql: txv roles (spec 20_ §4.2; plan §3.0 #5, §3.7).
-- Run once per cluster by the installer (customer admin; on RDS a member of rds_superuser).
-- Roles are cluster-wide. No passwords are set here: `txv install` sets them from secrets.
-- Application roles get NO grants on schemas txv or bank, ever.
\set ON_ERROR_STOP on

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;   -- installer session: no search_path shadowing

CREATE ROLE txv_owner    NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE txv_executor LOGIN   NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
-- On RDS: create txv_verifier without REPLICATION, then GRANT rds_replication TO txv_verifier.
CREATE ROLE txv_verifier LOGIN   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT REPLICATION;

-- Executor: bounded transactions, READ COMMITTED (spec §0 #11, §4.2).
ALTER ROLE txv_executor SET statement_timeout = '5s';
ALTER ROLE txv_executor SET idle_in_transaction_session_timeout = '5s';
ALTER ROLE txv_executor SET default_transaction_isolation = 'read committed';

-- Verifier: pgoutput text mode formats values with the decoding session's settings,
-- so every verifier session is pinned (plan §3.0 #5, spike S2). The verifier also sets
-- and asserts these on every poll and records them in attested metadata (attack case E11).
ALTER ROLE txv_verifier SET TimeZone = 'UTC';
ALTER ROLE txv_verifier SET DateStyle = 'ISO, YMD';
ALTER ROLE txv_verifier SET IntervalStyle = 'postgres';
ALTER ROLE txv_verifier SET bytea_output = 'hex';
ALTER ROLE txv_verifier SET extra_float_digits = 3;

COMMIT;
