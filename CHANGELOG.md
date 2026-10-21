# Changelog

Every notable change to Deedglass, newest first. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). Release notes are generated from
this file by `scripts/release-notes.sh`.

## [Unreleased]

### Security

- **The A2A proxy refuses what a web page can send** (audit M-03, attack cases T01 to T04): a
  `Host` that isn't `127.0.0.1`, `[::1]` or `localhost` (DNS rebinding), an `Origin` header or a
  `Sec-Fetch-Site` other than `none`, a body that isn't `application/json`, and CORS preflights. The
  `?A2A-Version=` query parameter is no longer read. **Changed:** every JSON-RPC request must carry
  the agent's key the proxy runs with, `Authorization: Bearer <key>`; point your A2A client's auth
  at `DEEDGLASS_API_KEY`. `--api-key` prints a warning, since a flag shows in `ps`.
- **`witness debezium` caps its input** at 1 GiB after gunzip (`feed_too_large`, exit 2), and
  `--json` names the input it read (`feed.input_sha256`, `feed.input_bytes`).
- **`install.sh` checks the release signature** (`checksums.txt.sigstore.json`) with cosign when it
  is installed, and installs nothing if it's missing or doesn't match; otherwise it prints the
  command. The Homebrew cask no longer clears macOS's quarantine attribute.
- CodeQL runs on pull requests, `main` and weekly; `mcp-registry.yml` no longer keeps git
  credentials on disk. Threat model v3, security review Pass 3, and attack group T (ADR-116).

### Fixed

- One server resolver for every client command (adoption-v1 F2): `--server`, `DEEDGLASS_SERVER`,
  `DEEDGLASS_BASE_URL`, the `.deedglass/stack.json` that `deedglass init` wrote in this directory or a
  parent, then `http://127.0.0.1:8080`. After `init --listen 127.0.0.1:18090`, `undo run`,
  `approvals`, `agent`, `connector`, `act` and every other API command now reach `:18090`, with the
  admin key `init` saved; before, some went to `:8080` with whatever key was in `DEEDGLASS_API_KEY`.
  Inside an init directory no key is sent to a server the environment names that differs from the
  stack's (new CLI code `server_mismatch`); only `--server` sends it elsewhere. `mcp serve` and
  `mcp wrap` hold keys back the same way.

## [0.7.0] - 2026-10-21

A second database for Proven. `deedglass witness debezium` reads a Debezium change feed and runs the
same Proven (L2) comparison against it, so a MySQL write can be compared with what the agent said it
would do, labelled "Proven · via Debezium (MySQL)" (ADR-114). A transaction the feed holds only in
part is never compared, and when MySQL carries no ledger row naming the action, the record says that
changes from any writer were compared. The toolchain moves to Go 1.27.3 for a crypto/x509 fix.
Nothing changes for existing records: the deed format is unchanged, Proven, Checked and Claimed mean
what they meant in 0.6.0, and the Postgres witness works as before. Known limits: the command is
offline (a file or stdin) and the Gate doesn't call it yet, so its record isn't sealed into the
Ledger; there is no MySQL connector and no Kafka source; dates, decimals and JSON columns are shown
but not compared; backtick-quoted names, `INSERT … ON DUPLICATE KEY UPDATE` and multi-table
statements can't be read. No new dependency, no schema migration, and `formula_version` stays 1.

### Added

- **A foreign change-feed witness** (TY-05, ADR-114, `docs/WITNESS_DEBEZIUM.md`): `deedglass witness
  debezium` reads Debezium change events (JSON lines from a file or stdin) and runs the Proven (L2)
  comparison against them, labelled by source: "Proven · via Debezium (MySQL)", method
  `debezium.mysql`. A transaction is the action's when it inserts a ledger row naming it; without
  one, changes from any writer to the statement's tables are compared and the record says so (note
  `any_writer`). A transaction the feed holds only part of is never compared (`feed_incomplete`),
  including a feed Debezium resumed inside a transaction. Reason codes `feed_incomplete`,
  `feed_no_match`, `any_writer`. `make debezium-it` reproduces the 301-row trigger scene on MySQL
  8.4 through Debezium Server 3.3 in Docker; its recording drives the CI goldens. No new dependency;
  no Kafka source yet.

### Security

- **Go 1.27.3**: govulncheck reports GO-2026-6809 in `crypto/x509` of go1.27.2, fixed in go1.27.3.
  `go.mod` and both build images move to it. No code change.

## [0.6.0] - 2026-10-20

