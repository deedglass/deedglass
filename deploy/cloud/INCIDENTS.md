# Incidents

For each: the first three commands, what to tell people and when, and the follow-up. Commands run on
the VM from `/opt/deedglass/deploy/cloud`; `dc` means `sudo docker compose -f compose.yaml`.

## The site or the API is down

1. `dc ps` (which container isn't healthy)
2. `dc logs --tail 100 deedglass caddy`
3. `sudo systemctl restart deedglass-stack && curl -s https://api.deedglass.com/v1/health`

Tell beta orgs by email within 30 minutes if it isn't back: what is down, since when, the next update
time. Follow-up: a dated note in the changelog's incident list with the cause.

## The disk is filling

1. `df -h / && sudo du -sh /var/lib/docker /var/backups/deedglass`
2. `dc exec -T -u postgres postgres psql -h /run/postgresql -d deedglass -c "SELECT slot_name, pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) FROM pg_replication_slots"`
3. `sudo docker system prune -f` (images and stopped containers only, never volumes)

A slot holding WAL is the usual cause; see the next section. Postgres caps it at 2 GB
(`max_slot_wal_keep_size`), so it can't fill the disk on its own.

## A replication slot is lagging

1. `dc run --rm deedglass --org <slug> decoder status`
2. `dc logs --tail 200 deedglass | grep decoder`
3. If the customer's connector is gone for good: `dc run --rm deedglass --org <slug> decoder drop --connector <name> --confirm <name>`

Tell the org that L2 verdicts for that connector are paused and why. Follow-up: their database's
`wal_level` and network path.

## Suspected seal-key compromise

[SEAL_KEYS.md](SEAL_KEYS.md) step 6, at once. Tell every beta org within 72 hours, in writing, with
the affected `seq` range.

## A beta org reports wrong data

1. `dc run --rm deedglass --org <slug> ledger check` (the chain)
2. `dc run --rm deedglass --org <slug> actions show <act_…> --json` (what was recorded)
3. Export the org's deeds and verify them offline: `dc run --rm deedglass --org <slug> deeds export --out - > deeds.jsonl` then `deedglass verify deeds.jsonl --keys https://deedglass.com/.well-known/deedglass/keys.json`

Reply the same day with what the deeds show. If the chain doesn't check, stop the stack and treat it
as a compromise until you know otherwise.

## What this doesn't cover

A status page and an on-call rotation; Better Stack's alerts reach your phone.
