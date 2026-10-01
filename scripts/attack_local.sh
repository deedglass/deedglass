#!/usr/bin/env bash
# make attack (S18): the V7 attack suite on a THROWAWAY PostgreSQL cluster, then the report.
#
#   DEEDGLASS_IT_PG=18|17|16      major version (default 18)
#   DEEDGLASS_IT_MODE=brew|docker default: brew if /opt/homebrew/opt/postgresql@$DEEDGLASS_IT_PG exists, else docker
#   DEEDGLASS_IT_ATTACK_RUN=<regex>  run some cases only (go test -run 'TestAttack/H03')
#   DEEDGLASS_IT_ADMIN_URL        set it to use a cluster you started yourself (CI's service); the
#                                 script then starts nothing
#
# brew mode: initdb into .tmp/attack-pg$VER (gitignored), trust auth, TCP on localhost only, port
# 5544<ver> (so it never collides with make integration's 5543<ver> or the founder's dev cluster on
# 55432), wal_level=logical; stopped and deleted on exit. Never uses `brew services`.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VER="${DEEDGLASS_IT_PG:-18}"
case "$VER" in
  18) TAG=18.6 ;;
  17) TAG=17.11 ;;
  16) TAG=16.15 ;;
  *) echo "unsupported DEEDGLASS_IT_PG=$VER (16, 17 or 18)" >&2; exit 2 ;;
esac
PORT="5544${VER: -1}"
BIN="/opt/homebrew/opt/postgresql@${VER}/bin"
export DEEDGLASS_IT_THROWAWAY=1

run_suite() {
  cd "$ROOT"
  local st=0
  if [ -n "${DEEDGLASS_IT_ATTACK_RUN:-}" ]; then
    go test -tags attack -count=1 -timeout 10m ${DEEDGLASS_IT_ATTACK_GOFLAGS:-} -run "$DEEDGLASS_IT_ATTACK_RUN" ./attack/... || st=$?
  else
    go test -tags attack -count=1 -timeout 10m ${DEEDGLASS_IT_ATTACK_GOFLAGS:-} ./attack/... || st=$?
  fi
  return $st
}

if [ -n "${DEEDGLASS_IT_ADMIN_URL:-}" ]; then
  st=0; run_suite || st=$?
  exit $st
fi

MODE="${DEEDGLASS_IT_MODE:-}"
if [ -z "$MODE" ]; then
  if [ -x "$BIN/postgres" ]; then MODE=brew; else MODE=docker; fi
fi

case "$MODE" in
  brew)
    DATA="$ROOT/.tmp/attack-pg$VER"
    LOG="$ROOT/.tmp/attack-pg$VER.log"
    mkdir -p "$ROOT/.tmp"
    if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
    "$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
    cat >> "$DATA/postgresql.conf" <<EOF

# --- deedglass attack suite (throwaway) ---
listen_addresses = 'localhost'
port = $PORT
unix_socket_directories = ''
wal_level = logical
max_replication_slots = 10
max_wal_senders = 10
max_connections = 300
EOF
    # Passwords are checked for every role but the installer (K03 needs a wrong password to fail).
    cat > "$DATA/pg_hba.conf" <<EOF
host all postgres 127.0.0.1/32 trust
host all postgres ::1/128 trust
host all all 127.0.0.1/32 scram-sha-256
host all all ::1/128 scram-sha-256
EOF
    cleanup() {
      "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true
      rm -rf "$DATA"
    }
    trap cleanup EXIT
    "$BIN/pg_ctl" -D "$DATA" -l "$LOG" -w start >/dev/null
    echo "attack: throwaway PostgreSQL $("$BIN/postgres" --version | awk '{print $3}') on 127.0.0.1:$PORT"
    export DEEDGLASS_IT_ADMIN_URL="postgres://postgres@127.0.0.1:$PORT/postgres?sslmode=disable"
    st=0; run_suite || st=$?
    exit $st
    ;;
  docker)
    command -v docker >/dev/null || { echo "docker not found" >&2; exit 2; }
    docker info >/dev/null 2>&1 || { echo "docker daemon not running (start Docker Desktop, or use DEEDGLASS_IT_MODE=brew)" >&2; exit 2; }
    NAME="deedglass-attack-pg$VER"
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
    echo "attack: docker postgres:$TAG"
    docker run -d --name "$NAME" -e POSTGRES_PASSWORD=deedglass-it-throwaway -p "127.0.0.1:$PORT:5432" \
      "postgres:$TAG" postgres -c wal_level=logical -c max_connections=300 >/dev/null
    for _ in $(seq 1 60); do
      docker exec "$NAME" pg_isready -U postgres -q && break
      sleep 1
    done
    sleep 1
    export DEEDGLASS_IT_ADMIN_URL="postgres://postgres:deedglass-it-throwaway@127.0.0.1:$PORT/postgres?sslmode=disable"
    st=0; run_suite || st=$?
    exit $st
    ;;
  *) echo "unknown DEEDGLASS_IT_MODE=$MODE" >&2; exit 2 ;;
esac
