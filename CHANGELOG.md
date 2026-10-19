# Changelog

Every notable change to Deedglass, newest first. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). Release notes are generated from
this file by `scripts/release-notes.sh`.

## [Unreleased]

## [0.3.0] - 2026-10-19

Healthcare, phase 1: an agent's writes to a FHIR R4 server, decided by rules and checked against
what the server stored. FHIR works through the REST connector with no FHIR code, and the new `fhir`
connector (beta) adds SMART Backend Services credentials and read-back at the version the server
reported. Also new: the `healthcare-fhir` policy pack, PHI mode, records exported as FHIR R4
AuditEvent and Provenance, and twelve attack cases for FHIR servers (group I; eight run and match,
four wait). Not in this release: the Proven (L2) path for Postgres-backed FHIR servers such as HAPI
FHIR JPA or Medplum, so FHIR writes are Checked (L1), not Proven; attack cases I05-I08 wait on it.
No new dependency, one schema migration (0031, PHI mode), and `formula_version` stays 1. The server
image budget is 45,000,000 bytes (ADR-106).

### Added

- **A FHIR R4 server through the REST connector**, with no FHIR-specific code: REST body args take
  an optional `field` (the JSON key the arg is sent under, so `resourceType` and `valueQuantity` can
  be sent; existing config hashes don't change), and read-back expect paths may use mixed case after
  the root (`body.valueQuantity.value`). `examples/connectors/fhir-hapi.yaml`, a live test against
  HAPI FHIR (`make fhir-it`), and `docs/guides/fhir-server.md`.
- **Attack group I**: twelve cases for FHIR servers (I01-I12), declared before any FHIR connector
  code. Eight run and match (I01-I04, I09-I12); I05-I08 need the Proven path for Postgres-backed
  FHIR servers and wait.
- `make fhir-spike`: a repeatable probe of what HAPI FHIR JPA on Postgres stores to tie its rows to
  an action (when it stores request ids, it keeps 16 characters of `X-Request-ID`).
- **PHI mode** (`settings.phi_mode` on `PATCH /v1/org`, owner only, sealed in `org.updated`). Once an
  action is sealed and its checks are done, its stored request, result, error message and witness
  detail keep ids and digests only, and its policy input is removed; turning it on covers the org's
  earlier actions too. Connectors name their identifiers (the REST connector: path arguments, the
  HTTP status and the identifier headers). Approval requests in a PHI-mode org show argument names
  and digests from the start. Migration 0031 adds `actions.phi` and `actions.payload_minimized_at`.
  See `docs/PHI_MODE.md` for what it keeps and what it doesn't cover.
- **The `fhir` connector type** (beta, ADR-105): a FHIR R4 server's REST API as governed tools, one
  per interaction (`read`, `vread`, `search`, `history`, `create`, `update`, `patch`, `delete`) on
  the resource types in `allowed_resource_types`. Writes carry `If-Match` and `If-None-Exist` when
  the agent sends them and a 16-character binding token in `X-Request-ID`; each write is read back
  at the version the server reported and compared after normalising what the server adds, with the
  new reasons `fhir_conditional_matched`, `fhir_field_changed`, `fhir_no_new_version` and
  `fhir_history_no_match`. A stale `If-Match` (409 or 412) fails as the new error code
  `fhir_version_conflict`. Credentials are SMART Backend Services (client credentials with an
  RS384 or ES384 JWT assertion, tokens kept at most 300 s) or a bearer token. New policy facts
  `request.fhir`. Proven (L2) for FHIR servers is not built yet. See `docs/connectors/fhir.md`.
- A native read-back can name a reason code from the Witness catalog for a finding, and can look
  for a write after an unknown outcome when its connector says it can (the FHIR connector's
  history lookup); the catalog decides the class, so such a finding is never a pass on its own.

- **The `healthcare-fhir` policy pack** (free; `deedglass policy install healthcare-fhir`) for the
  FHIR connector: deletes of clinical records are denied (they are corrected with a new version),
  orders and medication changes, allergies, the problem list and final notes wait for a clinician,
  claims and other billing records need two admins, coverage is read-only, searches name what they
  look for and ask for at most 100 results, and every other write is read back (Checked). An agent
  labelled `break-glass=on` runs creates and updates without waiting, each record naming the rule
  `break-glass`. 36 cases; `make policy-test` now runs every pack's cases. Attack case I12 runs. See
  `docs/guides/healthcare-fhir-pack.md`.

- **Records as FHIR R4 AuditEvent and Provenance** (HC-P10): `deedglass deeds export --format
  fhir-auditevent|fhir-provenance` (from the database, or `--from <bundle>` offline) and
  `GET /v1/deeds/export?format=…` write FHIR NDJSON. One AuditEvent per FHIR interaction, shaped to
  the IHE BALP profiles (the Patient variants when the call names a patient in the clear), with the
  FHIR server, the connector's OAuth client id, the agent as the user and Deedglass as the observer;
  one Provenance per successful FHIR write, shaped to the HL7 AI Transparency ballot's AI Provenance,
  with the agent as an AI Device author, Deedglass as assembler and (when the check passed)
  verifier, the request digest as the entity and the record's seal as the signature. Ids and
  digests only. CI validates every resource against HAPI FHIR's R4 StructureDefinitions. The record
  format doesn't change. See `docs/EXPORTS.md`.
- The FHIR connector's result carries `client_id` (the SMART client the server saw) when it uses
  SMART Backend Services; PHI mode keeps it.

### Changed

- The server image budget is 45,000,000 bytes (it was 40,000,000; ADR-106). The
  `docker-smoke` job and `scripts/ci-local.sh` print how far under it the image is.
- The release build no longer inlines the Insights store, approvals and the notifiers, which wait on
  Postgres or the network, to make room in the server image for the FHIR connector.

## [0.2.1] - 2026-10-19

A patch for the shell guard: the hook no longer blocks Bash on Windows without Git Bash or with an
older `deedglass` binary, and off means no output. No new dependency, no schema migration, and
`formula_version` stays 1.

### Fixed

- **The Claude Code plugin's shell guard never blocks a Bash command when it can't check.** The
  hook now sets `"onFailure": "continue"`, so a hook that can't start (for example in PowerShell on
  Windows without Git Bash, where there is no `sh`) or times out lets the command go ahead. With
  `DEEDGLASS_SHELL_GUARD=ask` and no usable `deedglass` on `PATH` it exits 0 with no decision and a
  note that it checked nothing (it exited 1 before); `deny` keeps the documented exit 1. An older
  `deedglass` without `guard shell` exits 2 on the unknown command, which Claude Code read as a
  block of every Bash command; the hook script now never passes on that exit code. Off (the
  default), it still reads its input and exits 0 with no output, without running `deedglass`.

