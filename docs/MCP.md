# MCP: put the Gate in front of an MCP server

Deedglass governs tool calls that MCP clients (Claude Desktop, Claude Code, Cursor, VS Code,
Windsurf and any other MCP client) make to MCP servers. This page covers the stdio wrapper,
`deedglass mcp wrap` (S14). The Streamable HTTP gateway at `/mcp/{connector}` and `deedglass mcp
serve` arrive in S15. Per-client config is in [MCP_CLIENTS.md](MCP_CLIENTS.md); authorization is in
[MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md).

## Wrap a stdio server

Change one line of your MCP client's config. Instead of starting the server:

```json
"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/deedglass-demo"]
```

start the wrapper, which starts the server:

```json
"command": "/Users/you/txv/product/bin/deedglass",
"args": ["mcp", "wrap", "--connector", "scratch", "--",
         "/usr/local/bin/npx", "-y", "@modelcontextprotocol/server-filesystem@2026.8.31", "/tmp/deedglass-demo"]
```

`deedglass mcp snippet` prints the whole block for your client, with absolute paths and a `PATH`
entry (GUI apps on macOS start with a minimal `PATH`, so `npx` and `node` are otherwise not found).

```text
 MCP client (Claude Desktop)        deedglass mcp wrap                  MCP server (child)
 ───────────────────────────  stdio  ─────────────────────────  stdio  ──────────────────
   tools/call ───────────────────►  1 POST /v1/actions (mode proxy) ──► Gate
                                    2a authorized: forward (new id) ─────────────────────►
                                                 ◄──────────────────────────────── result
                                    3  POST /v1/actions/{id}/result (background, retried)
   ◄──────────── result + _meta "com.deedglass/action"
                                    2b denied, waiting, unreachable: a tool result that
                                       says so; nothing is forwarded
```

### Two-minute setup

1. Start Deedglass: `deedglass serve` (or `make dev`).
2. Create the agent and its key: `deedglass agent create claude-desktop`, then
   `deedglass agent keys create claude-desktop`. Keep the key out of chat and tickets.
3. Push and activate a policy that names the connector. The demo policy is
   `examples/demo/policies/acme-demo.yaml`:
   `deedglass policy push examples/demo/policies/acme-demo.yaml`, then
   `deedglass policy activate acme-demo --version 1 --mode enforce --reason "MCP demo"`.
4. `deedglass mcp snippet --client claude-desktop --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo`,
   paste it into the client's config, replace `dg_test_REPLACE_ME` with the agent's key, and
   restart the client.

Policies see wrapped tools as `<connector>.<tool>` (`scratch.write_file`), with
`connector_type: mcp` (or what `--connector-type` says) and `side_effect: unknown`:

```yaml
rules:
  - id: allow-scratch-reads
    match: { tool: ["scratch.read_*", "scratch.list_*", scratch.get_file_info, scratch.search_files, scratch.directory_tree] }
    decision: allow
  - id: scratch-writes-only-in-allowed
    match: { tool: [scratch.write_file, scratch.edit_file] }
    when: '!has(args.path) || !args.path.startsWith("/tmp/deedglass-demo/allowed/") || args.path.contains("..")'
    decision: deny
    reason: Agents may only write inside /tmp/deedglass-demo/allowed.
```

### What it governs, and what passes through

| Message | What the wrapper does |
|---|---|
| `tools/call` | Asks the Gate first (proxy mode). Forwards only on `authorized`. |
| `tools/call deedglass.check_approval` | Handled by the wrapper (below). |
| `tools/list` | Forwarded; the answer is filtered (tools the policy always denies, tools whose definition changed, server tools named `deedglass.*`), and `deedglass.check_approval` is added to the first page. For 2026-07-28 requests, `cacheScope` becomes `"private"` and `ttlMs` at most 60,000. |
| `initialize`, `server/discover` | Forwarded; one line is added to `instructions` and `capabilities.tools` is made present. |
| `notifications/cancelled` | Mapped to the server's id and forwarded (below). |
| Everything else | Passed through with only the request id rewritten: `ping`, `resources/*`, `prompts/*`, `completion/complete`, `subscriptions/listen`, `logging/setLevel`, notifications, unknown methods and extension fields. |
| Server-to-client requests (2025 revisions: `sampling/createMessage`, `elicitation/create`, `roots/list`) | Passed through ungoverned in v1; the client shows its own confirmation. |

