# Record spec change log

Every change to the text in `spec/`, newest first. Dates are UTC. "Edition" is a directory
(`v1/`); an erratum changes no verifier's behaviour.

## Unreleased

- **The dual seal** (TY-02, ADR-109), in `v2/DEED_SPEC.md`: ML-DSA-65 (FIPS 204, pure, empty context)
  in the algorithm table (§7.4), RFC 9964 AKP keys in `keys.json` (§10.1), adding an ML-DSA-65 key
  beside Ed25519 (§10.4), and COSE −49 for v2 records (§12). Vectors: dual-sealed records, a chain
  across the rotation that adds the key, and every ML-DSA-65 failure in `testdata/deeds/v2/`. This
  completes edition 2.

## Edition 2 · 2026-10-19 · Deed v2 (ADR-109, TY-01)

- **`v2/DEED_SPEC.md`**: Deed v2. The body names every seal it carries (`seal_keys`), the envelope
  carries one seal per key (`seals`), every seal names its algorithm, and every algorithm has its own
  domain tag (§7.4). v1 and v2 records sit in one chain.
- **Verification** (§11): the v2 order, and an accept policy decided by `issued_at` only, applied to
  v1 and v2 records: Ed25519 alone only before 2035-01-01 (NIST IR 8547 draft), and the key-set rule
  (an algorithm with a key active at `issued_at` must have a seal). New codes `unsupported_algorithm`
  (exit 2) and `seal_missing`; none renamed.
- **Key sets** (§10.2): a key of an algorithm the verifier doesn't implement is kept and never used,
  instead of refusing the whole set. This changes edition 1's rule; no record that verified under
  edition 1 fails under edition 2 (every v1 record so far was issued before 2035 and before any
  second key).
- **`v2/BUNDLE_FORMAT.md`**: the bundle format is unchanged; bundles may hold v2 lines and keys of
  several algorithms.
- Vectors: `testdata/deeds/v2/` (with a 200-case parity corpus). The v1 vector `invalid/version-2`
  is now `invalid/version-3`, since v2 is a known version; its expected answer is unchanged.
- `v1/` is not rewritten and stays the text for `v: 1` records under edition 1.

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
