# Upgrade

The image on ghcr.io is private for now, so an upgrade needs a registry login with read access to
it (as in [GO_LIVE.md](GO_LIVE.md) step 7); a public image is coming.

```sh
sudo /opt/deedglass/deploy/cloud/scripts/upgrade.sh ghcr.io/deedglass/deedglass:0.1.1@sha256:<digest>
```

It runs preflight, takes a backup, pulls the image by digest, runs `migrate up` once, restarts the
stack and waits for health. If `migrate up` fails it puts the old image back and doesn't restart.

Expected: `✓ Backup · …` then `✓ Upgraded · ghcr.io/deedglass/deedglass:0.1.1@sha256:… · was …`.

Copy the image line from the release notes (it has the digest). Never upgrade by tag alone.

## Going back

Migrations are forward-only. To go back to an earlier version, restore the backup the upgrade took
([BACKUP_RESTORE.md](BACKUP_RESTORE.md)) and set the old image in `.env`.

## What this doesn't cover

Zero-downtime upgrades: the server restarts, which takes a few seconds.
