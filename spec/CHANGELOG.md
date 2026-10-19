# Record spec change log

Every change to the text in `spec/`, newest first. Dates are UTC. "Edition" is a directory
(`v1/`); an erratum changes no verifier's behaviour.

## Unreleased

- **Deed v2 (placeholder).** Seal algorithm agility and ML-DSA-65 seals beside Ed25519 (ADR-109,
  TY-01/TY-02). That work fills this entry, the `v2` row of `README.md`, and the text.

## Edition 1 · 2026-10-19 · published under CC-BY-4.0

- `DEED_SPEC.md` (Deed v1) and `BUNDLE_FORMAT.md` (Export bundle v1) moved from `docs/` to
  `spec/v1/` and are published under CC-BY-4.0 (`spec/LICENSE`). The code stays Apache-2.0.
- No normative change: the only edit to either file is the licence note under its title. Every
  record and bundle that verified before verifies the same way, and the test vectors are unchanged.
- Added `README.md` (editions, implementations, where the spec lives, how to credit it) and this
  change log.

## Before CC-BY-4.0

- 2026-10-01: Deed v1 normative (ADR-025), checked against RFC 8785, RFC 8032 and RFC 9052, with
  test vectors in `testdata/deeds/`.
- Export bundle v1 normative with `deedglass deeds export` and `deedglass verify`; its worked
  example is generated from a real bundle (`go test ./internal/deed/archive -run TestBundleFormatDoc`).
