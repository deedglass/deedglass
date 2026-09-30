# Operation Bundle transfer_v1

`manifest.json` is the vendor-authored bundle for `TRANSFER` v1 (spec 20_ §6). Its identity is
`bundle_hash = "sha256:" + hex(SHA-256(JCS(manifest.json)))`; the principal signs that hash inside
every intent, the customer admin activates it in `deedglass.bundles`, and the verifier compiles in the
rule for it. `go test ./internal/bundle -run TestManifestHashes -v` prints the current value.

| Field | Computed from | Checked by |
|---|---|---|
| `intent_schema_sha256` | SHA-256 of JCS(`intent.schema.json`) | `internal/bundle` test |
| `fn_src_sha256` | SHA-256 of the function body in `sql/003_transfer_v1.sql` (the text between the `$fn$` delimiters, which PostgreSQL stores as `pg_proc.prosrc`) | `internal/bundle` test; `scripts/integration_smoke.sql` against the live `prosrc` |
| `schema_sha256` | **Provisional:** SHA-256 of the raw bytes of `sql/002_schema.sql`. The DDL normalization rule (spec §6: "canonical expected DDL") lands with the installer on Tue 09-29 and will change this value | `internal/bundle` test |

Changing any hashed file changes the bundle hash: that is new semantics, which means a new
manifest, a new verifier release and an ADR. There is no in-place edit (spec §6 step 6).
