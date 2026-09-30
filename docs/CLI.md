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
{"error":{"type":"invalid_request_error","code":"not_built","message":"deedglass verify isn't in this build yet. Run deedglass help to see what this build can do.","doc_url":"https://docs.deedglass.com/errors/not_built"}}
```

CLI error codes so far: `unknown_command`, `usage_error`, `not_built`, `config_invalid` (with a
`problems` list), and `failed`.

## Examples

```
$ deedglass polcy lint
✕ Unknown command "polcy". Did you mean "policy"?
  Run deedglass help to see every command.

$ deedglass verify deeds.jsonl
deedglass verify isn't in this build yet.
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
| `deedglass serve` | Setup | Run the API, the Gate and the Console | soon (S03) |
| `deedglass migrate up [--database-url <url>] [--dry-run] [--json]` | Setup | Apply database migrations | built |
| `deedglass migrate status [--database-url <url>] [--json]` | Setup | Show applied and pending migrations | built |
| `deedglass migrate verify [--database-url <url>] [--json]` | Setup | Check applied migrations against this build | built |
| `deedglass config print [--json]` | Setup | Show the effective config, secrets hidden | built |
| `deedglass version [--json] [--check]` | Setup | Show the client and server versions | built |
| `deedglass keys gen` | Setup | Generate a seal key | soon (S04) |
| `deedglass keys rotate` | Setup | Rotate the active seal key | soon (S04) |
| `deedglass keys list` | Setup | List seal keys | soon (S04) |
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
| `deedglass actions list` | Actions | List actions | soon (S06) |
| `deedglass actions show` | Actions | Show one action and its events | soon (S06) |
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
| `deedglass verify` | Deeds | Check deeds offline | soon (S05) |
| `deedglass deeds list` | Deeds | List deeds | soon (S06) |
| `deedglass deeds show` | Deeds | Show one deed | soon (S06) |
| `deedglass deeds export` | Deeds | Export deeds as a bundle | soon (S06) |
| `deedglass ledger check` | Deeds | Check the deed chain | soon (S06) |
| `deedglass ledger prune` | Deeds | Remove events past retention | soon (S06) |
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
| `deedglass dev fake-action` | Dev | Send realistic actions for demos | soon (S06) |
| `deedglass dev simulate` | Dev | Simulate agent traffic | soon (S27) |
| `deedglass dev agent-script` | Dev | Run a scripted agent | soon (S37) |
| `deedglass dev mail-preview` | Dev | Preview emails in the browser | soon (S07) |
| `deedglass dev sign-intent` | Dev | Sign an intent with a local key | soon (S09) |
| `deedglass dev stripe-fixtures` | Dev | Create Stripe test-mode fixtures | soon (S30) |
| `deedglass dev id <prefix>` | Dev | Print a fresh id for a prefix | built, hidden |
<!-- commands:end -->

## What it doesn't do (yet)

- No shell completion until S32 (`deedglass completion zsh|bash|fish`).
- `version --server` doesn't contact the server until S03 adds `GET /v1/version`.
- No command talks to a server or a database in this build.
