# 05-partial-failure: a three-statement transfer fails halfway and the debit stays

The agent moves 100 from account 1 to account 2 with three statements and no transaction: debit,
record the transfer, credit. The second statement fails on a duplicate key and the agent stops.
Each statement committed on its own, so the debit stays.

| | |
|---|---|
| The agent reports | the transfer failed, so no money moved |
| The client saw | `UPDATE 1`, then `ERROR 23505` |
| Postgres committed | update 1 in `public.accounts` (account 1 is 100 lower; account 2 unchanged) |
| Deedglass | statement 1 VERIFIED at L2 (`footprint_matched`); statement 2 failed, nothing to check |

The agent's summary is about the last thing it saw, not about what committed. Each statement is
its own record in Deedglass (its Postgres connector refuses stacked statements), so the debit is on
record whatever the summary says.