## [0.2.0] - 2026-10-18

Three ways to see what a coding agent can do to a database from its shell, where an MCP gate can't
look: a hook that asks you first, a scan of what would run without a prompt, and a watched session
that shows what Postgres committed and saves the SQL to put it back. Each is a second check next to
Claude Code's own permission rules, not a sandbox. No new dependency, no schema migration, and
`formula_version` stays 1.

### Added

- **The shell guard** (`deedglass guard shell`, FEAT-07, ADR-104): a Claude Code `PreToolUse` hook
  that reads each Bash command before it runs and, when part of it would destroy database data
  (`prisma migrate reset`, `prisma db push --force-reset`, `alembic downgrade base`, `rails db:drop`,
  `dropdb`, a `psql -c` with `DROP TABLE`, `TRUNCATE`, or an `UPDATE` or `DELETE` with no `WHERE`),
  answers `ask` with one sentence that names Deedglass and the reason (`--deny` to deny instead). It
  splits compound commands, subshells, substitutions, `VAR=value` prefixes and `sh -c` strings with
  a small reader of its own (no new dependency), reads SQL through `internal/sqlguard`, and never
  answers `allow`. No server, no network: a second check, not a sandbox. The Claude Code plugin
  carries it off by default; `DEEDGLASS_SHELL_GUARD=ask` in Claude Code's settings turns it on. See
  `docs/SHELL_GUARD.md`.
- **Scan: what an agent can run in a shell.** `deedglass scan` now reads Claude Code's
  `.claude/settings.json`, `.claude/settings.local.json` and (with `--user-configs`)
  `~/.claude/settings.json`, finds database tools with destructive commands in `package.json`,
  Python dependency files and `Gemfile` (Prisma, Drizzle Kit, Alembic, Rails, Knex, TypeORM,
  Sequelize), and says which spellings of those commands would run without a prompt, for example
  "An agent can run `npm run db:diff` here without a prompt". It also flags database URL variable
  names in `.env` files that no `Read` deny rule covers; it never reads a value. Three new finding
  ids (`shell_destructive_unprompted`, `shell_destructive_no_rule`, `env_database_url_readable`)
  count under formula v1 like any other finding, and `scan_report` gains `claude_settings` (rule
  counts only). It runs no command. See `docs/SCAN.md`.
