#!/usr/bin/env bash
# First start of an empty pgdata volume (the postgres image runs this once, as the postgres user):
# the control-plane roles and database (deploy/postgres/control-plane-roles.sql) and the read-only
# backup role (20-backup-role.sql). Passwords come from /etc/deedglass/postgres.env on the host,
# which only root can read; nothing here is printed.
set -euo pipefail
: "${PG_MIGRATOR_PASSWORD:?is empty. Fill /etc/deedglass/postgres.env (GO_LIVE.md step 5).}"
: "${PG_APP_PASSWORD:?is empty. Fill /etc/deedglass/postgres.env (GO_LIVE.md step 5).}"
: "${PG_BACKUP_PASSWORD:?is empty. Fill /etc/deedglass/postgres.env (GO_LIVE.md step 5).}"
psql -X -q -v ON_ERROR_STOP=1 --username postgres --dbname postgres \
  -v migrator_password="$PG_MIGRATOR_PASSWORD" -v app_password="$PG_APP_PASSWORD" \
  -f /deedglass/control-plane-roles.sql
psql -X -q -v ON_ERROR_STOP=1 --username postgres --dbname postgres \
  -v backup_password="$PG_BACKUP_PASSWORD" -v db=deedglass \
  -f /deedglass/20-backup-role.sql
echo "deedglass: roles deedglass_migrator, deedglass_app and deedglass_backup, and the database deedglass"