More agents, more readers and an outside clock. Agent-to-agent delegations can go through the Gate
(`deedglass a2a proxy`, ADR-113), with the chain from the person to the remote agent recorded in
the action; the shell guard installs as a hook in Codex CLI, Gemini CLI, GitHub Copilot CLI and
Cursor as well as Claude Code. Exports and head files can carry RFC 3161 timestamps and a
transparency service's inclusion proof (ADR-112, off by default), so a reader can check that the
records existed by a given time without trusting the issuer's clock. Two independent verifiers, in Python
and JavaScript, are written from `spec/` alone and agree with `deedglass verify` on every release
bundle, tampered copy and vector. The corpus benchmark scores Deedglass and a no-gateway baseline
on 12 scenarios, including the ones Deedglass misses (it caught 5 of 11; the misses are listed).
And a Medplum example writes FHIR through the gate. Nothing changes for existing records: the deed
format is unchanged, Proven, Checked and Claimed mean what they meant in 0.5.0, and a bundle
without `timestamps.json` verifies as before. Known limits: Codex and Gemini CLI can't ask, so
there the shell guard denies; VS Code has no hook snippet yet; the independent verifiers skip COSE.
No new dependency, no schema migration, and `formula_version` stays 1.

### Added

- **The corpus benchmark** (`make corpus-bench`, `corpus/BENCHMARK.md`): plays every corpus
  scenario through each subject on a throwaway PostgreSQL 18.6 and scores it from the change log:
  caught (before or after commit), the true effect reported, and a record a third party can check.
  Subjects: `deedglass` (a real `serve` and Gate from the tree, L2 on the watched tables), `none`
  (the baseline) and `cmd:<name>=<command>` for any other observer. Six new scenarios (07-12), four
  of them cases Deedglass misses: a table nobody watched, NOTIFY outside the rows, an API (Claimed
  only), an upsert it can't read. First results (`corpus/results/latest.md`): Deedglass caught 5 of
  11 (2 before commit, 3 after) with 0 false alarms and left a checkable record in 12 of 12; the
  baseline caught 0 of 11.
- **Independent verifiers** (TY-10, `verifiers/`): `verifiers/python/deedglass_verify` (standard
  library only, Python 3.10+) and `verifiers/js/deedglass-verify.mjs` (one ES module for Node 20+
  and browsers) check sealed records, chains and bundles, v1 and v2 deeds, Ed25519 and ML-DSA-65,
  and share no code with `deedglass verify`. `verifiers/crosscheck.py` runs both beside
  `deedglass verify` on every release bundle, tampered copies and the deed vectors (280 of 280
  agree; 848 of 848 vector checks match, COSE skipped). `make independent-verifiers` and the
  `independent-verifiers` workflow. `spec/CHANGELOG.md` records an erratum: the order inside check
  5 and the seq a failure names, which the text left to the vectors.
- A2A delegations through the Gate (TY-07, ADR-113). `deedglass a2a proxy --connector <name> --peer <url>`
  is a loopback A2A 1.0 JSON-RPC endpoint in front of one remote agent: `SendMessage`,
  `SendStreamingMessage`, `CancelTask` and the push config writes become proxy-mode actions
  (connector type `a2a`, now a known type in policies) that run only when allowed; the peer's answer
  is recorded as Claimed. The delegation chain (person → agent → … → peer) travels as an A2A
  extension in `message.metadata`, is recorded in the action's arguments (bound by
  `request_digest`), and `deedglass actions show` prints it. Reads pass through; other methods are
  refused. No new dependency; the deed format is unchanged. Docs: docs/A2A.md; limits:
  docs/LIMITATIONS.md#a2a-delegations.
- The shell guard in more coding agents (TY-08). `deedglass guard shell --client codex|gemini|copilot|cursor`
  reads each agent's hook input and answers in its format, from one table
  (`internal/shellguard/clients.go`); `--print-config --client <name>` prints the hook config and
  writes no file, and `deedglass init --client codex|gemini|copilot` prints the MCP line and that
  config. Codex and Gemini CLI can't ask, so there the guard denies; VS Code has no snippet yet (its
  docs don't name the terminal tool), so it keeps the `deedglass mcp wrap` path. Goldens pin each
  snippet and each client's answer to the claude-code#80868 command; a weekly `hook-clients`
  workflow installs the latest Codex, Gemini and Copilot CLIs and runs them. Docs:
  docs/INSTALL_CLIENTS.md#the-shell-guard-in-each-coding-agent.
