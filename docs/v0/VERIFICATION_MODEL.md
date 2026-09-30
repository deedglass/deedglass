> V0 reference. Superseded by Deedglass; kept for the L2 design.

# Verification model (V0)

The scoped claim, the property a VALID statement asserts, and the assumptions A1–A6 it rests on (`20_ARCHITECTURE_SPEC_V0.md` §1.1, §10.6).

## The V0 claim, in short

For each committed Postgres transaction that carries a ledger entry, the verifier compares the row changes decoded from the database's own logical-replication stream, inside the declared observation boundary, with the changes the pinned Operation Bundle computes from the signed intent and the decoded before-images. It then issues a signed VALID, INVALID or INCONCLUSIVE statement.

## What a VALID statement asserts

1. The transaction committed on the cluster (sysid, timeline) at `commit_lsn`, as decoded through `deedglass_v0`.
2. It held exactly one ledger row and one `deedglass` marker, consistent with each other.
3. The intent is canonical, signed by a key active at `iat`, with matching `aud` and bundle, and committed inside its window.
4. The decoded changes on boundary relations equal the expected changes.
5. Boundary snapshots on both sides of `commit_lsn` match the committed hash.
6. The `(kid, nonce)` pair was not seen before.

**Precedence:** any contradiction gives INVALID; otherwise any gap gives INCONCLUSIVE; otherwise VALID.

**Assumptions:** A1 an honest Postgres engine, owners and superusers · A2 an honest verifier host and key · A3 an uncompromised principal key · A4 an unmodified decoding path · A5 secure Ed25519 and SHA-256 · A6 no privileged A-B-A boundary change between snapshots.

## Where this lands in Deedglass

The Witness (S19, S21) keeps the precedence rule with the new words (contract §6): any violation gives MISMATCH, any gap gives UNVERIFIABLE, at least one clean pass gives VERIFIED, and no checks gives UNVERIFIABLE.

## What this page does not do

It does not cover a hijacked agent or a stolen key that signs a well-formed, in-policy intent (that gets VALID), malicious superusers or hosts, effects outside the WAL, or whether the intent was a good idea.
