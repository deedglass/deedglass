#!/usr/bin/env bash
# preflight.sh (S35): checks the VM before the first start and before every upgrade. Each check
# prints ✓, ✕ (with the exact command that fixes it) or ○ (could not confirm). Exit 1 on any ✕.
#
#   sudo deploy/cloud/scripts/preflight.sh
#   deploy/cloud/scripts/preflight.sh --env-file <file> --etc <dir> --compose <file> --offline   (tests)
#
# It reads files and settings only; it never prints a secret's value.
set -uo pipefail
ETC=/etc/deedglass
ENV_FILE=""
COMPOSE="$(cd "$(dirname "$0")/.." && pwd)/compose.yaml"
DOTENV=""
OFFLINE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --env-file) ENV_FILE="$2"; shift 2 ;;
    --etc) ETC="$2"; shift 2 ;;
    --compose) COMPOSE="$2"; shift 2 ;;
    --dotenv) DOTENV="$2"; shift 2 ;;
    --offline) OFFLINE=1; shift ;;
    *) echo "usage: preflight.sh [--env-file F] [--etc DIR] [--compose F] [--dotenv F] [--offline]" >&2; exit 2 ;;
  esac
done
ENV_FILE="${ENV_FILE:-$ETC/deedglass.env}"
DOTENV="${DOTENV:-$(dirname "$COMPOSE")/.env}"
fail=0
ok()   { echo "✓ $*"; }
bad()  { echo "✕ $1"; echo "  next  $2"; fail=1; }
soft() { echo "○ $1"; [ -n "${2:-}" ] && echo "  next  $2"; }
mode_of()  { stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"; }
owner_of() { stat -c '%u:%g' "$1" 2>/dev/null || stat -f '%u:%g' "$1"; }
val() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; }

app="$(sed -n 's/^APP_HOST=//p' "$DOTENV" 2>/dev/null)"; app="${app:-app.deedglass.com}"
api="$(sed -n 's/^API_HOST=//p' "$DOTENV" 2>/dev/null)"; api="${api:-api.deedglass.com}"
echo "Deedglass cloud preflight · $app · $api"

# DNS: both names point to this server.
if [ "$OFFLINE" = 1 ]; then
  soft "DNS · not checked (offline)"
else
  me="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  a1="$(getent ahostsv4 "$app" 2>/dev/null | awk 'NR==1{print $1}')"; a2="$(getent ahostsv4 "$api" 2>/dev/null | awk 'NR==1{print $1}')"
  if [ -n "$me" ] && [ "$a1" = "$me" ] && [ "$a2" = "$me" ]; then ok "DNS · both names point to this server ($me)"
  else bad "DNS · $app → ${a1:-nothing}, $api → ${a2:-nothing}, this server is ${me:-unknown}" "Add A records for both names to ${me:-this server} with DNS only (grey cloud), then wait a minute."; fi
  # Ports 80 and 443 free (or already Caddy's).
  busy="$(ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E ':(80|443)$' || true)"
  if [ -z "$busy" ] || docker ps --format '{{.Names}}' 2>/dev/null | grep -q caddy; then ok "Ports · 80 and 443 free"
  else bad "Ports · something already listens on $(echo $busy | tr '\n' ' ')" "sudo ss -ltnp | grep -E ':(80|443) ' shows what; stop it before Caddy starts."; fi
fi

# The env file: present, root-owned, 0600, with every required name and no forbidden one.
if [ ! -f "$ENV_FILE" ]; then
  bad "Secrets · $ENV_FILE is missing" "sudo install -m 0600 -o root -g root deploy/cloud/deedglass.env.example $ENV_FILE, then fill it in on the VM."
