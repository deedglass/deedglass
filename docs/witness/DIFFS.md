# Reading an L2 diff

When the Witness compares a Postgres statement with what Postgres's change log shows (L2, S21), it
keeps a diff: what the statement should have touched, what committed, and the rows. This page
explains the text `deedglass actions show <id> --diff` prints and the JSON in
`witness_detail.levels.L2`. [WITNESS.md](../WITNESS.md) explains how the verdict is decided.

## The text

```text
$ deedglass actions show act_01m3xaj5vh9frq7c2wzx6hnkfp --diff
✕ postgres.execute · act_01m3xaj5vh9frq7c2wzx6hnkfp
  witness  L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.

  expected   UPDATE 1 row in public.orders where id = 42 · sets status
  observed   UPDATE 301 rows in public.orders · 300 outside id = 42

  public.orders    id    status
  ✓ expected       42    paid → shipped
  ✕ unexpected     251   cancelled → shipped
  ✕ unexpected     82    cancelled → shipped
  …                first 20 of 301 rows · the rest in Console › Actions

  Witnessed by Postgres · commit 0/1DD2520 · 2026-10-02 03:29:40 UTC
  deed     deed_01m3xaj5… · sealed
```

Line by line:

1. **The claim**: the level, the verdict and the one-sentence summary. A support engineer can read
   it aloud.
2. **expected**: the footprint Deedglass derived from the statement before it looked at anything:
   the operation, the reported row count, the table, the keys the WHERE named, the SET columns.
   `can't be predicted (UPDATE … FROM)` means the footprint is unknown, which is a gap.
3. **observed**: the exact counts per table from the WAL, and how many changed rows fall outside
   the keys the statement named.
4. **The rows**: the rows the statement named come first (`✓ expected`), then the rest in the
   order Postgres wrote them (`✕ unexpected`). Each cell is `before → after`; an insert shows
   `→ after`, a delete `before → deleted`, and `?` stands for an old value Postgres didn't send
   (the table isn't `REPLICA IDENTITY FULL`). Only columns that changed are shown, at most four;
   the rest are counted.
5. **The footer**: the transaction the verdict is about, by its commit LSN and time.

The terminal width decides how much fits: on a narrow terminal (under 100 columns) long values are
cut with `…` and the observed line drops the key text. Colour only repeats what the symbols say,
and `NO_COLOR` (or a pipe) prints plain text.

## The JSON

`GET /v1/actions/{id}?expand[]=witness.detail` (or `deedglass actions show <id> --diff --json`):

```json
"L2": {
  "verdict": "MISMATCH",
  "method": "postgres.pgoutput",
  "transaction": { "system_id": "7691907368266014763", "timeline": 1, "commit_lsn": "0/1DD2520",
                   "xid": 1043, "committed_at": "2026-10-02T03:29:40.112Z",
                   "marker_lsn": "0/1DC0FD0", "changes_digest": "sha256:9d41…07ac" },
  "expected": { "derivation": "exact_keys",
                "tables": [ { "table": "public.orders", "ops": ["update"], "rows": 1,
                              "keys": [ { "id": "42" } ], "columns": ["status"] } ] },
  "observed": { "total": 301,
                "tables": [ { "table": "public.orders", "insert": 0, "update": 301, "delete": 0 } ] },
  "rows": [
    { "table": "public.orders", "op": "update", "key": { "id": "42" }, "expected": true,
      "cells": [ { "column": "status", "before": "paid", "after": "shipped" } ] },
    { "table": "public.orders", "op": "update", "key": { "id": "251" }, "expected": false,
      "cells": [ { "column": "status", "before": "cancelled", "after": "shipped" } ] }
  ],
  "rows_shown": 20,
  "rows_more": 281,
  "columns_more": 0,
  "redacted": [],
  "frames": 1,
  "frame_ids": ["dtx_01m3xaj5…"]
}
```

- **Every L2 verdict that read a frame names its transaction**: `system_id`, `timeline` and
  `commit_lsn` identify it on that Postgres cluster. `marker_lsn` is kept as evidence and is never
  compared with `commit_lsn` (they are equal only by coincidence).
- **`changes_digest`** is the decoder's digest over every change of the transaction in stream
  order, including those past the image cap.
- **`frames`** counts the transactions that named the action; more than one is the violation
  `multiple_transactions`.
- `rows_shown` is at most `witness.diff_rows` (20). `rows_more` counts the rest. When the whole
  detail would pass `witness.max_detail_bytes` (64 KiB), rows are dropped first and counted.

## Why values are text

The decoder stores what Postgres prints for each column under pinned session settings (UTC,
ISO dates, hex bytea). The diff shows those strings as they are. A value isn't converted to JSON
numbers or booleans, so `1.10` stays `1.10` and a timestamp keeps its zone. The value check
compares text only for the types it understands (text, varchar, char, integers, booleans).

## Redaction

A column listed in the connector's `witness.sensitive_columns` is never stored: the decoder keeps
only `sha256:` of its text. The diff shows it as `•••` and lists the column in `redacted`. The
value check never compares a sensitive column. A diff never shows a sensitive value
(`TestCompareTable`, "sensitive column changed").
