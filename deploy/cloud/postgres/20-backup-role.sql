-- 20-backup-role.sql: the read-only backup role for the cloud kit (S35, ADR-068).
--
-- Row-level security makes pg_dump fail or skip rows for an ordinary role, so the nightly dump runs
-- as a dedicated role that can read every row and change nothing: BYPASSRLS, pg_read_all_data and a
-- read-only default. Run once per cluster as a superuser, after control-plane-roles.sql:
--
--   psql -v ON_ERROR_STOP=1 -v backup_password='…' -v db=deedglass -f 20-backup-role.sql
--
-- The password is typed on the server (openssl rand -base64 32), never committed or pasted in chat.
\set ON_ERROR_STOP on
BEGIN;
CREATE ROLE deedglass_backup LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS PASSWORD :'backup_password';
GRANT pg_read_all_data TO deedglass_backup;
ALTER ROLE deedglass_backup SET default_transaction_read_only = on;
REVOKE ALL ON DATABASE :"db" FROM deedglass_backup;
GRANT CONNECT ON DATABASE :"db" TO deedglass_backup;
COMMIT;
