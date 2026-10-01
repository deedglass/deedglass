# Configuration

Deedglass reads one typed configuration for every command. Each setting is declared once, in
`internal/config/keys.go`; the table below is generated from it, and a test fails if the two drift.

## Where values come from

The first source that sets a key wins:

1. **The process environment** (`DEEDGLASS_*`).
2. **`.env`** in the working directory. Dev only: prod ignores it and says so. Plain `KEY=VALUE`
   lines, optional `export ` and matching quotes, no interpolation and no inline comments. Only
   `DEEDGLASS_*` lines are read, and a real environment variable always wins.
3. **`deedglass.yaml`** in the working directory. Keys are the env names without the prefix, in
   lowercase (`listen`, `log_level`). See `deedglass.example.yaml`.
4. **`.deedglass/dev-secrets.json`**. Dev only; see below.
5. **The default** in the table.

`deedglass config print` shows each effective value next to its source, with every secret shown as
`••••••••`. The database URL shows everything but its password.

## Settings

<!-- keys:begin (generated from internal/config/keys.go; go test ./internal/config -run TestDocsTable -update) -->
| Key | deedglass.yaml | Default | Secret | Needed by | What it does | Added |
|---|---|---|---|---|---|---|
| `DEEDGLASS_ENV` | `env` | `dev` | no | all | dev or prod. Prod ignores .env and dev secrets and refuses unsafe settings. | S01 |
| `DEEDGLASS_DATABASE_URL` | `database_url` | — | yes | serve, migrate, org, dev | The control-plane Postgres URL for the runtime role. Printed with only the password hidden. Prod needs sslmode=require or verify-full unless the host is loopback or a Unix socket. | S01 |
| `DEEDGLASS_MIGRATE_DATABASE_URL` | `migrate_database_url` | — | yes | migrate | The control-plane Postgres URL for the migrator role (deedglass_migrator), used only by deedglass migrate. Dev falls back to DEEDGLASS_DATABASE_URL; prod requires it or --database-url. | S02 |
| `DEEDGLASS_LISTEN` | `listen` | `127.0.0.1:8080` | no | serve | Address the API, the Gate and the Console listen on (host:port). | S01 |
| `DEEDGLASS_BASE_URL` | `base_url` | `http://127.0.0.1:8080` | no | serve, org login-link | The URL people and agents use to reach this server. Prod needs https. | S01 |
| `DEEDGLASS_SEAL_KEY_FILE` | `seal_key_file` | — | no | serve, keys | Path to the seal key file used to seal deeds. | S01 |
| `DEEDGLASS_VAULT_KEY` | `vault_key` (dev only) | — | yes | serve, vault, connector credentials | A key ring of 1 to 4 comma-separated base64 keys of exactly 32 bytes; the first encrypts connector credentials, the rest only decrypt during a rotation (S16). config print shows only the key ids (kek:…). Dev generates one in .deedglass/. | S01 |
| `DEEDGLASS_API_KEY_PEPPER` | `api_key_pepper` (dev only) | — | yes | serve, org keys, agent keys | Base64 of at least 32 bytes, mixed into API key hashes. Keep it stable: changing it invalidates every API key. Dev generates one in .deedglass/. | S01 |
| `DEEDGLASS_LOG_LEVEL` | `log_level` | `info` | no | all | Log level. | S01 |
| `DEEDGLASS_SIGNUP` | `signup` | `invite` | no | serve | Who can join: invite (people an admin invites) or closed. | S01 |
| `DEEDGLASS_RESEND_API_KEY` | `resend_api_key` (dev only) | — | yes | serve | Resend API key for sign-in and approval emails (starts with re_). Without it, dev prints emails to the log. | S01 |
| `DEEDGLASS_MAIL_FROM` | `mail_from` | — | no | serve, org create | The From address of sign-in and invite emails, e.g. Deedglass <signin@deedglass.com>. Required when DEEDGLASS_RESEND_API_KEY is set. | S07 |
| `DEEDGLASS_ANTHROPIC_API_KEY` | `anthropic_api_key` (dev only) | — | yes | serve, insights | Optional Anthropic API key for Insights phrasing (starts with sk-ant-). | S01 |
| `DEEDGLASS_TRUSTED_PROXIES` | `trusted_proxies` | — | no | serve | Comma-separated CIDRs whose X-Forwarded-For is believed. | S01 |
| `DEEDGLASS_ALLOW_PRIVATE_NETWORKS` | `allow_private_networks` | `false` | no | serve | Dev only: lets connectors reach private networks. Refused in prod. | S01 |
| `DEEDGLASS_EGRESS_ALLOW_CIDRS` | `egress_allow_cidrs` | — | no | serve | Comma-separated CIDRs of private networks connectors may reach, in prod too, for example 10.20.0.0/16. Never cloud metadata, reserved, IPv4-embedding or loopback ranges. Deedglass's own database is reachable only when its exact address is listed (a /32 or /128). | S16 |
| `DEEDGLASS_METRICS_LISTEN` | `metrics_listen` | — | no | serve | Address for a separate listener serving Prometheus metrics at /metrics (host:port). Off when unset; bind it to localhost or a private network. | S03 |
| `DEEDGLASS_SLOT_LAG_ALERT_MB` | `slot_lag_alert_mb` | `512` | no | serve | Alert when a Postgres replication slot holds more WAL than this (1-1048576 MB). | S01 |
| `DEEDGLASS_API_KEY` | `api_key` (dev only) | — | yes | CLI, mcp, SDKs | Client key; `serve` ignores it. An agent or admin API key (dg_live_… or dg_test_…) for client commands. | S01 |
| `DEEDGLASS_SERVER` | `server` | — | no | CLI, mcp, SDKs | Client key; `serve` ignores it. The Deedglass server client commands talk to. | S01 |
| `DEEDGLASS_INIT_DSN` | `init_dsn` (dev only) | — | yes | connector postgres init | Client key; `serve` ignores it. The admin DSN deedglass connector postgres init connects with to prepare a customer database (a superuser, or a role with CREATEROLE such as the RDS master user). Read once, never stored or printed. Rename it with --admin-dsn-env. | S17 |
| `DEEDGLASS_NO_BANNER` | `no_banner` | `false` | no | CLI | Client key; `serve` ignores it. Never show the terminal banner or the inline mark (bare deedglass, version, help). Scripts, pipes and CI never see them anyway. | S05X |
| `DEEDGLASS_THEME` | `theme` | `auto` | no | CLI | Client key; `serve` ignores it. Draw the terminal mark for a light or dark terminal. auto: COLORFGBG decides, else dark. | S05X |
| `DEEDGLASS_NO_ANIM` | `no_anim` | `false` | no | CLI | Client key; `serve` ignores it. Turn off the Sealed animation at the end of deedglass init; only its final frame shows. | S05X |
<!-- keys:end -->

