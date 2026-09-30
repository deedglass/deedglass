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
| `DEEDGLASS_VAULT_KEY` | `vault_key` (dev only) | — | yes | serve, vault, connector credentials | Base64 of exactly 32 bytes; encrypts connector credentials. Dev generates one in .deedglass/. | S01 |
| `DEEDGLASS_API_KEY_PEPPER` | `api_key_pepper` (dev only) | — | yes | serve, org keys, agent keys | Base64 of at least 32 bytes, mixed into API key hashes. Keep it stable: changing it invalidates every API key. Dev generates one in .deedglass/. | S01 |
| `DEEDGLASS_LOG_LEVEL` | `log_level` | `info` | no | all | Log level. | S01 |
| `DEEDGLASS_SIGNUP` | `signup` | `invite` | no | serve | Who can join: invite (people an admin invites) or closed. | S01 |
| `DEEDGLASS_RESEND_API_KEY` | `resend_api_key` (dev only) | — | yes | serve | Resend API key for sign-in and approval emails (starts with re_). Without it, dev prints emails to the log. | S01 |
| `DEEDGLASS_ANTHROPIC_API_KEY` | `anthropic_api_key` (dev only) | — | yes | serve, insights | Optional Anthropic API key for Insights phrasing (starts with sk-ant-). | S01 |
| `DEEDGLASS_TRUSTED_PROXIES` | `trusted_proxies` | — | no | serve | Comma-separated CIDRs whose X-Forwarded-For is believed. | S01 |
| `DEEDGLASS_ALLOW_PRIVATE_NETWORKS` | `allow_private_networks` | `false` | no | serve | Dev only: lets connectors reach private networks. Refused in prod. | S01 |
| `DEEDGLASS_SLOT_LAG_ALERT_MB` | `slot_lag_alert_mb` | `512` | no | serve | Alert when a Postgres replication slot holds more WAL than this (1-1048576 MB). | S01 |
| `DEEDGLASS_API_KEY` | `api_key` (dev only) | — | yes | CLI, mcp, SDKs | Client key; `serve` ignores it. An agent or admin API key (dg_live_… or dg_test_…) for client commands. | S01 |
| `DEEDGLASS_SERVER` | `server` | — | no | CLI, mcp, SDKs | Client key; `serve` ignores it. The Deedglass server client commands talk to. | S01 |
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

- **Tooling names** are ignored by the binary: `DEEDGLASS_IT_*` (integration scripts),
  `DEEDGLASS_DEV_*` (local compose) and `DEEDGLASS_REPO_PUBLIC` (the banned-words check).
- **Any `TXV_*` variable** is an error in both modes, with its new name:
  `✕ TXV_DATABASE_URL is from the old name. Rename it to DEEDGLASS_DATABASE_URL.`

## `deedglass.yaml`

- One YAML document, a mapping of `key: value`, at most 64 KiB.
- No anchors, aliases or merge keys. This keeps a hostile file from expanding in memory.
- Unknown keys are errors, with a did-you-mean.
- `trusted_proxies` may be a list; every other value is a single scalar.
- A world-readable file gets a warning.

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
