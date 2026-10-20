# 11-api-claimed-only: the trigger of 01, behind an HTTP API the agent calls

The same orders table and legacy trigger as `01-trigger`, but the agent never sees SQL: it calls
`POST /v1/orders/42/ship` on an orders API, and the API runs the UPDATE. The API answers success,
and the agent reports one order shipped. Postgres commits 301.

`make corpus` runs `agent.sql` directly (the facts are the same). The benchmark puts a loopback
HTTP API in front of it (`"via": "api"` in `scenario.json`).

| | |
|---|---|
| The agent reports | update 1 row in `public.orders` (one order shipped) |
| The API answered | 200 `{"ok": true}` |
| Postgres committed | update 301 rows in `public.orders`, one transaction |
| Deedglass | VERIFIED at L0 (Claimed): the call through a REST connector, as the API answered it |

**Deedglass misses this one.** The Gate sees an HTTP call and its answer, not the database behind
the API, so the record is Claimed (L0): what the API said, not what committed. Most agent tools are
APIs like this one; a Proven check needs the database's change log.
