#!/usr/bin/env bash
# The FHIR write-integrity benchmark (corpus/fhir/BENCHMARK.md): starts a throwaway Medplum 5.2.6 on
# PostgreSQL 18.6 with Redis in Docker (the database in memory, wal_level=logical, a witness role and
# a publication on the history tables the scenarios write), plays every scenario in corpus/fhir
# through each subject with bench/cmd/fhir-bench, and removes every container at the end. Synthetic
# data only; every port is on 127.0.0.1 and never 8080 or 55432; the logins are made up here.
#
#   scripts/fhir_bench.sh                               none, http-status and deedglass, every scenario
#   SUBJECTS=none,http-status scripts/fhir_bench.sh     some subjects
#   SUBJECTS='none,cmd:mine=./my-checker' scripts/fhir_bench.sh   your checker (corpus/fhir/BENCHMARK.md)
#   RUN=conditional scripts/fhir_bench.sh               the scenarios whose id contains "conditional"
#   OUT=corpus/fhir/results/latest.json scripts/fhir_bench.sh   where the results go (default .tmp/fhir-bench/bench.json)
#   WRITE_EXPECTED=1 scripts/fhir_bench.sh              record expected.json from the baseline (review the diff)
#   KEEP=1 scripts/fhir_bench.sh                        leave the containers up (prints how to remove them)
#
# The deedglass subject builds deedglass from this tree and runs a real serve; its control plane is
# another database on the same throwaway server. The images are pinned by digest (ADR-024).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MEDPLUM="medplum/medplum-server:5.2.6@sha256:2968c9aeb85d03968370855a5dc409a6f28ef5abda0996e353be06c525b61216"
REDIS="redis:7.4.6-alpine@sha256:3b73847e72874be07e6657b129a94761662b79bc0f679273757d4218573b2a98"
PG="postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
TAG="deedglass-fhir-bench-$$"
NET="$TAG-net"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

command -v docker >/dev/null || fail "docker not found. Install Docker, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start it, then run this again."
df -h "$ROOT" | tail -1 | awk '{ print "disk: " $4 " free on " $NF }'

cleanup() {
  if [ "${KEEP:-}" = 1 ]; then
    echo "Containers kept. Remove them with: docker rm -f -v $TAG-med $TAG-pg $TAG-redis; docker network rm $NET"
    return
  fi
  docker rm -f -v "$TAG-med" "$TAG-pg" "$TAG-redis" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

port() {
  local p
  for _ in 1 2 3 4 5; do
    p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
    case "$p" in 8080|8090|8091|55432) continue ;; esac
    echo "$p"; return
  done
  fail "No free port found."
}
secret() { python3 -c 'import secrets; print(secrets.token_hex(16))'; }

PGPASS="$(secret)" WITNESS_PASS="$(secret)" REDIS_PASS="$(secret)" MED_PASS="$(secret)"
PG_PORT="$(port)" MED_PORT="$(port)"
docker network create "$NET" >/dev/null

# Medplum's database lives in memory (tmpfs): nothing is written to disk, and it is gone with the
# container.
docker run -d --name "$TAG-pg" --network "$NET" -p "127.0.0.1:$PG_PORT:5432" --tmpfs /var/lib/postgresql:rw,size=2g \
  -e POSTGRES_PASSWORD="$PGPASS" -e POSTGRES_DB=medplum \
  "$PG" -c wal_level=logical -c max_slot_wal_keep_size=1GB -c max_replication_slots=32 -c max_wal_senders=32 -c max_connections=300 >/dev/null
