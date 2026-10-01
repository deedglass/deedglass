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

## Deeds (S04)

| Threat | What Deedglass does |
|---|---|
| A stored deed is edited (database superuser, backup restore, a bad migration) | Normal paths can't: the append-only trigger and the app role's grants refuse it. If they are bypassed, any verifier sees `body_hash_mismatch` or `chain_prev_hash_mismatch`; S06's `ledger check` names the first break. |
| A deed is removed from the middle of a chain | `chain_seq_gap` names the missing seq. |
| Deeds are removed from the end of a chain | Not visible in the chain alone. Exports record the head (S05, S06); keep the heads you receive. |
| Two deeds claim the same parent (a fork) | The chain's advisory lock and `UNIQUE (org_id, prev_hash)`: the second insert aborts. |
| A signature made for something else is presented as a seal | Domain tags (`deedglass/deed/v1\n`, `deedglass/bundle/v1\n`, `deedglass/intent/v1\n`); seal keys are never intent keys. |
| A small-order or malformed public key enters a key set | Refused at key-file load, at registration and when a verifier parses `keys.json` (A13). |
| The seal key is stolen | Out of scope to prevent. The thief can seal false deeds with any `issued_at`. Mitigations: 0600 key files, the seed is never logged, prod refuses test keys, rotation, and revocation (a revoked key fails every deed). KMS or HSM signing is planned. |
| A payload leaks through a deed | The schema is closed and holds digests only; free text is capped (agent name, tool, references, reason). |
| Canonicalization ambiguity (duplicate keys, 2^53+1, `1.0`, lone surrogates) | The strict profile refuses it before JCS; nothing is repaired. |
| Clocks disagree between instances | `issued_at` comes from the database clock under the chain lock and never decreases. |
| An instance keeps sealing after a rotation | It refuses a key that retires within 30 s; a restart with a retired key is refused. |
| `keys.json` served partially during an outage | 503 `service_unavailable`, never a partial list. |

## What this page does not do

It is not a security review or a penetration-test report, and it makes no claim until S34 writes one with its evidence. The V0 trust model lives in `~/txv/ops/plan/20_ARCHITECTURE_SPEC_V0.md` §2.2.