else
  m="$(mode_of "$ENV_FILE")"; o="$(owner_of "$ENV_FILE")"
  if [ "$m" = 600 ] && [ "$o" = "0:0" ]; then ok "Secrets · $ENV_FILE is root, 0600"
  else bad "Secrets · $ENV_FILE is $o, $m" "sudo chown root:root $ENV_FILE && sudo chmod 0600 $ENV_FILE"; fi
  missing=""
  for k in DEEDGLASS_ENV DEEDGLASS_BASE_URL DEEDGLASS_DATABASE_URL DEEDGLASS_MIGRATE_DATABASE_URL DEEDGLASS_SEAL_KEY_FILE DEEDGLASS_VAULT_KEY DEEDGLASS_API_KEY_PEPPER DEEDGLASS_SIGNUP DEEDGLASS_TRUSTED_PROXIES; do
    v="$(val "$k")"; { [ -z "$v" ] || [[ "$v" == *CHANGE_ME* ]]; } && missing="$missing $k"
  done
  if [ -z "$missing" ]; then ok "Settings · every required name is set"
  else bad "Settings · not set:$missing" "Edit $ENV_FILE on the VM (GO_LIVE.md step 5); generate secrets with openssl rand -base64 32."; fi
  [ "$(val DEEDGLASS_ENV)" = prod ] || bad "Settings · DEEDGLASS_ENV is '$(val DEEDGLASS_ENV)'" "Set DEEDGLASS_ENV=prod."
  [ "$(val DEEDGLASS_SIGNUP)" = invite ] || bad "Settings · DEEDGLASS_SIGNUP is '$(val DEEDGLASS_SIGNUP)'" "The beta is invite-only: set DEEDGLASS_SIGNUP=invite."
  if grep -q '^DEEDGLASS_ALLOW_PRIVATE_NETWORKS=' "$ENV_FILE"; then
    bad "Settings · DEEDGLASS_ALLOW_PRIVATE_NETWORKS is set" "Delete that line: it is dev only, and in prod it would let connectors reach this VM's private ranges."
  fi
  for k in DEEDGLASS_DATABASE_URL DEEDGLASS_MIGRATE_DATABASE_URL; do
    v="$(val "$k")"
    case "$v" in *"host=/run/postgresql"*) ;; "") ;; *) bad "Database · $k doesn't use the Unix socket" "Use postgres://<role>:<password>@/deedglass?host=/run/postgresql (Postgres has no TCP listener)." ;; esac
  done
  # The seal key: the exact file, 0400, owned by 65532, not a symlink.
  sk="$(val DEEDGLASS_SEAL_KEY_FILE)"; host_sk="$ETC/seal/$(basename "${sk:-none}")"
  if [ -z "$sk" ] || [[ "$sk" == *CHANGE_ME* ]]; then :
  elif [ -L "$host_sk" ]; then bad "Seal key · $host_sk is a symlink" "Copy the key file itself into $ETC/seal/ (SEAL_KEYS.md step 3)."
  elif [ ! -f "$host_sk" ]; then bad "Seal key · $host_sk is missing" "scp it from your Mac (SEAL_KEYS.md step 3), then run preflight again."
  else
    m="$(mode_of "$host_sk")"; o="$(owner_of "$host_sk")"
    if [ "$m" = 400 ] && [ "${o%%:*}" = 65532 ]; then ok "Seal key · $(basename "$host_sk") is 0400, owned by 65532"
    else bad "Seal key · $host_sk is $m, owned by $o" "sudo chown 65532:65532 $ETC/seal/*.json && sudo chmod 0400 $ETC/seal/*.json"; fi
  fi
fi

# The compose file and its .env: pinned images, no Postgres port.
if [ -f "$DOTENV" ]; then
  img="$(sed -n 's/^DEEDGLASS_IMAGE=//p' "$DOTENV")"
  if [[ "$img" =~ ^ghcr\.io/deedglass/deedglass:[0-9][^@]*@sha256:[0-9a-f]{64}$ ]]; then ok "Image · $img"
  else bad "Image · DEEDGLASS_IMAGE is '${img:-unset}', not a version with its digest" "Set DEEDGLASS_IMAGE=ghcr.io/deedglass/deedglass:<version>@sha256:<digest> in $DOTENV (UPGRADING.md)."; fi
else
  bad "Image · $DOTENV is missing" "cp deploy/cloud/.env.example $DOTENV and set DEEDGLASS_IMAGE."
fi
if [ -f "$COMPOSE" ]; then
  unpinned="$(grep -E '^\s+image:' "$COMPOSE" | grep -v '@sha256:' | grep -v 'DEEDGLASS_IMAGE' || true)"
  [ -z "$unpinned" ] || bad "Images · not pinned by digest: $unpinned" "Pin every image in compose.yaml as name:tag@sha256:<digest>."
  pgblock="$(awk '/^  postgres:/{on=1;next} on && /^  [a-z]/{exit} on' "$COMPOSE")"
  if grep -qE '^\s+ports:' <<<"$pgblock" || ! grep -q 'listen_addresses=$' <<<"$pgblock"; then
    bad "Postgres · it publishes a port or listens on TCP" "Remove ports: from the postgres service and keep listen_addresses= empty; deedglass uses the Unix socket."
  else ok "Postgres · no TCP listener, no published port"; fi
fi

# The clock: deeds carry UTC timestamps.
if command -v timedatectl >/dev/null 2>&1 && [ "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)" = yes ]; then ok "Clock · NTP synchronized"
else soft "Clock · NTP not confirmed" "timedatectl set-ntp true   (deeds carry UTC timestamps; a drifting clock makes them wrong)"; fi

[ "$fail" = 0 ] || exit 1
