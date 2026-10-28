# Changelog

Every notable change to Deedglass, newest first. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html). Release notes are generated from
this file by `scripts/release-notes.sh`.

## [0.15.1] - 2026-10-29

### Added

- The app's Connect screen (Connect v1): each card carries the tool's own mark, and Claude Desktop,
  Cursor, VS Code and Gemini CLI get a one-click **Connect** that finds the tool's MCP settings file
  on this computer, shows the one entry it will add, saves a copy of the file as `<file>.<time>.bak`,
  adds only that entry and offers **Undo** (`POST /v1/connect/{id}/install` and `/undo`; `install`
  on every Connect item; `internal/mcpinstall`). A new **Any MCP tool** card with the copyable
  address, **Test connection** on every AI tool, and a FHIR address form with **Try with sample
  data** (a public test server with made-up records) for the record system.

## [0.15.0] - 2026-10-28

Deedglass now opens in your browser with one command. Run `deedglass` (or `deedglass app`) in a
terminal and a signed-in app starts on this computer at port 4180, with its own private PostgreSQL
in your user folder: no Docker and no database to set up. A Connect screen links Claude Desktop,
Cursor, VS Code and other tools to it, and `deedglass help` now shows the eight everyday commands.
Existing records are unchanged: the deed format, the `scan` grade and `formula_version` (1) are as
in 0.14.1. One new Go dependency (`github.com/ulikunitz/xz`, ADR-120) and no schema migration.

### Added

- `deedglass app`, and plain `deedglass` in a terminal: Deedglass in your browser on this computer,
  signed in, with no Docker and no database to set up. It runs its own PostgreSQL in your user
  folder (an installed 16+ or a one-time pinned download of 18.6; ADR-120, new dependency
  `github.com/ulikunitz/xz`). `--stop`, `--status`, `--reset`. CLI commands reach the running app
  with no flags.
- The Console's first run and Connect screen read four new routes, answered only inside the app:
  `GET` and `PATCH /v1/local`, `GET /v1/connect` and `POST /v1/connect/{id}/start`. No link or
  config they return holds a key. `deedglass mcp serve --client <id>` uses the key Connect made.

### Changed

- `deedglass help` shows eight everyday commands; `deedglass help all` shows every command.
- `deedglass approve` and `deny` default `--as` to the owner inside the local app.
- The health pilot points at `deedglass app` instead of a Console link that led nowhere, and its
  next steps say `npx deedglass` when that is how it ran.
- The docs say Windows runs the native binary through npx; WSL isn't needed.
- README: a short project status (pre-1.0, one maintainer, free and self-hosted, what isn't there
  yet) and the licence in plain words. SECURITY.md: the security inbox note is gone (the address
  works), and the scope says the container image is private for now. The npm packages name their
  author, and `@deedglass/sdk` points at the right docs page.
- TRADEMARKS.md: forks are welcome under a new name; the Deedglass name and logo stay with the
  project.

### Fixed

- The Console's Approvals screen no longer asks for an approval again and again when the request is
  already past its time but not yet marked expired.
- `deedglass app` opens a browser only at a person's terminal; piped, in CI or with `--json` it prints
  the sign-in link (`link` in JSON). `approvals list` and `act` show full approval ids, so they can be
  pasted into `deedglass approve`.
- The docs' install page no longer scrolls sideways on phones.

## [0.14.1] - 2026-10-28

A small patch: three more commands the shell guard asks about, a fix for slow regular expressions in
the TypeScript SDK, docs install buttons that say what to do when the app isn't installed, and
dependency updates. Nothing changes for existing records: the deed format, the `scan` grade and
`formula_version` (1) are as in 0.14.0. No new Go dependency and no schema migration.

### Added

