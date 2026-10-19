#!/usr/bin/env bash
# HC-P05's live run (ADR-108): Proven (L2) against real FHIR servers on Postgres, in Docker.
# It starts HAPI FHIR JPA on PostgreSQL 18 and Medplum on PostgreSQL 18 (with Redis), each database
# with wal_level=logical, a witness role (LOGIN REPLICATION) and the publication deedglass_pub on the
# server's version tables, all on free localhost ports (never 8080 or 55432) and a private network;
# then runs cmd/deedglass TestFHIRProvenLive on a throwaway control-plane Postgres
# (scripts/integration_local.sh); then removes every container and the network. Synthetic data only.
# The images are pinned by digest (ADR-024). The Medplum admin login is made up here for this run.
#
#   scripts/fhir_proven.sh          (make fhir-proven-it)
#   KEEP=1 scripts/fhir_proven.sh   leave the containers up (prints how to remove them)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HAPI="hapiproject/hapi:v8.12.0-2@sha256:0ff86aac4c7bd13a1ee226444a70263bb46ed1c28ef543c30a44f865b80ca04e"
MEDPLUM="medplum/medplum-server:5.2.6@sha256:2968c9aeb85d03968370855a5dc409a6f28ef5abda0996e353be06c525b61216"
REDIS="redis:7.4.6-alpine@sha256:3b73847e72874be07e6657b129a94761662b79bc0f679273757d4218573b2a98"
PG="postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
TAG="deedglass-proven-$$"
NET="$TAG-net"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

command -v docker >/dev/null || fail "docker not found. Install Docker Desktop, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start Docker Desktop, then run this again."
command -v curl >/dev/null || fail "curl not found."
df -h "$ROOT" | tail -1 | awk '{ print "disk: " $4 " free on " $NF }'

