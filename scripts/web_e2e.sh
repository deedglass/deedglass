#!/usr/bin/env bash
# The Console against a real server (S24 §6): a THROWAWAY PostgreSQL 18 cluster, migrations, the Acme
# org with an owner, an admin and a viewer, and bin/deedglass serve (dev) on 127.0.0.1:$WEB_E2E_PORT.
#
#   scripts/web_e2e.sh test         Playwright (system Chrome): shell, keyboard, palette, Stop all, live events, axe, CSP
#   scripts/web_e2e.sh lighthouse   Lighthouse 13 on /app, /app/_kit and /login (desktop and mobile)
#   scripts/web_e2e.sh shots        the screenshot pass (writes ~/txv/ops/plan/prompts-v7/reports/screens/S24)
#   scripts/web_e2e.sh shots28      the S28 Insights screenshot pass (reports/screens/S28)
#   scripts/web_e2e.sh serve        keep the stack up until Ctrl-C (for playwright-cli and manual looks)
#
# The cluster lives in .tmp/web-e2e-pg (port 5546x, never the founder's 55432); the server's working
# directory is .tmp/web-e2e (its dev seal key and secrets). Everything is stopped and deleted on exit.
#
# S25 seeds real data before the tests run (WEB_E2E_SEED=0 skips it): fake-action volume, then the
# L2 hero on a second throwaway cluster (.tmp/web-e2e-store, port 55469: the Acme Store orders
# database, because the egress door refuses Deedglass's own database cluster): the postgres
# connector, the acme-demo policy, support-bot ships order 42 (L2 VERIFIED), the legacy trigger
# goes on, and it ships 42 again (L2 MISMATCH, 301 rows). The real ids go to $WORK/fixtures.json,
# which web/e2e/fixtures.ts reads.
set -euo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"
[ "${DEEDGLASS_IT_THROWAWAY:-}" = "1" ] || { echo "refusing: set DEEDGLASS_IT_THROWAWAY=1 (throwaway clusters only)" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-test}"
BIN="/opt/homebrew/opt/postgresql@18/bin"
PGPORT=55468
PORT="${WEB_E2E_PORT:-18241}"
PW=deedglass-e2e-throwaway
DATA="$ROOT/.tmp/web-e2e-pg"
WORK="$ROOT/.tmp/web-e2e"
DG="$ROOT/bin/deedglass"
[ -x "$DG" ] || { echo "bin/deedglass is missing: run make build-only" >&2; exit 2; }
[ -x "$BIN/postgres" ] || { echo "no Homebrew postgresql@18 at $BIN" >&2; exit 2; }

STORE="$ROOT/.tmp/web-e2e-store"
STOREPORT=55469
SERVE_PID=""
cleanup() {
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null || true
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null || true
  "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true
  "$BIN/pg_ctl" -D "$STORE" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$DATA" "$WORK" "$STORE"
}
trap cleanup EXIT

mkdir -p "$ROOT/.tmp"
if [ -d "$DATA" ]; then "$BIN/pg_ctl" -D "$DATA" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$DATA"; fi
rm -rf "$WORK" && mkdir -p "$WORK" && chmod 700 "$WORK"
"$BIN/initdb" -D "$DATA" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
cat >> "$DATA/postgresql.conf" <<EOF
listen_addresses = 'localhost'
port = $PGPORT
unix_socket_directories = ''
wal_level = logical
max_connections = 200
EOF
"$BIN/pg_ctl" -D "$DATA" -l "$ROOT/.tmp/web-e2e-pg.log" -w start >/dev/null
psql_admin() { "$BIN/psql" -h localhost -p "$PGPORT" -U postgres -v ON_ERROR_STOP=1 -qAt "$@"; }
psql_admin -d postgres -c "CREATE ROLE deedglass_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
  -c "CREATE ROLE deedglass_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '$PW'" \
  -c "CREATE DATABASE deedglass OWNER deedglass_migrator" -c "REVOKE ALL ON DATABASE deedglass FROM PUBLIC" \
  -c "GRANT CONNECT ON DATABASE deedglass TO deedglass_app"