docker run -d --name "$TAG-redis" --network "$NET" "$REDIS" redis-server --requirepass "$REDIS_PASS" --save '' >/dev/null
for _ in $(seq 1 60); do docker exec "$TAG-pg" pg_isready -h 127.0.0.1 -U postgres -q && break; sleep 1; done
docker exec "$TAG-pg" pg_isready -h 127.0.0.1 -U postgres -q || fail "Postgres didn't start."
docker run -d --name "$TAG-med" --network "$NET" -p "127.0.0.1:$MED_PORT:8103" \
  -e MEDPLUM_PORT=8103 -e MEDPLUM_BASE_URL="http://127.0.0.1:$MED_PORT/" -e MEDPLUM_APP_BASE_URL="http://127.0.0.1:$MED_PORT/" \
  -e MEDPLUM_STORAGE_BASE_URL="http://127.0.0.1:$MED_PORT/storage/" -e MEDPLUM_BINARY_STORAGE=file:/tmp/binary/ \
  -e MEDPLUM_DATABASE_HOST="$TAG-pg" -e MEDPLUM_DATABASE_PORT=5432 -e MEDPLUM_DATABASE_DBNAME=medplum \
  -e MEDPLUM_DATABASE_USERNAME=postgres -e MEDPLUM_DATABASE_PASSWORD="$PGPASS" \
  -e MEDPLUM_REDIS_HOST="$TAG-redis" -e MEDPLUM_REDIS_PORT=6379 -e MEDPLUM_REDIS_PASSWORD="$REDIS_PASS" \
  -e MEDPLUM_SUPPORT_EMAIL=support@example.test -e MEDPLUM_SHUTDOWN_TIMEOUT_MILLISECONDS=5000 \
  -e MEDPLUM_DEFAULT_SUPER_ADMIN_EMAIL=bench-admin@example.test -e MEDPLUM_DEFAULT_SUPER_ADMIN_PASSWORD="$MED_PASS" \
  "$MEDPLUM" env >/dev/null
for _ in $(seq 1 240); do
  curl -fsS -o /dev/null "http://127.0.0.1:$MED_PORT/healthcheck" 2>/dev/null && break
  docker inspect -f '{{.State.Running}}' "$TAG-med" 2>/dev/null | grep -q true || { docker logs --tail 40 "$TAG-med" >&2 || true; fail "Medplum stopped while starting."; }
  sleep 1
done
curl -fsS -o /dev/null "http://127.0.0.1:$MED_PORT/healthcheck" || fail "Medplum didn't answer within 4 minutes."
ok "Medplum ${MEDPLUM%@*} on ${PG%@*} (in memory): http://127.0.0.1:$MED_PORT/fhir/R4"

# What the database owner does for a witnessed fhir connector (docs/connectors/fhir.md).
docker exec -i "$TAG-pg" psql -U postgres -d medplum -v ON_ERROR_STOP=1 -q <<SQL
CREATE ROLE deedglass_witness LOGIN REPLICATION PASSWORD '$WITNESS_PASS';
CREATE PUBLICATION deedglass_pub FOR TABLE "MedicationRequest_History", "Observation_History", "Patient_History";
SQL
ok "witness role and publication created"

OUT="${OUT:-$ROOT/.tmp/fhir-bench/bench.json}"
mkdir -p "$(dirname "$OUT")"
args=(-dir "$ROOT/corpus/fhir" -subjects "${SUBJECTS:-none,http-status,deedglass}" -out "$OUT")
[ -n "${RUN:-}" ] && args+=(-run "$RUN")
[ "${WRITE_EXPECTED:-}" = 1 ] && args+=(-write-expected)
cd "$ROOT"
DEEDGLASS_IT_THROWAWAY=1 DEEDGLASS_IT_ADMIN_URL="postgres://postgres:$PGPASS@127.0.0.1:$PG_PORT/postgres?sslmode=disable" \
FHIRBENCH_MEDPLUM_URL="http://127.0.0.1:$MED_PORT" FHIRBENCH_MEDPLUM_ADMIN=bench-admin@example.test FHIRBENCH_MEDPLUM_PASSWORD="$MED_PASS" \
FHIRBENCH_MEDPLUM_DSN="postgres://postgres:$PGPASS@127.0.0.1:$PG_PORT/medplum?sslmode=disable" \
FHIRBENCH_MEDPLUM_WITNESS_DSN="postgres://deedglass_witness:$WITNESS_PASS@127.0.0.1:$PG_PORT/medplum?sslmode=disable" \
FHIRBENCH_MEDPLUM_VERSION=5.2.6 FHIRBENCH_MEDPLUM_IMAGE="$MEDPLUM" \
  go run -tags integration ./bench/cmd/fhir-bench "${args[@]}"
