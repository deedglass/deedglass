#!/usr/bin/env bash
# scripts/quickstart_timed.sh (S32 §4 step 13): the quickstart, timed from a clean directory:
# deedglass init --yes (Postgres in Docker, serve, org, demo database, agent, policy, smoke: a read,
# a blocked DROP TABLE, an UPDATE witnessed at L2), then a rerun that must say Nothing to change.
# Runs RUNS times (default 2), each in a new directory with its own Docker names and ports, and
# writes bench/results/<date>-<host>-quickstart.json. Everything it starts is removed at the end.
# Needs DEEDGLASS_IT_THROWAWAY=1 (it creates and deletes Docker containers and volumes).
set -euo pipefail
[ "${DEEDGLASS_IT_THROWAWAY:-}" = "1" ] || { echo "✕ Set DEEDGLASS_IT_THROWAWAY=1: this script creates and deletes Docker containers." >&2; exit 2; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNS="${RUNS:-2}"
BIN="$(mktemp -d)/deedglass"
(cd "$ROOT" && go build -trimpath -o "$BIN" ./cmd/deedglass)
host="$(uname -s | tr A-Z a-z)-$(uname -m)"
out="$ROOT/bench/results/$(date +%F)-$host-quickstart.json"
mkdir -p "$ROOT/bench/results" "$ROOT/.tmp/quickstart"
runs_json=""
for i in $(seq 1 "$RUNS"); do
  dir="$(mktemp -d)"
  proj="qs-$i-$$"
  listen="127.0.0.1:$((18330 + i))"; pg=$((55470 + i))
  cleanup() {
    (cd "$dir" && env -u DEEDGLASS_API_KEY "$BIN" init --stop >/dev/null 2>&1) || true
    docker rm -f "deedglass-$proj-pg" "deedglass-$proj-demo" >/dev/null 2>&1 || true
    docker volume rm "deedglass-$proj-pgdata" "deedglass-$proj-demodata" >/dev/null 2>&1 || true
    rm -rf "$dir"
  }
  trap cleanup EXIT
  t0=$(date +%s)
  (cd "$dir" && env -u DEEDGLASS_API_KEY -u DEEDGLASS_SERVER -u DEEDGLASS_DATABASE_URL NO_COLOR=1 "$BIN" --json init --yes --client none --no-open --listen "$listen" --pg-port "$pg" --project "$proj") > "$ROOT/.tmp/quickstart/run-$i.json"
  t1=$(date +%s)
  (cd "$dir" && env -u DEEDGLASS_API_KEY NO_COLOR=1 "$BIN" init --yes --no-open) > "$ROOT/.tmp/quickstart/rerun-$i.txt"
  t2=$(date +%s)
  grep -q "Nothing to change." "$ROOT/.tmp/quickstart/rerun-$i.txt" || { echo "✕ run $i: the rerun changed something" >&2; cat "$ROOT/.tmp/quickstart/rerun-$i.txt" >&2; exit 1; }
  (cd "$dir" && env -u DEEDGLASS_API_KEY NO_COLOR=1 "$BIN" doctor) > "$ROOT/.tmp/quickstart/doctor-$i.txt" || true
  steps="$(python3 -c "import json,sys; d=json.load(open('$ROOT/.tmp/quickstart/run-$i.json')); print(','.join('\"%s\":%d' % (s['id'], s['took_ms']) for s in d['steps']))")"
  verified="$(python3 -c "import json; d=json.load(open('$ROOT/.tmp/quickstart/run-$i.json')); print('true' if any('L2 · VERIFIED' in s['detail'] for s in d['steps']) else 'false')")"
  echo "run $i: init $((t1 - t0)) s · rerun $((t2 - t1)) s · L2 verified: $verified"
  runs_json="$runs_json${runs_json:+,}{\"init_s\":$((t1 - t0)),\"rerun_s\":$((t2 - t1)),\"l2_verified\":$verified,\"steps_ms\":{$steps}}"
  cleanup
  trap - EXIT
done
printf '{"object":"bench","name":"quickstart","host":"%s","images":"cached","runs":[%s],"date":"%s"}\n' "$host" "$runs_json" "$(date -u +%FT%TZ)" > "$out"
echo "wrote $out"
