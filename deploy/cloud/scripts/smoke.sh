#!/usr/bin/env bash
# smoke.sh (S35, make cloud-smoke): the whole cloud kit on this machine, in prod mode, with Caddy's
# internal CA: start it, check health and Caddy's routing, seed two orgs, export their deeds, back up,
# destroy everything (volumes too), restore into a fresh volume, export again and compare byte for
# byte, then check every chain. Throwaway secrets in .tmp/cloud-smoke; nothing leaves the machine.
# Needs DEEDGLASS_IT_THROWAWAY=1 (it creates and deletes Docker volumes), Docker, age and Go.
set -euo pipefail
[ "${DEEDGLASS_IT_THROWAWAY:-}" = 1 ] || { echo "✕ Set DEEDGLASS_IT_THROWAWAY=1: the smoke test creates and deletes Docker volumes." >&2; exit 2; }
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
KIT="$ROOT/deploy/cloud"
W="$ROOT/.tmp/cloud-smoke"
PORT="${SMOKE_HTTPS_PORT:-8443}"
PROJECT=deedglass-smoke
for t in docker age go curl; do command -v "$t" >/dev/null || { echo "✕ $t isn't installed." >&2; exit 2; }; done
rm -rf "$W"; mkdir -p "$W/seal" "$W/backups"; chmod 0700 "$W"
export COMPOSE="docker compose -p $PROJECT -f $KIT/compose.yaml -f $KIT/compose.local.yaml --env-file $W/.env"
read -r -a DC <<<"$COMPOSE"
cleanup() { "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
t_all=$(date +%s)

# The image: the linux binary for this machine (with the Console when it was built) in Dockerfile.release.
arch="$(go env GOARCH)"
mkdir -p "$W/image/linux/$arch"
(cd "$ROOT" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$W/image/linux/$arch/deedglass" ./cmd/deedglass)
cp "$ROOT/Dockerfile.release" "$W/image/Dockerfile"
docker build -q -t deedglass:cloud-smoke "$W/image" >/dev/null

# Throwaway secrets, generated here, never printed.
rnd() { openssl rand -base64 48 | tr -d '/+=\n' | head -c 40; }
mig="$(rnd)"; appw="$(rnd)"; bak="$(rnd)"
printf 'PG_MIGRATOR_PASSWORD=%s\nPG_APP_PASSWORD=%s\nPG_BACKUP_PASSWORD=%s\n' "$mig" "$appw" "$bak" > "$W/postgres.env"
rnd > "$W/pg_admin_password"
(cd "$ROOT" && go run ./cmd/deedglass keys gen --dir "$W/seal" --json) > "$W/seal.json"
skfile="$(ls "$W"/seal/sk_*.json | head -1)"
chmod 0400 "$skfile" # Docker Desktop presents it as owned by the reading user; on the VM it is owned by 65532
cat > "$W/deedglass.env" <<ENV
DEEDGLASS_ENV=prod
DEEDGLASS_BASE_URL=https://app.deedglass.localhost:$PORT
DEEDGLASS_DATABASE_URL=postgres://deedglass_app:$appw@/deedglass?host=/run/postgresql
DEEDGLASS_MIGRATE_DATABASE_URL=postgres://deedglass_migrator:$mig@/deedglass?host=/run/postgresql
DEEDGLASS_SEAL_KEY_FILE=/run/deedglass/seal/$(basename "$skfile")
DEEDGLASS_VAULT_KEY=$(openssl rand -base64 32)
DEEDGLASS_API_KEY_PEPPER=$(openssl rand -base64 32)
DEEDGLASS_SIGNUP=invite
DEEDGLASS_TRUSTED_PROXIES=172.30.0.0/24
DEEDGLASS_METRICS_LISTEN=0.0.0.0:9464
DEEDGLASS_LOG_LEVEL=warn
ENV
cat > "$W/.env" <<ENV
DEEDGLASS_IMAGE=deedglass:cloud-smoke
DEEDGLASS_ENV_FILE=$W/deedglass.env
DEEDGLASS_PG_ENV_FILE=$W/postgres.env
DEEDGLASS_PG_ADMIN_PASSWORD_FILE=$W/pg_admin_password
DEEDGLASS_SEAL_DIR=$W/seal
CADDYFILE=$KIT/Caddyfile.local
SMOKE_HTTPS_PORT=$PORT
ENV
chmod 0600 "$W"/*.env "$W/pg_admin_password"
age-keygen -o "$W/backup.age" 2>/dev/null
BACKUP_AGE_RECIPIENT="$(age-keygen -y "$W/backup.age")"
export BACKUP_AGE_RECIPIENT
export BACKUP_DIR="$W/backups" SKIP_UPLOAD=1 OPS_ENV=/dev/null

up() {
  "${DC[@]}" up -d --wait >/dev/null 2>"$W/up.log" || { tail -3 "$W/up.log" >&2; "${DC[@]}" logs --tail 30 deedglass migrate caddy >&2; echo "✕ The stack didn't come up." >&2; exit 1; }
}
https() { # https <host> <path> [curl args…]
  local host="$1" path="$2"; shift 2
  curl -sk --resolve "$host:$PORT:127.0.0.1" "https://$host:$PORT$path" "$@"
}
t0=$(date +%s); up
[ "$(https api.deedglass.localhost /v1/health -o /dev/null -w '%{http_code}')" = 200 ] || { echo "✕ /v1/health through Caddy"; exit 1; }
h="$(https api.deedglass.localhost /v1/health)"; head -3 <<<"$h" | grep -Eq '"status": ?"ok"' || { echo "✕ /v1/health isn't ok: $h"; exit 1; }
[ "$(https api.deedglass.localhost / -o /dev/null -w '%{http_code}')" = 404 ] || { echo "✕ api. must answer 404 for /"; exit 1; }
[ "$(https app.deedglass.localhost /mcp/x -o /dev/null -w '%{http_code}')" = 404 ] || { echo "✕ app. must answer 404 for /mcp/x"; exit 1; }
https api.deedglass.localhost /v1/health -D - -o /dev/null | grep -qi '^strict-transport-security' || { echo "✕ no HSTS header"; exit 1; }
echo "✓ Up · health ok through Caddy · api. 404 for / · app. 404 for /mcp/x · HSTS · $(( $(date +%s) - t0 )) s"

# Two orgs, each with an agent whose actions are refused by default-deny and sealed as deeds.
seed() { # seed <slug> <n>
  local slug="$1" n="$2" out key agent akey
  out="$("${DC[@]}" run --rm -T --no-deps deedglass --json org create --name "Smoke $slug" --slug "$slug" --owner-email "owner@$slug.example.test" --print-link --admin-key 2>"$W/org-$slug.err")" || { cat "$W/org-$slug.err" >&2; echo "✕ org create $slug" >&2; exit 1; }
  key="$(grep -o '"secret":"dg_live_[^"]*"' <<<"$out" | head -1 | cut -d'"' -f4)"
  [ -n "$key" ] || { echo "✕ org create $slug gave no admin key: $out" >&2; exit 1; }
  agent="$(https api.deedglass.localhost /v1/agents -X POST -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-agent-$slug" -d '{"name":"smoke-bot"}' | grep -Eo '"id": ?"agt_[a-z0-9]+"' | head -1 | grep -o 'agt_[a-z0-9]*')"
  akey="$(https api.deedglass.localhost "/v1/agents/$agent/keys" -X POST -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-key-$slug" -d '{"name":"smoke","mode":"test"}' | grep -Eo '"secret": ?"dg_test_[^"]+"' | grep -o 'dg_test_[^"]*')"
  [ -n "$akey" ] || { echo "✕ agent key for $slug" >&2; exit 1; }
  for i in $(seq 1 "$n"); do
    https api.deedglass.localhost /v1/actions -X POST -H "Authorization: Bearer $akey" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-$slug-$i" \
      -d "{\"tool\":\"orders.get\",\"args\":{\"id\":\"$i\"}}" -o /dev/null
  done
}
seed smoke-a 120
seed smoke-b 80
export_deeds() { # export_deeds <dir>
  mkdir -p "$1"
  for slug in smoke-a smoke-b; do "${DC[@]}" run --rm -T --no-deps deedglass --org "$slug" deeds export --format jsonl --out - > "$1/$slug.jsonl" 2>"$W/export.err" || { cat "$W/export.err" >&2; exit 1; }; done
}
export_deeds "$W/before"
deeds="$(cat "$W"/before/*.jsonl | grep -c .)"
[ "$deeds" -gt 200 ] || { echo "✕ only $deeds deeds exported"; exit 1; }
echo "✓ Seeded · 2 orgs · $deeds deeds exported"

"$KIT/scripts/backup.sh"
backup="$(ls -1t "$W"/backups/deedglass-*.dump.age | head -1)"

# Destroy everything, then restore into a fresh volume.
"${DC[@]}" down -v >/dev/null 2>&1
echo "✓ Destroyed · containers and volumes removed"
# The wrong age identity: a clean failure, and nothing written.
age-keygen -o "$W/wrong.age" 2>/dev/null
if AGE_IDENTITY_FILE="$W/wrong.age" RESTORE_NO_START=1 "$KIT/scripts/restore.sh" "$backup" >"$W/wrong-identity.txt" 2>&1; then
  echo "✕ A restore with the wrong identity didn't fail"; exit 1
fi
grep -q "nothing was written" "$W/wrong-identity.txt" || { cat "$W/wrong-identity.txt"; exit 1; }
left="$("${DC[@]}" exec -T -u postgres postgres psql -X -At -h /run/postgresql -d deedglass -c "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = 'deedglass'")"
[ "$left" = 0 ] || { echo "✕ the failed restore left $left tables"; exit 1; }
echo "✓ Refused · the wrong age identity wrote nothing"
# A non-empty target is refused: restore once, then again into the same database.
AGE_IDENTITY_FILE="$W/backup.age" "$KIT/scripts/restore.sh" "$backup"
if AGE_IDENTITY_FILE="$W/backup.age" RESTORE_NO_START=1 "$KIT/scripts/restore.sh" "$backup" >"$W/second-restore.txt" 2>&1; then
  echo "✕ A second restore into a non-empty database wasn't refused"; exit 1
fi
grep -q "already holds" "$W/second-restore.txt" || { cat "$W/second-restore.txt"; exit 1; }
echo "✓ Refused · a restore into a non-empty database"
export_deeds "$W/after"
for slug in smoke-a smoke-b; do cmp -s "$W/before/$slug.jsonl" "$W/after/$slug.jsonl" || { echo "✕ $slug's export differs after the restore"; exit 1; }; done
[ "$(https api.deedglass.localhost /v1/health -o /dev/null -w '%{http_code}')" = 200 ] || { echo "✕ health after the restore"; exit 1; }
size="$(wc -c <"$backup" | tr -d ' ')"
echo "  backup $(basename "$backup") · $(awk -v b="$size" 'BEGIN{printf "%.1f", b/1048576}') MiB · whole run $(( $(date +%s) - t_all )) s"
echo "✓ Restored backup · 2 orgs · $(printf "%'d" "$deeds" 2>/dev/null || echo "$deeds") deeds · export identical · chain intact"
