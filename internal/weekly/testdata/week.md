# What your agents did · Acme · 12 to 18 Oct 2026

412 actions · 371 allowed · 22 waited for a person · 19 blocked by rules  
Check: 387 match · 1 don't match · 24 can't check

## Deedglass caught 2 things

- One transaction changed 301 rows in public.orders.
  Undo it yourself, free: `deedglass observe show obs_01k7ses0000000000000000000 --undo-sql`  
- The L2 check of postgres.execute doesn't match what ops-agent said.
  Undo it yourself, free: `deedglass undo preview act_01k7mis0000000000000000000`  

## Actions by agent

| Agent | Actions |
|---|---:|
| ops-agent | 334 |
| support-agent | 78 |

## Actions by tool

| Tool | Actions |
|---|---:|
| postgres.execute | 250 |
| postgres.query | 110 |
| stripe.refund | 52 |

## Blocked most by

| Rule | Reason | Times |
|---|---|---:|
| block-destructive-sql | DROP is never allowed for agents. | 12 |
| refund-limit | Refunds over $200 need a person. | 5 |
| @default |  | 2 |

## Approvals

22 asked · 20 approved · 1 denied · 1 expired · decided in 4 minutes (median)

## Undo and Stop

1 undo (1 action, 0 watched sessions) · Stop used 0 times

## Watched sessions

3 watched sessions · 1 with something caught

## Records

Records chain intact.

Security review pack built on 16 Oct 2026: security-pack-acme-2026-10-16. Check it with deedglass security-pack verify &lt;folder&gt;.

---

Built by Deedglass from acme's own records on 19 Oct 2026 08:00 UTC. Numbers, ids and names only: no arguments, results or row values.
