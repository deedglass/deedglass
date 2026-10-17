# deedglass init and deedglass doctor

`deedglass init` sets up a local Deedglass in one command. `deedglass doctor` checks it, and any
other Deedglass, before you open an issue. Both are part of the CLI (S32).

## deedglass init

```sh
deedglass init                    # asks once, then runs every step
deedglass init --yes --client none
deedglass init --listen 127.0.0.1:8090 --pg-port 55440   # when 8080 or 55432 is taken
deedglass init --stop             # stop the local stack; the data is kept
```

Prerequisites: Docker running, and about 2 GiB of free disk. Measured on an M-series Mac with the
Postgres image already pulled: 6 to 8 seconds from an empty directory to the L2-witnessed smoke
action, and about 1 second for a rerun (`bench/results/2026-10-05-darwin-arm64-quickstart.json`).
The first `docker pull` of `postgres:18.6` adds its download time.

### The steps

Each step is idempotent: run init again and every step says it is already set up, then
`✓ Nothing to change.`

| # | Step | What it does | On a second run |
|---|---|---|---|
| 1 | Preflight | Docker reachable (5 s), the listen port and the Postgres ports free or already this directory's, at least 2 GiB free | Ports that are ours count as free |
| 2 | Local state | `.deedglass/` at mode 0700 (a wider mode is fixed and said so; a symlink is refused); `.gitignore` gains `.deedglass/` in a git checkout | Nothing |
| 3 | Keys | `.deedglass/dev-secrets.json` (vault key and API key pepper), a seal key in `.deedglass/keys/` (via `deedglass keys gen`), `.deedglass/stack.json` (ports and the local database passwords), every file 0600 | Never overwritten |
| 4 | Postgres | Two `postgres:18.6` containers (pinned by digest, `wal_level=logical`): Deedglass's own database on `--pg-port`, the demo database 10 ports above it; the control-plane roles from `deploy/postgres/control-plane-roles.sql` | Started if stopped |
| 5 | Deedglass | `deedglass migrate up` and `migrate verify`, then `deedglass serve` in the background (log `.deedglass/serve.log`, pid `.deedglass/serve.pid`) | Restarted only if it isn't answering |
| 6 | Org | Org `Local` (slug `local`), owner `you@localhost.test` (`--owner-email`), an org admin key in `.deedglass/admin.key` | Reused |
| 7 | Demo database | The Acme orders schema and seed (the same SQL as `make demo-up`), the Postgres connector `postgres` with `connector postgres init --apply`, the decoder slot | `connector postgres init` says `Nothing to change.` |
| 8 | Agent | `quickstart-agent` and one `dg_test_` key, written once to `.deedglass/quickstart.key` and shown once | A key is made only when the file is missing |
| 9 | Policies | `postgres-safe` pushed and enforced (it replaces `default-deny`, the policy every new org starts with; one policy is enforced at a time) | Skipped when that exact version hash is enforced |
| 10 | Client | `--client claude-code` prints the `claude mcp add` line; `claude-desktop`, `cursor`, `vscode` and `windsurf` print their config snippet; init never writes a client's config file | Printed again |
| 11 | Smoke | As `quickstart-agent`: `postgres.query` (L0 VERIFIED), `DROP TABLE public.orders` (denied by `block-destructive-sql`), `UPDATE public.orders SET status = 'shipped' WHERE id = 42` (`postgres-safe` asks for approval; init countersigns as the local owner through the dev-only `approve --as`, says so, then waits for L2 VERIFIED) | Runs only when something changed, or with `--smoke` |
| 12 | Console | Opens `/app` on a terminal (not with `--no-open`) | Only when something changed |

`--json` prints one `init_result` object: `steps` (each with `id`, `status` done, changed, skipped
or failed, `detail`, `took_ms`), `next` and `elapsed_ms`. No key appears in it; keys are referenced
by their files.

A second `deedglass init` in the same directory while one runs is refused (`.deedglass/init.lock`).
Stopping init between steps leaves a consistent state; the next run resumes.

### The quickstart step line

After a successful run, `init` (and `doctor`, when no check has a problem) ends with one line such as
`Quickstart step 2 of 5 · next: deedglass doctor`. The five steps are `init`, a clean `doctor`,
`init --trigger`, the undo of what it caught (`deedglass undo preview`, then `undo run`), and a person
countersigning that undo in the Console. The line names the step after the furthest one done, read
from `.deedglass/state.json` and `stack.json` and from the local Ledger with `.deedglass/admin.key`
(two GET `/v1/actions` reads on the local server: an action that doesn't match, an undo and whether
it ran). Only `doctor` knows that `doctor` passed; a later step counts for it. There are no points
or streaks, nothing is stored, and the line is absent with `--quiet` or `--json`, outside a
directory `init` set up, and once all five are done.

