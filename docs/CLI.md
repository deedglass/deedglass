# The deedglass CLI

`deedglass` is one binary: the server (`deedglass serve`, from S03), the operator tools and the
client. This page covers how it behaves. `deedglass help` is always the source for what this build
can do.

## Principles

- **Calm and exact.** Short sentences, sentence case, no exclamation marks, no emoji.
- **Every error ends with a next step.**
- **Honest about what exists.** Every command in the contract is listed. A command that isn't built
  yet says so and exits 2; it never pretends to work.
- **Scriptable.** `--json` on every command, stable exit codes, colour only on a terminal.

## Global flags

Accepted anywhere before `--`, in any order:

| Flag | What it does |
|---|---|
| `--json` | One JSON object on stdout. Errors go to stderr in the API error shape. |
| `--server <url>` | The Deedglass server for client commands (or `DEEDGLASS_SERVER`). |
| `--api-key <key>` | The API key for client commands (or `DEEDGLASS_API_KEY`). Prefer the variable: flags show up in shell history. |
| `--org <id>` | The org, for people who belong to several. |
| `--no-color` | No colour, even on a terminal. |
| `-v`, `-vv` | More detail. |
| `-h`, `--help` | Help for the command it follows. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | Failure, or invalid input or config |
| 2 | Usage error, unknown command, not built yet, or cannot determine |
| 3 | Not authenticated |

## Output and style

- **Symbols:** `✓` verified or valid (teal), `✕` mismatch, denied or error (red), `◐` pending
  (violet), `○` unverifiable or a notice (amber), `·` separators, `—` not set. Colour only repeats
  what the symbol and text already say.
- **Colour** only when stdout is a terminal, `NO_COLOR` is unset, `TERM` isn't `dumb`, and
  `--no-color` isn't given. It uses the 16 ANSI colours, so it follows your terminal theme.
- **Secrets** never print. `config print` shows `••••••••` for every secret and only the password
  of the database URL is hidden.

## JSON mode

With `--json`, a command writes one JSON object to stdout, with an `object` field, like the API.
Errors go to stderr in the API's error shape:

```json
{"error":{"type":"invalid_request_error","code":"not_built","message":"deedglass deeds export isn't in this build yet. Run deedglass help to see what this build can do.","doc_url":"https://docs.deedglass.com/errors/not_built"}}
```

CLI error codes so far: `unknown_command`, `usage_error`, `not_built`, `config_invalid` (with a
`problems` list), and `failed`; `deedglass verify` adds `input_unreadable`, `input_too_large`,
`keys_unreadable`, `keys_invalid`, `keys_unreachable` and `keys_conflict`. Its own answer is not an
error: `--json` prints the `verification_report` on stdout whatever the result.

## Banner and quiet mode

![The deedglass banner in Terminal.app, dark theme](images/cli-banner-dark.jpg)

The Deedglass mark, drawn in the terminal: the Split Orb in half-block pixels, a calm welcome and
one next step. It appears in exactly three places, and only on an interactive terminal:

| Where | What |
|---|---|
| `deedglass` (no command) | the banner, then `Run deedglass init to connect your first agent.` |
| `deedglass version`, `deedglass --version` | the banner, with commit, build date and platform as its last lines |
| `deedglass help` | the inline mark `◖│◗ deedglass` as the header line |

`init` and `doctor` will use the same mark from S32 (`internal/cli/mark`: `Banner`, `Inline`,
`Sealed`). Every other command stays quiet.

**It never shows** (bare `deedglass` prints the plain help instead, and `version` its plain line):

| Switch | Effect |
|---|---|
| stdout is not a terminal (a pipe, a file, `/dev/null`) | no banner, no inline mark, no escape codes, even when stderr is a terminal |
| `--json` | unchanged JSON, byte for byte |
| `CI` set (`true`, `1`, …; not `0`/`false`) | no banner |
| `DEEDGLASS_NO_BANNER=1` | no banner and no inline mark |
| `-q` / `--quiet` (bare `deedglass`, `version`) | no banner |

