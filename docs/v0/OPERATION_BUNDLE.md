> V0 reference. Superseded by Deedglass; kept for the L2 design.

# Operation Bundles (V0)

Bundle manifest format, content addressing, and the vendor, admin, principal and verifier control chain (`20_ARCHITECTURE_SPEC_V0.md` §6).

## What V0 specified

- A bundle is a vendor-authored manifest. The one V0 bundle is `bundles/transfer_v1` (`TRANSFER` v1).
- Its identity is `bundle_hash = "sha256:" + hex(SHA-256(JCS(manifest.json)))`.
- The principal signs that hash inside every intent (`deedglass.transfer/1`). The customer admin activates it in `deedglass.bundles`. The verifier compiles in the matching rule.
- The manifest pins three hashes: `intent_schema_sha256`, `fn_src_sha256` (the body of `deedglass.execute_transfer_v1`, compared with the live `pg_proc.prosrc`) and `schema_sha256`.
- Changing any hashed file is new semantics: a new manifest, a new verifier release and an ADR. There is no in-place edit.
- The S01 rename changed all three hashes. ADR-012 records the old and new values.

## Where this lands in Deedglass

The Postgres connector absorbs the bundle idea in S17 (`internal/connector/postgres`). `internal/bundle` and its test stay until then.

## What this page does not do

It does not describe how Deedglass connectors are defined, and it does not cover any connector other than the V0 transfer demo.