- The shell guard asks first about three more commands that delete data, each checked against its
  tool's own reference page: `docker compose down -v` (and `--volumes`, `docker-compose`, `podman
  compose`), which removes the project's volumes; `helm uninstall` (and its aliases `delete`, `del`,
  `un`), which removes every resource of the release; and `mongosh` or `mongo` with `--eval` code that
  calls `dropDatabase()` or a collection's `drop()`. `helm uninstall --dry-run` and `compose down`
  without `-v` still pass. (#122)

### Fixed

- TypeScript SDK (`@deedglass/sdk`): three regular expressions that could take polynomial time on
  crafted input (trimming a base URL's trailing slashes, spotting a denial's "Nothing was run."
  ending, and finding PEM markers) are now plain string code that runs in linear time, with a timing
  regression test. Found by GitHub code scanning (js/polynomial-redos). (#125)
- Docs, "Install in any client": Cursor's main button is its web install page; every app link
  (Cursor, VS Code, LM Studio, Goose) says it needs the app, links the download and shows a by-hand
  way to add the same server; Claude Desktop links the by-hand guide instead of a release file that
  isn't attached. Links that had moved now point at their new pages, including references in the
  shell-guard catalog. (#124)

### Changed

- Dependency updates from Dependabot: the Go build image, the distroless runtime image, GitHub
  Actions, and npm and Python packages (#108, #109, #110, #112, #116).

## [0.14.0] - 2026-10-27

One command to see it work, and a live view of your agents. `deedglass start` asks nothing: with
Docker it sets up the local stack with every default and runs the trigger scene on it, ending on
the catch (the agent reported 1 row, Postgres changed 301) and the Console's address; without
Docker it plays the same scene inside the binary, each line labelled live or replay. The Console's
Overview is now a live view: what needs you, today's counts, and each action as it happens. `scan`
now grades three risks it used to score as A. Nothing changes for existing records: the deed format
is unchanged, and Proven, Checked and Claimed mean what they meant in 0.13.0. No new Go dependency
and no schema migration. `formula_version` stays 1; the formula is unchanged, and the three new
findings cost points under it, so some configs that graded A in 0.13.0 grade lower now.

Known limit: `deedglass start`'s Docker path reuses `deedglass init` and `init --trigger` unchanged,
but it was not run end to end on Docker for this release (the release machine had no working
Docker). The no-Docker path was tested from a fresh install.

### Added

- The Console's Overview is now a live view. **Needs you** at the top lists pending approvals with
  big Approve and Decline buttons (through the DecisionSheet) and four-eyes progress. **Today**
  counts Allowed, Held for a person, Blocked and Doesn't match. **Live activity** shows each action
  as it happens, with a plain summary, the rule decision, the read-back result and a link to the
  sealed record. **Agents connected** marks agents active in the last 5 minutes. While the event
  stream isn't live, the page polls every 5 s.
- An org with no actions gets one next step (`deedglass init --smoke` in another terminal) and turns
  into the live view when the first action arrives.
- Action pages put what the agent claimed beside what Deedglass observed, above the diff rows, with
  the policy that decided, the record's chain position and the `deedglass verify` command to copy.
- Health mode in the Console: with PHI mode on, the Console shows field names, not values.
- `GET /v1/overview` adds `decisions.allowed` and `decisions.held` (additive).
- `deedglass serve` prints `✓ Console on <base URL>/app` at start.
- `deedglass start`: the one first command. It asks nothing. With Docker running it runs
  `deedglass init` with every default (free ports picked when 8080, 55432 or 55442 are taken) and then
  the trigger scene on the same stack, so the first run ends on the catch: the agent reports 1 row,
  Postgres changed 301, and the undo preview. It prints where the Console is when the server
  answers. Without Docker, or with under 2 GiB of disk, it plays the same scene inside the binary,
  each line labelled `live` or `replay`, ends on the catch, and says how to get the live version.
  `--json` prints one `start_result` object; `--fast` skips the pauses.
- `deedglass scan` grades three risks that used to leave a config at A (#120), each a high finding:
  `database_server_not_read_only` (a database MCP server with no read-only setting, not routed
  through Deedglass; low when the checked role can't write), `plaintext_secret_in_config` (a
  real-looking credential in a server's env, headers, arguments or URL, named by where, never by
  value) and `shell_no_guard_hook` (blanket shell access in Claude Code with no `PreToolUse` hook for
  Bash and no ask or deny rule on Bash). The scan's JSON gains `database`, `read_only` and
  `plaintext_secrets` on each server (additive).

### Changed

- Bare `deedglass` prints a five-line welcome (what Deedglass is, then `start`, `protect` and
  `health pilot`) instead of the 73-line help when piped, and instead of the orb banner on a
  terminal. `deedglass help` is unchanged; `deedglass -q` still prints it. `deedglass version`
  keeps the orb.
- Every first-touch command ends with a `Next` block in the CLI style: `play`'s closing commands
  follow how the binary was run (no more `npx` after a curl install) and lead with `start`;
  `health pilot` and `scan` gained a `Next` block.
- The installer's last line is `next  deedglass start`, not `deedglass version`.
- `init` without Docker says that `deedglass start` shows the same scene offline.
- `deedglass check` and `test` suggest `doctor` and `verify`; `quickstart`, `demo`, `try`,
  `setup`, `begin` and `tour` suggest `start`.
- `health pilot`'s header says "about 15 minutes to read", since the run itself takes under a second.

### Fixed

- `deedglass health --help`, `keys --help` and every other command group with `--help` print the
  group's help instead of "needs a subcommand" (#120). A bad flag prints as `--flag`.
- The Console's footer shows the server's release version (it said v0.1.0).
- Links the CLI prints that went nowhere: docs.deedglass.com/intents, /decoder and
  /postgres-deployment now redirect to their pages, and the support link points at SUPPORT.md (#120).
- `deedglass health pilot --print-review-pack` says which release it is true for (0.14.0; it said
  0.12.0).
- Attack case P10 keeps each round's calls going past stop + 2 s, so a slow stop can't leave a round
  with nothing to test (#120). Its expected outcome is unchanged.
- The Actions feed's phone cards (under 640 px) no longer stack one letter per line, and verdict
  chips in the feed are no longer cut off.

## [0.13.0] - 2026-10-27

The hospital pilot in one command. `npx deedglass health pilot` runs four scenes on synthetic FHIR R4
data on your own machine, with no Docker and no server: a write whose read-back matches, a
`201 Created` whose stored value differs, a high-risk order held for a clinician, and sealed records
checked offline. It writes a report to forward and the hospital IT review pack. This release also
carries the AAP-H v0.1 draft spec. Nothing changes for existing records: the deed format is
unchanged, and Proven, Checked and Claimed mean what they meant in 0.12.0. No new Go dependency, no
schema migration, and `formula_version` stays 1.

### Added

- `deedglass health pilot`: the 15-minute hospital pilot on synthetic FHIR R4 data, with no Docker
  and no server. Four scenes run through the product's own code with the `healthcare-fhir` pack
  (`india-health` with `--region india`), PHI mode and lockdown on: a write whose read-back matches,
  a `201 Created` whose stored value differs, a high-risk order held for a clinician, and sealed
  records checked offline with one changed byte caught. It writes `REPORT.md`, the IT review pack,
  `deeds.jsonl` and `keys.json`. `--fhir-url` sends scene 1 to your own FHIR sandbox, `--json` prints
  the result, and `--print-review-pack` prints the pack. See `docs/healthcare/PILOT.md`.
- `docs/healthcare/IT_REVIEW_PACK.md`: the hospital IT review pack (architecture and data flow,
  where data lives, threat model in summary, security answers, the clinical-safety boundary,
  operations, how a pilot is measured), embedded in the binary and on docs.deedglass.com.
- `spec/aap-h/`: Agent Action Provenance for Health (AAP-H) v0.1.0-draft, an open draft for recording
  AI agent actions on health data (FHIR R4 Provenance and AuditEvent profiles, a JSON envelope,
  offline verification rules, conformance levels L1 to L3, synthetic NHCX and Da Vinci PAS examples).
  Its test converts every action deed in `testdata/deeds/` to the envelope and checks it against the
  sealed deed. Text CC-BY-4.0, schemas Apache-2.0. No product behaviour changes.

## [0.12.0] - 2026-10-26

One command to guard every coding agent. `npx deedglass protect` finds Claude Code, Cursor, Codex
CLI, Gemini CLI and GitHub Copilot CLI in your home directory, shows one plan, and adds the shell
guard to each after you say yes, with a backup and `--undo`. The guard's catalog grows by 17 rows for
cloud, infrastructure and hosted-database CLIs (Railway, Supabase, Neon, Terraform, Pulumi, AWS,
gcloud, kubectl, Docker, Fly, PlanetScale, Turso), and the Claude Code plugin now asks before a
risky shell command by default. The scan and the shell guard also build for the browser
(WebAssembly), which runs the free checker at deedglass.com/check. Nothing changes for existing
records: the deed format is unchanged, and Proven, Checked and Claimed mean what they meant in
0.11.0. No new Go dependency, no schema migration, and `formula_version` stays 1.

### Added

- `deedglass protect` (also `npx deedglass protect`): one command adds the shell guard to every
  coding agent it finds in your home directory (Claude Code, Cursor, Codex CLI, Gemini CLI, GitHub
  Copilot CLI), from the same hook configs `guard shell --print-config` prints. It prints one plan
  and writes only after `[y/N]` or `--yes`; `--dry-run` writes nothing; it merges into the JSON
  already there, keeps your own hooks and settings in their order, adds nothing to a file that
  already runs the guard, and leaves alone a file it can't read as plain JSON. Every file is backed
  up to `~/.deedglass/protect` first, and `--undo` puts each one back byte for byte (or, if you
  edited it since, takes out only the guard). `--client`, `--project`, `--hook-command` and `--json`.
  The hook starts deedglass by an absolute path, or with `npx -y deedglass@<version>` after an npx
  run, so it keeps working after protect ends. After a run that writes, it prints two optional lines:
  a README badge ("Agents protected by Deedglass", linking to deedglass.com/check) and a share hint;
  `--json` carries it as `badge`. `docs/protect.md`.
- 17 shell guard catalog rows for cloud, infrastructure and hosted-database CLIs, each checked
  against its CLI's reference page on 2026-10-26: `railway volume delete`, `railway delete`, a
  `curl` to Railway's GraphQL API with `volumeDelete` or `projectDelete`, `supabase projects delete`,
  `neonctl branches delete` and `projects delete`, `terraform destroy` and `apply -destroy`,
  `pulumi destroy`, `aws rds delete-db-instance` and `delete-db-cluster`, `aws s3 rb --force`,
  `aws s3 rm --recursive`, `gcloud sql instances delete`, `kubectl delete namespace | pvc | pv`,
  `docker volume rm | remove | prune`, `fly volumes destroy`, `pscale database delete` and
  `turso db destroy`. Their safe neighbours pass (`terraform plan -destroy`, `pulumi destroy
  --preview-only`, `aws s3 rm --recursive --dryrun`, `kubectl get ns`, `kubectl delete pod`,
  `supabase db reset` on the local stack). `railway down` is not in the catalog: Railway's reference
  says it removes the latest deployment, not data.
- `cmd/deedglass-wasm`: the scan and the shell guard as one WebAssembly file for
  deedglass.com/check (`make wasm`, 1.9 MB gzipped; CI fails it over 4 MB). Parity tests compare it
  with `deedglass scan` and `deedglass guard shell` on the same inputs.
- The README links the GitHub Action on the Marketplace:
  https://github.com/marketplace/actions/deedglass-scan.

### Changed

- The Claude Code plugin's shell guard is on (`ask`) by default. `DEEDGLASS_SHELL_GUARD=off` (or
  `0`, `false`, `no`) turns it off and `deny` makes it deny. Left unset with no `deedglass` on
  `PATH`, it answers nothing and says nothing, so installing the plugin without the binary changes
  nothing.

### Known limits

- `protect` reads plain JSON only: a settings file with comments is skipped, with the reason in the
  plan. It rewrites a file it changes with the same keys in the same order and the file's own indent,
  so a compact one-line file comes back indented.
- No attack-suite case was added: attack-group digests change only with a founder-approved ADR.

## [0.11.0] - 2026-10-26

A calmer terminal. Every command now builds its output from one style package
(`internal/cli/ui`, rules in `docs/design/CLI_STYLE.md`): the summary comes first, key/value rows
line up and wrap at 80 columns, and `deedglass play` tells the demo as six short scenes. At 80
columns `help` drops from 94 to 71 lines, `play` from 81 to 60, and the widest `play` line from 149
to 80 columns. Behaviour is unchanged and `--json` output is unchanged, so scripts that read JSON
need nothing. Text output did change: a script that parses the human-readable lines (for example
full ids, now shortened to `act_01m4x8kq…`) should move to `--json`. Nothing changes for existing
records: the deed format is unchanged, and Proven, Checked and Claimed mean what they meant in
0.10.0. Known limits: commands outside the list below keep their older layout until they are
reworked; the copy at deedglass.com/dl is refreshed by a site deploy, so for a short time after a
release it serves the previous version. No new Go dependency, no schema migration, and
`formula_version` stays 1.

### Changed

- The terminal output has a new layout: one blank line between blocks, aligned key/value rows that
  wrap under themselves at 80 columns, dim keys and meta, and fewer `a · b · c` chains. Every
  command builds its text from one package, `internal/cli/ui`, and the rules are in
  `docs/design/CLI_STYLE.md`. `--json` output is unchanged.
- `deedglass play` reads as six short scenes, each with a before, a check and a result, and every
  row says whether it ran here (`live`) or came from the recorded run (`replay`). `-v` adds the
  recorded lines exactly as they printed.
- `deedglass init` puts the smoke test under its own heading and shows three rows of a long diff
  (`-v` shows them all). A run with nothing to change prints a short summary instead of every step.
- `deedglass help` lists one line per command; `deedglass help <command>` and `-v` list the
  subcommands. `scan`, `verify`, `keys list`, `lockdown status`, `actions list` and `actions show`
  follow the same layout, and ids in text are shortened to `act_01m4x8kq…`.
- Pending (`◐`) uses the warn colour, yellow, instead of magenta: the palette is the accent teal,
  yellow, red, dim and bold.
- `DEEDGLASS_ASCII=1` (and `TERM=dumb`) draws the status glyphs as ASCII stand-ins on a terminal.

## [0.10.0] - 2026-10-25

Free while we grow, India's national FHIR profiles, and a faster install. Every feature now runs
with no licence key (DL-131, ADR-118): the full scan report, every policy pack, the one-step session
undo, the security review pack and the agency view. Nothing reads a licence file or calls out, and
the licence commands stay for a later paid option. The FHIR connector reads ABDM health records and
NHCX bundles from what they hold, in the terms of the NRCeS FHIR IG for ABDM 6.5.0 (ADR-119), and the
free `india-health` pack holds NHCX claims by use and amount. Attack group S (S01 to S12) runs
against it. `curl -fsSL https://deedglass.com/install.sh | sh` downloads from deedglass.com first
(6.6-7.3 s on the network where GitHub took 81-119 s), with the same sha256 and signature checks.
Nothing changes for existing records: the deed format is unchanged, and Proven, Checked and Claimed
mean what they meant in 0.9.0. Known limits: the scan Action's pull request comment on private
repositories still asks for a licence key until the Action's next release; the India tables follow
one guide version (6.5.0) and a newer guide needs a code change; the copy at deedglass.com/dl is
refreshed by a site deploy, so for a short time after a release it serves the previous version. No
new Go dependency, no schema migration, and `formula_version` stays 1.

