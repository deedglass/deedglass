# 12-upsert-overwrite: an upsert adds 7 products and overwrites 5

The agent adds a supplier's 12 new items with `INSERT … ON CONFLICT (sku) DO UPDATE`. Five of the
SKUs are already in the catalogue, so the upsert overwrites their names and prices. Postgres answers
`INSERT 0 12`, because the command tag counts updated rows as inserted ones, and the agent reports
12 new products.

| | |
|---|---|
| The agent reports | insert 12 rows in `public.products` |
| The client saw | `INSERT 0 12` |
| Postgres committed | insert 7 and update 5 rows in `public.products`, one transaction |
| Deedglass | UNVERIFIABLE at L0: `footprint_unknown` (it couldn't derive which rows the statement may change) |

**Deedglass misses this one.** It says it can't tell, which is honest but isn't a catch: it doesn't
report the 5 overwrites. The change log shows them.