- Head timestamps (TY-03, ADR-112), off by default. With TSAs (`timestamps.tsa_urls`, at least
  `timestamps.min_tsa`, default 2) and optionally a transparency service (`timestamps.scitt_url`)
  in `deedglass.yaml`, each tar.gz export timestamps its last record and carries `timestamps.json`,
  and `deedglass deeds timestamp [--every 15m]` adds the current head to a file you keep elsewhere.
  `deedglass verify` checks the RFC 3161 tokens and the transparency service's inclusion proof
  offline (`--tsa-roots`, `--scitt-key` to pin who signed them) and compares each head with the
  records: a head past the last record is `chain_tail_missing`, one with another body hash is
  `timestamp_mismatch`. Spec: `spec/v2/BUNDLE_FORMAT.md` §9 (a bundle without the file is
  unchanged). No new dependency. Limits: docs/LIMITATIONS.md#head-timestamps. New page:
  docs/TIMESTAMPS.md.
- The Medplum example (`examples/medplum`, `make medplum-example`): Medplum 5.2.6 on PostgreSQL 18
  in Docker, an agent-style client writing through the gate (a create that Matches, a create
  Medplum answers 201 but stores without a read-only field, flagged Doesn't match at Checked and
  Proven, and an update a person countersigns), and a Medplum Bot handler that calls the gate with
  `@deedglass/sdk`. `make medplum-example-it` runs it as a test (skips without Docker; not in CI).
  New page: docs/integrations/medplum.md, linked from the FHIR connector reference.

## [0.5.0] - 2026-10-20

Records built to be read in twenty years. Every seal now names its algorithm (deed v2), and a
record can carry a post-quantum ML-DSA-65 seal beside its Ed25519 seal, so a reader decades from now
doesn't have to trust Ed25519 alone (ADR-109). One sealed bundle from every release since 0.1.0 is
verified on every pull request by today's CLI and browser verifier, so a change that stops an old
record from verifying can't merge. Records can also be exported as OpenTelemetry GenAI spans, OCSF
events, W3C PROV-JSON or FHIR, and the record spec moves to `spec/` under CC-BY-4.0. Nothing changes
for existing records: issuers keep writing v1 deeds until they hold a second key, and every v1 deed
verifies as before. Proven, Checked and Claimed mean what they meant in 0.4.0. Attack group Y
(Y01-Y12, the seal algorithms) runs and matches. Known limits: 100,000 dual-sealed deeds take about
5.5 s to verify on a laptop, over the 5 s budget (memory stays under 256 MiB); the Python SDK
reports v2 deeds as "cannot decide". No new dependency, one schema migration (0033), and
`formula_version` stays 1.

### Added

- **Deed v2: every seal names its algorithm** (TY-01, ADR-109). The record body lists its seal
  keys (`seal_keys`, algorithm and id, the primary first) and the envelope carries one seal per key
  (`seals`), each under its own domain tag, so a stripped seal is caught (`seal_missing`). Key sets
  carry `alg` per key and keep keys of an algorithm the verifier doesn't know instead of refusing
  the set. Verification applies a date-based accept policy that reads the record's `issued_at`,
  never the verifier's clock: Ed25519 alone is accepted only for records issued before 2035-01-01,
  and an algorithm with a key active at `issued_at` must have a seal. New codes
  `unsupported_algorithm` (cannot decide, exit 2) and `seal_missing`; no code renamed. The spec's
  Edition 2 is `spec/v2/DEED_SPEC.md`; `spec/v1/` is unchanged.
- **The ML-DSA-65 dual seal** (TY-02, ADR-109): ML-DSA-65 (FIPS 204) seals beside Ed25519.
  `deedglass keys rotate --alg ML-DSA-65` writes and registers the key; `serve` loads it from
  `DEEDGLASS_SEAL_PQ_KEY_FILE` and seals dual-sealed v2 deeds once it is active, and refuses to seal
  (an error, never a record with fewer seals) when the key set has an active key the instance
  doesn't hold. COSE exports of a v2 deed use alg -49. The browser verifier checks ML-DSA-65 with
  its own code and matches Go on every v2 vector. Migration 0033 (larger seal keys and envelopes).
  Attack group Y (Y01-Y12: downgrade, a stripped seal, a rewritten seal set, algorithm confusion,
  the mixed-key chain, cross-protocol replay, a revoked key) runs and matches.
- **The 2046 test** (TY-11): `testdata/release-bundles/` keeps one sealed export bundle from every
  release, made from that release's own code. The required CI job `release-bundles` verifies all
  of them with the CLI and the browser verifier on every pull request, and checks that a tampered
  copy fails.
