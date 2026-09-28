#!/usr/bin/env bash
# Local integration run (`make integration`) on a THROWAWAY PostgreSQL cluster, then scripts/integration.sh.
#
#   TXV_PG=18|17|16            major version (default 18)
#   TXV_IT_MODE=brew|docker    default: brew if /opt/homebrew/opt/postgresql@$TXV_PG exists, else docker
#   TXV_IT_KEEP=1              brew mode: keep the data directory (.tmp/pgdata$TXV_PG) after the run
#
# brew mode: initdb into .tmp/ (gitignored), trust auth, TCP on localhost only (no Unix socket: some
# checkout paths exceed the socket path limit), wal_level=logical, max_slot_wal_keep_size=2GB; the
# cluster is stopped (and deleted unless TXV_IT_KEEP=1) on exit. Never uses `brew services`.
# docker mode: postgres:<pinned minor> with the same -c flags as deploy/docker-compose.yml; removed on exit.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"   # macOS: Homebrew postmaster aborts "became multithreaded" without it

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VER="${TXV_PG:-18}"
case "$VER" in
  18) TAG=18.6 ;;
  17) TAG=17.11 ;;
  16) TAG=16.15 ;;
  *) echo "unsupported TXV_PG=$VER (16, 17 or 18)" >&2; exit 2 ;;
esac
PORT="5543${VER: -1}"   # 55438, 55437, 55436
BIN="/opt/homebrew/opt/postgresql@${VER}/bin"
MODE="${TXV_IT_MODE:-}"
if [ -z "$MODE" ]; then
  if [ -x "$BIN/postgres" ]; then MODE=brew; else MODE=docker; fi
fi
export TXV_IT_THROWAWAY=1

case "$MODE" in
  brew)
    [ -x "$BIN/postgres" ] || { echo "no Homebrew postgresql@$VER at $BIN" >&2; exit 2; }
    have="$("$BIN/postgres" --version | awk '{print $3}')"
    echo "brew mode: PostgreSQL $have (pinned minimum $TAG)"
    DATA="$ROOT/.tmp/pgdata$VER"
    LOG="$ROOT/.tmp/pg$VER.log"
    mkdir -p "$ROOT/.tmp"
    if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
    "$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
    cat >> "$DATA/postgresql.conf" <<EOF

# --- txv integration (throwaway) ---
listen_addresses = 'localhost'
port = $PORT
unix_socket_directories = ''
wal_level = logical
max_slot_wal_keep_size = 2GB
max_replication_slots = 10
max_wal_senders = 10
EOF
    cleanup() {
      "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true
      [ "${TXV_IT_KEEP:-}" = "1" ] || rm -rf "$DATA"
    }
    trap cleanup EXIT
    "$BIN/pg_ctl" -D "$DATA" -l "$LOG" -w start >/dev/null
    PSQL="$BIN/psql -h localhost -p $PORT" PGUSER=postgres "$ROOT/scripts/integration.sh"
    ;;
  docker)
    command -v docker >/dev/null || { echo "docker not found" >&2; exit 2; }
    docker info >/dev/null 2>&1 || { echo "docker daemon not running (start Docker Desktop, or use TXV_IT_MODE=brew)" >&2; exit 2; }
    NAME="txv-it-pg$VER"
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
    echo "docker mode: postgres:$TAG"
    docker run -d --name "$NAME" -e POSTGRES_PASSWORD=txv-it-throwaway -p "127.0.0.1:$PORT:5432" \
      "postgres:$TAG" postgres -c wal_level=logical -c max_slot_wal_keep_size=2GB >/dev/null
    for _ in $(seq 1 60); do
      docker exec "$NAME" pg_isready -U postgres -q && break
      sleep 1
    done
    PSQL="docker exec -i $NAME psql" PGUSER=postgres "$ROOT/scripts/integration.sh"
    ;;
  *) echo "unknown TXV_IT_MODE=$MODE" >&2; exit 2 ;;
esac
