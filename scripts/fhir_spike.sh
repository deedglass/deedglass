#!/usr/bin/env bash
# The HC-P05 spike as a repeatable probe (ADR-105, "HC-P05 spike findings"): what a Postgres-backed
# FHIR server stores that could tie its own rows to a Deedglass action. It starts HAPI FHIR JPA on
# PostgreSQL in Docker (a private network, a free localhost port, both images pinned by digest),
# sends a few writes, reads the version rows back from Postgres, and checks each finding. Synthetic
# data only; everything is removed on exit. Run it again when the pinned HAPI image changes: a
# finding that no longer holds fails the run and the line says which.
#
#   scripts/fhir_spike.sh          (make fhir-spike)
set -euo pipefail

HAPI="hapiproject/hapi:v8.12.0-2@sha256:0ff86aac4c7bd13a1ee226444a70263bb46ed1c28ef543c30a44f865b80ca04e"
PG="postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
TAG="deedglass-fhir-spike-$$"
NET="$TAG-net"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
bad()  { printf '%s✕%s %s\n' "$RED" "$RESET" "$*"; FAILED=1; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
FAILED=0

command -v docker >/dev/null || fail "docker not found. Install Docker Desktop, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start Docker Desktop, then run this again."
command -v curl >/dev/null || fail "curl not found."

cleanup() {
  docker rm -f -v "$TAG-hapi" "$TAG-pg" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
case "$PORT" in 8080|8090|8091|55432) fail "Got a reserved port ($PORT); run it again." ;; esac
U="http://127.0.0.1:$PORT/fhir"

docker network create "$NET" >/dev/null
docker run -d --name "$TAG-pg" --network "$NET" -e POSTGRES_PASSWORD=spike-throwaway -e POSTGRES_DB=hapi "$PG" >/dev/null
for _ in $(seq 1 60); do docker exec "$TAG-pg" pg_isready -U postgres -q && break; sleep 1; done

# start_hapi [extra -e flags…]: (re)start HAPI on the same database and wait for /metadata.
start_hapi() {
  docker rm -f "$TAG-hapi" >/dev/null 2>&1 || true
  docker run -d --name "$TAG-hapi" --network "$NET" -p "127.0.0.1:$PORT:8080" \
    -e spring.datasource.url="jdbc:postgresql://$TAG-pg:5432/hapi" -e spring.datasource.username=postgres \
    -e spring.datasource.password=spike-throwaway -e spring.datasource.driverClassName=org.postgresql.Driver \
    -e spring.jpa.properties.hibernate.dialect=ca.uhn.fhir.jpa.model.dialect.HapiFhirPostgresDialect \
    "$@" "$HAPI" >/dev/null
  for _ in $(seq 1 180); do
    curl -fsS -o /dev/null "$U/metadata" 2>/dev/null && return
    docker inspect -f '{{.State.Running}}' "$TAG-hapi" 2>/dev/null | grep -q true || { docker logs --tail 40 "$TAG-hapi" >&2; fail "HAPI stopped while starting."; }
    sleep 1
  done
  fail "HAPI didn't answer $U/metadata within 3 minutes."
}
sql() { docker exec "$TAG-pg" psql -U postgres -d hapi -AtF '|' -c "$1"; }
# post BODY [headers…]: create an Observation; prints "status location".
post() { local b="$1"; shift; curl -s -o /dev/null -w '%{http_code} %header{location}' -X POST "$U/Observation" -H 'Content-Type: application/fhir+json' "$@" -d "$b"; }
obs() { printf '{"resourceType":"Observation",%s"status":"final","code":{"text":"heart rate"},"subject":{"reference":"Patient/%s"},"valueQuantity":{"value":%s}}' "${3:-}" "$1" "$2"; }
idof() { sed -E 's#.*/Observation/([^/]+)/_history/.*#\1#'; }
row() { sql "select v.request_id, coalesce(v.source_uri,''), v.xmin from hfj_res_ver v join hfj_resource r on r.res_id = v.res_id where r.res_type = '$1' and r.fhir_id = '$2' order by v.res_ver desc limit 1"; }

echo "HAPI FHIR JPA ${HAPI%@*} on ${PG%@*}, http://127.0.0.1:$PORT/fhir"

# 1. The starter's defaults store neither the request id nor meta.source.
start_hapi -e hapi.fhir.auto_create_placeholder_reference_targets=true
id="$(post "$(obs ghost-0 70 '"meta":{"source":"urn:example:spike"},')" -H 'X-Request-ID: aaaaaaaaaaaaaaaa' | idof)"
if [ "$(row Observation "$id")" = "||$(row Observation "$id" | cut -d'|' -f3)" ]; then
  ok "(a) starter defaults: request_id and source_uri stay empty (store_meta_source_information NONE)"
else bad "(a) starter defaults stored something: $(row Observation "$id")"; fi

# 2. With SOURCE_URI_AND_REQUEST_ID, X-Request-ID lands in request_id, truncated to 16 characters.
start_hapi -e hapi.fhir.auto_create_placeholder_reference_targets=true -e hapi.fhir.store_meta_source_information=SOURCE_URI_AND_REQUEST_ID
a="$(post "$(obs ghost-1 71)" -H 'X-Request-ID: act_01m58nq2jthtfkkv8qdejqts5b' | idof)"
b="$(post "$(obs ghost-1 72)" -H 'X-Request-ID: act_01m58nq2jthtfkkv8qdejqts5c' | idof)"
ra="$(row Observation "$a" | cut -d'|' -f1)" rb="$(row Observation "$b" | cut -d'|' -f1)"
[ "$(sql "select character_maximum_length from information_schema.columns where table_name = 'hfj_res_ver' and column_name = 'request_id'")" = 16 ] &&
  ok "(a) hfj_res_ver.request_id is varchar(16)" || bad "(a) request_id isn't varchar(16)"
[ "$ra" = "act_01m58nq2jtht" ] && [ "$ra" = "$rb" ] &&
  ok "(a) a 30-character act_ id is cut to $ra, the same for two different actions" || bad "(a) act_ ids stored as '$ra' and '$rb'"
c="$(post "$(obs ghost-1 73)" -H 'X-Request-ID: 8qdejqts5bk2v7hm' | idof)"
[ "$(row Observation "$c" | cut -d'|' -f1)" = "8qdejqts5bk2v7hm" ] && ok "(a) a 16-character token is stored whole" || bad "(a) the 16-character token: $(row Observation "$c")"
d="$(post "$(obs ghost-1 74)" -H 'X-Request-ID: act_01m5/../x;y' | idof)"
[ "$(row Observation "$d" | cut -d'|' -f1)" != "act_01m5/../x;y" ] && ok "(a) a token with / . ; is replaced by a generated id" || bad "(a) an odd token was stored"
e="$(post "$(obs ghost-1 75 '"meta":{"source":"urn:deedglass:act_01m58nq2jthtfkkv8qdejqts5d"},')" | idof)"
[ "$(row Observation "$e" | cut -d'|' -f2)" = "urn:deedglass:act_01m58nq2jthtfkkv8qdejqts5d" ] &&
  ok "(a) a client's meta.source goes to source_uri (client-controlled)" || bad "(a) meta.source: $(row Observation "$e")"
[ "$(sql "select count(*) from hfj_res_ver where res_text_vc like '%\"meta\"%' or res_text_vc like '%\"id\":%'")" = 0 ] &&
  ok "(a) the stored JSON has no id or meta; they come from hfj_resource and hfj_res_ver columns" || bad "(a) stored JSON carries id or meta"

# 3. Placeholder reference targets: one create, two versions in one transaction.
f="$(post "$(obs ghost-9 76)" -H 'X-Request-ID: 9placeholder0001' | idof)"
pat="$(row Patient ghost-9)" ob="$(row Observation "$f")"
[ -n "$pat" ] && [ "$(echo "$pat" | cut -d'|' -f3)" = "$(echo "$ob" | cut -d'|' -f3)" ] && [ "$(echo "$pat" | cut -d'|' -f1)" = 9placeholder0001 ] &&
  ok "(c) one Observation create committed Patient/ghost-9 too: same transaction (xmin), same request_id" || bad "(c) placeholder: patient '$pat', observation '$ob'"

# 4. The traps the read-back must know.
cc='"identifier":[{"system":"urn:example:spike","value":"cc-1"}],'
g="$(post "$(obs ghost-1 90 "$cc")" -H 'If-None-Exist: identifier=urn:example:spike|cc-1')"
h="$(post "$(obs ghost-1 95 "$cc")" -H 'If-None-Exist: identifier=urn:example:spike|cc-1')"
[ "${g%% *}" = 201 ] && [ "${h%% *}" = 200 ] && [ "$(echo "$g" | idof)" = "$(echo "$h" | idof)" ] &&
  ok "conditional create: the second answers 200 with the first resource and writes nothing" || bad "conditional create: '$g' then '$h'"
put() { curl -s -o /dev/null -w '%{http_code} %header{etag}' -X PUT "$U/Observation/$c" -H 'Content-Type: application/fhir+json' "$@"; }
body="{\"resourceType\":\"Observation\",\"id\":\"$c\",\"status\":\"amended\",\"code\":{\"text\":\"heart rate\"},\"subject\":{\"reference\":\"Patient/ghost-1\"},\"valueQuantity\":{\"value\":73}}"
p1="$(put -d "$body")" p2="$(put -d "$body")"
[ "${p1#* }" = "${p2#* }" ] && [ "$(sql "select count(*) from hfj_res_ver v join hfj_resource r on r.res_id = v.res_id where r.fhir_id = '$c'")" = 2 ] &&
  ok "no-change update: 200 with the same ETag (${p2#* }) and no new version row" || bad "no-change update: '$p1' then '$p2'"
p3="$(put -H 'If-Match: W/"1"' -d "${body/amended/corrected}")"
[ "${p3%% *}" = 409 ] && ok "a stale If-Match answers 409, not 412" || bad "a stale If-Match answered ${p3%% *}"

[ "$FAILED" = 0 ] || fail "A finding no longer holds. Update ADR-105's spike section before relying on it."
ok "every finding in ADR-105's HC-P05 section holds for ${HAPI%@*}"