Client keys are read by the CLI, the MCP commands and the SDKs. `serve` ignores them.

## Dev and prod

`DEEDGLASS_ENV` is `dev` unless it is set to `prod` in the environment or in `deedglass.yaml`
(`prod` in `.env` is refused, because prod ignores `.env`).

**Prod refuses**, all problems at once and each with a next step:
- a `DEEDGLASS_BASE_URL` that isn't https;
- a missing `DEEDGLASS_VAULT_KEY` or `DEEDGLASS_API_KEY_PEPPER`;
- `DEEDGLASS_ALLOW_PRIVATE_NETWORKS=true`;
- a `DEEDGLASS_DATABASE_URL` to a remote host without `sslmode=require` or `verify-full`
  (loopback and Unix sockets are fine);
- secrets inside `deedglass.yaml`;
- unknown `DEEDGLASS_*` names (dev only warns, with a did-you-mean).

## Dev secrets

In dev, a missing vault key or pepper is filled from `.deedglass/dev-secrets.json`. The first run
creates it with 32 random bytes for each, so `deedglass config print` works with no setup:

```
✓ Valid for dev · 2 local secrets generated in .deedglass/ (never used in prod)
```

- `.deedglass/` is created with mode 0700 and the file with mode 0600, using `O_EXCL`.
- Deedglass refuses a `.deedglass` that is a symlink or writable by other users, and a secrets file
  that is a symlink or readable by other users.
- On a read-only filesystem it stops and asks you to set both keys in the environment.
- Dev API keys depend on the dev pepper: deleting the file invalidates them.
- Prod never reads this file.

## Names Deedglass ignores or refuses