**How it looks:**

| Switch | Effect |
|---|---|
| `COLORTERM=truecolor` or `24bit` | 24-bit colour |
| `TERM=*256color` | the nearest xterm-256 colours (chosen by ΔE in OKLab when the grid is generated) |
| any other colour terminal | the 16 ANSI colours |
| `NO_COLOR` (non-empty) or `--no-color` | a monochrome orb in shades (`▓ │ ░`), no escape codes |
| `TERM=dumb`; the Windows console outside Windows Terminal | ASCII only: `(\|) deedglass` and the text |
| narrower than 60 columns | the inline mark and the text, no orb; under 40, the mark and one line |
| `DEEDGLASS_THEME=light` / `dark` / `auto` | the palette for a light or dark background; `auto` reads `COLORFGBG`, else dark |
| `DEEDGLASS_NO_ANIM=1` | the Sealed moment at the end of `init` (S32) shows only its final frame |

On a light theme the right half turns to light glass and the pane takes your terminal's foreground
colour:

![The banner on a light Terminal.app theme](images/cli-banner-light.jpg)

The banner is decoration, never a dependency: if detection fails it falls back to plain output,
and it can never change an exit code. Scripts and CI never see it.

Notes: Terminal.app renders 24-bit colour but doesn't set `COLORTERM`, so it gets the 256-colour
orb unless you export `COLORTERM=truecolor`. The terminal isn't asked for its background colour
(OSC 11) in this build; set `DEEDGLASS_THEME=light` on a light terminal that doesn't set
`COLORFGBG`.

## Examples

```
$ deedglass polcy lint
✕ Unknown command "polcy". Did you mean "policy"?
  Run deedglass help to see every command.

$ deedglass verify deeds.jsonl --keys https://api.deedglass.com/.well-known/deedglass/keys.json
✓ 1,204 deeds · chain intact · 2 seal keys · 2026-10-01 → 2026-10-02

$ deedglass deeds export
deedglass deeds export isn't in this build yet.
Run deedglass help to see what this build can do.

$ deedglass executor serve
✕ "executor serve" was a V0 command. Its successor is deedglass serve (S03).
  See docs/NAMING.md for every renamed command.
```

## Commands

`soon (Snn)` means the command is in the contract but not in this build; Snn is the session that
builds it. Hidden commands work but aren't in `deedglass help`.

