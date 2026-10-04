# Overview metrics: `GET /v1/overview`

The Console's first screen (`/app`) shows a handful of numbers about your org. This page defines
every one of them exactly: which rows are counted, over which window, in which time zone, and
when an item asks for your attention. The numbers come from one route, `GET /v1/overview`; the
Console only formats them.

## Who can read it

| Credential | Answer |
|---|---|
| A Console session (viewer and up) | The overview of the session's org |
| An org admin key | The overview of the key's org |
| An agent key | `403 insufficient_scope`. The Overview is a Console screen; an agent reads its own actions with `GET /v1/actions`. |
| No credential | `401 authentication_required` |

Every read runs in one read-only transaction scoped to your org by row-level security, with a
2 second statement timeout. Nothing from another org is counted, listed or named.

## Request

```
GET /v1/overview?window=24h
```

| Parameter | Values |
|---|---|
| `window` | `24h` (the default) or `7d`, sent once |

Any other value, a repeated `window`, or any other parameter is `400 validation_failed` with
`param` naming it.

## The window

| Window | Buckets | Bucket size |
|---|---|---|
| `24h` | 24 | 1 hour |
| `7d` | 28 | 6 hours |

- Buckets are aligned on UTC: hours start at `:00`, six-hour buckets at 00:00, 06:00, 12:00 and
  18:00 UTC. Daylight saving time never moves them.
- `to` is the first bucket boundary at or after the moment of the read. The bucket you are in now
  is the last one in every series, and it is partial.
- `from` is `to` minus the window (24 hours or 7 days).
- An action is in the window when `from <= created_at < to`. An action created exactly at `from`
  counts; one created exactly at `to` does not.
- `created_at` is when Deedglass received the action, not when it finished or was sealed.
- Each series lists its buckets oldest first; the sum of a series is its `total`.
- `previous` is the same count over the window just before: `from - window <= created_at < from`.
  The Console shows the change from `previous` to `total`.

`from`, `to` and every other time are RFC 3339 in UTC with milliseconds, like the rest of the API.
`timezone` is your org's time zone (Settings). It decides one thing on this screen: where "today"
starts for the agents' usage.

## Actions and blocked

- `actions.total`: every action created in the window, whatever happened to it.
- `blocked.total`: the actions in the window whose decision is `deny`. A deny comes from a policy
  rule, a budget (`budget_exceeded`), Stop (a stopped or suspended agent, or a stopped org), or a
  tool name no connector serves (`tool_unknown`). The Gate records every one of these with the
  decision `deny`. An action that waited for approval and was then denied or expired keeps the
  decision `require_approval`, so it is not counted as blocked.

## Verification

Counted over the actions created in the window, by the witness verdict stored on each action
(the latest one, after any amendment):

- `verified`, `mismatch`, `unverifiable`: actions with that verdict.
- `not_witnessed`: `total - verified - mismatch - unverifiable`. These are actions with no verdict
  yet or ever: denied before they ran, failed before execution, waiting for approval, or still in
  flight.
- `by_level`: witnessed actions (any verdict) by witness level, `L0` Reported, `L1` Confirmed,
  `L2` Witnessed. The three always add up to `verified + mismatch + unverifiable`.
- `series`: one series per verdict, with the window's buckets.

## Approvals

Approvals are counted whatever the window, because a pending approval matters until someone
decides it.

- `pending`: every approval with status `pending`.
- `expiring_soon`: pending approvals whose `expires_at` is less than 15 minutes away.
- `next_expires_at`: the earliest `expires_at` of a pending approval, or null when none is pending.

## Agents

`agents` is a list of at most 8 agents: the most recently active first (by the newest action's
`created_at`), then agents with no action, by name. The org's system agent (the one the Console
uses for undo) is never listed. `has_more` is true when the org has more agents than shown.

Each agent carries:

- `status`: `active`, `suspended` or `stopped`.
- `last_action_at`: when its newest action was created, or null.
- `usage.calls_today`: calls counted today, where today starts at midnight in the org's time zone.
- `usage.spend_today`: spend counted today, in minor units, in the currency of the agent's
  spend budget (usd when it has none); null when nothing was spent today and the agent has no
  spend budget.
- `budgets`: the limits set on the agent (`calls_per_minute`, `calls_per_day`, `spend_per_day`).
  A limit that isn't set is left out.

The usage numbers are the counters the Gate reserves and the budgets check (docs/BUDGETS.md),
read for the same windows the budgets use. They are not recounted from the actions, so they match
what a budget would see.

