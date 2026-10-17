<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/lockup-horizontal-dark.svg">
    <img src="docs/assets/lockup-horizontal-light.svg" alt="Deedglass" width="360">
  </picture>
</p>

<h3 align="center">An agent said it updated 1 row. Postgres changed 301.</h3>

<p align="center">
<!-- description:summary (docs/description.json; docs/description_test.go checks it) -->
Deedglass sits between AI agents and the systems they change. Rules decide each tool call, a check
compares what the system really changed with what the agent reported (Claimed, Checked or Proven;
Matches, Doesn't match or Can't check), and each result is sealed as a signed record anyone can
check offline. It can undo the rows an action changed.
</p>

<p align="center">
  <a href="https://github.com/deedglass/deedglass/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/deedglass/deedglass/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="https://github.com/deedglass/deedglass/releases"><img alt="Release" src="https://img.shields.io/github/v/release/deedglass/deedglass?sort=semver&label=release"></a>
  <a href="https://www.npmjs.com/package/deedglass"><img alt="npm" src="https://img.shields.io/npm/v/deedglass?label=npm"></a>
  <a href="#license"><img alt="Licence: Apache-2.0, with ee/ under FSL-1.1-ALv2" src="https://img.shields.io/badge/licence-Apache--2.0%20%2B%20ee%2F%20FSL--1.1--ALv2-0F766E"></a>
  <a href="https://pkg.go.dev/github.com/deedglass/deedglass/pkg/deedglass"><img alt="Go reference" src="https://pkg.go.dev/badge/github.com/deedglass/deedglass/pkg/deedglass.svg"></a>
  <a href="docs/ATTACK_SUITE.md"><img alt="Attack suite" src="https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fdeedglass%2Fdeedglass%2Fmain%2Fattack%2Fresults%2Fbadge.json"></a>
</p>

<p align="center">
<a href="https://deedglass.com/play">Try it in your browser</a> · <a href="https://docs.deedglass.com/quickstart/">Quickstart</a> ·
<a href="https://docs.deedglass.com">Docs</a> · <a href="SECURITY.md">Security</a> · <a href="https://deedglass.com">deedglass.com</a>
</p>

An agent was asked to mark order 42 as shipped. It ran one `UPDATE` and its tool reported 1 row. A
legacy trigger shipped 300 more orders in the same transaction. This is the real output of the
Quickstart's last step on 2026-10-14 (shortened where you see `…`):

```text
$ npx -y deedglass init --trigger
…
✕ UPDATE order 42         Proven · Doesn't match · Postgres changed 301 rows in public.orders. The agent reported 1.
  expected   UPDATE 1 row in public.orders where id = 42 · sets status
  observed   UPDATE 301 rows in public.orders · 300 outside id = 42
…
✓ Trigger off             orders_ship_open_batch dropped
✓ Undo preview            Undo available · 301 rows in public.orders · 301 updates
    policy   undo-needs-approval · a person countersigns first
```

The agent's log says 1 row. Deedglass read Postgres's own change log for that transaction, found
301, sealed the difference as a record, and planned an undo from those rows that runs only after a
person countersigns.

<p align="center">
  <img src="docs/assets/demo-hero.svg" width="846" alt="Terminal recording of a real make demo-hero run. Order 42 ships: Proven, Matches, 1 row. A legacy trigger is turned on and the same UPDATE runs: Proven, Doesn't match, Postgres changed 301 rows in public.orders, the agent reported 1, with the row diff. Then every record verifies offline.">
</p>
<p align="center"><sub>A real run of <code>make demo-hero</code> (commit 97a524c, 2026-10-14), each line shown at the time it printed.
Source: <a href="docs/assets/demo-hero.cast.json"><code>docs/assets/demo-hero.cast.json</code></a>, drawn by <a href="scripts/demo/cast2svg.mjs"><code>scripts/demo/cast2svg.mjs</code></a>.</sub></p>

## Four ways in

