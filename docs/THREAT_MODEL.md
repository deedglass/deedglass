# Threat model

Who Deedglass trusts for what, and what happens when each of them is compromised. **Owner: S34**, which writes the full model and the static guards G6–G10. This page is a stub.

## What S34 will cover

- **Actors:** the agent and its process, the person it acts for, approvers, org admins, the Deedglass operator and host, the seal-key holder, the Postgres server and its superusers, upstream tools and MCP servers, and the network.
- **Assets:** connector credentials in the vault, seal keys, API keys and the pepper, the deed chain, and the Ledger.
- **For each actor and asset:** what an attacker gains, and whether Deedglass blocks it, detects it, reports it as UNVERIFIABLE, or leaves it outside scope.

## Already decided (contract)

- Agents never receive connector credentials in gateway mode.
- API key secrets are stored as `sha256(pepper ‖ secret)` and shown once.
- Ids are not secrets and never authorize anything.
- All outbound traffic goes through one egress path that refuses private and metadata addresses in prod.
- Deeds and action events are append-only, and the database checks the deed chain on insert.

## What this page does not do

It is not a security review or a penetration-test report, and it makes no claim until S34 writes one with its evidence. The V0 trust model lives in `~/txv/ops/plan/20_ARCHITECTURE_SPEC_V0.md` §2.2.