cleanup() {
  if [ "${KEEP:-}" = 1 ]; then
    echo "Containers kept. Remove them with: docker rm -f -v $TAG-hapi $TAG-hapi-pg $TAG-med $TAG-med-pg $TAG-redis; docker network rm $NET"
    return
  fi
  docker rm -f -v "$TAG-hapi" "$TAG-hapi-pg" "$TAG-med" "$TAG-med-pg" "$TAG-redis" >/dev/null 2>&1 || true
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
wait_pg() { for _ in $(seq 1 60); do docker exec "$1" pg_isready -U postgres -q && return; sleep 1; done; fail "$1 didn't start."; }
wait_http() {
  for _ in $(seq 1 240); do
    curl -fsS -o /dev/null "$2" 2>/dev/null && return
    docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null | grep -q true || { docker logs --tail 40 "$1" >&2 || true; fail "$1 stopped while starting."; }
    sleep 1
  done
  docker logs --tail 40 "$1" >&2 || true
  fail "$1 didn't answer $2 within 4 minutes."
}
psql_in() { docker exec -i "$1" psql -U postgres -d "$2" -v ON_ERROR_STOP=1 -q; }

PGPASS="$(secret)" WITNESS_PASS="$(secret)" REDIS_PASS="$(secret)" MED_PASS="$(secret)"
HAPI_PG_PORT="$(port)" HAPI_PORT="$(port)" MED_PG_PORT="$(port)" MED_PORT="$(port)"
docker network create "$NET" >/dev/null

# HAPI FHIR JPA on PostgreSQL 18: request ids stored (SOURCE_URI_AND_REQUEST_ID) and placeholder
# reference targets on, as in HC-P05's spike.
docker run -d --name "$TAG-hapi-pg" --network "$NET" -p "127.0.0.1:$HAPI_PG_PORT:5432" -e POSTGRES_PASSWORD="$PGPASS" -e POSTGRES_DB=hapi \
  "$PG" -c wal_level=logical -c max_slot_wal_keep_size=1GB >/dev/null
wait_pg "$TAG-hapi-pg"
docker run -d --name "$TAG-hapi" --network "$NET" -p "127.0.0.1:$HAPI_PORT:8080" \
  -e spring.datasource.url="jdbc:postgresql://$TAG-hapi-pg:5432/hapi" -e spring.datasource.username=postgres \
  -e spring.datasource.password="$PGPASS" -e spring.datasource.driverClassName=org.postgresql.Driver \
  -e spring.jpa.properties.hibernate.dialect=ca.uhn.fhir.jpa.model.dialect.HapiFhirPostgresDialect \
  -e hapi.fhir.store_meta_source_information=SOURCE_URI_AND_REQUEST_ID -e hapi.fhir.auto_create_placeholder_reference_targets=true \
  "$HAPI" >/dev/null

# Medplum on PostgreSQL 18 with Redis; its first start creates the schema and a super admin whose
# login is the one made up above.
docker run -d --name "$TAG-med-pg" --network "$NET" -p "127.0.0.1:$MED_PG_PORT:5432" -e POSTGRES_PASSWORD="$PGPASS" -e POSTGRES_DB=medplum \
  "$PG" -c wal_level=logical -c max_slot_wal_keep_size=1GB >/dev/null
docker run -d --name "$TAG-redis" --network "$NET" "$REDIS" redis-server --requirepass "$REDIS_PASS" >/dev/null
wait_pg "$TAG-med-pg"
docker run -d --name "$TAG-med" --network "$NET" -p "127.0.0.1:$MED_PORT:8103" \
  -e MEDPLUM_PORT=8103 -e MEDPLUM_BASE_URL="http://127.0.0.1:$MED_PORT/" -e MEDPLUM_APP_BASE_URL="http://127.0.0.1:$MED_PORT/" \
  -e MEDPLUM_STORAGE_BASE_URL="http://127.0.0.1:$MED_PORT/storage/" -e MEDPLUM_BINARY_STORAGE=file:/tmp/binary/ \
  -e MEDPLUM_DATABASE_HOST="$TAG-med-pg" -e MEDPLUM_DATABASE_PORT=5432 -e MEDPLUM_DATABASE_DBNAME=medplum \
  -e MEDPLUM_DATABASE_USERNAME=postgres -e MEDPLUM_DATABASE_PASSWORD="$PGPASS" \
  -e MEDPLUM_REDIS_HOST="$TAG-redis" -e MEDPLUM_REDIS_PORT=6379 -e MEDPLUM_REDIS_PASSWORD="$REDIS_PASS" \
  -e MEDPLUM_SUPPORT_EMAIL=support@example.test -e MEDPLUM_SHUTDOWN_TIMEOUT_MILLISECONDS=5000 \
  -e MEDPLUM_DEFAULT_SUPER_ADMIN_EMAIL=it-admin@example.test -e MEDPLUM_DEFAULT_SUPER_ADMIN_PASSWORD="$MED_PASS" \
  "$MEDPLUM" env >/dev/null

wait_http "$TAG-hapi" "http://127.0.0.1:$HAPI_PORT/fhir/metadata"
ok "HAPI FHIR JPA ${HAPI%@*} on ${PG%@*}: http://127.0.0.1:$HAPI_PORT/fhir"
wait_http "$TAG-med" "http://127.0.0.1:$MED_PORT/healthcheck"
ok "Medplum ${MEDPLUM%@*} on ${PG%@*}: http://127.0.0.1:$MED_PORT/fhir/R4"

# What the database owner does for a witnessed fhir connector (docs/connectors/fhir.md): a witness
# role with REPLICATION and a publication limited to the server's version tables.
psql_in "$TAG-hapi-pg" hapi <<SQL
CREATE ROLE deedglass_witness LOGIN REPLICATION PASSWORD '$WITNESS_PASS';
CREATE PUBLICATION deedglass_pub FOR TABLE hfj_resource, hfj_res_ver, hfj_history_tag, hfj_res_tag, hfj_tag_def;
SQL
psql_in "$TAG-med-pg" medplum <<SQL
CREATE ROLE deedglass_witness LOGIN REPLICATION PASSWORD '$WITNESS_PASS';
CREATE PUBLICATION deedglass_pub FOR TABLE "Observation_History", "Patient_History";
SQL
ok "witness roles and publications created"

DEEDGLASS_IT_HAPI_URL="http://127.0.0.1:$HAPI_PORT/fhir" \
DEEDGLASS_IT_HAPI_WITNESS_DSN="postgres://deedglass_witness:$WITNESS_PASS@127.0.0.1:$HAPI_PG_PORT/hapi?sslmode=disable" \
DEEDGLASS_IT_HAPI_ADMIN_DSN="postgres://postgres:$PGPASS@127.0.0.1:$HAPI_PG_PORT/hapi?sslmode=disable" \
DEEDGLASS_IT_MEDPLUM_URL="http://127.0.0.1:$MED_PORT" \
DEEDGLASS_IT_MEDPLUM_ADMIN="it-admin@example.test" DEEDGLASS_IT_MEDPLUM_PASSWORD="$MED_PASS" \
DEEDGLASS_IT_MEDPLUM_WITNESS_DSN="postgres://deedglass_witness:$WITNESS_PASS@127.0.0.1:$MED_PG_PORT/medplum?sslmode=disable" \
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run ^TestFHIRProvenLive$" \
  "$ROOT/scripts/integration_local.sh"
