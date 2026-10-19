# Retention

Payloads age out; the record of what happened does not. Pruning is itself a sealed deed, so even
removal leaves a signature.

## What is removed

For actions in state `sealed` created before the cutoff:

| Column | After pruning |
|---|---|
| `actions.request` (the tool arguments) | `NULL` |
| `actions.result` (the tool result) | `NULL` |
| `actions.witness_detail` (the expected-vs-observed diff) | `NULL` |
| `actions.payload_pruned_at` | the time it was pruned |

An org in [PHI mode](PHI_MODE.md) doesn't wait for pruning: once an action is sealed and its checks
are done, its request, result, error message and witness detail are replaced by ids and digests,
and its policy input is removed. Pruning later removes what is left as usual.

## What is kept

Everything else, always:

- every deed, every action event and every admin event (all append-only)
- every digest: `request_digest`, `result_digest`, `witness_detail_digest`, `data_digest`
- every id, state, decision, verdict, timestamp, latency and cost
- approvals, and every action that isn't sealed yet (a pending approval keeps its payload)

## Why every deed still verifies

A deed holds digests of payloads, never the payloads themselves ([DEED_SPEC.md](DEED_SPEC.md)).
Removing a payload changes nothing that was hashed or sealed, so `deedglass ledger check` and
`deedglass verify` of a fresh export give the same answer before and after pruning. What you lose
is the ability to show the original arguments; the digest still shows whether a copy you kept
elsewhere is the same.

## The `ledger.pruned` deed

Each run that removes anything seals one admin deed, `ledger.pruned`, whose data records:

```json
{"actions":3410,"bytes":18200000,"cutoff":"2026-07-04T09:00:00.000Z","first_action":"act_…","last_action":"act_…","retention_days":90}
```

The actor is `{"type":"system","id":"operator"}` for the CLI and `{"type":"system","id":"deedglass"}`
for jobs (S07 adds the signed-in person).

## Defaults

- Retention is the org's `orgs.retention_days`, 1–3,650. `NULL` means **90 days**
  (`ledger.DefaultRetentionDays`). S26's Settings screen and `PATCH /v1/org` (owner only) write the
  same column.
- `--older-than` overrides it for one run (`90d`, `36h`). It must be at least 24 hours unless
  `DEEDGLASS_ENV=dev` (tests and demos prune fresh data with `0s`).

## Commands

```
$ deedglass ledger prune --older-than 90d --dry-run
○ Dry run · nothing changes
  org_01j9z8m2…   3,410 sealed actions before 2026-07-04 · 18.2 MB of payloads
  kept            deeds, events, digests and ids
  next            Run again without --dry-run. Deedglass seals a "ledger.pruned" deed when it's done.

$ deedglass ledger prune --older-than 90d --yes
✓ Removed payloads from 3,410 actions · 18.2 MB (space returns after autovacuum)
✓ Deed sealed · seq 1,205 · ledger.pruned
  Every deed still verifies. Payloads are gone; their digests stay.
```

Without `--yes`, on a terminal, it asks you to type the org id. Without a terminal it refuses.

## How it runs

- One prune per org at a time: a session advisory lock on `deedglass/prune/<org>`. A second run
  stops at once with "Another prune is running for this org."
- Batches of 1,000 actions (`FOR UPDATE SKIP LOCKED`), each its own short transaction, so a large
  prune never holds a long lock. An interrupted run is safe: the next run continues and seals its
  own deed.
- "Removed", never "freed": Postgres returns the space to the operating system only after
  autovacuum (or `VACUUM FULL`, which locks the table).

## What it doesn't do

- It never removes a deed, an event, a digest or an id, and it never touches an action that isn't
  sealed.
- It isn't automatic yet: a daily sweep inside `serve` is planned; today it is an operator
  command.
- It doesn't remove anything outside `actions`: `admin_events.data` is kept (it is small and
  bounded at 16 KiB), as are webhook deliveries until S22 sets their own window.
