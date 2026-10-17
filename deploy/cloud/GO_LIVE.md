# Go live: the cloud beta on one VM

The founder's ordered list for `app.deedglass.com` and `api.deedglass.com`. One action per step, the
exact command, what you should see, and what to do if you don't. Every purchase and account here is
yours to make; nothing in this kit creates one.

## 1. Create the VM

In the Hetzner console: **Add server** › location Falkenstein or Helsinki › Ubuntu 24.04 › **CX23**
(2 vCPU, 4 GB, 40 GB) › IPv4 and IPv6 › your SSH key › name `deedglass-1`. No password.

You should see the server's IPv4 and IPv6 addresses. Note both.

## 2. DNS

In Cloudflare DNS for `deedglass.com`, add, each with **DNS only** (grey cloud):

| Type | Name | Value |
|---|---|---|
| A | app | the IPv4 |
| AAAA | app | the IPv6 |
| A | api | the IPv4 |
| AAAA | api | the IPv6 |
| CAA | @ | `0 issue "letsencrypt.org"` and `0 issue "zerossl.com"` |

DNS only means TLS ends at Caddy on your VM and client addresses stay simple. The trade-off: no
Cloudflare proxy in front of the API.

```sh
dig +short app.deedglass.com api.deedglass.com
```

Expected: the IPv4 twice. If not, wait a minute and run it again.

## 3. Get the kit onto the VM

```sh
ssh root@<ipv4>
git clone https://github.com/deedglass/deedglass.git /srv/deedglass
cd /srv/deedglass && git checkout v0.1.0
```

While the repository is private, copy it instead: `rsync -a --exclude .git ~/txv/product/ root@<ipv4>:/srv/deedglass/`.

## 4. Bootstrap

```sh
sudo /srv/deedglass/deploy/cloud/bootstrap.sh
```

Expected: ten `✓` lines ending with `systemd · units installed`. It links `/opt/deedglass` to the checkout.

## 5. Settings and secrets, typed on the VM

```sh
cd /opt/deedglass/deploy/cloud
sudo install -m 0600 -o root -g root deedglass.env.example /etc/deedglass/deedglass.env
sudo install -m 0600 -o root -g root postgres.env.example /etc/deedglass/postgres.env
sudo install -m 0600 -o root -g root ops.env.example /etc/deedglass/ops.env
openssl rand -base64 32 | tr -d '/+=' | head -c 40 | sudo install -m 0600 -o root -g root /dev/stdin /etc/deedglass/pg_admin_password
cp .env.example .env
```

Then edit each file with `sudo nano`, replacing every `CHANGE_ME`. Generate each secret on the VM:
`openssl rand -base64 32`. The app and migrator passwords in `postgres.env` go into the two URLs in
`deedglass.env`. Never paste a secret into chat, a ticket or git.

## 6. The seal key

Follow [SEAL_KEYS.md](SEAL_KEYS.md) steps 1 to 3. Put the exact file name in `DEEDGLASS_SEAL_KEY_FILE`.

## 7. The image

The image on ghcr.io is private for now, so this step needs a registry login with read access to it;
a public image is coming. From the release page, copy the image line with its digest into `.env`:

```sh
DEEDGLASS_IMAGE=ghcr.io/deedglass/deedglass:0.1.0@sha256:<digest>
```

## 8. Preflight, then the first start on the staging CA

```sh
sudo ./scripts/preflight.sh
```

Expected: every line `✓` (the clock may say `○` for a minute after boot). Fix each `✕` with the
command under it. Then, with `ACME_CA` still on staging:

```sh
sudo systemctl enable --now deedglass-stack.service
curl -sk https://api.deedglass.com/v1/health
```

Expected: `"status": "ok"`. Staging TLS isn't trusted by browsers; that's expected now.

## 9. Switch to the production CA and turn on the timers

In `.env`, set `ACME_CA=https://acme-v02.api.letsencrypt.org/directory`, then:

```sh
sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml up -d --force-recreate caddy
curl -s https://api.deedglass.com/v1/health
sudo systemctl enable --now deedglass-backup.timer deedglass-health.timer deedglass-usage.timer
```

Expected: health `ok` with no `-k`. If Caddy can't set up TLS: `docker compose logs caddy`.

## 10. Monitors in Better Stack

Create five: `https://api.deedglass.com/v1/health` with keyword `"status"`, `https://app.deedglass.com/login`,
`https://deedglass.com`, and two heartbeats (backup: every 24 h, grace 2 h; health: every 5 min,
grace 10 min). Put the two heartbeat URLs in `/etc/deedglass/ops.env`.

## 11. The first backup and a restore drill

```sh
sudo /opt/deedglass/deploy/cloud/scripts/backup.sh
```

Expected: `✓ Backup · …`, `✓ Copied to R2 · …`, `✓ Heartbeat sent`. Then follow
[BACKUP_RESTORE.md](BACKUP_RESTORE.md) "Restore drill" on your Mac.

## 12. The keys mirror

[SEAL_KEYS.md](SEAL_KEYS.md) step 4. Then `sudo ./scripts/keys-mirror.sh` → `✓ Keys mirror · … identical`.

## 13. Your own test org, then the first invite

```sh
sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml run --rm deedglass org create --name "Deedglass" --owner-email you@deedglass.com --print-link
```

Open the printed link, sign in, and look around. Then the first beta org per [INVITES.md](INVITES.md).

## What this doesn't cover

High availability, a second region, point-in-time recovery and a status page are post-launch. The
VM is one machine: if it dies, you restore last night's backup onto a new one (RPO 24 h, RTO under 1 h).
