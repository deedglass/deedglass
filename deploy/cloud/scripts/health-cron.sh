#!/usr/bin/env bash
# health-cron.sh (S35): every 5 minutes (deedglass-health.timer). Pings HEALTH_HEARTBEAT_URL only when
# every local check passes, so Better Stack alerts on a missing ping and the VM sends no email:
# disk under 80%, newest backup under 26 h old, /v1/health ok, TLS valid for 14+ days, replication slot
# lag under DEEDGLASS_SLOT_LAG_ALERT_MB, and the keys mirror matching.
set -uo pipefail
. "$(dirname "$0")/lib.sh"
fail=0
bad() { echo "✕ $*"; fail=1; }
use="$(df --output=pcent / | tail -1 | tr -dc 0-9)"; [ "${use:-100}" -lt 80 ] && echo "✓ Disk · ${use}% used" || bad "Disk · ${use}% used (limit 80%)"
newest="$(ls -1t "$BACKUP_DIR"/deedglass-*.dump.age 2>/dev/null | head -1)"
if [ -n "$newest" ] && [ $(( $(date +%s) - $(stat -c %Y "$newest") )) -lt $((26 * 3600)) ]; then echo "✓ Backup · $(basename "$newest")"; else bad "Backup · none in the last 26 h"; fi
if "${DC[@]}" exec -T deedglass /deedglass version --server http://127.0.0.1:8080 >/dev/null 2>&1; then echo "✓ Health · the server answers"; else bad "Health · the server doesn't answer"; fi
api="$(sed -n 's/^API_HOST=//p' "$KIT/.env")"
end="$(echo | openssl s_client -servername "${api:-api.deedglass.com}" -connect 127.0.0.1:443 2>/dev/null | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2)"
if [ -n "$end" ] && [ $(( ($(date -d "$end" +%s) - $(date +%s)) / 86400 )) -ge 14 ]; then echo "✓ TLS · valid until $end"; else bad "TLS · expires ${end:-unknown}"; fi
limit="$(sed -n 's/^DEEDGLASS_SLOT_LAG_ALERT_MB=//p' "$ETC/deedglass.env")"; limit="${limit:-1024}"
lag="$("${DC[@]}" exec -T -u postgres postgres psql -X -At -h /run/postgresql -d deedglass -c "SELECT coalesce(max(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) / 1048576, 0)::bigint FROM pg_replication_slots" 2>/dev/null || echo 0)"
[ "${lag:-0}" -lt "$limit" ] && echo "✓ Slots · lag ${lag:-0} MB" || bad "Slots · lag $lag MB (limit $limit MB)"
"$(dirname "$0")/keys-mirror.sh" >/dev/null 2>&1 && echo "✓ Keys mirror · the site matches the API" || bad "Keys mirror · differs or unreachable"
if [ "$fail" = 0 ]; then
  hb="$(opsval HEALTH_HEARTBEAT_URL)"; [ -n "$hb" ] && curl -fsS -m 10 --retry 2 "$hb" >/dev/null && echo "✓ Heartbeat sent"
else
  echo "  next  No heartbeat was sent, so Better Stack will alert. INCIDENTS.md has the first commands for each line above."
  exit 1
fi
