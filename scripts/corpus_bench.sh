#!/usr/bin/env bash
# The corpus benchmark (corpus/BENCHMARK.md): starts a throwaway PostgreSQL 18.6 in Docker on a
# free localhost port (wal_level=logical, trust auth on 127.0.0.1 only), plays every scenario in
# corpus/ through each subject with bench/cmd/corpus-bench, and removes the container at the end.
#
#   scripts/corpus_bench.sh                             deedglass and none, every scenario
#   SUBJECTS=none scripts/corpus_bench.sh               one subject
#   SUBJECTS='none,cmd:mine=./my-checker' scripts/corpus_bench.sh   your checker (corpus/BENCHMARK.md)
#   RUN=trigger scripts/corpus_bench.sh                 the scenarios whose id contains "trigger"
#   OUT=corpus/results/latest.json scripts/corpus_bench.sh   where the results go (default .tmp/corpus/bench.json)
#
# The deedglass subject builds deedglass from this tree and runs a real serve against a control
# plane on the same throwaway server. The image is pinned by digest.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
NAME="deedglass-corpus-bench-$$"

TEAL="" RED="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' RESET=$'\033[0m'; fi
ok()   { printf '%s✓%s %s\n' "$TEAL" "$RESET" "$*"; }
fail() { printf '%s✕%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

command -v docker >/dev/null || fail "docker not found. Install Docker, then run this again."
docker info >/dev/null 2>&1 || fail "The Docker daemon isn't running. Start it, then run this again."

free_port() {
  local p
  for _ in 1 2 3 4 5; do
    p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
    case "$p" in 8080|8090|8091|55432) continue ;; esac
    echo "$p"; return
  done
  fail "No free port found."
}

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }

PORT="$(free_port)"
trap cleanup EXIT
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:5432" -e POSTGRES_HOST_AUTH_METHOD=trust "$IMAGE" \
  -c wal_level=logical -c max_replication_slots=16 -c max_wal_senders=16 -c max_connections=200 >/dev/null
DSN="postgres://postgres@127.0.0.1:$PORT/postgres?sslmode=disable"
for _ in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$NAME" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1 || { docker logs --tail 40 "$NAME" >&2 || true; fail "Postgres didn't start within 60 s."; }
ok "PostgreSQL 18.6 on 127.0.0.1:$PORT"

OUT="${OUT:-$ROOT/.tmp/corpus/bench.json}"
mkdir -p "$(dirname "$OUT")"
args=(-dsn "$DSN" -dir "$ROOT/corpus" -subjects "${SUBJECTS:-deedglass,none}" -out "$OUT")
[ -n "${RUN:-}" ] && args+=(-run "$RUN")
cd "$ROOT"
DEEDGLASS_IT_THROWAWAY=1 DEEDGLASS_IT_ADMIN_URL="$DSN" go run -tags integration ./bench/cmd/corpus-bench "${args[@]}"
