#!/usr/bin/env bash
# The intent-vs-effect corpus (TY-13): starts a throwaway PostgreSQL 18.6 in Docker on a free
# localhost port (wal_level=logical, trust auth on 127.0.0.1 only), runs every scenario in corpus/
# with internal/corpus/cmd/corpus, and removes the container at the end.
#
#   scripts/corpus.sh                 every scenario
#   RUN=trigger scripts/corpus.sh     the scenarios whose id contains "trigger"
#   KEEP=1 scripts/corpus.sh          keep the container and the databases (prints the DSN)
#   CORPUS_TOOL=./my-check scripts/corpus.sh   run your tool around each scenario (corpus/README.md)
#
# What each scenario saw is written to .tmp/corpus/results.json. The image is pinned by digest.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
NAME="deedglass-corpus-$$"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

command -v docker >/dev/null || fail "docker not found. Install Docker, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start it, then run this again."

# free_port asks the OS for a free localhost port, avoiding the ports other tools here use.
free_port() {
  local p
  for _ in 1 2 3 4 5; do
    p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
    case "$p" in 8080|8090|8091|55432) continue ;; esac
    echo "$p"; return
  done
  fail "No free port found."
}

cleanup() {
  if [ "${KEEP:-0}" = "1" ]; then
    ok "Kept $NAME on 127.0.0.1:$PORT (remove it with: docker rm -f $NAME)"
  else
    docker rm -f "$NAME" >/dev/null 2>&1 || true
  fi
}

PORT="$(free_port)"
trap cleanup EXIT
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:5432" -e POSTGRES_HOST_AUTH_METHOD=trust "$IMAGE" \
  -c wal_level=logical -c max_replication_slots=16 >/dev/null
DSN="postgres://postgres@127.0.0.1:$PORT/postgres?sslmode=disable"
for _ in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 || { docker logs --tail 40 "$NAME" >&2 || true; fail "Postgres didn't start within 60 s."; }
ok "PostgreSQL 18.6 on 127.0.0.1:$PORT"
[ "${KEEP:-0}" = "1" ] && echo "  dsn  $DSN"

mkdir -p "$ROOT/.tmp/corpus"
args=(-dsn "$DSN" -dir "$ROOT/corpus" -json "$ROOT/.tmp/corpus/results.json")
[ -n "${RUN:-}" ] && args+=(-run "$RUN")
[ "${KEEP:-0}" = "1" ] && args+=(-keep)
cd "$ROOT"
go run ./internal/corpus/cmd/corpus "${args[@]}"
