#!/usr/bin/env bash
# backup.sh (S35): the nightly backup (deedglass-backup.timer, 02:30 IST). pg_dump -Fc as the read-only
# role deedglass_backup, inside one REPEATABLE READ snapshot that also counts every table, so the
# manifest describes exactly what the dump holds. The stream is encrypted with age to your offline
# recipient before it touches the disk; then local retention (7 daily + 4 weekly), rclone to R2, and
# the heartbeat, which is pinged only when every step succeeded.
#
#   sudo deploy/cloud/scripts/backup.sh            SKIP_UPLOAD=1 skips R2 and the heartbeat (the smoke test)
set -euo pipefail
. "$(dirname "$0")/lib.sh"
recipient="${BACKUP_AGE_RECIPIENT:-$(opsval BACKUP_AGE_RECIPIENT)}"
[[ "$recipient" == age1* ]] || { echo "✕ BACKUP_AGE_RECIPIENT isn't an age public key."; echo "  next  Put your age1… recipient in $OPS_ENV (BACKUP_RESTORE.md)."; exit 1; }
for t in age jq; do command -v "$t" >/dev/null || { echo "✕ $t isn't installed."; echo "  next  sudo apt-get install -y age jq (bootstrap.sh installs both)"; exit 1; }; done
mkdir -p "$BACKUP_DIR" && chmod 0700 "$BACKUP_DIR"
ts="$(date -u +%Y-%m-%dT%H-%MZ)"
name="deedglass-$ts.dump.age"
out="$BACKUP_DIR/$name"
t0=$(date +%s)
# One psql session: open the snapshot, count, dump inside it, close. Only pg_dump writes to stdout.
"${DC[@]}" exec -T -u postgres postgres sh -c 'export PGPASSWORD="$PG_BACKUP_PASSWORD"; psql -X -q -v ON_ERROR_STOP=1 -h /run/postgresql -U deedglass_backup -d deedglass' <<'SQL' | age -r "$recipient" > "$out.part"
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT pg_catalog.pg_export_snapshot() AS snap \gset
\o /tmp/deedglass-manifest.json
\pset tuples_only on
\pset format unaligned
SELECT pg_catalog.json_build_object(
  'postgres', pg_catalog.current_setting('server_version'),
  'tables', (SELECT pg_catalog.json_object_agg(t.table_name, (xpath('/row/c/text()', query_to_xml(pg_catalog.format('SELECT count(*) AS c FROM %I.%I', t.table_schema, t.table_name), false, true, '')))[1]::text::bigint ORDER BY t.table_name)
               FROM information_schema.tables AS t WHERE t.table_schema = 'deedglass' AND t.table_type = 'BASE TABLE'),
  'orgs', (SELECT coalesce(pg_catalog.json_agg(pg_catalog.json_build_object('org', o.id, 'slug', o.slug, 'deeds', (SELECT count(*) FROM deedglass.deeds AS d WHERE d.org_id = o.id), 'max_seq', (SELECT max(d.seq) FROM deedglass.deeds AS d WHERE d.org_id = o.id)) ORDER BY o.slug), '[]'::json) FROM deedglass.orgs AS o));
\o
\setenv DUMP_SNAPSHOT :snap
\! pg_dump -h /run/postgresql -U deedglass_backup -d deedglass -Fc --compress=zstd:6 --snapshot="$DUMP_SNAPSHOT"
\if :SHELL_ERROR
  SELECT 'pg_dump failed' AS backup, 1 / 0;
\endif
COMMIT;
SQL
counts="$("${DC[@]}" exec -T -u postgres postgres sh -c 'cat /tmp/deedglass-manifest.json; rm -f /tmp/deedglass-manifest.json')"
# A pipeline's status is its last command's (age); check the dump itself too.
if [ "$(wc -c <"$out.part" | tr -d ' ')" -lt 1024 ] || ! jq -e '.tables.deeds >= 0' <<<"$counts" >/dev/null 2>&1; then
  rm -f "$out.part"; echo "✕ Backup · the dump or its counts are missing; nothing was kept."; echo "  next  Check that the stack is up (${COMPOSE} ps) and read the error above."; exit 1
fi
mv "$out.part" "$out"
size=$(wc -c <"$out" | tr -d ' ')
sha="$(sha256sum "$out" 2>/dev/null | awk '{print $1}' || shasum -a 256 "$out" | awk '{print $1}')"
image="$("${DC[@]}" images deedglass --format json 2>/dev/null | head -c 2000 | sed -n 's/.*"ID":"\([^"]*\)".*/\1/p' | head -1)"
printf '{"object":"backup","file":"%s","sha256":"%s","bytes":%s,"created_at":"%s","image":"%s","counts":%s}\n' \
  "$name" "$sha" "$size" "$ts" "${image:-unknown}" "$counts" > "$out.manifest.json"
orgs="$(jq '.counts.orgs | length' "$out.manifest.json")"
deeds="$(jq '.counts.tables.deeds' "$out.manifest.json")"
mib="$(awk -v b="$size" 'BEGIN{printf "%.1f", b/1048576}')"
echo "✓ Backup · $name · $mib MiB · $orgs orgs · $(printf "%'d" "$deeds" 2>/dev/null || echo "$deeds") deeds · $(( $(date +%s) - t0 )) s"
# Retention: everything from the last 7 days, then the newest backup of each ISO week up to 35 days.
now=$(date +%s); kept_weeks=" "
mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"; }
# shellcheck disable=SC2045 # backup names have no spaces; newest first matters
for f in $(ls -1t "$BACKUP_DIR"/deedglass-*.dump.age 2>/dev/null); do
  m="$(mtime "$f")"; days=$(( (now - m) / 86400 ))
  [ "$days" -lt 7 ] && continue
  wk="$(date -u -d "@$m" +%G-%V 2>/dev/null || date -u -r "$m" +%G-%V)"
  if [ "$days" -lt 35 ] && [[ "$kept_weeks" != *" $wk "* ]]; then kept_weeks="$kept_weeks$wk "; continue; fi
  rm -f "$f" "$f.manifest.json"
done
if [ "${SKIP_UPLOAD:-}" = 1 ]; then exit 0; fi
bucket="$(opsval BACKUP_BUCKET)"; dest="r2:${bucket:-deedglass-backups}/$(date -u +%Y/%m/%d)/"
set -a; eval "$(grep '^RCLONE_CONFIG_R2_' "$OPS_ENV")"; set +a
rclone copy --no-traverse "$out" "$dest" && rclone copy --no-traverse "$out.manifest.json" "$dest"
echo "✓ Copied to R2 · ${dest#r2:}"
hb="$(opsval BACKUP_HEARTBEAT_URL)"
if [ -n "$hb" ]; then curl -fsS -m 10 --retry 3 "$hb" >/dev/null && echo "✓ Heartbeat sent"; fi