<!-- commands:begin (generated from cmd/deedglass/registry.go; go test ./cmd/deedglass -run TestDocsTree -update) -->
| Command | Group | What it does | Status |
|---|---|---|---|
| `deedglass init` | Setup | Set up Deedglass in this directory | soon (S32) |
| `deedglass doctor` | Setup | Check config, database and keys | soon (S32) |
| `deedglass serve [--migrate]` | Setup | Run the API, the Gate and the Console | built |
| `deedglass migrate up [--database-url <url>] [--dry-run] [--json]` | Setup | Apply database migrations | built |
| `deedglass migrate status [--database-url <url>] [--json]` | Setup | Show applied and pending migrations | built |
| `deedglass migrate verify [--database-url <url>] [--json]` | Setup | Check applied migrations against this build | built |
| `deedglass config print [--json]` | Setup | Show the effective config, secrets hidden | built |
| `deedglass version [--json] [--check]` | Setup | Show the client and server versions | built |
| `deedglass keys gen [--dir <dir>] [--json]` | Setup | Generate a seal key | built |
| `deedglass keys rotate [--overlap 5m] [--dir <dir>] [--json]` | Setup | Rotate the active seal key | built |
| `deedglass keys list [--server <url>] [--json]` | Setup | List seal keys | built |
| `deedglass vault status` | Setup | Show the credential vault's keys | soon (S16) |
| `deedglass vault rotate` | Setup | Re-encrypt credentials with a new key | soon (S16) |
| `deedglass help [<command>] [--json]` | Setup | Show help for deedglass or one command | built, hidden |
| `deedglass completion zsh` | Setup | Print zsh completion | soon (S32) |
| `deedglass completion bash` | Setup | Print bash completion | soon (S32) |
| `deedglass completion fish` | Setup | Print fish completion | soon (S32) |
| `deedglass agent create` | Agents | Register an agent | soon (S08) |
| `deedglass agent list` | Agents | List agents | soon (S08) |
| `deedglass agent show` | Agents | Show one agent | soon (S08) |
| `deedglass agent suspend` | Agents | Suspend an agent | soon (S08) |
| `deedglass agent resume` | Agents | Resume a suspended agent | soon (S08) |
| `deedglass agent stop` | Agents | Stop an agent now | soon (S08) |
| `deedglass agent start` | Agents | Start a stopped agent | soon (S08) |
| `deedglass agent keys create` | Agents | Create an API key for an agent | soon (S08) |
| `deedglass agent keys list` | Agents | List an agent's API keys | soon (S08) |
| `deedglass agent keys roll` | Agents | Replace an API key, with an overlap | soon (S08) |
| `deedglass agent keys revoke` | Agents | Revoke an API key | soon (S08) |
| `deedglass agent intent keygen` | Agents | Generate a signed-intent key pair | soon (S09) |
| `deedglass agent intent add` | Agents | Register an intent public key | soon (S09) |
| `deedglass agent intent list` | Agents | List an agent's intent keys | soon (S09) |
| `deedglass agent intent remove` | Agents | Remove an intent key | soon (S09) |
| `deedglass agent intent mode` | Agents | Require, allow or ignore signed intents | soon (S09) |
| `deedglass org create` | Agents | Create an org and its owner | soon (S07) |
| `deedglass org login-link` | Agents | Print a one-time sign-in link | soon (S07) |
| `deedglass org keys create` | Agents | Create an org admin key | soon (S08) |
| `deedglass org keys list` | Agents | List org admin keys | soon (S08) |
| `deedglass org keys revoke` | Agents | Revoke an org admin key | soon (S08) |
| `deedglass org stop-all` | Agents | Stop every agent in the org | soon (S08) |
| `deedglass org start-all` | Agents | Start every stopped agent | soon (S08) |
| `deedglass policy lint` | Policies | Check a policy file | soon (S10) |
| `deedglass policy fmt` | Policies | Format a policy file | soon (S10) |
| `deedglass policy test` | Policies | Run a policy's test cases | soon (S12) |
| `deedglass policy push` | Policies | Upload a new policy version | soon (S12) |
| `deedglass policy activate` | Policies | Activate a policy version | soon (S12) |
| `deedglass policy evaluate` | Policies | Evaluate one action against a policy | soon (S11) |
| `deedglass policy replay` | Policies | Replay past actions against a version | soon (S27) |
| `deedglass policy diff` | Policies | Compare two policy versions | soon (S12) |
| `deedglass act` | Actions | Send a test action through the Gate | soon (S13) |
| `deedglass actions list [--agent agt_…] [--tool stripe.*] [--state …] [--decision …] [--verdict MISMATCH] [--level L2] [--since 1h] [--q text] [--limit 25] [--cursor …] [--json]` | Actions | List actions | built |
| `deedglass actions show <act_…> [--json]` | Actions | Show one action and its events | built |
| `deedglass actions cancel` | Actions | Cancel a pending action | soon (S13) |
| `deedglass approve` | Actions | Approve a pending action | soon (S22) |
| `deedglass deny` | Actions | Deny a pending action | soon (S22) |
| `deedglass approvals list` | Actions | List approvals | soon (S22) |
| `deedglass approvals show` | Actions | Show one approval | soon (S22) |
| `deedglass undo preview` | Actions | Show what an undo would change | soon (S23) |
| `deedglass undo run` | Actions | Undo an action | soon (S23) |
| `deedglass webhooks create` | Actions | Add a webhook endpoint | soon (S22) |
| `deedglass webhooks list` | Actions | List webhook endpoints | soon (S22) |
| `deedglass webhooks test` | Actions | Send a test event | soon (S22) |
| `deedglass webhooks delete` | Actions | Remove a webhook endpoint | soon (S22) |
| `deedglass verify <path|->... [--keys <file|url>] [--pin sha256:…] [--anchor <seq>:sha256:…] [--strict] [--all] [-q]` | Deeds | Check deeds offline | built |
| `deedglass deeds list [--from-seq n] [--to-seq n] [--kind action,admin,amendment] [--action act_…] [--order asc|desc] [--limit 25] [--json]` | Deeds | List deeds | built |
| `deedglass deeds show <deed_…|seq> [--json]` | Deeds | Show one deed | built |
| `deedglass deeds export [--out <file>] [--format tar.gz|jsonl] [--from-seq n] [--to-seq n] [--json]` | Deeds | Export deeds as a bundle | built |
| `deedglass ledger check [--from-seq n] [--to-seq n] [--json]` | Deeds | Check the deed chain | built |
| `deedglass ledger prune [--older-than 90d] [--dry-run] [--yes] [--json]` | Deeds | Remove old payloads; deeds stay | built |
| `deedglass connector create` | Connectors | Add a connector | soon (S16) |
| `deedglass connector list` | Connectors | List connectors | soon (S16) |
| `deedglass connector show` | Connectors | Show one connector | soon (S16) |
| `deedglass connector test` | Connectors | Test a connector's credentials | soon (S16) |
| `deedglass connector types` | Connectors | List connector types | soon (S16) |
| `deedglass connector credentials set` | Connectors | Store a connector's credentials | soon (S16) |
| `deedglass connector credentials remove` | Connectors | Delete a connector's credentials | soon (S16) |
| `deedglass connector postgres init` | Connectors | Prepare a Postgres database | soon (S17) |
| `deedglass connector postgres check` | Connectors | Check a prepared Postgres database | soon (S17) |
| `deedglass connector gmail authorize` | Connectors | Authorize the Gmail connector | soon (S30) |
| `deedglass decoder status` | Connectors | Show Postgres change readers | soon (S20) |
| `deedglass decoder drop` | Connectors | Remove a replication slot | soon (S20) |
| `deedglass slack manifest` | Connectors | Print the Slack app manifest | soon (S29) |
| `deedglass mcp wrap` | MCP | Run an MCP server behind the Gate | soon (S14) |
| `deedglass mcp serve` | MCP | Serve the Gate's native MCP tools | soon (S15) |
| `deedglass mcp snippet` | MCP | Print client config for an MCP client | soon (S15) |
| `deedglass insights stats` | Insights | Show usage per agent and tool | soon (S27) |
| `deedglass insights shadow` | Insights | Show shadow-policy results | soon (S27) |
| `deedglass insights anomalies` | Insights | List anomalies | soon (S27) |
| `deedglass insights detect` | Insights | Run anomaly detection now | soon (S27) |
| `deedglass insights settings` | Insights | Show or change Insights settings | soon (S27) |
| `deedglass insights suggestions list` | Insights | List suggestions | soon (S28) |
| `deedglass insights suggestions show` | Insights | Show one suggestion | soon (S28) |
| `deedglass insights suggestions accept` | Insights | Accept a suggestion | soon (S28) |
| `deedglass insights suggestions reject` | Insights | Reject a suggestion | soon (S28) |
| `deedglass insights suggestions generate` | Insights | Generate suggestions now | soon (S28) |
| `deedglass insights digest` | Insights | Preview the weekly digest | soon (S28) |
| `deedglass dev seed [--json]` | Dev | Load demo data into a dev database | built |
| `deedglass dev fake-action [--n 20] [--seed 42] [--kinds allow,deny,…] [--agents 3] [--batch 1] [--pace 0s] [--org-slug demo] [--quiet] [--json]` | Dev | Seal realistic fake actions (dev only) | built |
| `deedglass dev simulate` | Dev | Simulate agent traffic | soon (S27) |
| `deedglass dev agent-script` | Dev | Run a scripted agent | soon (S37) |
| `deedglass dev mail-preview` | Dev | Preview emails in the browser | soon (S07) |
| `deedglass dev sign-intent` | Dev | Sign an intent with a local key | soon (S09) |
| `deedglass dev stripe-fixtures` | Dev | Create Stripe test-mode fixtures | soon (S30) |
| `deedglass dev id <prefix>` | Dev | Print a fresh id for a prefix | built, hidden |
<!-- commands:end -->

