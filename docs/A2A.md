# A2A: delegations to other agents

`deedglass a2a proxy` puts the Gate between your agent and a remote agent it reaches over the
Agent2Agent (A2A) protocol (TY-07, ADR-113). It works like `deedglass mcp wrap` does for MCP
servers: when your agent asks the remote agent to do something, the Gate decides first, the call is
forwarded only if it is allowed, and what the remote agent answered is recorded.

What Deedglass sees here is the delegation, not the work. The remote agent runs somewhere else, so
its answer is recorded as **Claimed**: what the peer reported, nothing more. When the remote agent's
own tool calls also go through Deedglass (its own `mcp wrap`, gateway or connectors), each of those
is a separate action with its own Checked or Proven verdict, and it carries the same delegation
chain, so you can follow a request from the person to the row that changed.

## The spec this follows

A2A **v1.0.1**, as published by its project: `github.com/a2aproject/A2A`, tag `v1.0.1`
(2026-05-28). The proxy implements the JSON-RPC binding (spec §9) with ProtoJSON field names (§5.5)
and enum values such as `TASK_STATE_COMPLETED`. The proto it was checked against,
`specification/a2a.proto` at that tag, has SHA-256
`e195bf96ab630c69797851970203e1b2b6b19528f2e9803b7d904b91a5104016`. Only the fields the proxy reads
are modelled; everything else passes through as bytes.

## Run it

```sh
export DEEDGLASS_API_KEY=dg_…            # the calling agent's key
export RESEARCH_AUTH="Bearer …"          # your credential for the remote agent, if it needs one
deedglass a2a proxy --connector researcher \
  --peer https://research.example.com/a2a/v1 \
  --peer-header-env Authorization=RESEARCH_AUTH \
  --on-behalf-of u_5780
```

Then point your agent's A2A client at `http://127.0.0.1:7420/` instead of the remote agent. The
proxy serves the remote agent's Agent Card at `/.well-known/agent-card.json` with its JSON-RPC
interface pointing at the proxy, so a client that discovers agents by their card finds the proxy.

- The proxy listens on loopback only, and every JSON-RPC request must carry the agent's key the
  proxy runs with: `Authorization: Bearer dg_…` (the same `DEEDGLASS_API_KEY`). A request without
  it, or with another key, is answered HTTP 401 and nothing is forwarded. The card at
  `/.well-known/agent-card.json` needs no key.
- Web pages can't drive it. A request with an `Origin` header, a `Sec-Fetch-Site` other than
  `none`, a `Host` that isn't `127.0.0.1`, `[::1]` or `localhost` (DNS rebinding), or a body that
  isn't `Content-Type: application/json` is refused before anything else; CORS preflights get 403
  and no `Access-Control-*` header is ever sent. These are the checks the server applies to
  `/mcp/*`; attack group T (T01 to T05) runs them.
- Pass the key in `DEEDGLASS_API_KEY`, not `--api-key`: a flag shows in `ps` to every user on the
  machine, and the proxy warns when it's used.
- The agent's key goes to the Deedglass server only. The calling agent's own credentials
  (`Authorization`, cookies) are removed before anything is sent to the peer; the peer gets only
  the headers you name with `--peer-header-env`.
- Peers are reached through the same egress rules as connectors: `https` only, public addresses
  only, unless `DEEDGLASS_ENV=dev` with `DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1`, or the range is in
  `DEEDGLASS_EGRESS_ALLOW_CIDRS`.
- Every request needs the `A2A-Version: 1.0` header. A request without it is A2A 0.3 by the spec's
  rule and is refused with `VersionNotSupportedError` (-32009). The `?A2A-Version=` query parameter
  isn't read: a plain link or form could set it.

## What goes through the Gate

| Method | What happens |
|---|---|
| `SendMessage`, `SendStreamingMessage` | An action, tool `<connector>.SendMessage` (or `.SendStreamingMessage`). The chain is added to the message. |
| `CancelTask` | An action, `<connector>.CancelTask`. |
| `CreateTaskPushNotificationConfig`, `DeleteTaskPushNotificationConfig` | An action each: they make the peer call a URL later. |
| `GetTask`, `ListTasks`, `SubscribeToTask`, `GetTaskPushNotificationConfig`, `ListTaskPushNotificationConfigs`, `GetExtendedAgentCard` | Passed through, not recorded (reads). |
| Anything else, and batches | Refused with -32601 or -32600. Nothing is forwarded. |

Actions are proxy mode with connector type `a2a`, so policies can match them by tool or by type:

```yaml
rules:
  - id: no-cancel-by-delegation
    match: { tool: "*.CancelTask", connector_type: a2a }
    decision: deny
    reason: Peers' tasks are cancelled by a person, not by another agent.
  - id: allow-a2a-delegation
    match: { connector_type: a2a }
    decision: allow
```