- **Tooling names** are ignored by the binary: `DEEDGLASS_IT_*` (integration scripts and
  `make attack`), `DEEDGLASS_DEV_*` (local compose; `DEEDGLASS_DEV_ATTACK_HOSTS` is read only by a
  `-tags attackhooks` build in dev, for the attack suite's host answers) and `DEEDGLASS_REPO_PUBLIC`
  (the banned-words check).
- **Any `TXV_*` variable** is an error in both modes, with its new name:
  `✕ TXV_DATABASE_URL is from the old name. Rename it to DEEDGLASS_DATABASE_URL.`

## `deedglass.yaml`

- One YAML document, a mapping of `key: value`, at most 64 KiB.
- No anchors, aliases or merge keys. This keeps a hostile file from expanding in memory.
- Unknown keys are errors, with a did-you-mean.
- `trusted_proxies` may be a list; every other value is a single scalar.
- A world-readable file gets a warning.

### Sections (file-only settings)

Everything else tunable lives in sections of `deedglass.yaml`, never in new environment variables
(contract §11). A section is a mapping; a nested mapping makes a dotted path. Unknown settings are
errors with a did-you-mean, and every value has a range. S13 added the `gate` section:

```yaml
gate:
  wait_timeout: 10s          # POST /v1/actions waits this long for a gateway action (at most 25s)
  execute_timeout: 30s       # a connector call; past it the outcome is unknown
  edge_result_timeout: 10m   # proxy mode: how long the edge has to report
  max_args_bytes: 262144
  max_result_bytes: 262144
  worker:
    concurrency: 4
    max_attempts: 8
```

<!-- settings:begin (generated from internal/config/settings.go; go test ./internal/config -run TestSettingsDocs -update) -->
| Setting | Default | Range | What it does | Added |
|---|---|---|---|---|
| `gate.wait_timeout` | `10s` | 0s to 25s | How long POST /v1/actions waits for a gateway action to finish before it answers 202 processing (at most 25s). | S13 |
| `gate.execute_timeout` | `30s` | 100ms to 10m0s | How long the Gate lets a connector call run. Past it, the outcome is unknown and the deed says UNVERIFIABLE. | S13 |
| `gate.edge_result_timeout` | `10m` | 1s to 24h0m0s | Proxy mode: how long the edge has to report a result before the action is sealed UNVERIFIABLE (edge_lost). | S13 |
| `gate.max_args_bytes` | `262144` | 1024 to 262144 | The largest args object POST /v1/actions accepts, in bytes (at most 256 KiB, the policy input limit). | S13 |
| `gate.max_result_bytes` | `262144` | 1024 to 1048576 | The largest tool result stored on the action, in bytes. Larger results are digested, and only the digest is kept. | S13 |
| `gate.worker.concurrency` | `4` | 1 to 64 | Witness jobs one instance runs at once. | S13 |
| `gate.worker.max_attempts` | `8` | 1 to 100 | Attempts per witness job before the action is sealed UNVERIFIABLE (witness_gave_up). | S13 |
<!-- settings:end -->

### The `mcp` section (S15)

The MCP gateway's settings ([MCP.md](MCP.md)). Unlike `gate`, `upstreams` maps names you choose to
their settings, so it is validated on its own; an unknown key is a problem like any other.

| Setting | Default | What it does |
|---|---|---|
| `mcp.allowed_origins` | `[]` | Browser origins (scheme and host) allowed to call `/mcp/*`, besides the server's own. Every other `Origin` gets 403. |
| `mcp.passthrough` | `[]` | Method families forwarded to an upstream and logged, never sealed: `resources`, `prompts`, `completion`. |
| `mcp.upstreams.<name>.url` | required | The upstream's Streamable HTTP endpoint. https; http only in dev with `allow_private_networks`; never credentials in the URL. `<name>` is 2 to 32 lowercase letters, digits or hyphens, not `deedglass`, `mcp`, `native` or `fake`. |
| `mcp.upstreams.<name>.headers_from_env` | `{}` | Header name → environment variable name. Values are read once at start, never logged, returned or sealed. Names can't start with `DEEDGLASS_`. Dev only: prod refuses it (use an `mcp_http` connector with its credential in the vault; S18). |
| `mcp.upstreams.<name>.timeout` | `30s` | Each HTTP request to the upstream (100ms to 10m). |
| `mcp.upstreams.<name>.tools_ttl` | `30s` | How long its `tools/list` is cached (100ms to 10m; a shorter upstream `ttlMs` wins). |

## Examples

```sh
# Dev, with a local Postgres from deploy/docker-compose.yml
DEEDGLASS_DATABASE_URL=postgres://deedglass_app:local@127.0.0.1:55432/deedglass deedglass config print

# Prod: generate the two keys once and keep the pepper stable
export DEEDGLASS_ENV=prod
export DEEDGLASS_BASE_URL=https://deedglass.example.com
export DEEDGLASS_VAULT_KEY="$(openssl rand -base64 32)"
export DEEDGLASS_API_KEY_PEPPER="$(openssl rand -base64 32)"
deedglass config print --json
```

## What this doesn't do

- It doesn't read secrets from a secret manager; the environment is the interface.
- It doesn't reload at run time. Restart after a change.
- Later sessions add their settings as rows in the table (for example
  `DEEDGLASS_MIGRATE_DATABASE_URL` in S02). A name that isn't in the table is refused in prod.