## Seal keys

`deedglass keys gen | rotate | list` manage the keys deeds are sealed with (`docs/SEAL_KEYS.md`).

| Command | Needs | Output | Exit |
|---|---|---|---|
| `keys gen [--dir <dir>]` | nothing (offline) | the key id, the file (0600), its thumbprint, and the `export DEEDGLASS_SEAL_KEY_FILE=…` line | 0; 1 when the file can't be written |
| `keys rotate [--overlap 5m] [--dir <dir>]` | `DEEDGLASS_DATABASE_URL` | the new active key, when each old key retires, and what to restart | 0; 1 when the database refuses (the new file is removed); 2 for an overlap outside 1s–168h |
| `keys list` | `DEEDGLASS_DATABASE_URL` | every key, newest first, with status, activation, retirement and thumbprint | 0; 1 when the database is unreachable |
| `keys list --server <url>` | nothing | the server's public `keys.json`, checked like a verifier does | 0; 1 when unreachable or a key is unsafe |

`--json` prints the `seal_key_set` (list), a `seal_key_rotation` (rotate) or a `seal_key_file`
summary without the seed (gen). Thumbprints show as `sha256:3f9a…c21e` for people and in full in
JSON.

## Verify

`deedglass verify` checks deeds offline (S05; the auditor's guide is `docs/VERIFYING_DEEDS.md`). No
database, no account, no telemetry; the network only for a `--keys` URL.

```
deedglass verify <path|->... [flags]
```

| Input | Detected by | Handling |
|---|---|---|
| `-` | the argument | stdin: a bundle by its gzip magic bytes, else one deed or JSONL |
| `bundle.tar.gz` | gzip magic bytes, not the name | export bundle v1 (`docs/BUNDLE_FORMAT.md`), streamed, nothing written to disk |
| `deeds.jsonl` | the `.jsonl` name, or several lines of compact deeds | one envelope per line, checked as one chain; a blank line is `malformed` |
| `deed.json` | one top-level value | one deed, checked on its own (its seal, and the genesis link at seq 1); pretty-printed is fine |
| a directory | `stat` | every `*.json` / `*.jsonl` in it (no recursion, no symlinks, at most 100,000 files), sorted by org and seq, one chain per org |
| `deed.cose` | the `.cose` name and CBOR tag 18 | the optional COSE_Sign1 encoding, checked on its own |

Several `.jsonl` or bundle inputs are one chain, in the order given: the second must start where
the first ended. Loose `.json` deeds are checked one by one, and the headline says
`chain not checked`.

| Flag | What it does |
|---|---|
| `--keys <file\|https URL>` | The seal key set (repeatable; merged by key id, and the same id with different content refuses everything). Keys you choose are **pinned**. |
| `--pin sha256:…` | Accept only keys with this thumbprint (repeatable); works on any key set, the bundle's included. |
| `--org <org_…>` | The org every deed must belong to (the global flag). |
| `--anchor <seq>:sha256:…` | The `body_hash` of the deed just before the first one given (a partial export). |
| `--strict` | Require canonical bytes (`non_canonical`) and pinned keys (exit 2 without them). |
| `--all` | List every failure (at most 100) instead of stopping at the first. |
| `--max-bytes <n>` | The largest `deeds.jsonl` a bundle may hold (default 8 GiB). |
| `-q`, `--quiet` | Print nothing; the exit code is the answer. |
| `--json`, `-v`, `--no-color` | The global flags: the full report, details, no colour. |

Keys, highest trust first: `--keys` (pinned), `--pin` (pinned), the bundle's own `keys.json` (not
pinned: integrity, not authenticity, and the output says so), none (`keys_missing`, exit 2).

