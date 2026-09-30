#!/usr/bin/env bash
# First-start setup for the local Postgres in deploy/docker-compose.yml. The postgres image runs every
# executable file in /docker-entrypoint-initdb.d once, as the postgres user, on an empty volume only.
#
#  1. The control plane: deploy/postgres/control-plane-roles.sql (mounted at /deedglass/) creates
#     deedglass_migrator, deedglass_app and the database deedglass, owned by the migrator (ADR-022).
#  2. The customer side: an empty database demo, used by the demo stack and the Postgres connector
#     in later sessions. It must never be the control plane (docs/DATABASE_SETUP.md).
#
# The passwords come from DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD and DEEDGLASS_DEV_PG_APP_PASSWORD, which
# scripts/dev.sh writes to .env. An existing volume keeps its roles; make dev-reset starts over.
set -euo pipefail

: "${DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD:?is empty. Run make dev, which fills .env.}"
: "${DEEDGLASS_DEV_PG_APP_PASSWORD:?is empty. Run make dev, which fills .env.}"

psql -X -q -v ON_ERROR_STOP=1 --username "${POSTGRES_USER:-postgres}" --dbname postgres \
  -v migrator_password="$DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD" \
  -v app_password="$DEEDGLASS_DEV_PG_APP_PASSWORD" \
  -f /deedglass/control-plane-roles.sql

psql -X -q -v ON_ERROR_STOP=1 --username "${POSTGRES_USER:-postgres}" --dbname postgres \
  -c 'CREATE DATABASE demo'

echo "deedglass: created roles deedglass_migrator and deedglass_app, and databases deedglass and demo"