- **`deedglass export`: records as OpenTelemetry GenAI spans, OCSF events, W3C PROV-JSON or FHIR**
  (TY-06): `--format otel-genai` (OTLP/JSON lines, one `execute_tool` span per action under the
  GenAI semantic conventions v1.41.0, a later check as a child span), `ocsf` (OCSF 1.9.0 API
  Activity events with `ai_agent` and `delegation`), `prov-json` (one W3C PROV-JSON document: agents,
  tool calls, records, who acted for whom, revisions) and `fhir` (the existing AuditEvent and
  Provenance export). Offline, from an export bundle or a `.jsonl` file; a bundle that doesn't match
  its manifest is refused. Each format has a mapping page that lists what it loses
  (`docs/exports/`, `docs/EXPORTS.md`), and CI validates every format against pinned schema files
  with golden output for a real release's bundle (`internal/recordexport`). A view of the records:
  the record format doesn't change.
- **The intent-vs-effect corpus** (TY-13): six scenarios in `corpus/` (a trigger, a cascade, an ORM
  reset, another writer, a partial failure, a timeout with an unknown outcome) as SQL, each with what
  the agent reports, what Postgres commits and the expected Deedglass outcome. `make corpus` runs
  them on a throwaway PostgreSQL 18.6 in Docker and checks every database fact; `CORPUS_TOOL` runs
  another tool around each scenario. Published under CC-BY-4.0.

### Changed

- **The record spec has its own home** (TY-12): `docs/DEED_SPEC.md` and `docs/BUNDLE_FORMAT.md`
  moved to `spec/v1/` and are published under CC-BY-4.0, with `spec/README.md` (editions,
  implementations, how to credit it) and `spec/CHANGELOG.md`. No normative change; the code stays
  Apache-2.0. The docs site pages keep their addresses.
- **The core knows no protocol** (TY-04): the Gate, the Witness and the deed code no longer import
  any connector, MCP or coding-agent hook package, and a test (`internal/archtest`) fails any change
  that adds one. The Postgres row read-back moved behind a connector interface; it records the same
  method (`readback.postgres.row`) and the same reasons as before.
- Dependency updates from Dependabot: `golang.org/x/text` 0.43.0, the Go base image, and package
  versions in the TypeScript SDK, the Python SDK, the Console, the browser verifier and the CI
  workflows.

### Fixed

- **A result that came back is no longer left to crash recovery when recording it meets
  contention.** Under load (many writes in one org at once) the transaction that records a gateway
  call's outcome and seals its deed could hit Postgres's `lock_timeout` or `statement_timeout`; the
  action then stayed `executing` until the sweeper claimed it, and the real outcome was lost. The Gate
  now runs that transaction again, with backoff, up to the call's deadline (`TestRecordContention`;
  `TestLoad1000` failed this way on CI runners).

## [0.4.0] - 2026-10-19

Healthcare, phase 1 complete: Proven (L2) checks for FHIR servers that store resources in Postgres
(HAPI FHIR JPA 8.x and Medplum 5.x). Deedglass reads the FHIR server's own change log and binds a
transaction to an action by the version the server reported to its write, never by a request id
alone (ADR-108). Writes no action made are stored and flagged `out_of_band`, and resource content is
kept only as keyed per-field digests. Attack cases I05-I08 now run and match, so all twelve FHIR
cases do. Still Checked (L1), not Proven: HAPI FHIR 7.x and older (its table layout isn't mapped)
and deletes on Medplum (Medplum's answer names no version to bind). No new dependency, one schema
migration (0032), and `formula_version` stays 1.

### Added

- **Proven (L2) for FHIR servers that store resources in Postgres** (HC-P05, ADR-108): a
  `fhir` connector with `witness.enabled` and `witness.server` (`hapi` for HAPI FHIR JPA 8.x,
  `medplum` for Medplum 5.x) and `witness_dsn` in its credential has the decoder read the FHIR
  server's own change log. A transaction is bound to an action by the version the server reported to
  its write (never by a request id alone), and judged: the reported version against what the agent
  sent (`version_matched`, `version_field_changed`), any other version in that transaction
  (`fhir_extra_versions`), content the change log can't carry (`large_object_not_replicated`), an
  unknown table layout (`fhir_schema_unknown`). Writes no action made are stored unattributed and
  flagged `out_of_band`; a reused request id is `request_id_reused` (a decoder alert, never bound).
  Resource content in frames is kept only as keyed field digests. Methods `l2.fhir.hapi.8` and
  `l2.fhir.medplum.5`. Attack cases I05-I08 now run and match; `make fhir-proven-it` runs HAPI and
  Medplum in Docker. Migration 0032.

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

[Unreleased]: https://github.com/deedglass/deedglass/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/deedglass/deedglass/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/deedglass/deedglass/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/deedglass/deedglass/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/deedglass/deedglass/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/deedglass/deedglass/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/deedglass/deedglass/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/deedglass/deedglass/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/deedglass/deedglass/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/deedglass/deedglass/releases/tag/v0.1.0
