#!/usr/bin/env bash
# Integration run on a THROWAWAY cluster.
#  1. Customer side (V0 demo): apply sql/001-004 to a FRESH database, then scripts/integration_smoke.sql
#     (one real transfer, a few refusals, and decode checks through both pgoutput and test_decoding).
#  2. Control plane (S02), when DEEDGLASS_IT_ADMIN_URL is set: deploy/postgres/control-plane-roles.sql,
#     a live `deedglass migrate up` (twice) and `verify`, the acceptance checks in psql, then the Go
#     integration suites (go test -tags integration).
# This is not the attack suite; attack/ holds that.
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
#   DEEDGLASS_IT_PKGS          the Go packages to test (default: every integration suite)
#   DEEDGLASS_IT_GOFLAGS       extra go test flags (-v, -run X, -coverprofile=…)
#   DEEDGLASS_IT_ADMIN_URL     postgres:// URL of the same cluster as the superuser, reachable from this host;
#                              enables step 2 (make integration sets it; CI maps the service port)
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

if [ -z "${DEEDGLASS_IT_ADMIN_URL:-}" ]; then
  echo "SKIP: control plane (set DEEDGLASS_IT_ADMIN_URL to run the S02 checks and the Go integration suites)"
  exit 0
fi
IT_PW=deedglass-it-throwaway
# Same host and port as the admin URL, as the migrator, on database deedglass.
MIG_URL="$(printf '%s' "$DEEDGLASS_IT_ADMIN_URL" | sed -E "s#^postgres(ql)?://[^@]*@([^/?]+).*#postgres://deedglass_migrator:${IT_PW}@\2/deedglass?sslmode=disable#")"
app() { "${PSQL_CMD[@]}" -X -q -v ON_ERROR_STOP=1 -U deedglass_app -d deedglass "$@"; }

echo "== control plane: reset, then deploy/postgres/control-plane-roles.sql"
for d in $(q -d postgres -Atc "select datname from pg_database where datname = 'deedglass' or datname like 'deedglass\_it\_%'"); do
  q -d postgres -c "DROP DATABASE $d WITH (FORCE)"
done
q -d postgres -c "SET client_min_messages = warning" -c "DROP ROLE IF EXISTS deedglass_app, deedglass_migrator"
q -d postgres -v migrator_password="$IT_PW" -v app_password="$IT_PW" < "$ROOT/deploy/postgres/control-plane-roles.sql"

echo "== control plane: deedglass migrate (live)"
DG=("go" "run" "$ROOT/cmd/deedglass")
cd "$ROOT"
out="$(DEEDGLASS_ENV=dev "${DG[@]}" migrate up --database-url "$MIG_URL" --no-color)"; echo "$out"
nmig="$(ls "$ROOT"/internal/store/migrations/[0-9][0-9][0-9][0-9]_*.sql | wc -l | tr -d ' ')"
grep -q "Schema at $(printf '%04d' "$nmig") · $nmig applied" <<<"$out" || { echo "FAIL: first migrate up" >&2; exit 1; }
out="$(DEEDGLASS_ENV=dev "${DG[@]}" migrate up --database-url "$MIG_URL" --no-color)"; echo "$out"
grep -q "Schema is current" <<<"$out" || { echo "FAIL: second migrate up" >&2; exit 1; }
DEEDGLASS_ENV=dev "${DG[@]}" migrate verify --database-url "$MIG_URL" --no-color

echo "== control plane: acceptance checks"
denied="$(app -c "UPDATE deedglass.deeds SET seal = 'x'" 2>&1 || true)"
echo "$denied"
grep -q "permission denied for table deeds" <<<"$denied" || { echo "FAIL: deedglass_app could UPDATE deeds" >&2; exit 1; }
norls="$(q -d deedglass -Atc "SELECT string_agg(relname, ',' ORDER BY relname) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE nspname = 'deedglass' AND relkind = 'r' AND NOT relrowsecurity")"
echo "tables without RLS: $norls"
[ "$norls" = "schema_migrations,seal_keys" ] || { echo "FAIL: tables without RLS: $norls" >&2; exit 1; }

echo "== control plane: Go integration suites"
go test -tags integration -count=1 ${DEEDGLASS_IT_GOFLAGS:-} ${DEEDGLASS_IT_PKGS:-./internal/store/... ./internal/gate/... ./internal/ledger/... ./internal/identity/... ./internal/sqlguard/ ./internal/vault/ ./internal/connector/postgres/ ./internal/decoder/ ./internal/approve/ ./internal/notify/... ./cmd/deedglass/}
if [ -n "${DEEDGLASS_IT_BENCH_OUT:-}" ]; then
  echo "== store budget (S02 §3.9) -> $DEEDGLASS_IT_BENCH_OUT"
  run="${DEEDGLASS_IT_BENCH_RUN:-TestPerfBudget|TestExplainShowsInitPlan}"
  pkg="${DEEDGLASS_IT_BENCH_PKG:-./internal/store/}"
  DEEDGLASS_IT_BENCH=1 go test -tags integration -count=1 -timeout 30m -json -run "$run" "$pkg" > "$DEEDGLASS_IT_BENCH_OUT"
  sed -n 's/.*PERF \({.*}\)\\n".*/\1/p' "$DEEDGLASS_IT_BENCH_OUT" | sed 's/\\"/"/g'
  first="${run%%|*}"
  grep -q '"Action":"pass","Package":"github.com/deedglass/deedglass/'"${pkg#./}"'","Test":"'"$first"'"' "$DEEDGLASS_IT_BENCH_OUT" ||
    grep -q '"Action":"pass","Package":"github.com/deedglass/deedglass/'"$(echo "${pkg#./}" | sed 's|/$||')"'","Test":"'"$first"'"' "$DEEDGLASS_IT_BENCH_OUT" || { echo "FAIL: budget $first" >&2; exit 1; }
fi
echo "PASS: control plane"
