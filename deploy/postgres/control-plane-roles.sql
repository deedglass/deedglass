-- control-plane-roles.sql: the Deedglass control-plane roles and database (S02, ADR-022).
--
-- Run once per cluster as an admin (a superuser, or a managed provider's admin role that can
-- create roles and databases):
--
--   psql -v ON_ERROR_STOP=1 -v migrator_password='…' -v app_password='…' -f control-plane-roles.sql
--
-- deedglass_migrator owns the schema and runs `deedglass migrate` (DEEDGLASS_MIGRATE_DATABASE_URL).
-- deedglass_app is the server's runtime role (DEEDGLASS_DATABASE_URL): it owns nothing, so row-level
-- security applies to it, and it can never ALTER a table, disable a trigger or disable RLS.
-- Neither role is a superuser or bypasses RLS, and deedglass_app is not a member of the migrator.
--
-- Role names are cluster-wide. They don't collide with the customer-side roles (deedglass_owner,
-- deedglass_agent, deedglass_witness), and this database must never be a watched customer database:
-- `deedglass migrate up` refuses a database that holds deedglass.ledger or a deedglass_pub publication.
\set ON_ERROR_STOP on

CREATE ROLE deedglass_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'migrator_password';
CREATE ROLE deedglass_app      LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';
CREATE DATABASE deedglass OWNER deedglass_migrator;
REVOKE ALL ON DATABASE deedglass FROM PUBLIC;
GRANT CONNECT ON DATABASE deedglass TO deedglass_app;
