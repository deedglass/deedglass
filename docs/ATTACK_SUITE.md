# Attack suite

Every attack case with its expected outcome, declared before it is implemented, and its result. **Owner: S18**, which generates this page from `attack/attack_meta.json` plus test results.

## Today

- `attack/attack_meta.json` holds the V0 suite: 66 cases and 1 pending case, frozen on 2026-09-28.
- `attack/meta_test.go` pins a digest of every `id`, `expected`, `expected_detail` and `priority` (`503500269d…c14b`). Changing one needs a founder-approved ADR.
- The S01 rename changed only text outside the digest. The digest is unchanged.

## Outcome words

The suite keeps its V0 words until S18 applies ADR-021 (approved 2026-10-01): the outcomes VALID, BLOCKED, DETECTED, INCONCLUSIVE, OUTSIDE SCOPE and NO EFFECT, and INVALID in the case details. ADR-021 (proposed) renames VALID to VERIFIED, INVALID to MISMATCH and INCONCLUSIVE to UNVERIFIABLE. BLOCKED, DETECTED, NO EFFECT and OUTSIDE SCOPE stay. S18 applies the change and the new digest after approval.

## What this page does not do

It does not report results yet. No case runs against Postgres until S18.