**Governed methods:** `tools/call` (and the wrapper's own `deedglass.check_approval`).
**Rewritten results:** `initialize`, `server/discover`, `tools/list`. **Passthrough:** everything
else. S15's HTTP gateway follows the same lists.

### Both MCP eras

The wrapper relays the 2026-07-28 revision (stateless, `server/discover`, per-request `_meta`,
`resultType`, `input_required` results) and the `initialize`-based revisions (2025-11-25,
2025-06-18, 2025-03-26). It rewrites only request ids and the three results above; a field or
method it has never seen passes through unchanged. Its own results carry `resultType: "complete"`
only when the request was 2026-07-28. An `input_required` result passes through, and the action is
reported as `unknown` (`input_required`); the client's retry is a new action.

Tested combinations: the official Go SDK's client at 2026-07-28 and at 2025-11-25, each against an
SDK server that speaks every version and one that speaks 2025-11-25 only, all through the wrapper;
and the real `@modelcontextprotocol/server-filesystem` (2026.8.31) through npx (`make mcp-it`).

### Fail closed

If the Gate can't be reached, answers slowly (10 s), answers something the wrapper can't read, or
refuses the key, the call does not run. The model sees a calm reason:

```text
Deedglass could not be reached at http://127.0.0.1:8080, so this was not run. Start it with "deedglass serve", then try again.
```

`--fail-open` forwards calls when the Gate is unreachable, with no decision and no deed, and logs
`✕ fail-open: …` on every call. It works only with `DEEDGLASS_ENV=dev`; anywhere else
`deedglass mcp wrap` exits 2.

### What the model sees

Every refusal is a tool result with `isError: true`, so the model reads it and can explain it. It
says what happened, whether anything ran, the rule or cause, and one next step. It carries the
rule id, the policy author's reason and ids only: never tool arguments or server output.

```text
Blocked by policy "scratch-writes-only-in-allowed": Agents may only write inside /tmp/deedglass-demo/allowed. Nothing was run. Deedglass sealed a deed of this attempt (deed_01k6…). If this should be allowed, a person can change the policy in Console › Policies.
```

Allowed results come back exactly as the server sent them, plus `_meta["com.deedglass/action"]`.
Synthetic results carry `com.deedglass/action`, `/deed`, `/decision`, `/rule` and `/approval` as
they apply.

### Approvals: `deedglass.check_approval`

A rule with `decision: require_approval` pauses the call:

```text
Waiting for approval under policy "writes-need-approval": Writes wait for a human. Nothing has run yet. Approval apr_01k6… expires at 10:42 UTC. To continue, call deedglass.check_approval with {"approval_id": "apr_01k6…"}.
```

After a person approves it, the model calls `deedglass.check_approval`; the wrapper then runs the
**original** call (the name and arguments the Gate decided on) once, and returns `Approved by a
person. Ran scratch.write_file.` followed by the tool's own result. A second check says it already
ran. Only the wrapper process that created the action runs it; after a restart, the answer asks the
agent to make the call again, so a call never runs twice. Claude Code shows the tool as
`mcp__scratch__deedglass_check_approval`. Until S22, approvals are decided in the database by tests
and S22's routes; there are no notifications yet.

### Tool pinning

The first definition of each tool (`name`, `title`, `description`, `inputSchema`, `outputSchema`,
`annotations`, digested as `sha256:` of their RFC 8785 form) is pinned for the life of the process.
If the server later changes it, the tool is hidden and its calls are refused until the wrapper
restarts, so a definition can't change under the model after it was first shown. MCP `annotations`
(`readOnlyHint`, `destructiveHint`) come from the server and are never used for decisions.

### Hidden tools

`POST /v1/tools/resolve` tells the wrapper which tools the enforced policy denies on every call
(the first rule whose `match` covers the tool decides; a rule with a `when` can't be decided ahead
of time and counts as visible; budgets and Stop are ignored). Those tools are left out of
`tools/list` (`--hide-denied=false` keeps them). Hiding is a convenience: a hidden tool called by
name still goes to the Gate and is sealed as a denial.

### Cancellation, crashes, timeouts

- A cancellation before the Gate answers: nothing is forwarded, and the action is reported
  `failed` (`client_cancelled`). After forwarding: the server gets `notifications/cancelled` with
  its own id, and the action is reported `unknown` (it may have run).
- The server stops mid-call: `The MCP server stopped while running scratch.write_file. It may have
  run. Deedglass recorded it as UNVERIFIABLE (act_…).` The wrapper restarts it (250 ms, doubling to
  8 s), replays a 2025-era `initialize`, and stops restarting after five crashes in a minute.
- `--call-timeout 2m` gives up on a call after two minutes and reports it `unknown` (`timeout`).
- A server answer over 4 MiB is refused for that id (`-32603`) and reported `unknown`.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--connector` | (required) | The name policies match: `<connector>.<tool>`. `deedglass`, `mcp` and `native` are reserved. |
| `--server`, `--api-key` | `DEEDGLASS_SERVER`, `DEEDGLASS_API_KEY` | Global flags. https unless loopback. The key is never passed to the server. |
| `--connector-type` | `mcp` | `postgres` for servers whose tools take `args.sql` (turns on the SQL guard's facts). |
| `--hide-denied` | `true` | Hide tools the policy always denies. |
| `--fail-open` | off | Development only (above). |
| `--call-timeout` | none | Give up on a call after this long. |
| `--result-mode` | `full` | `full` reports results up to 256 KiB (larger ones as a digest); `digest` reports digests only. |
| `--on-behalf-of` | none | `Deedglass-On-Behalf-Of` on every action. |
| `--insecure-http` | off | Allow `http` to a non-loopback server (`DEEDGLASS_ENV=dev` only). |
| `--log-file`, `--no-log-file` | `~/.deedglass/logs/mcp-<connector>.log` | The log file (0600, directory 0700). |

The server process gets the wrapper's environment minus every `DEEDGLASS_*` variable, and runs in
its own process group. Closing the wrapper's stdin closes the server's, then sends `SIGTERM` after
5 s and `SIGKILL` 2 s later; results not yet reported are flushed for up to 5 s.

### Logs

The wrapper writes plain lines to stderr (which MCP clients keep in their own logs) and to its log
file: tool names, ids, decisions and timings, never arguments, results or the key.

```text
deedglass mcp wrap · connector scratch · server http://127.0.0.1:8080
✓ Gate reachable · agent claude-desktop (agt_01k6…) · key test
✓ upstream started · /usr/local/bin/npx -y @modelcontextprotocol/server-filesystem@2026.8.31 /tmp/deedglass-demo · pid 48121
◐ 2 tools hidden by policy · policy pv_01k6… · scratch.create_directory · scratch.move_file
✓ scratch.read_text_file · allowed · 3 ms · act_01k6…
✕ scratch.write_file · denied · scratch-writes-only-in-allowed · act_01k6…
```

### Performance

On an Apple M1 against a local Gate and Postgres 18 (`bench/results/2026-10-01-darwin-arm64-mcp.json`,
`make bench-mcp`): passthrough relay overhead p99 0.05 ms; `tools/call` added latency p50 2.9 ms,
p99 4.9 ms (the Gate's decision, recording and the result report); `tools/list` added p50 0.5 ms;
the wrapper's peak RSS with 100 concurrent 64 KiB calls 49 MiB.

## What it doesn't do

- **It doesn't govern resources, prompts, completion or legacy sampling and elicitation yet.**
  They pass through. Governing `resources/read` and `prompts/get` is on the v2 roadmap.
- **It can't stop a server that acts outside MCP**, for example one that writes files on its own
  at startup or on a timer. It governs the calls the client makes.
- **The result is the server's word.** An allowed call is sealed L0 VERIFIED with method
  `edge_reported`: the server said it succeeded. Independent checks come from the Witness (Postgres
  from S21).
- **It doesn't protect the server from itself.** A tool the policy allows can still do anything
  its server lets it do with the arguments it was given; the policy decides on the tool name and
  arguments, not on what the server does with them. Path rules compare text: the server resolves
  symlinks itself.
- **No signed intents from MCP clients in v1** ([INTENTS.md](INTENTS.md)); an agent in
  `required` intent mode can't act through the wrapper.
- **Approval resumes only in the same wrapper process** until S22.