export DEEDGLASS_ENV=dev
export DEEDGLASS_DATABASE_URL="postgres://deedglass_app:$PW@127.0.0.1:$PGPORT/deedglass?sslmode=disable"
export DEEDGLASS_MIGRATE_DATABASE_URL="postgres://deedglass_migrator:$PW@127.0.0.1:$PGPORT/deedglass?sslmode=disable"
export DEEDGLASS_BASE_URL="http://127.0.0.1:$PORT"
export DEEDGLASS_LISTEN="127.0.0.1:$PORT"
export DEEDGLASS_LOG_LEVEL=warn
# The seed's Acme Store cluster is on loopback; the egress door allows that in dev only with this.
export DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1
cd "$WORK"
"$DG" migrate up --no-color >/dev/null
"$DG" org create --name Acme --slug acme --owner-email owner@acme.example --no-color >/dev/null
"$DG" org create --name "Acme Staging" --slug acme-staging --owner-email owner@acme.example --no-color >/dev/null
# An admin and a viewer in Acme (direct rows in this throwaway database, as the superuser).
org="$(psql_admin -d deedglass -c "SELECT id FROM deedglass.orgs WHERE slug = 'acme'")"
newid() { python3 -c "import secrets;a='0123456789abcdefghjkmnpqrstvwxyz';print('$1_'+secrets.choice('01234567')+''.join(secrets.choice(a) for _ in range(25)))"; }
for spec in "admin|priya.raman@acme.example|Priya Raman" "viewer|sam.okafor@acme.example|Sam Okafor"; do
  IFS='|' read -r role email name <<<"$spec"
  uid="$(newid usr)"
  psql_admin -d deedglass -c "INSERT INTO deedglass.users (id, email, display_name, status) VALUES ('$uid', '$email', '$name', 'active')" \
    -c "INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at) VALUES ('$(newid mem)', '$org', '$uid', '$role', 'active', now())"
done
psql_admin -d deedglass -c "UPDATE deedglass.users SET display_name = 'Arjun Mehta' WHERE email = 'owner@acme.example'"

# S28: a simulated org for Insights (acme-sim: seed 7, a week ending two days ago, then the anomaly
# backfill), with Priya as an admin and Sam as a viewer. It runs before serve starts: serve's
# approval-expiry sweep runs on the real clock and would expire the simulator's virtual approvals.
# WEB_E2E_SIM=0 skips it (the Insights specs then skip themselves).
SIM_ORG=""
if [ "${WEB_E2E_SEED:-1}" = "1" ] && [ "${WEB_E2E_SIM:-1}" = "1" ]; then
  SIM_END="$(python3 -c 'import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(days=2)).strftime("%Y-%m-%dT06:30:00Z"))')"
  "$DG" dev simulate --days 7 --end "$SIM_END" --seed 7 --no-color >/dev/null
  "$DG" insights detect --backfill 9d --org acme-sim --no-color >/dev/null
  SIM_ORG="$(psql_admin -d deedglass -c "SELECT id FROM deedglass.orgs WHERE slug = 'acme-sim'")"
  for spec in "admin|priya.raman@acme.example" "viewer|sam.okafor@acme.example"; do
    IFS='|' read -r role email <<<"$spec"
    psql_admin -d deedglass -c "INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
      SELECT '$(newid mem)', '$SIM_ORG', id, '$role', 'active', now() FROM deedglass.users WHERE email = '$email'"
  done
fi

"$DG" serve > "$ROOT/.tmp/web-e2e-serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 60); do curl -fsS -m 1 "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break; sleep 0.25; done
curl -fsS -m 2 "http://127.0.0.1:$PORT/v1/health" >/dev/null || { cat "$ROOT/.tmp/web-e2e-serve.log" >&2; exit 1; }
echo "web-e2e: Acme on http://127.0.0.1:$PORT (PostgreSQL 18 on $PGPORT)"