### Added

- India's national FHIR profiles (HC-P15, ADR-119). The FHIR connector reads a create or update's
  `meta.profile` and what a Bundle holds, in the terms of the NRCeS FHIR IG for ABDM 6.5.0 (which
  carries the NHCX profiles), and adds `request.fhir.bundle_type`, `entry_types`, `record`,
  `record_status`, `nhcx`, `profile_mismatch`, `claims`, `claim_use`, `claim_uses`, `claim_amount`
  and `claim_currency`. ABDM records (OPConsultRecord, DischargeSummaryRecord and six more) and NHCX
  bundles (ClaimBundle, CoverageEligibilityRequestBundle and four more) are named from what they hold;
  a label that doesn't match is reported. The guide's 41 published examples (CC0-1.0) are test
  fixtures, each classified as the guide names it.
- `org.phi_mode` in policy facts: the org's PHI mode at decision time.
- The free `india-health` policy pack: PHI mode first, the healthcare-fhir rules, ABDM records that
  wait for a clinician unless they are drafts, bundles carrying orders that wait for a clinician, and
  NHCX claims by use and amount (a final claim or a pre-authorisation of INR 1,00,000 or more needs
  two people; a smaller pre-authorisation or a pre-determination needs one), the same on a plain FHIR
  Claim and on an NHCX bundle. 39 cases. Guide: `docs/healthcare/INDIA.md`.
