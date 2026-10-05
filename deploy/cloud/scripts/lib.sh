# shellcheck shell=bash
# lib.sh: shared settings for the cloud kit's host scripts (sourced, not run).
# KIT is deploy/cloud; COMPOSE runs docker compose on the kit (the smoke test overrides it).
KIT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE="${COMPOSE:-docker compose -f $KIT/compose.yaml}"
ETC="${DEEDGLASS_ETC:-/etc/deedglass}"
OPS_ENV="${OPS_ENV:-$ETC/ops.env}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/deedglass}"
# shellcheck disable=SC2034 # DC is used by the scripts that source this file
read -r -a DC <<<"$COMPOSE"
# opsval NAME: one value from ops.env, without sourcing the file into the environment.
opsval() { sed -n "s/^$1=//p" "$OPS_ENV" 2>/dev/null | tail -1; }
