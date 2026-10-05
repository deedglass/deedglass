#!/usr/bin/env bash
# restore.sh <file.dump.age> (S35): restores a backup into an EMPTY database and checks that it is complete.
#   1. starts Postgres alone and refuses a database that already holds the deedglass schema
#   2. decrypts with your age identity (AGE_IDENTITY_FILE) straight into pg_restore --exit-on-error
#      --single-transaction: a wrong identity or a damaged file writes nothing
#   3. deedglass migrate verify, then the row count of every table against the backup's manifest
#   4. deedglass ledger check for every org, then starts the rest of the stack
#
#   AGE_IDENTITY_FILE=~/deedglass-keys/backup.age sudo -E deploy/cloud/scripts/restore.sh /var/backups/deedglass/deedglass-….dump.age
set -euo pipefail
. "$(dirname "$0")/lib.sh"
file="${1:?usage: restore.sh <file.dump.age>}"
manifest="$file.manifest.json"
[ -s "$file" ] || { echo "✕ $file is missing or empty."; exit 1; }
[ -s "$manifest" ] || { echo "✕ $manifest is missing."; echo "  next  Copy the manifest that sits next to the backup (rclone copy r2:…/$(basename "$manifest") .)."; exit 1; }
for t in age jq; do command -v "$t" >/dev/null || { echo "✕ $t isn't installed."; exit 1; }; done
[ -r "${AGE_IDENTITY_FILE:-}" ] || { echo "✕ AGE_IDENTITY_FILE isn't a readable age identity."; echo "  next  AGE_IDENTITY_FILE=<path to your offline backup.age> sudo -E $0 $file"; exit 1; }
psql_su() { "${DC[@]}" exec -T -u postgres postgres psql -X -q -At -v ON_ERROR_STOP=1 -h /run/postgresql -d deedglass "$@"; }
echo "◐ Restoring $(basename "$file") into an empty database…"
t0=$(date +%s)
quiet() { local log; log="$(mktemp)"; "$@" >"$log" 2>&1 || { cat "$log" >&2; rm -f "$log"; return 1; }; rm -f "$log"; }
quiet "${DC[@]}" up -d --wait postgres
n="$(psql_su -c "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = 'deedglass'")"
if [ "$n" != 0 ]; then
  echo "✕ The database deedglass already holds $n Deedglass tables. Nothing was restored."
  echo "  next  Restore into a fresh volume: ${COMPOSE} down, move the pgdata volume aside, then run this again."
  exit 1
fi
if ! age -d -i "$AGE_IDENTITY_FILE" "$file" | "${DC[@]}" exec -T -u postgres postgres pg_restore -h /run/postgresql -d deedglass --exit-on-error --single-transaction; then
  echo "✕ The restore stopped and rolled back; nothing was written."
  echo "  next  Check that AGE_IDENTITY_FILE matches the recipient the backup was made for, and that the file's sha256 equals the manifest's."
  exit 1
fi
quiet "${DC[@]}" run --rm -T --no-deps migrate migrate verify
# Row counts against the manifest: every table, exactly.
want="$(jq -r '.counts.tables | to_entries[] | "\(.key):\(.value)"' "$manifest" | sort)"
got="$(psql_su -F: -c "SELECT t.table_name, (xpath('/row/c/text()', query_to_xml(pg_catalog.format('SELECT count(*) AS c FROM %I.%I', t.table_schema, t.table_name), false, true, '')))[1]::text FROM information_schema.tables AS t WHERE t.table_schema = 'deedglass' AND t.table_type = 'BASE TABLE' ORDER BY 1" | sort)"
if [ "$want" != "$got" ]; then
  echo "✕ Row counts differ from the manifest:"
  diff <(echo "$want") <(echo "$got") | sed 's/^/  /' | head -20
  exit 1
fi
tables="$(echo "$got" | grep -c .)"
echo "✓ Restored · $tables tables · row counts match the manifest · $(( $(date +%s) - t0 )) s"
orgs="$(psql_su -c "SELECT slug FROM deedglass.orgs ORDER BY slug")"
norg=0
for slug in $orgs; do
  quiet "${DC[@]}" run --rm -T --no-deps deedglass --org "$slug" ledger check || { echo "✕ Ledger · the chain of $slug doesn't check."; echo "  next  Don't start the stack; see INCIDENTS.md (wrong data)."; exit 1; }
  norg=$((norg + 1))
done
echo "✓ Ledger · $norg orgs · chain intact"
if [ "${RESTORE_NO_START:-}" != 1 ]; then quiet "${DC[@]}" up -d --wait && echo "✓ Stack · started"; fi
