> V0 reference. Superseded by Deedglass; kept for the L2 design.

# Verification statement specification (V0)

The COSE_Sign1 verification statement: protected header (alg -8 per ADR-008), payload fields, hash chain and offline checking (`20_ARCHITECTURE_SPEC_V0.md` §11).

## What V0 specified

- A COSE_Sign1 object (RFC 9052, tag 18), shaped as an RFC 9943 signed statement.
- Algorithm `-8` (EdDSA, Ed25519), per ADR-008, because go-cose 1.3.0 has no built-in `-19`.
- Payload: JCS JSON of type `deedglass.stmt/1`. It carries the verdict, the reasons, the subject (sysid, timeline, `commit_lsn`, `xid8`), the intent, the bundle, the policy, the boundary status and salted transition digests. No balances or amounts.
- Statements, alerts included, are hash-chained by `seq` and `prev`.
- A statement is a signed statement from the verifier. It is not an RFC 9942 transparency-log entry, and is never named as one.

## Where this lands in Deedglass

- The V0 statement becomes the **Deed** (contract §7). The deed format is owned by S04 (`internal/deed`, seal keys) and S05 (`deedglass verify`, export bundles).
- A deed is JCS JSON sealed with Ed25519. COSE_Sign1 stays an optional encoding.
- The V0 verdicts VALID, INVALID and INCONCLUSIVE become VERIFIED, MISMATCH and UNVERIFIABLE. The attack suite keeps the V0 words until S18 applies ADR-021 (approved 2026-10-01).

## What this page does not do

It does not define the deed format, and nothing new is built from it. It stays as a record of the V0 design.
