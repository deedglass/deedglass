# The Acme demo stack

A small, realistic setup for trying Deedglass in five minutes and filming it in one take: an
"Acme Store" orders database, Deedglass in front of it, the `acme-demo` policy, three agents with
keys, and ready-to-paste MCP client config. Everything runs on your machine; every port binds to
`127.0.0.1`.

## Quickstart

```sh
make demo-up                              # build, start, seed: ends with "Demo ready."
make demo-snippet CLIENT=claude-desktop   # the config to paste into Claude Desktop
make demo-follow                          # in a second terminal: every action as it is decided
```

Paste the snippet into Claude Desktop › Settings › Developer › Edit Config, replace
`dg_test_REPLACE_ME` with the `claude-desktop` key from `examples/demo/.demo-keys` (or run
`make demo-snippet CLIENT=claude-desktop KEYS=1` to fill it in), then quit and reopen Claude
Desktop. `SCRIPT.md` is the shot-by-shot script.

Port 8080 taken (by `deedglass serve` or `make dev`)? Use another: `make demo-up DEMO_PORT=8091`;
the snippets follow it.

## What runs

| Service | Image | Port (127.0.0.1) | What it is |
|---|---|---|---|
| `ledger-db` | `postgres:18.6@sha256:5a5a84b1…9722` | 55433 | Deedglass's own database: actions, Ledger, deeds |
| `orders-db` | `postgres:18.6@sha256:5a5a84b1…9722` | 55434 | "Acme Store", database `acme`: 120 customers, 40 products, 500 orders (37 cancelled, order 42 paid), about 1,250 order items, 60 refunds. `wal_level=logical` for the Witness later |
| `deedglass` | `deedglass:dev` (the repo's Dockerfile) | 8080 (`DEMO_PORT`) | `migrate up`, then `serve`, with `deedglass.demo.yaml` |
| `everything` (`HTTP=1`) | `node:24-alpine@sha256:ebfe2f90…c1c1` running `@modelcontextprotocol/server-everything@2026.8.31 streamableHttp` | 3001 | An upstream MCP server for the gateway: `/mcp/everything` |

The MCP servers your client starts are not services. The client runs them over stdio:

| Client entry | What it runs | Through Deedglass |
|---|---|---|
| `orders` | `crystaldba/postgres-mcp:0.3.0@sha256:dbbd3468…6b6b` in Docker, on `acme` | yes: `deedglass mcp wrap --connector orders --connector-type postgres` |
| `orders-direct` | the same server | **no**: for the contrast shot only |
| `scratch` | `@modelcontextprotocol/server-filesystem@2026.8.31` on `/tmp/deedglass-demo` | yes: `deedglass mcp wrap --connector scratch` |
| `deedglass` | the Gate's native tools (`fake.*` in dev) | it is Deedglass: `deedglass mcp serve` |

The `acme-demo` policy (`policies/acme-demo.yaml`, 42 cases): reads of the orders database run;
`DROP` never runs; schema changes, privileges, server code and multi-statement SQL never run;
`UPDATE` and `DELETE` need a `WHERE`; orders are never deleted; any other write waits for a person
to countersign; schema browsing and plain `EXPLAIN` run; at most 30 calls a minute per agent.

## Commands

| Command | What it does |
|---|---|
| `make demo-up [HTTP=1] [DEMO_PORT=8091]` | Writes `.env` (random local secrets, 0600) and a seal key on the first run, builds and starts the stack, seeds org `acme-demo`, agents `claude-desktop`, `claude-code`, `cursor` (keys in `.demo-keys`, 0600) and the policy. Running it again is a no-op with ✓ lines. |
| `make demo-status` | One ✓ or ✕ line per part, including that every port is on 127.0.0.1. |
| `make demo-snippet CLIENT=…` | `claude-desktop`, `claude-code`, `cursor`, `vscode` or `windsurf`. `KEYS=1` fills in the agent's key. Golden copies with placeholders are in `clients/`. |
| `make demo-follow` | `deedglass actions list --follow --connector orders` against the demo's Ledger (`CONNECTOR=everything` for the gateway). |
| `make demo-verify [SINCE=10m]` | Exports the recent deeds and verifies them: `✓ n deeds · chain intact`. |
| `make demo-psql` | `psql` on `acme` as `acme_owner`. |
| `make demo-reset` | Reloads `acme` from `sql/` and resets `/tmp/deedglass-demo/allowed/hello.txt`, for a new take; the old replication slot went with the database, so it is recorded as dropped and the decoder starts a new one. The Ledger keeps every deed. `HARD=1` also wipes the Ledger and re-seeds (new keys). |
| `make demo-smoke` | `TestDemoScript`: plays `SCRIPT.md` through the real wrapper and Postgres MCP server, then checks Postgres and the chain. |
| `make demo-hero` | S21's hero in one command (`scripts/hero.sh`): trigger off → `L2 · VERIFIED`; trigger on → `L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.`; the diff; verify. `HERO_PACE=2` paces it for recording ([SCRIPT.md §Hero](SCRIPT.md#hero-the-l2-clip-s21)). |
| `make demo-hero-check` | `TestHeroScript`: `demo-hero` twice, with `demo-reset` between. |
| `make demo-diff [ID=act_…]` | `deedglass actions show --diff` for the action (default: the newest `postgres.execute` one). |
| `make demo-trigger-on` / `demo-trigger-off` | The hero's legacy trigger (`sql/10_trigger_on.sql`, `sql/11_trigger_off.sql`) on the demo's `orders-db` only (127.0.0.1:55434). |
| `make demo-order-reset [ORDER=42]` | One order back to `paid`, or every order back to the seed's status (`sql/12_order_statuses.sql`), as `acme_owner`. |
| `make demo-down` | Stops the stack; the data stays. |

The gateway with the `everything` upstream:

```sh
KEY=$(grep '^claude-code=' examples/demo/.demo-keys | cut -d= -f2-)
curl -s localhost:8080/mcp/everything -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' -H 'Mcp-Name: echo' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'
```

## Honest notes

- **Both `orders` entries connect as the database owner, on purpose**, so the only difference on
  screen is Deedglass in the path. In real life an agent never gets owner credentials; S17's native
  Postgres connector uses a least-privilege role with its password in the vault (S16).
- **The secrets are local-only.** `.env`, `.demo-keys` and `.demo/` are random, 0600 and
  git-ignored. `DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1` exists only in this stack, so the gateway can
  reach the `everything` container. This stack is for local demos; never expose it.
- **`orders-direct` bypasses Deedglass.** It exists to show the difference, nothing else.
- **Approving "mark order 42 as shipped"** arrives with S22 (email link and Console); today the
  action waits, and expires after 30 minutes.
- The first `make demo-up` builds the Deedglass image and pulls about 1 GB of images; later runs
  take seconds. `HTTP=1` downloads the `everything` server from npm on its first start.

## Remove everything

```sh
make demo-down && docker volume rm deedglass-demo_ledger-data deedglass-demo_orders-data
rm -rf examples/demo/.env examples/demo/.demo-keys examples/demo/.demo /tmp/deedglass-demo
```
