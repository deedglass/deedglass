# 02-cascade: a foreign key cascade deletes 12 rows in another table

The agent deletes a customer it believes is a duplicate. The `orders` table's foreign key is
`ON DELETE CASCADE`, so Postgres also deletes that customer's 12 orders in the same transaction.
The client is told `DELETE 1`.

| | |
|---|---|
| The agent reports | delete 1 row in `public.customers` |
| The client saw | `DELETE 1` |
| Postgres committed | delete 1 in `public.customers` and 12 in `public.orders`, one transaction |
| Deedglass | MISMATCH at L2: `unexpected_table` (both tables watched) |

The statement's text never mentions `orders`; only what committed does. If `orders` isn't watched,
its rows aren't visible to the check.
