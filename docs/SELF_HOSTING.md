# Self-hosting with Docker

This page explains how to run the Deedglass server from its container image on your own infrastructure: the image, its settings, the database, migrations, TLS, health, metrics and backups. To work on Deedglass itself, see `docs/DEV_LOOP.md` instead.

## The image

There is no published image yet. Build it from a checkout:

```sh
make docker                                   # tags deedglass:dev
docker build --build-arg VERSION=0.1.0 --build-arg COMMIT="$(git rev-parse --short HEAD)" -t deedglass:0.1.0 .
```

What the root `Dockerfile` produces:

| Property | Value |
|---|---|
| Base | `gcr.io/distroless/static-debian13:nonroot`, pinned by digest (ADR-024) |
| Contents | One static binary, `/deedglass`, built with `CGO_ENABLED=0`. No shell, no package manager, no curl |
| User | `65532:65532` (non-root) |
| Entrypoint and command | `["/deedglass"]` and `["serve"]` |
| Listens on | `0.0.0.0:8080` inside the container (`DEEDGLASS_LISTEN`), port 8080 exposed |
| Size | Under 40 MB (ADR-032, amended under DL-031); CI fails the build otherwise |

The build context excludes `.env`, `.deedglass/`, key files and `.git` (see `.dockerignore`), so no secret enters a layer. Pass every setting at run time.

## Settings

The server reads its settings from the process environment. In prod it ignores `.env` and dev secrets. The ones you set for a deployment:

| Variable | Needed | What to set |
|---|---|---|
| `DEEDGLASS_ENV` | Yes | `prod`. Prod refuses unsafe settings instead of warning about them |
| `DEEDGLASS_DATABASE_URL` | Yes | The control-plane URL for `deedglass_app`, with `sslmode=require` or `verify-full` unless the host is loopback or a Unix socket |
| `DEEDGLASS_MIGRATE_DATABASE_URL` | For `migrate` only | The control-plane URL for `deedglass_migrator`. Give it to the migration step, never to the server |
| `DEEDGLASS_BASE_URL` | Yes | The public `https://` URL people and agents use. Prod needs https |
| `DEEDGLASS_VAULT_KEY` | Yes | Base64 of exactly 32 bytes: `openssl rand -base64 32`. Encrypts connector credentials |
| `DEEDGLASS_API_KEY_PEPPER` | Yes | Base64 of at least 32 bytes. Keep it stable: changing it invalidates every API key |
| `DEEDGLASS_LISTEN` | No | Defaults to `0.0.0.0:8080` in the image |
| `DEEDGLASS_LOG_LEVEL` | No | `debug`, `info` (default), `warn` or `error` |
| `DEEDGLASS_TRUSTED_PROXIES` | Behind a proxy | Comma-separated CIDRs whose `X-Forwarded-For` the server believes (see TLS below) |
| `DEEDGLASS_METRICS_LISTEN` | No | Off by default. An address such as `0.0.0.0:9464` serves `/metrics` (see Metrics below) |
| `DEEDGLASS_ANTHROPIC_API_KEY` | No | Off by default. An Anthropic API key (`sk-ant-…`) lets orgs that opt in have Claude reword Insights suggestions; without it no request to Anthropic is ever made ([INSIGHTS_PRIVACY.md](INSIGHTS_PRIVACY.md)) |
| `DEEDGLASS_ANTHROPIC_MODEL` | No | The model for that rewording: a Claude model id without a date suffix, default `claude-sonnet-5-5` |

`docs/CONFIG.md` lists every setting. Keep the secrets in your secret store and pass them as environment variables, for example with `docker run --env-file` from a file only root can read.

## The databases and roles

Deedglass needs one Postgres database of its own, the control plane (`deedglass`), on PostgreSQL 16, 17 or 18, with two roles:

- `deedglass_migrator` owns the schema and runs `deedglass migrate`.
- `deedglass_app` is the server's runtime role. It owns nothing, so row-level security applies to it and it can't alter tables or history.

A third role, `deedglass_backup`, is planned for backups. The databases Deedglass watches for you (the customer side) are separate and must never be the control plane. `docs/DATABASE_SETUP.md` has the bootstrap (`deploy/postgres/control-plane-roles.sql`), the two URLs and notes for managed Postgres.

The server checks its role at startup. In prod it refuses to start if the role is a superuser, has BYPASSRLS or owns the schema:

```
✕ Refusing to start: the runtime role "postgres" is a superuser, so row-level security would not apply. Connect as deedglass_app (docs/DATABASE_SETUP.md).
```

## Migrations are a separate step

Run migrations before you start or upgrade the server, as the migrator:

```sh
docker run --rm \
  -e DEEDGLASS_ENV=prod \
  -e DEEDGLASS_MIGRATE_DATABASE_URL \
  deedglass:0.1.0 migrate up
```

The server never migrates in prod. If migrations are pending, it refuses to start:

```
✕ Refusing to start: 2 migrations are pending. Run deedglass migrate up first.
```

`serve --migrate` exists for local development only. `docs/MIGRATIONS.md` explains the runner.

## Run the server

```sh
docker run -d --name deedglass \
  --read-only --tmpfs /tmp \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --stop-timeout 30 \
  -p 127.0.0.1:8080:8080 \
  --env-file /etc/deedglass/server.env \
  deedglass:0.1.0
```

