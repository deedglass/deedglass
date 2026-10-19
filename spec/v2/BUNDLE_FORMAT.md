# Export bundle (edition 2)

> Licence: [CC-BY-4.0](../LICENSE) (the code that implements it is Apache-2.0). Part of the record
> spec, edition 2: see [spec/README.md](../README.md).

**Status:** normative · **Owner:** `internal/deed/archive` · **Changes:** below

The export bundle format is **unchanged** in edition 2: a bundle is exactly what
[`../v1/BUNDLE_FORMAT.md`](../v1/BUNDLE_FORMAT.md) describes (`"v": 1` in `manifest.json`), and every
bundle written before verifies the same way (the 2046 test, `testdata/release-bundles/`). Edition 2
only says what a bundle may now hold:

- **`deeds.jsonl`** may hold Deed v2 lines beside v1 lines ([`DEED_SPEC.md`](DEED_SPEC.md) §9). A v1
  line is at most 16,384 bytes of `JCS(envelope)` and a v2 line at most 24,576 bytes; the file limits
  of v1 §2 are unchanged.
- **`keys.json`** holds every key the deeds name, of every algorithm: a v2 deed names one key per
  seal (`seal_keys`), so a dual-sealed bundle lists the issuer's Ed25519 and ML-DSA-65 keys. A key of
  an algorithm the reader doesn't implement is kept and never used (DEED_SPEC.md §10.2).
- **`manifest.seal.json`** stays an Ed25519 seal over `"deedglass/bundle/v1\n" + manifest_hash`,
  made with the exporter's active Ed25519 key, even when the deeds are dual-sealed. A second manifest
  seal is a follow-up (ADR-109 §9).

## Changes

| Date | Change | Record |
|---|---|---|
| 2026-10-19 | Edition 2: v2 lines in `deeds.jsonl` (24 KiB each), keys of more than one algorithm in `keys.json`. No change to the format, its limits or the manifest seal. | ADR-109, TY-01 |
