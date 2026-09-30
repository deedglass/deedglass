-- 001_roles.sql: deedglass roles (spec 20_ §4.2; plan §3.0 #5, §3.7).
-- Run once per cluster by the installer (customer admin; on RDS a member of rds_superuser).
-- Roles are cluster-wide. No passwords are set here: the installer (`deedglass connector postgres init`, S17) sets them.
-- Application roles get NO grants on schemas deedglass or bank, ever.
\set ON_ERROR_STOP on

BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;   -- installer session: no search_path shadowing

CREATE ROLE deedglass_owner    NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE deedglass_agent LOGIN   NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
-- On RDS: create deedglass_witness without REPLICATION, then GRANT rds_replication TO deedglass_witness.
CREATE ROLE deedglass_witness LOGIN   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOINHERIT REPLICATION;

-- Executor: bounded transactions, READ COMMITTED (spec §0 #11, §4.2).
ALTER ROLE deedglass_agent SET statement_timeout = '5s';
ALTER ROLE deedglass_agent SET idle_in_transaction_session_timeout = '5s';
ALTER ROLE deedglass_agent SET default_transaction_isolation = 'read committed';

-- Verifier: pgoutput text mode formats values with the decoding session's settings,
-- so every verifier session is pinned (plan §3.0 #5, spike S2). The verifier also sets
-- and asserts these on every poll and records them in attested metadata (attack case E11).
ALTER ROLE deedglass_witness SET TimeZone = 'UTC';
ALTER ROLE deedglass_witness SET DateStyle = 'ISO, YMD';
ALTER ROLE deedglass_witness SET IntervalStyle = 'postgres';
ALTER ROLE deedglass_witness SET bytea_output = 'hex';
ALTER ROLE deedglass_witness SET extra_float_digits = 3;

COMMIT;