The same counters are on the agent routes with `?expand[]=usage` (also `?expand=usage`):
`GET /v1/agents` and `GET /v1/agents/{id}` then add `usage` (`calls_minute` for the current UTC
minute, `calls_hour` for the current UTC hour, `calls_today` and `spend_today` as above) and
`last_action_at` to each agent. A list page is read in one batch, never one query per agent.
Without the expansion the agent object is unchanged.

## Attention

`attention` holds at most 8 items. Each item has a `kind`, a `severity`, a time `at`, a `ref` (the
id the Console links to, or null) and `data`. The server sends kinds and data, never sentences;
the Console turns them into words. The one exception is a mismatch's witness summary, which is
the Witness's own sentence, verbatim.

Items come in the priority order of the table below. The 8 slots are filled in this order:

1. Every system item (`chain_broken`, `org_stopped`, `slot_invalidated`, `slot_lagging`), up to
   all 8. System items are never dropped to make room for anything else.
2. Up to 3 `approval_expiring` items, then up to 2 `budget_near` items, from the slots left.
3. `mismatch` items fill what is left.
4. If slots still remain, more `approval_expiring` items, then more `budget_near` items.

So 20 mismatches, 4 expiring approvals and 3 budget warnings show as 3 mismatches, 3 expiring
approvals and 2 budget warnings; 20 mismatches alone show as 8 mismatches. Within each kind the
items keep their own order (newest, soonest or fullest first), and the answer lists the kinds in
priority order whatever slot they won. Anything left out still counts on the screen (the
approvals tile counts expiring approvals).

| Kind | Severity | When | `at` | `ref` | `data` |
|---|---|---|---|---|---|
| `chain_broken` | high | The latest background check of the last 24 hours of deeds found a break (below) | when the check ran | the first broken deed, or null | `seq`, `deed`, `code`, `checked`, `checked_at` |
| `org_stopped` | high | Stop all is on for the org | when it was stopped | null | `since`, `by` (a person's display name or email, or a key id), `reason` |
| `slot_invalidated` | high | A witnessed Postgres connector's decoder is halted, or its replication slot was invalidated or lost (`wal_status` `lost`) | when the decoder's status last changed | the connector | `connector`, `connector_id`, `status`, `status_reason` |
| `slot_lagging` | medium | A decoder's lag is above `DEEDGLASS_SLOT_LAG_ALERT_MB` (default 512 MB), the same threshold as the decoder's `lag_high` | when the decoder's status last changed | the connector | `connector`, `connector_id`, `lag_bytes`, `threshold_bytes` |
| `mismatch` | high | An action created in the last 24 hours has the verdict `MISMATCH` (whatever the window), newest first | the action's `created_at` | the action | `tool`, `agent`, `agent_id`, `summary`, `level` |
| `approval_expiring` | medium | A pending approval expires in less than 15 minutes, soonest first | when the approval was requested | the approval | `tool`, `agent`, `agent_id`, `expires_at`, `action` |
| `budget_near` | low | An agent has used at least 80% of one of its budgets in the budget's current window (calls this minute, calls today, spend today), fullest first | the moment of the read | the agent | `agent`, `agent_id`, `budget` (`calls_per_minute`, `calls_per_day` or `spend_per_day`), `used`, `limit`, and `currency` for spend |

Disabled and dropped decoders are never listed. Budgets are checked for up to 500 agents with a
budget.

### The background chain check

`chain_broken` comes from the same check as `GET /v1/ledger/check`, run over the deeds issued in
the last 24 hours. The Overview never waits for it:

- The first overview read for an org starts a check in the background and answers without it, so
  that first answer has no `chain_broken` item.
- A later read uses the latest finished check and starts a new one when the last one started 5
  minutes ago or more. Each server runs at most one check per org at a time, and at most two at
  once.
- An org with no deeds in the last 24 hours has nothing to break.
- A check that fails to run (the database is down) changes nothing; the previous result stays.

So a break shows up within about 5 minutes of the next overview read after it happens, on the
server that answered. Run `GET /v1/ledger/check` (or `deedglass ledger check`) for an answer now.

## Freshness

- Answers are cached on each server for 10 seconds per org and window. Reads of the same org and
  window that arrive while one is in progress wait for that one read instead of starting their
  own. `generated_at` is when the answer was read. Errors are never cached.
- The Console refetches the overview every 60 seconds and adjusts its counters from live events in
  between, so a number can run ahead of `generated_at` until the next refetch corrects it.

## Limits

- `GET /v1/overview` reads in under 80 ms at the 99th percentile with 100,000 actions in the last
  24 hours on a local PostgreSQL 18, with the cache bypassed (`make bench-overview`; the raw
  numbers are in `bench/results/`).
- If a read takes longer than 2 seconds the request fails with `500 internal_error` and the Console
  shows the tile's error state; the next read tries again.
