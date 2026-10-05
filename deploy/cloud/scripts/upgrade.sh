#!/usr/bin/env bash
# upgrade.sh <image@sha256:…> (S35): back up, pull the pinned image, run migrate up once, restart and
# wait for health. Any failure stops it. Migrations are forward-only: going back means restoring the
# backup this script just took (UPGRADING.md says how).
#
#   sudo deploy/cloud/scripts/upgrade.sh ghcr.io/deedglass/deedglass:0.1.1@sha256:<digest>
set -euo pipefail
. "$(dirname "$0")/lib.sh"
img="${1:-}"
[[ "$img" =~ ^ghcr\.io/deedglass/deedglass:[0-9][^@]*@sha256:[0-9a-f]{64}$ ]] || {
  echo "✕ \"$img\" isn't a release image with its digest."; echo "  next  Copy ghcr.io/deedglass/deedglass:<version>@sha256:<digest> from the release notes."; exit 2; }
"$KIT/scripts/preflight.sh" --offline >/dev/null || { echo "✕ preflight failed; nothing was changed."; echo "  next  sudo $KIT/scripts/preflight.sh"; exit 1; }
"$KIT/scripts/backup.sh"
docker pull -q "$img" >/dev/null
prev="$(sed -n 's/^DEEDGLASS_IMAGE=//p' "$KIT/.env")"
sed -i.bak "s|^DEEDGLASS_IMAGE=.*|DEEDGLASS_IMAGE=$img|" "$KIT/.env"
if ! "${DC[@]}" run --rm -T --no-deps migrate migrate up; then
  sed -i "s|^DEEDGLASS_IMAGE=.*|DEEDGLASS_IMAGE=$prev|" "$KIT/.env"
  echo "✕ migrate up failed; the old image is set again and the stack wasn't restarted."
  echo "  next  Read the error above. If a migration half-applied, restore the backup above (BACKUP_RESTORE.md)."
  exit 1
fi
"${DC[@]}" up -d --wait
echo "✓ Upgraded · $img · was $prev"