| Situation | Headline | Exit |
|---|---|---|
| Every check passes, keys pinned | `✓ … chain intact …` | 0 |
| Every check passes, bundle keys only | `✓ …` then `○ Seal keys came from the bundle itself …` | 0 (2 with `--strict`) |
| Any deed, chain or bundle failure | `✕ …` with the deed, the reason and the next step | 1 |
| A deed names a key that is not in the given key set | `✕ … not in the key set.` (`seal_key_unknown`) | 1 |
| No key set at all | `○ Cannot check deed 1: no seal keys were given.` | 2 |
| A newer deed format | `○ Deed 1 uses deed format v2. …` | 2 |
| A `--keys` URL unreachable, too large or redirected elsewhere | `○ Could not fetch seal keys from <host> (…)` | 2 |
| A usage error or an unreadable file | `✕ …` and what to do | 2 |

Every string that came from a deed or a file is shown safely on a terminal: control characters,
`ESC` and bidi overrides print as `\u001b`-style escapes, so a deed cannot recolour a line or fake a
`✓`. `--json` keeps the exact strings.

`--json` prints a `verification_report`:

| Field | Meaning |
|---|---|
| `result` | `valid`, `invalid` or `cannot_determine` |
| `deeds`, `intact` | deeds read (stops at the first failure without `--all`), deeds that passed every check |
| `org`, `first_seq`, `last_seq`, `from`, `to` | what the intact deeds cover |
| `chain` | `{status: intact\|broken\|not_checked, anchor: {kind: genesis\|deed\|none, seq, body_hash}}` |
| `seal_keys` | each key used: `{id, thumbprint, deeds, first_seq, last_seq, source}` |
| `keys_pinned` | whether the keys came from you (`--keys`, `--pin`) |
| `kinds`, `verdicts_recorded` | counts by deed kind, and by the Witness verdict written in each deed (`verify` does not re-run the Witness) |
| `bundle` | `{manifest: ok\|mismatch, manifest_seal: ok\|missing\|invalid, sealed_by, files, input}` or null |
| `failures` | `{code, seq, deed, input, line, field, expected, computed, message, next}`; `expected`/`computed` are the hashes for `body_hash_mismatch` |
| `warnings` | `{code, message, next}`: `keys_not_pinned`, `manifest_unsealed`, `test_keys` |
| `duration_ms`, `verifier` | how long it took, and which build checked |