- `--read-only`, `--tmpfs /tmp`, `--cap-drop ALL` and `no-new-privileges` match the local compose file. The server needs no writable path other than `/tmp`.
- `--stop-timeout 30` gives the server time to drain (see below). Docker's default is 10 seconds.
- Publish the port on `127.0.0.1` and put your TLS proxy in front.

With Docker Compose, the same service looks like this. The healthcheck must use the exec form, because the image has no shell:

```yaml
services:
  deedglass:
    image: deedglass:0.1.0
    command: ["serve"]
    environment:
      DEEDGLASS_ENV: prod
      DEEDGLASS_DATABASE_URL: ${DEEDGLASS_DATABASE_URL:?}
      DEEDGLASS_BASE_URL: https://deedglass.example.com
      DEEDGLASS_VAULT_KEY: ${DEEDGLASS_VAULT_KEY:?}
      DEEDGLASS_API_KEY_PEPPER: ${DEEDGLASS_API_KEY_PEPPER:?}
      DEEDGLASS_TRUSTED_PROXIES: 172.17.0.1/32 # your proxy's address as the server sees it (see below)
    ports: ["127.0.0.1:8080:8080"]
    read_only: true
    tmpfs: [/tmp]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 30s
    healthcheck:
      test: ["CMD", "/deedglass", "version", "--server", "http://127.0.0.1:8080"]
      interval: 10s
      timeout: 5s
      retries: 3
    restart: unless-stopped
```

`deedglass version --server URL` calls `GET /v1/version` with a 2 second timeout and exits 0 when the server answers, 1 when it doesn't. `docker run --health-cmd` wraps its command in a shell, which this image doesn't have, so use a Compose or orchestrator healthcheck in the exec form above.

## TLS and trusted proxies

The server speaks plain HTTP. TLS terminates at a proxy in front of it, such as Caddy or Cloudflare, which holds your TLS keys. A minimal Caddyfile:

```
deedglass.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

Set `DEEDGLASS_BASE_URL` to the public `https://` URL. In prod with an https base URL, the server sends `Strict-Transport-Security`.

The server records each request's client IP in its access log. It believes `X-Forwarded-For` only from the addresses in `DEEDGLASS_TRUSTED_PROXIES`; from anyone else, the client IP is the connection's address. Set it to the address your proxy connects from, as the server sees it:

- With Docker's published ports, connections usually arrive from the bridge gateway, not `127.0.0.1`. Find it with `docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}'`.
- If there are several proxies in a chain, list each one.
- The `ip` field in the access log shows what the server decided. Check it after you set this.

Never set it to `0.0.0.0/0`: anyone could then choose the IP the server logs.

## Health and drain

`GET /v1/health` returns JSON with a top-level `status` and a status per check (database, migrations). It shows statuses only, never connection details. Point your load balancer's health check at it.

On the first SIGTERM or SIGINT:

1. Health switches to `draining` and returns 503, so load balancers stop sending new requests.
2. In prod, the server keeps serving for 5 seconds so they notice.
3. It then shuts down, letting in-flight requests finish, within 20 seconds, and closes the database pool.
4. It logs how long the shutdown took and how many requests were cut off.

A second signal exits immediately with status 1. Give the container at least 30 seconds to stop (`--stop-timeout 30`, or `stop_grace_period: 30s`).

## Metrics

Metrics are off by default. Set `DEEDGLASS_METRICS_LISTEN` to serve Prometheus text format at `/metrics` on a separate listener, never on the API port.

Inside a container, `127.0.0.1` is the container itself, so an address like `127.0.0.1:9464` is unreachable from outside it. Either:

- set `DEEDGLASS_METRICS_LISTEN=0.0.0.0:9464` and publish it on the host's loopback only (`-p 127.0.0.1:9464:9464`), or
- scrape it over a private Docker network that only Prometheus joins.

Never publish the metrics port to the internet. `docs/OBSERVABILITY.md` lists the metric names, their labels, the log fields and a scrape config.

## Logs

In prod the server writes JSON logs to standard output, one line per event; read them with `docker logs deedglass`. The access log records the path without its query string and never a request body, and a redacting handler drops secret-named fields and scrubs API keys and URL passwords from values. `docs/OBSERVABILITY.md` lists the fields and the redaction rules.

## Backups

Back up the control-plane database with your provider's snapshots, or with `pg_dump` as an admin role. A read-only `deedglass_backup` role and a documented backup and restore routine come with the release kit (S35). Keep `DEEDGLASS_VAULT_KEY` and `DEEDGLASS_API_KEY_PEPPER` with your backups: without them, stored connector credentials and API keys can't be used.

## What this doesn't do

- There is no published image yet, and the image is built for your machine's platform only. Multi-arch images and publishing to a registry come with the release (S35).
- There is no Helm chart or Kubernetes manifest.
- The server doesn't terminate TLS. Put a proxy in front of it.
- The server doesn't run migrations in prod. Run `deedglass migrate up` as a separate step.
- The image doesn't include a healthcheck of its own, a shell or debugging tools.
- The API doesn't send CORS headers; call it from your server, not a browser.