### Files

| File | Holds |
|---|---|
| `.deedglass/state.json` | `init_state`: the org, agent, key id, connector and policy version. Never a secret |
| `.deedglass/stack.json` | Ports, container and volume names, the local database passwords (0600) |
| `.deedglass/dev-secrets.json` | The vault key and API key pepper, in the dev config's format (0600) |
| `.deedglass/keys/sk_….json` | The seal key (0600) |
| `.deedglass/admin.key` | The org admin key the CLI and doctor use (0600) |
| `.deedglass/quickstart.key` | `quickstart-agent`'s key (0600) |

### What init doesn't do

- It doesn't use an existing database yet: `--postgres url` lands in a later release. Use Docker.
- It doesn't write MCP client config files. It prints the snippet; you paste it.
- It doesn't run on Windows. Use WSL 2.
- It is for a laptop: the stack listens on 127.0.0.1 only, in dev mode, with throwaway data.

## deedglass doctor

```sh
deedglass doctor
deedglass doctor --only db.*
deedglass doctor --json | jq .summary
```

Sixteen checks run in parallel, three seconds each at most. Each line names the check and what it
saw; a warning or a problem has the fix on the next line. Doctor is read-only: its database
sessions set `default_transaction_read_only`, and it calls only GET routes (a test runs it under
`log_statement=all` and finds no write).

In a directory where you ran `deedglass init`, doctor reads `.deedglass/stack.json` and
`.deedglass/admin.key` to find the local stack. Anywhere else it reads the usual environment:
`DEEDGLASS_DATABASE_URL`, `DEEDGLASS_SEAL_KEY_FILE`, `DEEDGLASS_VAULT_KEY`, `DEEDGLASS_SERVER` and
`DEEDGLASS_API_KEY` (an org admin key).

Exit codes: 0 every check OK (warnings allowed) · 1 a problem · 2 doctor itself couldn't run · 3 the
server refused the API key.

### The checks

<a id="config-valid"></a>
**config.valid** · The config loads for this environment. Problem: the first config problem. Fix:
`deedglass config print` lists every one.

<a id="state-dir"></a>
**state.dir** · `.deedglass/` is a real directory at mode 0700 and every key file in it is 0600.
Skipped where there is no `.deedglass/`.

<a id="db-reachable"></a>
**db.reachable** · Deedglass's own database answers: its version, the round trip and the role.

<a id="db-role"></a>
**db.role** · The runtime role isn't a superuser and doesn't bypass row-level security. A warning in
dev, a problem in prod. Fix: run the server as `deedglass_app`.

<a id="db-migrations"></a>
**db.migrations** · No migration pending, and every applied migration's checksum matches this build.

<a id="seal-key"></a>
**seal.key** · `DEEDGLASS_SEAL_KEY_FILE` loads, and the server's `keys.json` lists the same key,
active, with the same thumbprint.

<a id="vault-key"></a>
**vault.key** · `DEEDGLASS_VAULT_KEY` parses, and its active key wraps and unwraps a canary data key
in memory. Nothing is read from or written to the database.

<a id="server-health"></a>
**server.health** · `GET /v1/health` and `GET /v1/version` answer, and the server speaks this CLI's
API version.

<a id="clock-skew"></a>
**clock.skew** · This machine's clock against the server's `Date` header: a warning above 2 s, a
problem above 30 s (signed intents allow ±60 s; deeds use the database's clock). Doctor makes no
call to any other host.

<a id="ledger-chain"></a>
**ledger.chain** · The last 1,000 deeds of the chain are intact (`GET /v1/ledger/check`). Needs an
org admin key.

<a id="connectors-postgres"></a>
**connectors.postgres** · Each Postgres connector's last test: ready, ready with notes (optional
hardening that `deedglass connector test` lists), failing, or not tested yet.

<a id="decoder-slots"></a>
**decoder.slots** · Each decoder slot runs, its lag is under `DEEDGLASS_SLOT_LAG_ALERT_MB` (512 MB by
default; a warning above it), and it isn't invalidated or missing (a problem).

<a id="wal-level"></a>
**wal.level** · Each L2 target has `wal_level=logical`. Fix: set it and restart Postgres.

<a id="disk-space"></a>
**disk.space** · Free space where you ran doctor: a warning under 2 GiB, a problem under 500 MiB.

<a id="version-drift"></a>
**version.drift** · The CLI and the server share a minor version (a warning when they don't).

<a id="policy-active"></a>
**policy.active** · A policy is enforced (a warning when none is), and which one is in shadow.

### What doctor doesn't do

- It doesn't fix anything. Every fix is a sentence you run yourself.
- It doesn't check databases the Postgres connector doesn't know about.
- It doesn't call anything on the internet.