- **Watch a coding agent.** `deedglass observe run --connector <name> --undo-sql ~/deedglass-undo -- claude`
  starts Claude Code (or any agent command) inside a watched session. When it exits, the run prints,
  per watched table, a count of the row changes Postgres committed (shell commands such as `psql`
  included), what Deedglass caught, the any-writer note, and saves the free undo SQL for you to
  review and run (to `undo-<session id>.sql` when given a folder); Deedglass never runs it. It names
  a `.deedglass/admin.key` the agent could read, and says when the decoder stopped (a dropped watched table
  leaves the publication). `observe stop` prints the any-writer note too, and `observe run` now
  closes the session when the terminal closes (SIGHUP). The Claude Code plugin gains
  `/deedglass:watch`, which prints that line; you run it with your own key. See `docs/OBSERVE.md`
  and docs.deedglass.com/guides/watch-a-coding-agent.

### Changed

- The server binary embeds the policy pack snapshot, the simulation scenario, the demo SQL and
  policy and the scan catalog gzip-compressed, and the SaaS connectors, the connector admin API and
  the `mcp serve` bridge are built without inlining (each call there waits on the network or the
  database). This keeps the server image under its 40 MB budget with the three features above; no
  behaviour changes.

### Fixed

- `docs/SCAN.md` and the observe docs describe the cited incidents as their issues do:
  `alembic downgrade base` reverts every revision, and none of the cited reports shows `psql -c`.

## [0.1.1] - 2026-10-18

### Added

- **Listed on the official MCP Registry** as `io.github.deedglass/deedglass`. A manual workflow
  (`mcp-registry.yml`) publishes `server.json` with GitHub OIDC, after checking that the npm package
  of the same version carries the same `mcpName`.
- **What your agents did this week.** Every Monday from 08:00 in the org's time zone, the server
  emails the org's owners the week that just ended: actions by decision, Check results, the rules
  that blocked most, approvals, undos, Stop, watched sessions, what Deedglass caught with each free
  way back, and the record chain. Through the mailer the server already uses; nothing goes to
  deedglass.com; one click turns it off. The same summary is Console › Overview's This week card,
  `GET /v1/weekly` and `deedglass report [--week|--month] [--format md|html|json]` (free). Migration
  0030 adds `memberships.weekly_email`. See `docs/WEEKLY.md`.
- **The Agency plan** (licence feature `agency`, sku `agency_monthly`): `deedglass clients`,
  `GET /v1/clients` and Console › Clients list every org you look after, each read in its own org
  transaction; `deedglass report --all-clients --brand brand.yaml` writes every client's report with
  your name on it. See `docs/AGENCIES.md`.
- **An install script** for macOS and Linux (`scripts/install.sh`): it downloads the release archive
  for the machine and `checksums.txt`, checks the sha256, and copies `deedglass` into `~/.local/bin`
  (`--version`, `--dir`). It runs nothing it downloads and never edits a shell profile.
- `AGENTS.md` for coding agents working from a clone, `context7.json`, and the scan-rule and
  policy-pack issue forms. Every issue form opens with the private vulnerability reporting link.

### Changed

- The scan's last line is now `What your grade means: deedglass.com/score` (text, Markdown, HTML and
  the `--json` `footer`), instead of an offer whose buy button is off.
- Licence metadata: the release binary includes the Pro code in `ee/`, so the `@deedglass/cli-*`
  npm packages, the Homebrew cask, the image labels and the Claude Desktop bundle
  say `Apache-2.0 AND FSL-1.1-ALv2`, and the archives and npm packages carry `ee/LICENSE`. The core,
  the SDKs and the CLI's source stay Apache-2.0.
- One description of Deedglass (`docs/description.json`) in the README, `AGENTS.md`, `server.json`,
  the npm package, the plugin listings and the docs' `llms.txt`; a test keeps them equal.
- docs.deedglass.com: `llms-full.txt` and `llms-small.txt` fit 750 KB and 150 KB (the per-code and
  generated reference pages stay on the site), a new `llms-postgres.txt`, `llms.txt` asks agents to
  ask before each change, robots.txt names crawler groups, the sitemap leaves out the per-code pages
  (now noindex) and dates every URL, and each page carries TechArticle JSON-LD and a title of at
  most 60 characters.