# ---------- S25 seed: volume, then the L2 hero (real decoder, real trigger) ----------
seed() {
  local demo="$ROOT/examples/demo" out admin agentkey sql args i v
  "$DG" dev fake-action --org-slug acme --n "${WEB_E2E_VOLUME:-600}" --seed 7 --agents 4 --batch 200 --quiet --no-color >/dev/null
  # The Acme Store: its own throwaway cluster, logical WAL for the decoder.
  [ -d "$STORE" ] && { "$BIN/pg_ctl" -D "$STORE" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$STORE"; }
  "$BIN/initdb" -D "$STORE" -U postgres --auth=trust -E UTF8 --locale=C >/dev/null
  printf "listen_addresses = 'localhost'\nport = %s\nunix_socket_directories = ''\nwal_level = logical\nmax_slot_wal_keep_size = 1GB\n" "$STOREPORT" >> "$STORE/postgresql.conf"
  "$BIN/pg_ctl" -D "$STORE" -l "$ROOT/.tmp/web-e2e-store.log" -w start >/dev/null
  store_sql() { "$BIN/psql" -X -h localhost -p "$STOREPORT" -U postgres -v ON_ERROR_STOP=1 -qAt "$@"; }
  store_sql -d postgres -c "CREATE DATABASE acme"
  for f in 01_schema 02_seed 03_roles 20_functions 11_trigger_off; do
    ACME_OWNER_PASSWORD=e2e-owner-throwaway store_sql -d acme -c "SET client_min_messages = warning" -f "$demo/sql/$f.sql" >/dev/null
  done
  admin="$("$DG" org keys create --org acme --name e2e-admin --mode test --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
  apiv() { DEEDGLASS_API_KEY="$admin" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$DG" --no-color "$@"; }
  apiv connector create --config "$demo/connectors/postgres.yaml" >/dev/null
  DEEDGLASS_INIT_DSN="postgres://postgres@127.0.0.1:$STOREPORT/acme?sslmode=disable" apiv connector postgres init --connector postgres --apply --rotate-passwords >/dev/null
  apiv policy push "$demo/policies/acme-demo.yaml" >/dev/null
  apiv policy activate acme-demo --version 1 --mode enforce --reason "web e2e: the Acme Store demo policy" --yes >/dev/null
  for i in $(seq 1 120); do apiv decoder status --connector postgres --json 2>/dev/null | grep -q '"status": *"running"' && break; sleep 0.25; done
  agentkey="$(apiv agent keys create support-bot --name e2e-hero --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
  sql="UPDATE public.orders SET status = 'shipped' WHERE id = 42"
  args="{\"sql\":\"$sql\"}"
  hero_act() {
    # act exits 1 on a MISMATCH; its JSON still names the action.
    { DEEDGLASS_API_KEY="$agentkey" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$DG" act postgres.execute --args "$args" --json --no-color || true; } |
      python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])'
  }
  latest() { apiv actions show "$1" --org acme --json 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);w=d.get("witness") or {};print(d["state"],w.get("level",""),w.get("verdict",""))' 2>/dev/null || true; }
  waitv() { for i in $(seq 1 80); do v="$(latest "$1")"; case "$v" in "sealed L2 "*) echo "$v"; return 0;; esac; sleep 0.25; done; echo "$v"; }
  VERIFIED_ID="$(hero_act)" || { echo "web-e2e seed: the first act failed" >&2; return 1; }; v="$(waitv "$VERIFIED_ID")"
  [ "$v" = "sealed L2 VERIFIED" ] || { echo "web-e2e seed: expected L2 VERIFIED for $VERIFIED_ID, got '$v'" >&2; return 1; }
  store_sql -d acme -c "SET client_min_messages = warning" -f "$demo/sql/10_trigger_on.sql" >/dev/null
  store_sql -d acme -c "UPDATE public.orders SET status = 'paid' WHERE id = 42" >/dev/null
  MISMATCH_ID="$(hero_act)" || { echo "web-e2e seed: the second act failed" >&2; return 1; }; v="$(waitv "$MISMATCH_ID")"
  [ "$v" = "sealed L2 MISMATCH" ] || { echo "web-e2e seed: expected L2 MISMATCH for $MISMATCH_ID, got '$v'" >&2; return 1; }
  store_sql -d acme -c "SET client_min_messages = warning" -f "$demo/sql/11_trigger_off.sql" >/dev/null
  # S26: acme-demo v2 adds a four-eyes rule for refund changes and asks for L2 on approved writes,
  # so the Approvals tests see "1 of 2" and "L2 · VERIFIED". Approvals snapshot their rule, so the
  # policy tests can change the enforced version later without touching seeded approvals.
  python3 - "$demo/policies/acme-demo.yaml" "$WORK/acme-demo-v2.yaml" <<'PY'
import sys
src = open(sys.argv[1]).read()
anchor = "  - id: orders-writes-need-approval\n"
four = """  - id: refunds-need-two
    description: S26 e2e. Changing a refund needs two people.
    match: { tool: postgres.execute }
    when: sql.kind == "update" && sql.tables == ["public.refunds"] && !sql.unbounded_write
    decision: require_approval
    reason: Changing a refund needs two people to countersign.
    approval: { approvers: [role:admin], required: 2, expires_in: 30m }
    verify: L2
"""
assert anchor in src
src = src.replace(anchor, four + anchor, 1)
i = src.index(anchor)
j = src.index("expires_in: 30m }\n", i) + len("expires_in: 30m }\n")
src = src[:j] + "    verify: L2\n" + src[j:]
open(sys.argv[2], "w").write(src)
PY
  apiv policy push "$WORK/acme-demo-v2.yaml" >/dev/null
  apiv policy activate acme-demo --version 2 --mode enforce --reason "web e2e: four-eyes refunds and L2 approvals" --yes >/dev/null
  # One refund waiting for a person, so Approvals (and Lighthouse) open on a real card.
  { DEEDGLASS_API_KEY="$agentkey" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$DG" act postgres.execute --no-color --json \
      --args "{\"sql\":\"INSERT INTO public.refunds (id, order_id, amount_cents, reason, created_at) VALUES (9001, 42, 12000, 'Damaged in transit', '2026-10-04 10:00:00+00')\"}" >/dev/null 2>&1 || true; }
  python3 - "$WORK/fixtures.json" "$VERIFIED_ID" "$MISMATCH_ID" "$admin" "$agentkey" <<'PY'
import json, sys
json.dump({"verified": sys.argv[2], "mismatch": sys.argv[3], "adminKey": sys.argv[4], "agentKey": sys.argv[5]}, open(sys.argv[1], "w"))
PY
  chmod 600 "$WORK/fixtures.json"
  echo "web-e2e: seeded · L2 VERIFIED $VERIFIED_ID · L2 MISMATCH $MISMATCH_ID"
}
if [ "${WEB_E2E_SEED:-1}" = "1" ]; then seed; fi
# S28: the simulated org's suggestions (a loosen, a tighten and a budget), generated through the API
# with a throwaway admin key; its id and key go to $WORK/sim.json for web/e2e/insights.spec.ts.
if [ -n "$SIM_ORG" ]; then
  simkey="$("$DG" org keys create --org acme-sim --name e2e-insights --mode test --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')"
  DEEDGLASS_API_KEY="$simkey" DEEDGLASS_SERVER="http://127.0.0.1:$PORT" "$DG" --no-color insights suggestions generate >/dev/null
  python3 - "$WORK/sim.json" "$SIM_ORG" "$simkey" <<'PY'
import json, sys
json.dump({"org": sys.argv[2], "slug": "acme-sim", "adminKey": sys.argv[3]}, open(sys.argv[1], "w"))
PY
  chmod 600 "$WORK/sim.json"
  echo "web-e2e: simulated org acme-sim ($SIM_ORG) with suggestions"
fi

# S26: the mock orders API (S16) for the REST connector test, on its own loopback port.
MOCK_PID=""
MOCK_PORT="${WEB_E2E_MOCK_PORT:-18290}"
export WEB_E2E_MOCK_TOKEN="mock-orders-e2e-throwaway"
if (cd "$ROOT" && /opt/homebrew/bin/go build -o "$WORK/mock-orders" ./examples/mock-orders); then
  MOCK_ORDERS_TOKEN="$WEB_E2E_MOCK_TOKEN" MOCK_ORDERS_ADDR="127.0.0.1:$MOCK_PORT" "$WORK/mock-orders" > "$ROOT/.tmp/web-e2e-mock.log" 2>&1 &
  MOCK_PID=$!
  export WEB_E2E_MOCK_URL="http://127.0.0.1:$MOCK_PORT"
fi
export WEB_E2E_BASE="http://127.0.0.1:$PORT"
export WEB_E2E_DG="$DG"
export WEB_E2E_WORK="$WORK"
export SHOTS_DIR="${SHOTS_DIR:-$HOME/txv/ops/plan/prompts-v7/reports/screens/S26}"
export WEB_E2E_SIM_DONE="${SIM_ORG:+1}"
export LIGHTHOUSE_DIR="${LIGHTHOUSE_DIR:-$HOME/txv/ops/plan/prompts-v7/reports/lighthouse/S28}"
cd "$ROOT/web"
st=0
case "$MODE" in
  test) npx playwright test --project=chromium ${PW_ARGS:-} || st=$? ;;
  lighthouse) node e2e/lighthouse.mjs || st=$? ;;
  shots) npx playwright test --project=shots || st=$? ;;
  shots28) SHOTS_DIR="${SHOTS28_DIR:-$HOME/txv/ops/plan/prompts-v7/reports/screens/S28}" npx playwright test --project=shots28 || st=$? ;;
  serve) echo "web-e2e: up. Sign-in links: $DG org login-link --email owner@acme.example --org acme (cwd $WORK). Ctrl-C to stop."; wait "$SERVE_PID" || st=$? ;;
  *) echo "unknown mode $MODE" >&2; st=2 ;;
esac
exit $st
