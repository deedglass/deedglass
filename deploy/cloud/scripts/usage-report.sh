#!/usr/bin/env bash
# usage-report.sh (S35): the weekly soft-limit report for the beta (read-only SQL as deedglass_backup).
# Lists orgs above 10,000 actions this month and each org's connector count. Beta limits are soft:
# nothing is blocked here; the Gate's rate limits are the hard edge (INVITES.md).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
"${DC[@]}" exec -T postgres sh -c 'PGPASSWORD="$PG_BACKUP_PASSWORD" psql -X -h /run/postgresql -U deedglass_backup -d deedglass' <<'SQL'
\pset footer off
SELECT o.slug AS org,
       (SELECT count(*) FROM deedglass.actions AS a WHERE a.org_id = o.id AND a.created_at >= pg_catalog.date_trunc('month', pg_catalog.now())) AS actions_this_month,
       (SELECT count(*) FROM deedglass.connectors AS c WHERE c.org_id = o.id) AS connectors,
       CASE WHEN (SELECT count(*) FROM deedglass.actions AS a WHERE a.org_id = o.id AND a.created_at >= pg_catalog.date_trunc('month', pg_catalog.now())) > 10000
            THEN 'above the 10,000 soft limit' ELSE '' END AS note
  FROM deedglass.orgs AS o ORDER BY actions_this_month DESC;
SQL
