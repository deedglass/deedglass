# Back up and restore

Every night at 02:30 IST, `backup.sh` dumps the database as the read-only role `deedglass_backup`
(it bypasses row-level security, so no org's rows are skipped), counts every table inside the same
snapshot, encrypts the stream with age to your offline recipient, keeps 7 daily and 4 weekly copies
in `/var/backups/deedglass`, copies them to the R2 bucket (35-day lifecycle), and pings the backup
heartbeat only when all of that worked.

**RPO 24 hours. RTO under 1 hour.** Point-in-time recovery is post-launch.

## Back up now

```sh
sudo /opt/deedglass/deploy/cloud/scripts/backup.sh
```

Expected:

```
✓ Backup · deedglass-2026-10-20T21-00Z.dump.age · 3.1 MiB · 2 orgs · 1,412 deeds · 4 s
✓ Copied to R2 · deedglass-backups/2026/10/20/
✓ Heartbeat sent
```

## Restore a backup

The age identity never lives on the VM. Copy it over for the restore and remove it afterwards.

```sh
sudo systemctl stop deedglass-stack
sudo docker volume rm deedglass_pgdata            # only after you have the backup file in hand
scp ~/deedglass-keys/backup.age root@<ipv4>:/root/backup.age
sudo AGE_IDENTITY_FILE=/root/backup.age /opt/deedglass/deploy/cloud/scripts/restore.sh /var/backups/deedglass/deedglass-<time>.dump.age
sudo shred -u /root/backup.age
```

Expected:

```
◐ Restoring deedglass-2026-10-20T21-00Z.dump.age into an empty database…
✓ Restored · 37 tables · row counts match the manifest
✓ Ledger · 2 orgs · chain intact
✓ Stack · started
```

`restore.sh` refuses a database that already holds Deedglass tables, restores in one transaction
(a wrong identity or a damaged file writes nothing), runs `deedglass migrate verify`, compares every
table's row count with the manifest written next to the backup, and checks every org's deed chain
before it starts the stack. The backup's manifest must sit next to the file (`….dump.age.manifest.json`).

From R2: `rclone copy r2:deedglass-backups/2026/10/20/ /var/backups/deedglass/` (with `ops.env` loaded).

## Restore drill (monthly, on your Mac)

`make cloud-smoke` in the product repository runs the whole cycle on throwaway volumes: seed, back up,
destroy, restore into a fresh volume, compare every org's deed export byte for byte. To drill a real
backup, run the kit locally with `compose.local.yaml` and `restore.sh` the downloaded file.

## What this doesn't cover

Point-in-time recovery (WAL archiving), a standby and cross-region copies are post-launch. A deed a
customer exported before a loss still verifies offline against the published keys.