Performance (S05 §3.9; `make bench-verify`, raw numbers in `bench/results/`): 100,000 deeds in
2.4 s wall with 71 MB peak RSS as `.jsonl`, 2.4 s as a bundle, one deed in under 10 ms, on an Apple
M1 (8 cores), 2026-10-01.

## Actions, deeds and the Ledger

These run in direct database mode (`DEEDGLASS_DATABASE_URL`, the runtime role); API mode
(`--server`, `--api-key`) arrives with S09's keys. With one org in the database they use it;
with several, `--org <id or slug>` is required (the error lists them). Text output is for people
(relative times, short ids); `--json` prints exactly the API's objects (RFC 3339 times, full ids).
Full reference: [LEDGER.md](LEDGER.md), [RETENTION.md](RETENTION.md).

| Command | Does | Exit |
|---|---|---|
| `actions list [--agent] [--tool stripe.*] [--state] [--decision] [--verdict] [--level] [--on-behalf-of] [--since 1h] [--q text] [--limit 25] [--cursor]` | newest first; the footer says `5 of 1,204` and prints the next page's command | 0; 2 for a bad filter |
| `actions show <act_…>` | the decision, execution, witness (with the diff sentence), deeds and the event path | 0; 1 when not found |
| `deeds list [--from-seq] [--to-seq] [--kind] [--action] [--order asc|desc]` | deeds by seq | 0 |
| `deeds show <deed_…|seq>` | a summary; `--json` prints the stored bytes verbatim | 0; 1 when not found |
| `deeds export [--out <file>] [--format tar.gz|jsonl] [--from-seq] [--to-seq]` | a sealed export bundle (or `.jsonl`), written to a temporary file and renamed at the end | 0; 1 for an empty range (nothing is written) |
| `ledger check [--from-seq] [--to-seq]` | the chain check, with the head to keep elsewhere | 0 intact; 1 broken; 2 incomplete |
| `ledger prune [--older-than 90d] [--dry-run] [--yes]` | removes payloads of old sealed actions and seals a `ledger.pruned` deed | 0; 1 not confirmed or another prune running; 2 without `--yes` off a terminal |
| `dev fake-action [--n 20] [--seed 42] [--kinds …] [--agents 3] [--batch 1] [--pace 0s] [--org-slug demo] [--quiet]` | realistic actions through the real deed path (below) | 0; 1 outside `DEEDGLASS_ENV=dev` |