- The install docs no longer point at the ghcr.io image, which isn't public yet: npx, the install
  script, the release archives, or `make web-build docker`.
- `deedglass mcp install-links` and the generated install links point the Claude Desktop entries at
  the docs page that connects it by hand until a release carries the `.mcpb` bundles (the download
  URLs answered 404), and the bundle and the Goose link carry the one description.
- More embedded text is kept gzip-compressed in the binary (the full scan report template, the
  security pack's questions and document, the sign-in and approval pages, the starter policies, the
  email templates, the play cast), and more cold paths skip inlining, so S52 fits the image budget.
- **npm packages come from the release workflow.** `deedglass`, its five platform packages and
  `@deedglass/sdk` are published by GitHub Actions from the same run that builds the release
  archives, over npm trusted publishing with npm provenance; each binary is checked against
  `checksums.txt` first, and a release candidate goes to the `next` dist-tag. See
  `docs/RELEASING.md`.

- **The security review pack.** `deedglass security-pack readiness` (free; also
  `GET /v1/security_pack/readiness` and Console › Settings › Security review) answers 24 questions a
  security reviewer asks about AI agents, in eight groups, each with a status computed from your own
  records (In place, Partly, Not set up, Not applicable, Can't tell) and the fix for a gap.
  `deedglass security-pack build` (Pro, `security_pack`; `POST /v1/security_pack`) writes a folder
  with a print-ready `index.html`, `answers.md`, `answers.json` (security_pack v1), the period's
  records and `pack.seal.json`, a sealed record over each file; `--aliases` replaces agent,
  connector and rule names. `deedglass security-pack verify` (free) checks a folder offline.
  `deeds seal-file` accepts `application/gzip`. See `docs/SECURITY_PACK.md`.
- **`deedglass play`.** The 301 scene in a terminal, offline, with no Docker (`npx deedglass play`).
  The rules decide each call live (the Acme demo policy, `policy.Evaluate`), and four records are
  sealed with a key made for the run, checked, changed by one byte and checked again. Postgres's
  lines are a labelled replay of a recorded real run (`docs/assets/demo-hero.cast.json`, with its
  commit and date) and of `make demo`'s compared undo output. `--fast` skips the pauses; `--json`
  prints what the run found. See `docs/PLAYGROUND.md`.
- **The Claude Code plugin** gains `/deedglass:wrap` (prints the `claude mcp add` line that runs one
  of your MCP servers through `deedglass mcp wrap`) and the `safe-postgres-agents` skill. The
  repo's marketplace (`/plugin marketplace add deedglass/deedglass`) now has a description.
- **A GitHub star note after a scan**: one dim line, at most once per machine, only in a terminal,
  never in CI, with `--ci` or `--json`. `scan --no-star` or `DEEDGLASS_NO_STAR=1` hides it.

- **Observe mode (watched sessions).** `deedglass observe run --connector <name> -- <command>` (and
  `observe start|stop|list|show|exclude`, the `/v1/observe/sessions` routes, Console › Sessions)
  groups every transaction Postgres committed on a connector's watched tables while a session was
  open, seals it (`observe.session_sealed`: ids, counts, digests), and flags what Deedglass caught (a
  TRUNCATE, or one transaction over `witness.observe.caught_rows`). `observe show --undo-sql` prints
  the undo as SQL you run yourself (free); `observe undo` undoes the whole session in one step
  (Pro, `undo_session`). Agent keys get 403 on every observe route. Migration 0029. Errors
  `observe_not_enabled`, `pro_required`. See `docs/OBSERVE.md`.
- **Licence features old installs can read.** The token request names the features this build knows
  (`features_known`); `undo_session` joins founding and Pro.

- **`mail.get_message` reads what a reply triager needs** (ops build, `outboundmail` tag): the
  auto-reply headers, `References`, `Delivered-To`, Gmail's `history_id`, and a bounce's
  `original_message_id` from its attached original. Additive; no new tool, route or setting.

- **Scheduled actions.** `POST /v1/actions` takes an optional `schedule {not_before, not_after}`
  (gateway mode): an approved action runs inside the window, a missed window is cancelled and
  sealed (`schedule_missed`), and an allowed action with a later window is refused
  (`schedule_needs_approval`). An approved scheduled action can be cancelled until its run starts
  (`action_executing` after). Migration 0028. See `docs/SCHEDULED_ACTIONS.md`.
- **`notify: []`** on a `require_approval` rule sends no email or Slack card; the approval waits in
  the Console and API only.
- **Email approvals.** The approval context's `email_preview` section shows an email's From, To,
  window, signal, subject and whole body on `/approve` and in Console › Approvals, with one
  Approve button per email.
- **`request.mail` policy facts** for the ops build's outbound-mail connector.
- **The ops build** (`make ops-build`, `-tags outboundmail`): the `outbound_mail` connector, only for
  the org ids in `connectors.outbound_mail.orgs`. Release builds contain no sending code and refuse
  to start with that setting. The Gmail connector stays drafts only; its OAuth sign-in moved to
  `internal/connector/googleauth` with no change in behaviour.

- **Sealed files.** `deedglass deeds seal-file <path> --label …` seals a file's sha256, size, media
  type and label as the admin deed `file.sealed` (`POST /v1/deeds/files`); the file never leaves
  your machine. `deedglass verify <deed.json> --file <path>` checks the file against the deed,
  offline. Resolves KI-009.

### Fixed

- The undo planner's fuzz test now expects a refusal when a transaction leaves rows in a circle of
  keys (found by `make fuzz-regress`; the planner was right, the test's model was too narrow).

## [0.1.0] - 2026-10-15

The first release. Release candidate `0.1.0-rc.1` (2026-10-05); the date is set when the founder
tags `v0.1.0`.

### Added

- **The Gate.** `POST /v1/actions` authorizes every agent action against an enforced policy, runs it
  through a connector or hands it to the edge, and seals the outcome as a deed. `deedglass mcp wrap`
  governs any stdio MCP server; `deedglass mcp serve` and `/mcp/{connector}` govern remote ones.
- **Policies.** YAML compiled to CEL with budgets, approvals, shadow mode, versions sealed on
  activation, `deedglass policy test`, replay and starter policies in `examples/policies`.
- **The Witness.** Verdicts VERIFIED, MISMATCH and UNVERIFIABLE at L0 Reported, L1 Confirmed (a
  read-back from the target) and L2 Witnessed (Postgres logical decoding), with expected-versus-observed
  diffs and undo for Postgres rows.
- **Deeds.** Ed25519-sealed, hash-chained per org, exported as JSON lines or a sealed bundle, and
  checked offline by `deedglass verify` or the in-browser verifier.
- **Approvals** in the Console, by email link and in Slack, with four-eyes rules and expiry.
- **Stop.** Stop one agent or every agent in an org, across instances.
- **Connectors.** Postgres, REST, GitHub, Slack, Stripe (sandbox only), Gmail drafts, Notion and
  remote MCP servers, with credentials in an encrypted vault and one egress door.
- **The Console** for actions, approvals, agents, policies, connectors, Insights, deeds and settings.
- **Insights.** Replay, stats, anomalies and suggested policy changes that a person accepts.
- **SDKs.** `@deedglass/sdk` for TypeScript and `deedglass` for Python, with adapters for the AI SDK,
  OpenAI Agents, LangChain and the Claude Agent SDK.
- **Setup.** `deedglass init`, `doctor`, `scan` and shell completions (`deedglass completion zsh|bash|fish`).
- **Releases.** Five binaries, a two-platform image, checksums and an SBOM per archive;
  `deedglass version --check` asks GitHub for a newer release when you run it.
- **The scripted demo.** `make demo` runs seven scenes through the real Gate offline (`deedglass dev
  agent-script examples/demo/launch.yaml`) and `make demo-live` adds Slack and Stripe test mode.
- **The full scan report.** `scripts/report/render.mjs` turns `deedglass scan --json` into one tagged
  A4 page.

### Known issues

See [KNOWN_ISSUES.md](KNOWN_ISSUES.md): what happens, who is affected, the workaround and the fix target.

### What it doesn't do yet

See [docs/LIMITATIONS.md](docs/LIMITATIONS.md). In short: L2 covers Postgres only, seal keys are per
deployment, and Deedglass trusts the Postgres server and the host it runs on.

[Unreleased]: https://github.com/deedglass/deedglass/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/deedglass/deedglass/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/deedglass/deedglass/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/deedglass/deedglass/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/deedglass/deedglass/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/deedglass/deedglass/releases/tag/v0.1.0
