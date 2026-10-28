# Known issues

What doesn't work yet in Deedglass 0.15.0, who it affects and what to do today. None of these
blocked the 0.15.0 release (2026-10-28). Limits that come from the design, and won't change soon,
are in [`docs/LIMITATIONS.md`](docs/LIMITATIONS.md). To report something new, open an issue:
[github.com/deedglass/deedglass/issues](https://github.com/deedglass/deedglass/issues).

## The app (`npx deedglass`)

| Id | What happens | Who is affected | What to do today |
|---|---|---|---|
| KI-011 | The app runs on one computer only. It listens on 127.0.0.1, so colleagues can't open it from their own computers. | Teams that want several people to approve or review in one place | Run the shared server instead: [Production](https://docs.deedglass.com/guides/production/). Each person can also run their own app. |
| KI-012 | The first start needs PostgreSQL. If PostgreSQL 16 or newer isn't on the computer, the app downloads PostgreSQL 18.6 once (14 to 71 MB, depending on the system). A ready-made download exists for macOS, Linux (Intel and Arm) and Windows (Intel) only. | Computers with no internet on first start; other systems, such as Windows on Arm | With no internet: download the file on another computer and set `DEEDGLASS_PG_BUNDLE` to it. On other systems: install PostgreSQL 16 or newer, or set `DEEDGLASS_PG_BIN` to its `bin` folder. |
| KI-013 | Claude Desktop has no one-click install yet. The `.mcpb` bundles aren't attached to the release. | People connecting Claude Desktop | Use the app's **Connect** screen: it shows the settings to paste into Claude Desktop, step by step. |

## Setup and installs

| Id | What happens | Who is affected | What to do today |
|---|---|---|---|
| KI-014 | There is no public container image yet. | People who deploy with Docker or Kubernetes | Build the image from a checkout: `make web-build docker`. Or use the release binary. |
| KI-010 | `make demo` runs offline for six of its seven scenes. The Stripe refund scene needs Stripe test mode and Slack (`make demo-live`). | People running the full demo from a checkout | None needed. The scene says it was skipped and why. |
| KI-008 | Three high npm advisories (`braces`, no fixed release exists) sit in the docs site's build tools. Nothing from them is in the published site or the product (SR-019). | People who build the docs site themselves | None needed. |

## Checks and undo

| Id | What happens | Who is affected | What to do today |
|---|---|---|---|
| KI-001 | The Postgres check says **Can't check** for a table without `REPLICA IDENTITY FULL` when it needs the old values of a row. | Postgres users who skip `deedglass connector postgres init` | Run `deedglass connector postgres check` and apply the SQL it prints. |
| KI-006 | TRUNCATE can't be undone: Postgres's change log keeps no rows for it. Undo also refuses while the table has an enabled trigger. | Tables an agent may truncate | Block TRUNCATE with a rule (the `postgres-safe` pack does). For triggers, turn the trigger off first; the undo preview prints the exact SQL. |
| KI-007 | `dg_test_` and `dg_live_` keys are recorded on every action, but test and live data aren't kept apart yet. | Teams that want a test mode on one server | Use a separate team (org) or a separate server for testing. |

## Large or busy servers

These were measured on 0.1.0 and haven't changed since.

| Id | What happens | Who is affected | What to do today |
|---|---|---|---|
| KI-002 | On a laptop with Postgres on the same machine, an allowed action without a check takes 5.8 ms at the median; the target is under 5 ms. Two Postgres round trips per action cause it. | Anyone timing Deedglass on a laptop | None needed for correctness. The p99 (19.4 ms) is inside its 25 ms target. |
| KI-003 | One team (org) seals about 230 records a second on an M1 laptop. Above that, Postgres checks queue up: at 500 writes a second for 20 s, every write matched, with a median of 39 s from commit to result. | Agents that write more than about 200 times a second to one org | Spread agents across orgs, or group writes into fewer actions. |
| KI-004 | `deedglass serve` has no limit of its own on open connections. A client that opens a new connection per request can use up the process's open-file limit; new connections then fail with `EOF`. Issues [#11](https://github.com/deedglass/deedglass/issues/11) and [#12](https://github.com/deedglass/deedglass/issues/12). | Load tests with unlimited clients; servers without a proxy in front | Put a proxy that caps connections in front of the server, raise `ulimit -n`, and cap your client's connections. |
| KI-005 | In one 30-minute run at 50 calls a second, memory grew from 43.2 MiB to 49.9 MiB, below the 57.1 MiB peak at minute 2. Connections and open files stayed flat, so it looks like normal settling, not a leak. | Long-running servers | None needed at this size. Watch `process_resident_memory_bytes`. |

## Fixed

- KI-009: sealing a file (`deedglass deeds seal-file`, `deedglass verify --file`) shipped after
  0.1.0-rc.1.
