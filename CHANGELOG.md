# Changelog

Every notable change to Deedglass, newest first. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). Release notes are generated from
this file by `scripts/release-notes.sh`.

## [Unreleased]

### Added

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