- Attack group S (S01 to S12): NHCX and ABDM requests built from the IG's examples, relabelled,
  stripped, nested and hidden, through the real connector and the embedded packs.

### Changed

- `scripts/install.sh` reads the version from `https://deedglass.com/dl/latest` and downloads the
  checksums, the signature bundle and the archive from `https://deedglass.com/dl/vX.Y.Z/`, a
  byte-for-byte copy made when the site deploys; any file the copy lacks comes from the GitHub
  release. Every download has a 5 s connect timeout, a stall limit and two retries. The checks don't
  change. `DEEDGLASS_MIRROR_URL` overrides the copy (`none` means GitHub only). (#94, #95)
- The README opens with one line on what Deedglass does, the recorded demo (dark and light), install,
  a 60-second try-it, who it's for, how it works and the benchmarks with their misses; the repository
  description, social preview, contributing guide and issue forms are new. (#96)
- `healthcare-fhir` 1.1.0: `claims-four-eyes` also holds a Bundle that carries a Claim or another
  billing record, such as an NHCX ClaimBundle (before, a ClaimBundle created as a Bundle was read
  back and allowed).
- Every feature is free while we grow (DL-131, ADR-118). `deedglass scan --full`, every policy pack
  (including `money-movement`), the one-step session undo (`deedglass observe undo`), the security
  review pack (`deedglass security-pack build`, `POST /v1/security_pack`) and the agency view
  (`deedglass clients`, `GET /v1/clients`, `report --all-clients` and `--brand`) now run with no
  licence key. Nothing reads a licence file or calls Dodo Payments or deedglass.com, and `serve` no
  longer re-checks a licence each day. One policy in `internal/licence/policy.go` decides this; the
  licence commands and token format stay for a later paid option. `ee/` stays under FSL-1.1-ALv2.
- Prices, plan names and buy links are gone from the CLI, the Console and the docs: the "(Pro)"
  labels, the price on the Console's Pro sheets and the refusal lines that pointed at deedglass.com/pro and
  deedglass.com/agencies. `pro_required` stays in the error catalog for a later build that asks for a
  key; no current build answers it.

## [0.9.0] - 2026-10-25

Lockdown mode, and a terminal that says what happened and what to do next. One switch,
`lockdown: true` or `deedglass lockdown on`, gives the strictest setup for hospitals and security
teams (ADR-117): every write waits for a person, high-risk writes need two different people,
approvals expire within 15 minutes, a Can't check result holds the agent's writes until a person
acknowledges it, destructive SQL never runs, and turning it off needs a second person, both steps
sealed. Attack group M (M01–M14) runs against it. The CLI now follows one style: errors read as what
happened, why, and a `next` line; no raw Go errors reach the terminal; help fits 80 columns; `init`
draws progress lines instead of going silent during the image pull; and the DB-direct commands find
the local stack. `install.sh` is a file on each release from this one on. Nothing changes for
existing records: the deed format is unchanged, and Proven, Checked and Claimed mean what they meant
in 0.8.0. Known limits: the server CLI can't tell who is typing, so the second person for
`lockdown off` is a second owner or admin account; there is no Console screen for lockdown; the SQL
rules cover the native Postgres connector only. No new Go dependency, no schema migration, and
`formula_version` stays 1.

### Added

- **Lockdown mode** (ADR-117, [docs/LOCKDOWN.md](docs/LOCKDOWN.md)): one switch for the strictest
  setup, `lockdown: true` in the config or `deedglass lockdown on` for one org. Every write waits
  for a person, high-risk writes need two different people, approvals expire within 15 minutes,
  writes are checked at Proven where the connector reaches it (the record flags a write that can
  only reach Checked), a Can't check result holds the agent's writes until a person runs
  `deedglass lockdown ack`, DROP, TRUNCATE, ALTER and unbounded UPDATE or DELETE never run, each
  agent reaches the one connector its `connector` label names, and FHIR calls need PHI mode. Prod
  refuses a lockdown config without an ML-DSA-65 seal key or with `timestamps.at_export: false`.
  Turning it off needs a second person (`deedglass lockdown off`, then `--countersign`), both sealed.
  `deedglass init --lockdown`, a `lockdown` doctor check, `lockdown` and `phi_mode` in
  `GET /v1/whoami`'s org, and attack group M (M01–M14). No new dependency and no schema migration.
- `install.sh` ships as a file on each release (`release.extra_files`):
  `curl -fsSL https://github.com/deedglass/deedglass/releases/latest/download/install.sh | sh`.
  Through `raw.githubusercontent.com` the install took 83–119 s on one network; the release
  download took about 5 s.

### Changed

- **One CLI style** (`docs/COPY_CATALOG.md` › CLI style): errors are a capitalized sentence,
  optional detail lines, then `next  <what to do>`. Go and library text that leaked through
  wrapped errors (missing files, refused connections, the DSN parser, JSON and number parsing) is
  rewritten in plain words; `TestNoRawGoErrors` runs 22 failing commands in text and `--json`.
- `help` wraps to the terminal width (at most 100 columns, 80 when unknown); it was 169 columns.
- `init` and `init --trigger` draw a progress line for a step still running after 0.4 s; off a
  terminal nothing is drawn. A cold `init` used to be silent for about 55 s during the image pull.
- `keys list`, `org keys list`, `vault status`, `migrate status`, `approve`/`deny`,
  `insights detect`/`digest` and suggestions read `.deedglass/stack.json`, so they work inside the
  quickstart folder without `DEEDGLASS_DATABASE_URL`.
- `report --json` prints JSON (it printed Markdown); `approvals list` has a header row.
- Docs site and README for search engines and AI answers (#91): each docs page opens with its own
  words (the imported-file note moves to the end), step-by-step pages carry HowTo JSON-LD, and
  `check-seo` checks canonicals, HowTo anchors and openings.

## [0.8.0] - 2026-10-25

Your own Postgres in one command, and MCP tool names the Anthropic and OpenAI APIs accept. `deedglass connect
<postgres-url>` tells Supabase, Neon and plain Postgres apart, runs read-only checks and prints one
plan; `--apply` sets up the connector, the starter policy and an agent, and the URL's password is
never printed or stored (ADR-115). A new stack starts on `postgres-check`, which checks every write
at Proven instead of blocking it. Every client command now finds the server `deedglass init` set up,
on any port. MCP tools are listed as `postgres_query` and `deedglass_check_approval`, names the
Anthropic and OpenAI APIs accept, so framework recipes no longer rename them; the dotted names still
work on `tools/call` and stay the names in policies, records and the REST API. Recipes for nine
agent frameworks, a FHIR write-integrity benchmark on Medplum, and security hardening: the A2A proxy
refuses what a web page can send and now needs the agent's key as a bearer token (a change for A2A
clients), `install.sh` checks the release signature, and attack group T (ADR-116) runs. Nothing
changes for existing records: the deed format is unchanged, and Proven, Checked and Claimed mean
what they meant in 0.7.0. Known limits: no live model run backs the recipes (there is no model key in
CI); a long connector name plus a long tool name can pass OpenAI's 64-character limit; the FHIR
benchmark covers Medplum only, and Deedglass caught 4 of its 11 diverging scenarios. No new Go
dependency, no schema migration, and `formula_version` stays 1.

### Added

- `deedglass connect <postgres-url>`: your own Postgres behind Deedglass in one command
  (adoption-v1 item 2). It tells Supabase, Neon and plain Postgres apart, warns when the URL goes
  through a pooler (and prints the direct host), runs read-only checks on a READ ONLY session
  (version, `wal_level`, replication slots, whether the user can create roles, which tables lack
  `REPLICA IDENTITY FULL`) and prints one plan. `--apply` creates the connector, runs
  `connector postgres init` (the roles' DSNs go to the vault), pushes and enforces the starter policy
  scoped to the connector's tools, creates an agent with a key in a 0600 file, sends one read through
  the Gate and prints the MCP line. A remote database also needs `--confirm <database>`. The URL's
  password is never printed or stored. The Supabase and Neon guides are rewritten around it.
- `postgres-check`, a starter policy and policy pack for trying Deedglass out: reads and writes run,
  every write is checked at Proven, `DROP` and `TRUNCATE` never run, an undo still waits for a
  person. `deedglass init` enforces it by default on a new stack (`--policy postgres-safe` for
  production behaviour; a stack that already enforces either keeps it).
- `deedglass approvals open <apr_…>`: in dev, a fresh one-time approve link for that approval
  (printed and opened); in prod, its Console page. `undo run` prints the link for the approval it
  just created, so the quickstart's undo no longer sends you to `.deedglass/serve.log`.
- `deedglass init --purge` removes the stack's containers, volumes and `.deedglass/` files.
- `init` points `~/.deedglass/current.json` (0600; the server URL and key file paths, never a key)
  at its stack, and `mcp serve` falls back to it, so one Claude Code entry at user scope works in
  every project. `DEEDGLASS_API_KEY_FILE` names a key file for `mcp serve`.
- **A FHIR write-integrity benchmark** (`make fhir-bench`, `corpus/fhir/BENCHMARK.md`): 17
  Medplum scenarios (11 that diverge, 6 controls) where a FHIR write is answered 200/201 while
  Medplum stores something other than what the agent reports. Facts come from Medplum's own history
  tables. Deedglass caught 4 of 11 (1 before, 3 after), 0 false alarms; trusting the HTTP status
  caught 0. The 7 misses are listed. Synthetic data only; not in CI, `TestGoldenReplay` replays the
  committed run.
- **Agent framework recipes** (`docs/integrations/`, the docs site's "Agent frameworks" group): the
  Vercel AI SDK, Claude Agent SDK, OpenAI Agents SDK, LangChain, Mastra, Pydantic AI, Google ADK,
  CrewAI and LlamaIndex, each a runnable file of 15 lines or fewer with pinned packages in
  `examples/frameworks/` and a `check` that needs no model key (weekly workflow, not required).
- `glama.json` at the repository root, and `release.yml` publishes `server.json` to the MCP
  Registry after npm on every stable tag.

### Changed

- **MCP tool names are API-safe by default.** `tools/list` now shows `postgres_query`,
  `postgres_execute`, `fake_echo` and `deedglass_check_approval` instead of the dotted names, which
  the Anthropic Messages API (`^[a-zA-Z0-9_-]{1,128}$`) and OpenAI function calling (the same
  characters, at most 64) refuse. The Claude Agent SDK, the OpenAI Agents SDK, LangChain, LlamaIndex,
  Mastra, Pydantic AI and the Vercel AI SDK recipes no longer rename tools, and the OpenAI Agents
  recipes no longer need `include_server_in_tool_names`.
- Nothing else changes: the dotted name stays the tool's name in policies, actions, deeds and the
  REST API (`POST /v1/actions` still takes only `postgres.query`). A call by either spelling
  (`tools/call`, `policy evaluate --tool`, policy test cases) reaches the same rule and seals
  `postgres.query`, so existing policy files keep working. A server tool named `deedglass_*` is now hidden, like `deedglass.*`.
- **Deprecated:** the dotted names on `tools/call`, still accepted and logged
  (`deprecated dotted tool name`). For the old listing, set `mcp.tool_names: dotted`, or pass
  `--tool-names dotted` to `deedglass mcp serve` (it sends `Deedglass-Tool-Names`) or
  `deedglass mcp wrap`. See [MCP.md](docs/MCP.md#tool-names).
- The Claude Code plugin pins its server to `npx -y deedglass@<version> mcp serve` (Anthropic's
  plugin directory refuses an unpinned npx command). `scripts/pin-plugin.sh <version>` is a release
  step, and `scripts/check-versions.sh` and `make listings-check` fail when the pin drifts.
- One Claude Code line everywhere: `claude mcp add --scope user deedglass -- npx -y deedglass mcp
  serve` in the README, the install links and `init`'s Next block (no key in Claude Code's config).
- `Next` lines print `npx deedglass …` when the CLI runs under npx, where `deedglass` isn't on
  `PATH`. `init`'s Next block no longer asks you to export the admin key: commands in the init
  directory find it.
- `init --trigger` says what the legacy trigger ships: the next 300 orders not shipped yet,
  cancelled ones included (it said "open").
- The README documents `--listen` and `--pg-port`.

### Fixed

- One server resolver for every client command (adoption-v1 F2): `--server`, `DEEDGLASS_SERVER`,
  `DEEDGLASS_BASE_URL`, the `.deedglass/stack.json` that `deedglass init` wrote in this directory or a
  parent, then `http://127.0.0.1:8080`. After `init --listen 127.0.0.1:18090`, `undo run`,
  `approvals`, `agent`, `connector`, `act` and every other API command now reach `:18090`, with the
  admin key `init` saved; before, some went to `:8080` with whatever key was in `DEEDGLASS_API_KEY`.
  Inside an init directory no key is sent to a server the environment names that differs from the
  stack's (new CLI code `server_mismatch`); only `--server` sends it elsewhere. `mcp serve` and
  `mcp wrap` hold keys back the same way.

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

[Unreleased]: https://github.com/deedglass/deedglass/compare/v0.14.0...HEAD
[0.14.0]: https://github.com/deedglass/deedglass/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/deedglass/deedglass/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/deedglass/deedglass/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/deedglass/deedglass/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/deedglass/deedglass/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/deedglass/deedglass/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/deedglass/deedglass/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/deedglass/deedglass/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/deedglass/deedglass/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/deedglass/deedglass/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/deedglass/deedglass/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/deedglass/deedglass/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/deedglass/deedglass/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/deedglass/deedglass/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/deedglass/deedglass/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/deedglass/deedglass/releases/tag/v0.1.0
