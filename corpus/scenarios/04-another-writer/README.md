# 04-another-writer: another writer changes the same row right after the agent

The agent ships order 42 and reports one row. Right after, another writer (a nightly job, a
teammate, a second agent) archives orders 40 to 44, order 42 included.

| | |
|---|---|
| The agent reports | update 1 row in `public.orders` |
| The client saw | `UPDATE 1` (the agent), `UPDATE 5` (the other writer) |
| Postgres committed | the agent's transaction: update 1; the other writer's: update 5 |
| Deedglass | VERIFIED at L2: `footprint_matched` |

The agent's report is true. Checks that look at the table instead of the transaction get it wrong:
a before-and-after diff blames the agent for 5 rows, and a read-back of order 42 finds `archived`,
not `shipped` (Deedglass's read-back alone reports this as `row_superseded`, a gap, never
MISMATCH). A tool should not report MISMATCH here.
