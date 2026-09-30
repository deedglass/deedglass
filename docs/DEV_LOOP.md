# Run Deedglass locally

One command starts everything you need to work on Deedglass: Postgres, the migrations and the server, in Docker, bound to your machine only.

## What you need

- Docker Desktop, running. It includes Docker Compose.
- Go 1.27.1, for `make run` (running the server from source).
- `openssl` and `curl`. macOS includes both.

## Start the stack

```sh
make dev
```

The first run takes about 90 seconds, most of it building the image. It prints:

```
◐ Creating .env with fresh local secrets (git-ignored, mode 0600)
◐ Building deedglass:dev · starting Postgres 18.6
✓ Postgres · 127.0.0.1:55432 · databases deedglass, demo
✓ Migrations · schema at 0009
✓ Deedglass 0.1.0-dev · http://127.0.0.1:8080 · api 2026-10-01
  Try: curl -s localhost:8080/v1/health
  Logs: make dev-logs · Stop: make dev-down
```

`make dev` runs `scripts/dev.sh up`, which:

1. Creates `.env` on the first run (see below).
2. Starts Postgres and checks that its volume holds the Deedglass roles and both databases.
3. Builds the `deedglass:dev` image from the root `Dockerfile`, then runs `docker compose -f deploy/docker-compose.yml --env-file .env up -d --build --wait`. A one-shot `migrate` container applies pending migrations and exits; the `deedglass` container starts only after it exits cleanly.
4. Waits up to 90 seconds for `GET /v1/health` to report `"status": "ok"`, then prints the summary.

Run `make dev` again whenever you want the containers to pick up your code changes. It rebuilds the image (cached layers make this quick), reapplies any new migrations and restarts the server. Your data is kept.

Docker's own output goes to `.tmp/dev.log`. To watch it as it happens, run `DEEDGLASS_DEV_VERBOSE=1 make dev`.

## Check it

```sh
curl -si localhost:8080/v1/health
```

You get `HTTP/1.1 200`, a `Deedglass-Request-Id: req_…` header, `Deedglass-Version: 2026-10-01`, and a JSON body with `"status": "ok"` and `"applied": 9` under `checks.migrations`.

## The `.env` file

`.env` holds your local settings and secrets. `make dev` creates it from `.env.example` the first time, with mode 0600, and fills every empty value:

| Line | Filled with | Used by |
|---|---|---|
| `DEEDGLASS_DEV_PG_ADMIN_PASSWORD` | `openssl rand -hex 24` | The `postgres` superuser inside the container |
| `DEEDGLASS_DEV_PG_MIGRATOR_PASSWORD` | `openssl rand -hex 24` | `deedglass_migrator`, which owns the schema |
| `DEEDGLASS_DEV_PG_APP_PASSWORD` | `openssl rand -hex 24` | `deedglass_app`, the server's runtime role |
| `DEEDGLASS_VAULT_KEY` | `openssl rand -base64 32` | Encrypting connector credentials |
| `DEEDGLASS_API_KEY_PEPPER` | `openssl rand -base64 32` | Hashing API keys |
| `DEEDGLASS_DATABASE_URL`, `DEEDGLASS_MIGRATE_DATABASE_URL` | The two passwords above | `make run`, from your machine to `127.0.0.1:55432` |

Two things read `.env`:

- **Docker Compose** fills the `${…}` values in `deploy/docker-compose.yml` from it. Each container gets only the settings listed there; the containers reach Postgres at `postgres:5432` inside Docker's network.
- **`deedglass` itself**, in dev only, when you run it from the repo root. Prod ignores `.env`. `docs/CONFIG.md` has the rules.

`DEEDGLASS_DEV_*` names are for the tooling; the binary ignores them. `.env` is git-ignored and `.dockerignore` keeps it out of the image. Existing values are never replaced, so you can edit it: for example, set `DEEDGLASS_LOG_LEVEL=debug`.

The Postgres passwords take effect when the volume is first created. If you change them, or lose `.env`, run `make dev-reset` so the database is created again with the new ones.

## Ports

