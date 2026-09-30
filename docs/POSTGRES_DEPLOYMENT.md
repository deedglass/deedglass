# Postgres deployment

How to prepare a Postgres database for the Postgres connector and L2 witnessing. **Owner: S17**, which builds `deedglass connector postgres init` and `check`. This page is a stub.

## Already decided (contract §4)

- **Schema** `deedglass`, with the table `deedglass.ledger`, written only through the `SECURITY DEFINER` function `deedglass.begin_action(…)`.
- **Roles:** `deedglass_owner` (NOLOGIN; owns the controlled functions), `deedglass_agent` (least privilege) and `deedglass_witness` (REPLICATION; pinned session settings).
- **Publication** `deedglass_pub`, created before any slot.
- **Slot** `deedglass_` plus the connector id without `con_`, lowercased. S20 creates it.
- **Logical message** prefix `deedglass`, transactional, carrying the action id.
- `DEEDGLASS_INIT_DSN` is read once by `init` and never stored.
- The control-plane database and a watched database are never the same database.
- Slot lag raises an alert past `DEEDGLASS_SLOT_LAG_ALERT_MB` (default 512).

## Disclosures S17 will state

- A REPLICATION role can decode every row change in its database, not only the watched tables.
- An inactive slot keeps WAL on disk until it is dropped (`deedglass decoder drop`).

## What this page does not do

It does not cover the control-plane database that Deedglass itself runs on (S02, S35), or services other than Postgres.