```
$ deedglass actions list --limit 5
TIME     AGENT         TOOL                    DECISION   WITNESS             LATENCY
2m ago   support-bot   postgres.execute        allow      ✕ L2 · MISMATCH     41 ms
3m ago   support-bot   stripe.create_refund    approval   ◐ pending           —
3m ago   billing-bot   stripe.create_refund    deny       ✕ denied            —
4m ago   ops-bot       github.create_issue     allow      ✓ L1 · VERIFIED     212 ms
5m ago   ops-bot       postgres.query          allow      ✓ L2 · VERIFIED     8 ms
5 of 1,204 · next page: deedglass actions list --cursor eyJpZCI6…
```

Chips follow the brand (§12): `✓ L2 · VERIFIED` teal, `✕ L2 · MISMATCH` bold red, `✕ denied`
plain red, `◐ pending` violet, `○ L1 · UNVERIFIABLE` amber; allowed and executed stay neutral on
purpose. Colour only repeats what the symbol and words already say.

### `dev fake-action`

Dev only: it refuses unless `DEEDGLASS_ENV=dev`, before it reads any config or opens the database,
and prints the database it writes to. It finds or creates the demo org (`--org-slug demo`, or an
existing org with `--org`) and `--agents` demo agents, then walks every action through legal edges
only (`ledger.Transition`) and seals it (`ledger.SealAction`), so the events fire and every deed is
real.

- **Deterministic:** `--seed` (PCG) fixes the tools, arguments, latencies and verdicts; ids and
  times are real.
- **Mix** (`--kinds` restricts it): 60% allowed and VERIFIED (L0 40%, L1 25%, L2 35%), 12% denied
  (`block-destructive-sql`, `refund-limit`, `budget`), 6% L2 MISMATCH on `postgres.execute`, 8%
  UNVERIFIABLE (`timeout`), 6% failed (`upstream_error`, `timeout`), 5% left waiting for approval
  (not sealed), 3% sealed UNVERIFIABLE then amended to VERIFIED in its own amendment deed.
- **Tools** are the canonical connector names: `postgres.query`, `postgres.execute`,
  `stripe.create_refund`, `github.create_issue`, `github.merge_pr`, `slack.post_message`,
  `gmail.create_draft`, `notion.create_page`, `orders.get`.
- `--pace 200ms` spaces actions for live demos. `--batch 500` walks actions in parallel
  transactions and seals them in order, 500 per transaction (bulk seeding).
- Nothing calls a real tool. `executing_at` is set back by the simulated latency so deeds show a
  realistic `latency_ms`; every other time is the database clock.

## What it doesn't do (yet)

- The Ledger commands use the database directly; `--server`/`--api-key` (API mode) come with S09.
  `actions list --follow` waits for S24's event stream.

- The banner doesn't ask the terminal for its background colour (OSC 11); light terminals need
  `COLORFGBG` or `DEEDGLASS_THEME=light`. No image-protocol (iTerm2, Kitty) rendering yet.

- No shell completion until S32 (`deedglass completion zsh|bash|fish`).
- `keys revoke` isn't built (planned; `docs/SEAL_KEYS.md`).
- `deedglass verify` does not re-run the Witness, fetch keys anywhere you did not name, or cache
  them (a key cache with `--offline` is planned). Bundles in the browser verifier are not built yet.
