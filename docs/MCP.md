# MCP: put the Gate in front of an MCP server

Deedglass governs tool calls that MCP clients (Claude Desktop, Claude Code, Cursor, VS Code,
Windsurf and any other MCP client) make to MCP servers. There are three ways in. Per-client config
is in [MCP_CLIENTS.md](MCP_CLIENTS.md); authorization is in
[MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md); the Acme demo that uses all three is in
`examples/demo/`.

| | Wrap (S14) | Gateway (S15) | Native (S15) |
|---|---|---|---|
| Transport to Deedglass | stdio (the wrapper runs beside the client) | Streamable HTTP | Streamable HTTP, or stdio through `deedglass mcp serve` |
| Who runs the tool | the server the wrapper starts, after the Gate says yes | **Deedglass**, through its MCP client | **Deedglass**, through its connector |
| Execution mode in the deed | `proxy` | `gateway` | `gateway` |
| Where the server's credentials live | the client's config (the child's environment) | Deedglass (`mcp.upstreams`; the vault from S16) | Deedglass (the vault from S16) |
| Endpoint | none (a CLI command) | `POST /mcp/{connector}` | `POST /mcp/native` |
| Tool names the model sees | the server's names | the server's names | `connector.tool`, for example `fake.echo` |

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
| `tools/list` | Forwarded; the answer is filtered (tools the policy always denies, tools whose definition changed, tools whose description carries instructions aimed at the model or characters people can't see (S34), server tools named `deedglass.*`), and `deedglass.check_approval` is added to the first page. For 2026-07-28 requests, `cacheScope` becomes `"private"` and `ttlMs` at most 60,000. |
| `initialize`, `server/discover` | Forwarded; one line is added to `instructions` and `capabilities.tools` is made present. |
| `notifications/cancelled` | Mapped to the server's id and forwarded (below). |
| Everything else | Passed through with only the request id rewritten: `ping`, `resources/*`, `prompts/*`, `completion/complete`, `subscriptions/listen`, `logging/setLevel`, notifications, unknown methods and extension fields. |
| Server-to-client requests: `roots/list`, `ping` | Passed through; the client shows its own confirmation. |
| Server-to-client requests: `sampling/createMessage`, `elicitation/create` (S34) | Refused: the wrapper answers the server with a JSON-RPC error (-32601) and logs it, and the client never sees the request. `--allow-server-requests sampling,elicitation` passes the named kinds through, for a server you trust to ask your model for text or you for input. The HTTP gateway never relays them: its MCP client declares neither capability, and logs each request it refuses. |

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
`mcp__scratch__deedglass_check_approval`.

Since S22, a person decides from the emailed link, the Console's API (`POST
/v1/approvals/{id}/approve` or `/deny`) or, on a dev server, `deedglass approve <apr_…> --as
<email>`. [APPROVALS.md](APPROVALS.md) has the whole flow.

**`wait_seconds`.** `deedglass.check_approval` takes `{"approval_id": "apr_…", "wait_seconds": 10}`.
It waits up to `wait_seconds` (0 to 20, default 10) for a decision before it answers, so one call
usually returns the outcome instead of "still waiting". 0 answers at once.

**What the agent is told.** Agent-facing texts never name the person:

| Outcome | The tool result |
|---|---|
| still pending | `Still waiting for approval apr_…. It expires at 05:11 UTC. Nothing has run yet. Check again in a minute.` |
| approved | `Approved by a person. Ran scratch.write_file.` and then the tool's own result |
| denied | `A person denied this action. Nothing was run. Reason: "Order 42 hasn't been paid yet."` (`isError: true`; the reason is quoted on one line, double quotes made single, at most 280 characters) |
| approved, agent stopped | `A person approved this, but agent "claude-desktop" is stopped, so nothing ran.` |
| expired | `Approval apr_… expired before a person decided. Nothing was run. Make the call again to ask for a new approval.` |
| no one can approve | the call is refused at once with `approval_unsatisfiable`: `No one in this org can approve this: the rule needs 2 people and only 1 is eligible. Nothing was run. …` |

**Restarts.** The wrapper remembers its pending calls in memory only. After it restarts, a check
for an approval it no longer knows asks the server (`GET /v1/approvals/{id}`): an approved one
answers that the call was made before the restart and should be made again, so the earlier call
never runs twice; a denied, expired or cancelled one gives its usual text. The gateway and
`/mcp/native` keep nothing in memory: their checks resume the action on the server.

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
| `--allow-server-requests` | none | `sampling`, `elicitation` (comma-separated): pass the server's requests for the client's model or for your input to the client. By default both are refused and logged (S34). |
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

The HTTP endpoints, against a local Gate and Postgres 18 with an in-process upstream
(`bench/results/2026-10-02-darwin-arm64-gateway.json`, `make bench-gateway`): `/mcp/native`
`fake.echo` end to end p50 5.9 ms, p99 7.2 ms; the gateway's added latency over calling the
upstream directly p50 5.4 ms, p99 6.5 ms; header validation and envelope parsing of a 2 KiB body
29 µs.

## What it doesn't do

- **It doesn't govern resources, prompts or completion yet.** They pass through. Governing
  `resources/read` and `prompts/get` is on the v2 roadmap. Sampling and elicitation are refused
  unless `--allow-server-requests` names them (S34); once allowed, they pass through ungoverned.
- **Description scanning is a heuristic (S34).** A tool is hidden when its title, description or
  a parameter's description contains a phrase that tells a model to drop its instructions or hide
  something from the user (`ignore previous instructions`, `do not tell the user`, …), an
  instruction tag such as `<IMPORTANT>`, or characters people can't see (Unicode tag characters,
  bidirectional overrides, zero-width characters). It doesn't understand intent: a reworded
  instruction passes, and a harmless description can be hidden. The hidden tool is logged with the
  admin's sentence, sealed as `connector.tool_hidden` on the HTTP gateway, and refused as an
  unknown tool; the agent is never told why.
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

## Put the Gate in front of a remote MCP server

The gateway is Deedglass as an MCP endpoint in front of another MCP server that speaks Streamable
HTTP. The agent talks only to Deedglass, with its Deedglass key; Deedglass holds the upstream's
credentials and runs every allowed `tools/call` itself (gateway mode), so the deed records what
Deedglass ran, not what an edge reported.

```text
 MCP client ── POST /mcp/everything ──► httpx (Origin, key, 4 MiB, JSON) ──► gateway (headers, era)
                                                                               │
                                       ┌───────────────────────────────────────┘
                                       ▼
                    Gate (decide · record) ── allowed ──► upstream pool ── tools/call ──► MCP server
                                       ▲                                   (SDK client, era negotiated)
                    result + _meta ◄───┴── executed → L0 → sealed
```

There are two ways to declare an upstream. **In production, use a connector of type `mcp_http`**:
its credential lives in the vault, and each org has its own.

```sh
cat > fs.yaml <<'YAML'
type: mcp_http
name: fs
config:
  url: https://mcp.example.com/mcp
  auth: {kind: bearer}
  timeout_ms: 30000
  tools_ttl_ms: 30000
YAML
deedglass connector create --config fs.yaml
deedglass connector credentials set fs --kind bearer --field token=env:FS_MCP_TOKEN
deedglass connector test fs
```

`auth.kind` is `bearer` (`Authorization: Bearer <token>`), `header` (`auth.header` names the header;
the credential is `{"value": …}`) or `none`. The `url` passes the egress door at create time
(`destination_blocked` for metadata, private or reserved addresses in prod) and again at every
connection. Point the client at `/mcp/fs`: the gateway finds the caller's org's `mcp_http`
connector by name. The credential is opened only when the Gate opens a session; replacing it opens
a new session on the next call. The connector's tools are also callable as `fs.<tool>` through
`POST /v1/actions` in gateway mode.

For local development you can still declare an upstream in `deedglass.yaml`, with its secrets in
the environment. **`headers_from_env` is refused in prod** (S18); `serve` logs a warning in dev. A
`deedglass.yaml` upstream is served to every org on the instance, and its name wins over an org's
connector of the same name.

```yaml
mcp:
  upstreams:
    everything:
      url: https://mcp.example.com/mcp      # http only with DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1 (dev)
      headers_from_env: {Authorization: EVERYTHING_MCP_AUTH}
      timeout: 30s                           # per HTTP request to the upstream
      tools_ttl: 30s                         # how long its tools/list is cached
```

The variable names in `headers_from_env` can't start with `DEEDGLASS_` (that prefix is
Deedglass's own); the values are read once at start and never logged, returned or sealed. Then
point the client at `http://127.0.0.1:8080/mcp/everything` with the agent's key
(`deedglass mcp snippet --transport http --client cursor --connector everything`).

What the gateway does with each message:

- **`tools/list`**: the upstream's tools (cached for the shorter of `tools_ttl` and the
  upstream's `ttlMs`), minus the ones the active policy always denies to this agent, plus
  `deedglass.check_approval`. Modern answers carry `ttlMs: 30000` and `cacheScope: "private"`,
  because the list is filtered per caller. 100 tools per page with an opaque cursor.
- **`tools/call`**: one action, `mode: gateway`, decided by the Gate. Allowed calls run on the
  upstream and the answer is the upstream's result with `_meta` `com.deedglass/action` and
  `com.deedglass/deed`. A denial, a pending approval or a Stop is an `isError` tool result that
  says so and that nothing ran. The call waits up to `gate.execute_timeout` plus 2 s for the
  outcome; past that it answers "Still running" with the action id.
- **How an upstream call ends:** the connection refused → `failed`, `connector_unavailable`
  (nothing ran); the upstream's JSON-RPC error → `failed`, `upstream_error`; `isError: true` →
  `failed`, `tool_error`; no answer within `gate.execute_timeout` → `unknown`, sealed
  UNVERIFIABLE (`timeout`); the client disconnects mid-call → the upstream request is cancelled
  and the action is UNVERIFIABLE (`client_cancelled`); an answer over 4 MiB → `unknown`. A
  `tools/call` is never retried, because it may have applied; only `tools/list` is retried once
  after a lost session.
- **`deedglass.check_approval`** finds the action from the approval id (no session needed) and,
  once a person approved it, returns the result of the run the Gate made when it resumed the
  action. Another agent's approval is not found.
- **Resources, prompts and completion** answer `-32601` unless `mcp.passthrough` lists their
  family (`resources`, `prompts`, `completion`); then they are forwarded and logged, not sealed.
- **Inbound credentials never travel upstream.** `Authorization`, `Proxy-Authorization`,
  `Cookie` and every `Deedglass-*` header are stripped before the operator's configured headers
  are added (`identity.StripInboundCredentials`; a test checks the upstream sees none of them).
- **Outbound safety:** every upstream connection goes through the egress door
  ([NETWORK_EGRESS.md](NETWORK_EGRESS.md)): TLS 1.2 or later, no redirects, no proxy from the
  environment, a 4 MiB response cap, `User-Agent: deedglass/<version>`, and a dial-time refusal of
  metadata, link-local, reserved and (in prod) private addresses. Since S18, upstreams can be
  connector rows of type `mcp_http` with credentials in the vault (above).

### Your org's own connectors on `/mcp/native`

Since S18, `/mcp/native` (and `GET /v1/tools`) lists the caller's org's own native connectors next to
the built-ins: a Postgres connector named `postgres` shows `postgres.query`, `postgres.execute`,
`postgres.call` and `postgres.describe`, and a REST connector named `orders` shows its tools as
`orders.<tool>`. Every call is decided and recorded by the Gate in gateway mode, with the
connector's credential added only when the call runs. An `mcp_http` connector is served at
`/mcp/<name>` instead. At most 100 connectors per org are listed; a connector whose tools can't be
listed is left out and logged, and a call to it is still recorded by the Gate.

## Use Deedglass as an MCP server

`POST /mcp/native` serves the Gate's own connectors as MCP tools, named `<connector>.<tool>`:
`fake.echo` in dev today, `postgres.*`, `rest.*` and the others as S16 to S30 add connectors. The
list is filtered per agent like the gateway's; `GET /v1/tools` returns the same tools as JSON for
SDKs (`?include_hidden=true` adds the hidden ones with `visibility: hidden`).

Stdio-only clients use the bridge:

```text
 Claude Desktop ──stdio──► deedglass mcp serve --connector native ──HTTP POST──► deedglass serve /mcp/native
```

`deedglass mcp serve` reads one JSON-RPC line at a time and POSTs it with the agent's key and the
headers a conforming HTTP client sends (`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` for
modern requests; the negotiated version for legacy ones). It holds no credentials but the key,
runs no tools, keeps up to 32 requests in flight, turns `notifications/cancelled` into closing the
matching request, and exits 0 when stdin ends. When the server can't be reached, a tool call gets
the calm "could not be reached" result and nothing runs. `--connector <name>` bridges to an
upstream behind the gateway instead.

## MCP versions

The two HTTP endpoints are **sessionless and dual-era** (ADR-037), checked against the
specification pages on 2026-10-01:
[Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http),
[Versioning](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning) and the
[2025-11-25 transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

| | 2026-07-28 (modern) | 2025-11-25, 2025-06-18, 2025-03-26 (legacy) |
|---|---|---|
| How a client starts | any request, with `_meta` `io.modelcontextprotocol/protocolVersion`; `server/discover` is optional | `initialize`; the requested version is echoed when served, else 2025-11-25 |
| Sessions | none | none minted; an `Mcp-Session-Id` sent is ignored (sessions were optional for servers) |
| Required headers | `MCP-Protocol-Version` equal to `_meta`, `Mcp-Method`, and `Mcp-Name` for `tools/call`, `resources/read`, `prompts/get` | `MCP-Protocol-Version` after `initialize` (none means 2025-03-26); any `Mcp-Method` or `Mcp-Name` sent must still match |
| A missing or different header | 400, JSON-RPC `-32020` (HeaderMismatch) | 400, `-32020` |
| `Mcp-Param-*` from `x-mcp-header` | checked: a present argument needs its header with an equal value (integers compared as numbers), and a header needs its argument | checked when sent |
| Values outside plain ASCII | `=?base64?…?=`, decoded before comparing; invalid base64 is `-32020` | same |
| Unsupported version | 400, `-32022`, `data.supported` (all four) and `data.requested` | 400, `-32022` |
| Unknown method | 404, `-32601` | 200, `-32601` |
| Notifications | 202, no body | 202, no body |
| GET, DELETE | 405 with `Allow: POST` | 405 (a server may refuse the GET stream) |
| Responses | `application/json`, `Cache-Control: no-store` | same |

## Security

- **Origin:** a browser `Origin` that isn't the server's own or listed in `mcp.allowed_origins`
  is refused with 403 before anything else (DNS-rebinding protection); no CORS headers are sent.
  The default listen address stays `127.0.0.1`.
- **Keys:** every request needs an agent key with `actions:execute`; a missing or bad key is 401
  with `WWW-Authenticate: Bearer … resource_metadata="<base>/.well-known/oauth-protected-resource/mcp/<connector>"`.
  Errors before the message is read are JSON-RPC errors whose `data` names the Deedglass error
  code, its `doc_url` and the request id.
- **Header smuggling:** a header that routes one way and a body that runs another is refused in
  both eras (`-32020`).
- **Limits:** 4 MiB per request, 32 requests in flight per agent (the 33rd gets 429 and a
  JSON-RPC error), S11 budgets on top.
- **Stop:** a stopped or suspended agent's `tools/call` is recorded and sealed like any other
  refusal, and a Stop cancels a call that is already running (`identity.Guard.Watch`).

## What the gateway and native endpoints don't do

- **No streaming yet.** Answers are `application/json`; progress notifications from an upstream
  are not forwarded (the bridge already reads `text/event-stream`).
- **Resources and prompts are not governed.** With `mcp.passthrough` they are forwarded and
  logged, never decided or sealed.
- **No OAuth with upstreams yet.** An upstream that needs OAuth can't be fronted until the token
  exchange S09 designed lands; static headers from the environment only.
- **Claude Desktop's remote connectors can't reach `127.0.0.1`.** They are reached from
  Anthropic's cloud, so Claude Desktop uses `deedglass mcp serve` (stdio) instead
  ([MCP_CLIENTS.md](MCP_CLIENTS.md)).
- **`Mcp-Param-*` through the bridge:** `deedglass mcp serve` doesn't read tool schemas, so it
  doesn't send `x-mcp-header` headers; a modern client calling such a tool through the bridge is
  refused with `-32020`. No native tool uses `x-mcp-header` today.
- **An approved call's result is shown in full only by the instance that ran it**, within a
  minute; afterwards `deedglass.check_approval` returns the stored result (up to
  `gate.max_result_bytes`), or says the call ran.
- **Upstream results are the upstream's word** (L0, method `reported`) until the Witness.
