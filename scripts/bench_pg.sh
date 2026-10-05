#!/usr/bin/env bash
# scripts/bench_pg.sh <command…> (S35): runs a command against a THROWAWAY PostgreSQL 18 cluster for
# the benchmarks, then deletes it. Native (Homebrew keg) when installed, else Docker postgres:18.6.
# The cluster listens on 127.0.0.1:55448 only, with wal_level=logical (the witness suite needs the
# decoder) and the settings recorded in every results file. It never touches any other database.
#
#   scripts/bench_pg.sh go run -tags integration ./bench/cmd/dgbench gate
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${DEEDGLASS_BENCH_PG_PORT:-55448}"
BIN="/opt/homebrew/opt/postgresql@18/bin"
export DEEDGLASS_IT_THROWAWAY=1
if [ -x "$BIN/postgres" ] && [ "${DEEDGLASS_BENCH_PG_MODE:-native}" = "native" ]; then
  export DEEDGLASS_BENCH_PG_MODE=native
  DATA="$ROOT/.tmp/benchpg"
  if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
  mkdir -p "$ROOT/.tmp"
  "$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
  cat >> "$DATA/postgresql.conf" <<CONF
# --- deedglass benchmarks (throwaway) ---
listen_addresses = 'localhost'
port = $PORT
unix_socket_directories = ''
wal_level = logical
max_slot_wal_keep_size = 2GB
max_replication_slots = 10
max_wal_senders = 10
max_connections = 300
CONF
  cleanup() { "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; }
  trap cleanup EXIT
  "$BIN/pg_ctl" -D "$DATA" -l "$ROOT/.tmp/benchpg.log" -w start >/dev/null
  export DEEDGLASS_IT_ADMIN_URL="postgres://postgres@localhost:$PORT/postgres?sslmode=disable"
else
  export DEEDGLASS_BENCH_PG_MODE=docker
  NAME="deedglass-bench-pg"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
  docker run -d --name "$NAME" -e POSTGRES_PASSWORD=deedglass-bench-throwaway -p "127.0.0.1:$PORT:5432" postgres:18.6 \
    postgres -c wal_level=logical -c max_slot_wal_keep_size=2GB -c max_connections=300 >/dev/null
  for _ in $(seq 1 60); do docker exec "$NAME" pg_isready -U postgres -q && break; sleep 1; done
  export DEEDGLASS_IT_ADMIN_URL="postgres://postgres:deedglass-bench-throwaway@127.0.0.1:$PORT/postgres?sslmode=disable"
fi
cd "$ROOT"
"$@"
