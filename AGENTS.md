# AGENTS.md

For AI coding agents and people working in this repository. `CLAUDE.md` holds the maintainer's
longer build rules; where the two differ, this file is the one written for a fresh clone.

## What Deedglass is

Deedglass sits between AI agents and the systems they change. Rules decide each tool call, a check
compares what the system really changed with what the agent reported (Claimed, Checked or Proven;
Matches, Doesn't match or Can't check), and each result is sealed as a signed record anyone can
check offline. It can undo the rows an action changed.

The core, the SDKs and the CLI are Apache-2.0. The Pro features in `ee/` are FSL-1.1-ALv2, and each
release of them becomes Apache-2.0 two years after it ships. The release binary, the
`@deedglass/cli-*` npm packages and the container image include `ee/`, so their licence is
`Apache-2.0 AND FSL-1.1-ALv2`.

Free features send nothing to deedglass.com. Only a machine with a Pro licence checks it, through
Dodo Payments and deedglass.com: `deedglass serve` when it starts and then daily, and Pro commands
when you run them; a request goes out only when the last successful check is a day old.
`deedglass version --check` and the `--latest` option of `deedglass policy install` and
`policy packs` contact GitHub only when you use them.

## Setting Deedglass up for a user

If the user asked you to set up Deedglass in their project (rather than to work on this repo), ask
before each change: starting Postgres in Docker, writing `.deedglass/`, and editing an MCP client's
config are each the user's call. The steps, with what each one changes, are in
[`llms-install.md`](llms-install.md). For giving an agent Postgres access carefully, read
[`integrations/claude-code/skills/safe-postgres-agents/SKILL.md`](integrations/claude-code/skills/safe-postgres-agents/SKILL.md).

Two commands send nothing and need no Docker (npx itself downloads the package from npm the first
time): `npx -y deedglass scan --no-write` (a read-only grade of the project's agent setup, printed
only; without `--no-write` it also writes `deedglass-scan.md` and `deedglass-scan.html`) and
`npx -y deedglass play` (the demo, offline).

## Working on this repo

### Set up

- Go 1.27.2 (`go.mod`). Most changes need nothing else: `go test ./internal/scan/ ./examples/policies/`.
- Docker, for `deedglass init` and the integration tests on a throwaway Postgres.
- Node 24, for the Console (`web/`), the browser verifier (`web/verify/`), the docs site
  (`docs-site/`) and the TypeScript SDK (`sdk/typescript/`). Install with `npm ci --ignore-scripts`.
- Python 3.10 or newer, for the Python SDK (`sdk/python/`).

### Commands

```sh
make build          # bin/deedglass
make ci             # what CI's lint and unit jobs run: fmt, tidy, vet, race tests, banned words, ...
make docs-check     # build docs-site/ and run its offline gates (llms files, sitemap, links, claims)
make integration    # integration tests on a throwaway Postgres 18 (CI runs them on every PR)
make help           # every target
```

A test that prints an `-update` command has a golden file: run it, read the diff, commit both.

### Where things are

| Path | What |
|---|---|
| `cmd/deedglass/` | The CLI and server entry point (`deedglass help` lists every command) |
| `internal/` | The server and CLI internals: `gate` (runs each call), `policy` (rules), `witness` (the check), `deed` (records), `ledger`, `decoder` (Postgres's change log), `connector/*`, `mcp/*`, `scan`, `setup` (`deedglass init`) |
| `ee/` | Pro features, under `ee/LICENSE` (FSL-1.1-ALv2) |
| `pkg/deedglass/` | The public Go client |
| `sdk/typescript/`, `sdk/python/` | The SDKs and their framework adapters |
| `web/`, `web/verify/` | The Console, and the in-browser record checker |
| `docs/`, `docs-site/` | Repo docs, and docs.deedglass.com (which imports many of them) |
| `examples/` | The Acme demo, starter policies (`examples/policies/`), SDK examples |
| `attack/` | The attack suite and its expected outcomes |
| `integrations/`, `npm/`, `deploy/` | The Claude Code plugin and listings, the npm packages, compose files and the cloud kit |

### Rules CI enforces

- **Words.** `scripts/banned_words.txt` applies to every file and commit message. People read plain
  names (Claimed, Checked, Proven; Matches, Doesn't match, Can't check; rules, check, record);
  programs keep the short names (`L0`/`L1`/`L2`, `VERIFIED`/`MISMATCH`/`UNVERIFIABLE`). See
  `docs/NAMING.md`.
- **Evidence.** No code path turns missing evidence into a result: when Deedglass can't tell, the
  result is Can't check. Never change an expected outcome in `attack/attack_meta.json` to make a
  test pass.
- **Network.** Every outbound HTTP call goes through `internal/connector/egress`
  (`TestSingleEgressDoor`). The free `deedglass scan` opens no connection except to the one
  database the user names with `--postgres-dsn-env`; `scan --full` (Pro) also checks the licence
  and, when a Deedglass server is configured, seals the report on the user's own server. Free code
  never imports `internal/licence`.
- **Dependencies.** Prefer the standard library. A new dependency needs an ADR (index:
  `docs/adr/README.md`) with its pinned version, licence and alternatives, so open an issue before
  adding one. No cgo in the main binary.
- **The description.** `docs/description.json` is the one description of Deedglass;
  `docs/description_test.go` checks this file, `README.md`, `server.json`, `context7.json` and the
  npm metadata against it.
- **Errors and migrations.** Messages come from catalogs and end with a next step; a new API error
  code needs its section in `docs/api/errors.md`. Migrations are forward-only; run `make embed-gz`
  after adding one.

### Pull requests

Sign each commit (`git commit -s`, the DCO), keep one topic per pull request, and say how you tested
it. `CONTRIBUTING.md` has the rest.

### Security

Never in a public issue or pull request. Report privately:
<https://github.com/deedglass/deedglass/security/advisories/new> ([`SECURITY.md`](SECURITY.md)).