| Port | What | When |
|---|---|---|
| `127.0.0.1:8080` | The Deedglass API | `make dev`, or `make run` |
| `127.0.0.1:55432` | Postgres 18.6 | Whenever the stack is up |
| `127.0.0.1:9464` | Prometheus metrics at `/metrics` | Optional, `make run` only: uncomment `DEEDGLASS_METRICS_LISTEN=127.0.0.1:9464` in `.env` |

Every port is bound to `127.0.0.1`, so nothing is reachable from other machines. Metrics are off by default; `docs/OBSERVABILITY.md` lists them.

## The two databases

| Database | What it is |
|---|---|
| `deedglass` | The control plane: Deedglass's own state. Owned by `deedglass_migrator`; the server connects as `deedglass_app`. |
| `demo` | An empty customer-side database. The demo stack and the Postgres connector use it in later sessions. |

`deploy/postgres/init/10-deedglass.sh` creates both, and the two roles, on the first start of an empty volume. They must stay separate databases; `docs/DATABASE_SETUP.md` explains why.

To open a SQL prompt:

```sh
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres psql -U postgres -d deedglass
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres psql -U postgres -d demo
```

## Run the server from source

```sh
make run
```

This starts Postgres (creating `.env` first if needed), stops the `deedglass` container so port 8080 is free, then runs `go run ./cmd/deedglass serve --migrate` from the repo root. `--migrate` applies pending migrations first as `deedglass_migrator`, using `DEEDGLASS_MIGRATE_DATABASE_URL` from `.env`. It works in dev only.

There is no automatic rebuild. The loop is:

1. Edit the code.
2. Press Ctrl-C in the terminal running `make run`. The server drains and stops.
3. Run `make run` again.

For the containers, run `make dev` again instead. `make dev` starts the `deedglass` container again, so stop `make run` first; they both use port 8080.

## Logs, stop and reset

```sh
make dev-logs     # follow the logs of postgres, migrate and deedglass
make dev-down     # stop the stack; your database volume is kept
make dev-reset    # delete the stack and its database volume
```

`make dev-reset` deletes every org, agent and deed in your local database. It asks you to type `reset` first; in a script, set `DEEDGLASS_DEV_CONFIRM=reset`. It keeps `.env`, so the next `make dev` creates an empty database with the same passwords and keys.

## Troubleshooting

**"Docker isn't running."** Start Docker Desktop, wait until it says it is running, then run `make dev` again.

**"Port 55432 is already in use."** Another Postgres, or an old container, is listening there. Find it:

```sh
lsof -nP -iTCP:55432 -sTCP:LISTEN
docker ps --filter publish=55432
```

Stop it, then run `make dev` again.

**"Port 8080 is already in use."** Usually `make run` in another terminal. Stop it with Ctrl-C, then run `make dev` again. `lsof -nP -iTCP:8080 -sTCP:LISTEN` shows what holds it.

**"The Postgres volume predates this setup."** The volume was created before the roles and the `demo` database were added, so the init script never ran on it. Run `make dev-reset`, then `make dev`.

**"The passwords in .env don't open this Postgres volume."** `.env` was recreated or edited after the volume was created. Run `make dev-reset`, then `make dev`.

**"Migrations failed."** The last lines of the `migrate` container's log are printed with the message. Fix the cause, then run `make dev` again. `docker compose -f deploy/docker-compose.yml --env-file .env logs migrate` shows the whole log.

**The image didn't build.** The full log is in `.tmp/dev.log`. `make docker` builds the same image on its own, with the output on screen.

## What this doesn't do

- It isn't for production. It runs in dev mode, over plain HTTP, with `sslmode=disable` and secrets in a local file. `docs/SELF_HOSTING.md` covers running the image for real.
- It doesn't rebuild or restart on file changes. Use the loop above.
- It doesn't seed any orgs, agents or data. The databases start empty.
- It doesn't expose metrics from the containers. Use `make run` with `DEEDGLASS_METRICS_LISTEN` for that.
- It doesn't serve TLS or accept connections from other machines.