| | Command | What it needs | What leaves your machine |
|---|---|---|---|
| **In your browser** | [deedglass.com/play](https://deedglass.com/play) | A browser tab. Postgres runs in the page; it opens on the "said 1, changed 301" scene. | Nothing |
| **In your terminal** | `npx deedglass play` | Node. No Docker. The rules decide each call live and the records are sealed and checked on your machine; Postgres's lines are replayed from a recorded real run, and labelled as such. | Nothing |
| **Scan your setup** | `npx deedglass scan` | Node. Reads your MCP configs and, if you name one, a database catalog, read-only. Grades what your agents could change, A to F. | Nothing |
| **Observe mode** | `deedglass observe run --connector pg-demo -- ./agent.sh` | A Postgres connector with `observe` on. Nothing to rewire: it reads Postgres's change log beside whatever gate you already use. | Nothing |

When a scan finds a trigger or a cascade on a table your agent can write, it says so and points to
the 301 scene. Observe mode is [below](#observe-mode-nothing-to-rewire).

## Quickstart

Docker running, Node 22.12 or newer, then (from the [tested Quickstart](https://docs.deedglass.com/quickstart/)):

<!-- quickstart:begin (scripted copy of docs-site/src/content/docs/quickstart.mdx; docs/readme_test.go compares them) -->
```sh
mkdir acme-agents && cd acme-agents
npx -y deedglass init --yes
npx -y deedglass doctor
npx -y deedglass init --trigger
```
<!-- quickstart:end -->

`deedglass init` starts two Postgres 18.6 containers in Docker (Deedglass's own database and a demo
database), runs `deedglass serve` on `127.0.0.1:8080`, creates an org, an agent and the
`postgres-safe` policy, and ends with a blocked `DROP TABLE` and an `UPDATE` checked at Proven.
`deedglass doctor` runs sixteen read-only checks, each with its fix. `deedglass init --trigger` turns
on the demo database's legacy trigger, sends the same one-row `UPDATE`, and ends on the 301-row
mismatch and the undo preview shown above.

## Observe mode: nothing to rewire

```sh
deedglass observe run --connector pg-demo -- ./agent.sh   # open a watched session, run the agent, close it
deedglass observe show obs_01k… --undo-sql                # the undo as SQL you review and run (free)
```

Turn it on with `witness.observe.enabled: true` in the connector's config. Observe mode reads only
Postgres's change log, through the role and replication slot that `deedglass connector postgres init`
creates, so it works beside any gate you already use, or none.
It shows every row committed on the watched tables while the session was open and seals the session
as a record. It can't tell which program wrote a row, so a session gets no Matches or Doesn't match
verdict; row images are kept 30 days; a `TRUNCATE` can't be undone from the record. Undo SQL is free;
a one-step undo through Deedglass is Pro. Details: [`docs/OBSERVE.md`](docs/OBSERVE.md).

## How it works

```mermaid
flowchart LR
  A["AI agents<br/>Claude · OpenAI · Cursor · any MCP client"] -->|tool call + agent key| G
  subgraph D["Deedglass · one binary + Postgres"]
    I[Identity] --> P[Policies] --> G[Rules]
    G --> W[Check] --> S[Records] --> L[Ledger] --> N[Insights]
  end
  G -->|credentials from the vault| T["Tools<br/>Postgres · GitHub · Slack · Stripe · MCP servers"]
  T -->|what actually happened| W
  H[People] -->|approve · stop| G
```

Every tool call passes through Deedglass with the agent's key. Your rules (policies) decide `allow`,
`deny` or `require_approval`; Deedglass runs the call with credentials the agent never holds; the
check compares what happened with what was asked; and the outcome is sealed as a record in the org's
chain.

## How sure each check is

| Level | What it checks | What it can't see |
|---|---|---|
| **Claimed** | The tool's own answer, against a schema and your postconditions | Whether the answer is true |
| **Checked** | A read-back from the target (GitHub, Slack, Stripe, Notion, REST, Postgres rows) | Side effects the read-back doesn't cover |
| **Proven** | Postgres's change log (logical decoding): every row the transaction committed | DDL, sequences, unlogged tables and anything outside that database's WAL |

Each record claims one level and one result: **Matches**, **Doesn't match** or **Can't check**. When
Deedglass can't tell whether a tool ran, the result is Can't check, never Matches. The API keeps
the short names (`L0`/`L1`/`L2`, `VERIFIED`/`MISMATCH`/`UNVERIFIABLE`); the
[glossary](https://docs.deedglass.com/concepts/glossary/) maps them.

## What it doesn't do

- Proven checks cover Postgres only, and need `wal_level=logical` and `REPLICA IDENTITY FULL` on watched tables.
- A hijacked agent with a valid key and an in-policy action is allowed. Policies, budgets and
  approvals narrow this; they don't close it.
- Deedglass trusts the Postgres server, its superusers, the host it runs on and its seal key. It
  can't protect you from a compromised host.
- Records hold digests, not payloads. One set of seal keys signs every org on an instance.
- It doesn't replace a least-privilege database role. Use both: the role limits what the agent can
  do; Deedglass checks what it did with that.

The whole list, with workarounds: [`docs/LIMITATIONS.md`](docs/LIMITATIONS.md) and [`KNOWN_ISSUES.md`](KNOWN_ISSUES.md).

## Alternatives

You may not need Deedglass, or you may want it next to one of these. Each has a longer page, with
sources and the date each was checked, at [deedglass.com/compare](https://deedglass.com/compare).
<!-- claims: C-109 (role), C-114 and C-116 (branches), C-117 (Docker's gateway), C-122 and C-123 (traces), C-110 (Claw Patrol), C-119 (Pipelock) -->

| If you need | Use this | Where Deedglass fits |
|---|---|---|
| An agent that only reads | A Postgres role with `SELECT` and nothing else. It can't change data whatever the agent sends, and there's nothing to install. | You don't need it. It's for agents that must write. ([compare](https://deedglass.com/compare/read-only-postgres-role)) |
| A place for an agent to try changes | A database branch: a Neon branch is a copy-on-write clone of your data; Supabase branches start without production data. | For the writes that must reach production: rules first, then the check and the record. ([compare](https://deedglass.com/compare/database-branches)) |
| Routing, isolation and credentials for many MCP servers | An MCP gateway, such as Docker's, which runs each server in an isolated container and injects credentials. | Behind the gateway, for the servers that write to Postgres. ([compare](https://deedglass.com/compare/mcp-gateways)) |
| Traces of each step, and evaluations | Langfuse or LangSmith. | Next to them: a trace is the agent's side of the story; the check reads what Postgres committed. ([compare](https://deedglass.com/compare/langfuse-langsmith)) |
| Rules at the wire for Postgres, Kubernetes and HTTP | Claw Patrol, which gates each action against rules over the parsed traffic. | It also decides first; it adds the check of what Postgres committed, records anyone can check offline, and undo. ([compare](https://deedglass.com/compare/claw-patrol)) |
| Scanning what an agent sends out and what its tools return | Pipelock, an agent firewall that scans MCP traffic in both directions. | It adds the check of what Postgres committed after each call, and undo for the rows. ([compare](https://deedglass.com/compare/pipelock)) |

## Install

| How | Command |
|---|---|
| npx (no install) | `npx -y deedglass help` |
| Install script (macOS, Linux) | `curl -fsSL https://raw.githubusercontent.com/deedglass/deedglass/main/scripts/install.sh \| sh` |
| Binaries | archives for macOS, Linux and Windows on the [latest release](https://github.com/deedglass/deedglass/releases/latest), with `checksums.txt` |
| Go | `go install github.com/deedglass/deedglass/cmd/deedglass@latest` (the CLI, without the Console) |
| Docker | build the image from a checkout: `make web-build docker` (tags `deedglass:dev`; Node 24 builds the Console) |

The install script downloads the release archive for your platform, checks its sha256 against the
release's `checksums.txt`, and copies `deedglass` into `~/.local/bin`; read it first:
[`scripts/install.sh`](scripts/install.sh). A public container image is coming; until it's
published, build the image locally as above. Checksums, signatures and SBOMs:
[`docs/INSTALL.md`](docs/INSTALL.md).

### Add it to your agent client

<!-- install-links:start -->
[![Add to Cursor](https://cursor.com/deeplink/mcp-install-dark.svg)](https://cursor.com/en/install-mcp?name=deedglass&config=eyJjb21tYW5kIjoibnB4IiwiYXJncyI6WyIteSIsImRlZWRnbGFzcyIsIm1jcCIsInNlcnZlIl19)
[Install in VS Code](https://vscode.dev/redirect/mcp/install?config=%7B%22command%22%3A%22npx%22%2C%22args%22%3A%5B%22-y%22%2C%22deedglass%22%2C%22mcp%22%2C%22serve%22%5D%7D&name=deedglass) · [LM Studio](https://deedglass.com/install#lm-studio) · [Goose](https://deedglass.com/install#goose) · [Claude Desktop](https://deedglass.com/install#claude-desktop) · [Every client](https://deedglass.com/install)

```sh
claude mcp add deedglass -- npx -y deedglass mcp serve
```

No link holds a key. Run `npx deedglass init` first; each client asks Deedglass for the key it wrote.
<!-- install-links:end -->

**Claude Code plugin** (the MCP server, a `/deedglass:scan` command, and skills for reading a
scan and for giving an agent Postgres access safely), from this repo's own marketplace:

```text
/plugin marketplace add deedglass/deedglass
/plugin install deedglass@deedglass
```

Every format, with its source: [`docs/INSTALL_CLIENTS.md`](docs/INSTALL_CLIENTS.md).

## SDKs and clients

- **TypeScript:** `@deedglass/sdk`, with adapters for the AI SDK, the OpenAI Agents SDK, LangChain and the Claude Agent SDK.
- **Python:** `pip install deedglass`, with LangChain and Claude Agent SDK adapters.
- **Go:** `github.com/deedglass/deedglass/pkg/deedglass`.
- **MCP:** `deedglass mcp wrap` governs any stdio MCP server; `deedglass mcp serve` governs remote ones
  for Claude Desktop, Claude Code, Cursor, VS Code and Windsurf.

## Connectors

Postgres (Proven), REST, GitHub, Slack, Stripe (test mode only), Gmail drafts, Notion and remote MCP
servers. Credentials live in an encrypted vault; every outbound call goes through one egress door.
See [`docs/CONNECTORS.md`](docs/CONNECTORS.md).

## Numbers

<!-- numbers:begin (from bench/results/SUMMARY.json; docs/readme_test.go checks every row; an Apple M1 laptop on AC power, Postgres on the same machine) -->
| What | Measured | Target |
|---|---|---|
| Gate · allow, no witness | p50 5.81 ms · p99 19.4 ms | p50 < 5 ms · p99 < 25 ms (p50 missed) |
| Policy · 50 rules | p99 148 µs | p99 < 1 ms |
| Witness · L2 at 100 writes/s | p50 118 ms · p99 317 ms | p50 < 1 s |
| Verify · 100,000 deeds | 1.53 s · 80.9 MiB | < 5 s · < 256 MiB |
| Stop · across 2 instances | max 23.5 ms over 20 trials | < 2 s |
<!-- numbers:end -->

How they were measured, and the misses: [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md).

## Security

The attack suite runs 70 cases against a real server and real Postgres on every release candidate:
70 matched, 0 differ (`attack/results/latest.json`, [`docs/ATTACK_SUITE.md`](docs/ATTACK_SUITE.md)).
Report a vulnerability privately: [`SECURITY.md`](SECURITY.md).

<!-- description:network -->
There's no telemetry. With Pro, the licence check contacts Dodo Payments and deedglass.com at most
once a day, and only when a Pro feature runs; `deedglass version --check` contacts GitHub only when
you run it.
`DEEDGLASS_NO_NETWORK=1` stops the licence check; Pro then runs on its cached token for up to 14 days.

## Contributing

Read [`CONTRIBUTING.md`](CONTRIBUTING.md): `make dev`, `make ci`, the tests a change needs, and DCO
sign-off (`git commit -s`). Questions go to Discussions ([`SUPPORT.md`](SUPPORT.md)); how decisions
are made is in [`GOVERNANCE.md`](GOVERNANCE.md).

If Deedglass showed you something your logs didn't, a GitHub star helps other people find it.

## License

<!-- description:licence -->
The core, the SDKs and the CLI are Apache-2.0. The Pro features in `ee/` are FSL-1.1-ALv2, and each
release of them becomes Apache-2.0 two years after it ships. The release binary, the
`@deedglass/cli-*` npm packages and the container image include `ee/`, so their licence is
`Apache-2.0 AND FSL-1.1-ALv2`.

FSL-1.1-ALv2 lets you read, run and change `ee/` for anything except offering a competing product
([`ee/LICENSE`](ee/LICENSE)); everything Deedglass does for safety is free and outside `ee/`
([`docs/LICENSING.md`](docs/LICENSING.md)). The rest is [`LICENSE`](LICENSE); third-party notices
are in [`NOTICE`](NOTICE).
