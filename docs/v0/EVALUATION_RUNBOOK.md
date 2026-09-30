> V0 reference. Superseded by Deedglass; kept for the L2 design.

# Evaluation runbook (V0)

Step-by-step paid technical evaluation, the TX0xx SQLSTATE table and the SOW technical annex inputs (`20_ARCHITECTURE_SPEC_V0.md` §4.4, §14.3).

## What V0 planned

- A scripted evaluation against the customer's own Postgres: install the roles (`deedglass_owner`, `deedglass_agent`, `deedglass_witness`), the `deedglass` schema, the controlled function, the publication `deedglass_pub` and the slot `deedglass_v0`, then run the transfer demo.
- The SQLSTATE table for the controlled function: `TX001`–`TX031`. The codes are unchanged by the rename (TX means transfer). Their messages now start with `deedglass:`.
- The inputs for the technical annex of a statement of work.

## Where this lands in Deedglass

- Setting up a customer Postgres becomes `deedglass connector postgres init` and `deedglass connector postgres check` (S17).
- The customer-side `begin_action` guards use `DG101`–`DG103` (contract §4). `TX001`–`TX031` stay with the V0 transfer demo.

## What this page does not do

It is not a runbook for Deedglass today. Nothing here was written past the outline.
