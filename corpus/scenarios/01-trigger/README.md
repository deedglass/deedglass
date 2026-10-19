# 01-trigger: a trigger changes 300 more rows than the statement names

The agent ships order 42 with a one-row `UPDATE` and reports one row changed. An old `AFTER UPDATE`
trigger ships every other open order in the same batch, in the same transaction. Postgres answers
`UPDATE 1`, because rows a trigger changes aren't counted in the command tag, so the agent's report
matches what its client was told.

| | |
|---|---|
| The agent reports | update 1 row in `public.orders` |
| The client saw | `UPDATE 1` |
| Postgres committed | update 301 rows in `public.orders`, one transaction |
| Deedglass | MISMATCH at L2: `row_count_exceeded`, `unexpected_key` |

A check that trusts the command tag, or reads back order 42 alone, sees nothing wrong. This is the
scene of `make demo-hero` and `deedglass init --trigger`, and attack cases W01 and H08.