The action's arguments are `{"peer": {"url", "name"}, "delegation": [hops], "request": <params as
forwarded>}`, so a rule's `when` can read the message too.

### What the caller gets back

The peer's answer reaches the caller byte for byte. When the Gate says no, the caller gets a JSON-RPC
error from the proxy instead, with a `google.rpc.ErrorInfo` detail (domain `deedglass.com`) that
names the action:

| Code | Reason | Meaning |
|---|---|---|
| -31001 | `DEEDGLASS_DENIED` | The policy, a budget or Stop refused it. It is recorded; nothing reached the peer. |
| -31002 | `DEEDGLASS_PENDING_APPROVAL` | A person has to approve it. Send the same request again: once approved, it runs once, on the same action. |
| -31003 | `DEEDGLASS_UNAVAILABLE`, `DEEDGLASS_BAD_KEY`, `DEEDGLASS_REJECTED` | No decision (the server is unreachable, the key is wrong, or the request was refused before it was recorded). Nothing was forwarded. |

These codes are outside the ranges JSON-RPC 2.0 and A2A reserve (-32768 to -32000).
`--approval-wait` (default 10 s, at most 25 s) is how long one call waits for a decision before
answering -31002.

### How the peer's answer is recorded

| The peer answered | Execution | error_code |
|---|---|---|
| A task in `TASK_STATE_COMPLETED`, or a message (no task) | succeeded | |
| A task in `TASK_STATE_FAILED`, `REJECTED` or `CANCELED` | failed | `task_failed`, `task_rejected`, `task_canceled` |
| A task still `SUBMITTED`, `WORKING`, `INPUT_REQUIRED` or `AUTH_REQUIRED` | unknown | `task_working` and so on |
| A JSON-RPC error | failed (the peer says it didn't act) | `task_not_found`, `unsupported_operation`, … (A2A §5.4) |
| JSON-RPC `InternalError` (-32603), no answer, a timeout, an unreadable answer | unknown | `peer_internal_error`, `peer_no_answer`, `timeout`, `peer_answer_unreadable` |
| The connection never opened | failed | `peer_unreachable` |

For `CancelTask`, `TASK_STATE_CANCELED` is the success. For a stream, the last event decides. An
unknown execution is sealed as UNVERIFIABLE, which is the honest outcome: Deedglass doesn't follow the
task afterwards. The answer itself is kept as the action's result (up to 256 KiB with
`--result-mode full`; its digest otherwise).

## The delegation chain

Who asked whom, from the person to the peer, travels with the message as an A2A extension, the way
spec §4.6 says extensions carry data: in `message.metadata` under the URI
`https://deedglass.com/a2a/delegation/v1`, with the URI listed in `message.extensions` and in the
`A2A-Extensions` header.

```json
"metadata": {
  "https://deedglass.com/a2a/delegation/v1": {
    "chain": [
      {"kind": "person", "ref": "u_5780"},
      {"kind": "agent", "ref": "agt_01m5…", "name": "planner"}
    ]
  }
}
```

- `kind` is `person` (first hop only) or `agent`; `ref` uses the `on_behalf_of` character set (1 to
  256 of letters, digits and `. _ ~ : @ / + = -`); `name` is at most 64 bytes. At most 16 hops; no
  other members.
- The proxy reads the chain the caller sent, puts the configured `--on-behalf-of` first when the
  chain names no person, adds its own agent (from its key), and forwards that. The recorded chain
  ends with the peer (its card's name, and its URL as `ref`).
- The person becomes the action's `on_behalf_of`. A chain that names a different person than
  `--on-behalf-of` is refused (-32602).
- A remote agent that delegates further passes the chain it received on in its own requests. When
  it does that through its own `deedglass a2a proxy`, the next action shows the longer chain.

`deedglass actions show` prints it:

```text
analyst.SendMessage · researcher on behalf of u_5780 · 2026-10-20 10:00:00 UTC
  chain      u_5780 → planner → researcher → Analyst
  decision   allow · rule allow-a2a-delegation
  check      Claimed · Matches · edge_reported
```

In the record, the chain is part of the action's arguments, which the deed binds through
`request_digest`, and the person is the deed's `on_behalf_of`. The deed format is unchanged.

**Who vouches for which hop.** The last two hops are Deedglass's own: the agent is the one whose key
made the call, and the peer is the URL the operator configured. Every hop before them is what the
calling agent sent, exactly as `on_behalf_of` is what an agent sends. Treat them as Claimed.

## Tests

`internal/a2a` runs every row above against a fake A2A peer on loopback (`internal/a2a/a2atest`) and a
fake Gate, with goldens of the whole exchange in `internal/a2a/testdata/golden`, and a two-agent
delegation through two proxies. `cmd/deedglass` `TestA2ALive` does the same on a real server and
database: two delegations sealed Claimed with the full chain, `actions show`, the deed's
`request_digest` recomputed from the arguments, a denied `CancelTask` that never reaches the peer, and
the export verified. No test uses the network.

Limits are listed in [LIMITATIONS.md](LIMITATIONS.md#a2a-delegations).
