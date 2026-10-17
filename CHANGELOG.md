# Changelog

Every notable change to Deedglass, newest first. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). Release notes are generated from
this file by `scripts/release-notes.sh`.

## [Unreleased]

### Added

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

## [0.1.0] - Unreleased

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

[Unreleased]: https://github.com/deedglass/deedglass/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/deedglass/deedglass/releases/tag/v0.1.0
