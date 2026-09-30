#!/usr/bin/env bash
# Integration smoke run: apply sql/001-004 to a FRESH database on a THROWAWAY cluster, then run
# scripts/integration_smoke.sql (one real transfer, a few refusals, and decode checks through both
# pgoutput and test_decoding). This is not the attack suite; attack/ holds that (from Tue 09-29).
#
# It drops and recreates database $DEEDGLASS_IT_DB and the cluster-wide roles deedglass_owner, deedglass_agent,
# deedglass_witness and deedglass_it_app, so it refuses to run unless DEEDGLASS_IT_THROWAWAY=1 (plan §3.12 rule 10:
# never production or customer databases).
#
# Environment:
#   DEEDGLASS_IT_THROWAWAY=1   required
#   PSQL                 psql command (default: psql). CI uses "docker exec -i <container> psql".
#   PGUSER               installer role, a superuser on the throwaway cluster (default: postgres)
#   PGHOST/PGPORT/PGPASSWORD  honoured by psql when it connects over TCP
#   DEEDGLASS_IT_DB            database name (default: deedglass_it)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[ "${DEEDGLASS_IT_THROWAWAY:-}" = "1" ] || { echo "refusing: set DEEDGLASS_IT_THROWAWAY=1 (throwaway clusters only)" >&2; exit 2; }
read -r -a PSQL_CMD <<< "${PSQL:-psql}"
DB="${DEEDGLASS_IT_DB:-deedglass_it}"
USER_NAME="${PGUSER:-postgres}"
q() { "${PSQL_CMD[@]}" -X -q -v ON_ERROR_STOP=1 -U "$USER_NAME" "$@"; }

echo "== server"
q -d postgres -Atc "select version()"
wal="$(q -d postgres -Atc "show wal_level")"
[ "$wal" = "logical" ] || { echo "FAIL: wal_level is '$wal', need logical" >&2; exit 1; }
echo "wal_level=$wal max_slot_wal_keep_size=$(q -d postgres -Atc "show max_slot_wal_keep_size")"

echo "== reset $DB and deedglass roles"
if [ "$(q -d postgres -Atc "select count(*) from pg_database where datname = '$DB'")" = "1" ]; then
  q -d "$DB" -Atc "select pg_drop_replication_slot(slot_name) from pg_replication_slots where database = '$DB'" >/dev/null
  q -d postgres -c "DROP DATABASE $DB"
fi
q -d postgres -c "DROP ROLE IF EXISTS deedglass_it_app, deedglass_agent, deedglass_witness, deedglass_owner"
q -d postgres -c "CREATE DATABASE $DB"

echo "== apply sql/"
for f in "$ROOT"/sql/0*.sql; do
  echo "   $(basename "$f")"
  q -d "$DB" < "$f"
done

FN_SRC_SHA256="$(sed -n 's/.*"fn_src_sha256": *"\([0-9a-f]\{64\}\)".*/\1/p' "$ROOT/bundles/transfer_v1/manifest.json")"
[ -n "$FN_SRC_SHA256" ] || { echo "FAIL: fn_src_sha256 not found in manifest" >&2; exit 1; }

echo "== smoke"
q -d "$DB" -v fn_src_sha256="$FN_SRC_SHA256" < "$ROOT/scripts/integration_smoke.sql"

echo "== cleanup"
q -d "$DB" -Atc "select pg_drop_replication_slot(slot_name) from pg_replication_slots where database = '$DB'" >/dev/null
echo "PASS: integration smoke"
