# Export bundle v1

> Licence: [CC-BY-4.0](../LICENSE) (the code that implements it is Apache-2.0). Part of the record
> spec, edition 1: see [spec/README.md](../README.md) for versions, the change log and how to cite it.

**Status:** normative for `deedglass deeds export` (S06), `GET /v1/deeds/export` (S06) and
`deedglass verify` (S05). The writer and reader are `internal/deed/archive`; nothing else writes
bundles. The words MUST, MUST NOT and SHOULD are used as in RFC 2119.

## 1. Shape

A bundle is a gzip-compressed POSIX tar. Its file name is
`deedglass-<org>-<first_seq>-<last_seq>.tar.gz`. It holds exactly these regular files, at the root,
**in this order**:

| # | File | Content |
|---|---|---|
| 1 | `README.txt` | a plain-text guide (§5), at most 72 columns |
| 2 | `keys.json` | a `seal_key_set` holding every key the deeds name (`docs/SEAL_KEYS.md`) |
| 3 | `deeds.jsonl` | `JCS(envelope)` per line, seq ascending, a `\n` after every line |
| 4 | `manifest.json` | `JCS(manifest)` (§3) |
| 5 | `manifest.seal.json` | optional: the exporter's seal over `manifest.json` (§4) |

Keys come before deeds so a reader can verify while streaming. The manifest comes after the deeds
because it carries their SHA-256, so a writer can stream too: it measures `deeds.jsonl` in a first
pass (tar headers need the size) and hashes it while writing.

## 2. Limits

| File | Limit |
|---|---|
| `README.txt` | 64 KiB |
| `keys.json` | 1 MiB |
| `deeds.jsonl` | 8 GiB (`deedglass verify --max-bytes` changes it) |
| `manifest.json` | 64 KiB |
| `manifest.seal.json` | 4 KiB |

## 3. manifest.json

```json
{
  "object": "deed_bundle_manifest",
  "v": 1,
  "org": "org_…",
  "issuer": "https://api.deedglass.com",
  "created_at": "2026-10-02T18:30:00.000Z",
  "generator": { "name": "deedglass", "version": "0.1.0-dev", "commit": "4f2c1a9" },
  "range": { "first_seq": 1, "last_seq": 1204, "count": 1204, "from": "…", "to": "…" },
  "chain": {
    "anchor": { "kind": "genesis", "seq": 0, "body_hash": "sha256:<64 hex>" },
    "head":   { "seq": 1204, "deed_id": "deed_…", "body_hash": "sha256:<64 hex>" }
  },
  "kinds": { "action": 1150, "admin": 50, "amendment": 4 },
  "files": {
    "README.txt":  { "bytes": 1432, "sha256": "<64 hex>" },
    "keys.json":   { "bytes": 1120, "sha256": "<64 hex>" },
    "deeds.jsonl": { "bytes": 2811004, "sha256": "<64 hex>", "lines": 1204 }
  }
}
```

- `manifest.json` MUST be exactly `JCS(manifest)` and MUST NOT hold other members.
- `range.count` MUST equal `last_seq − first_seq + 1`; `chain.head.seq` MUST equal `last_seq`.
- When `first_seq` is 1, `anchor` is `{"kind":"genesis","seq":0,"body_hash":<the org's genesis>}`.
  Otherwise it is `{"kind":"deed","seq":first_seq−1,"body_hash":<the first deed's prev_hash>}`. The
  anchor is informational: continuity with an earlier bundle is checked by verifying both together.
- `files` lists `README.txt`, `keys.json` and `deeds.jsonl` (with `lines`), never the manifest or
  its seal.
- `kinds` always lists `action`, `admin` and `amendment`.

## 4. manifest.seal.json

```json
{"manifest_hash":"sha256:<hex of SHA-256(manifest.json bytes)>","object":"deed_bundle_seal","seal":"ed25519:<b64url>","seal_key_id":"sk_…"}
```

The seal is pure Ed25519 over the bytes `"deedglass/bundle/v1\n" + manifest_hash`, made with the
exporter's active seal key. The seal key MUST be in the key set the reader trusts, MUST NOT be
revoked, and MUST have been active at the manifest's `created_at`. The file is `JCS` of the object.

The sealed manifest names the head and the count, so it makes **tail truncation** detectable: a
bundle cut at the end no longer matches its manifest. A missing seal is a warning
(`○ The bundle manifest is not sealed.`); an invalid one is a failure (`bundle_seal_invalid`,
exit 1).

## 5. README.txt

Plain text, at most 72 columns, generated from the header: the count and issuer, the org and seq
range, the dates, what is inside, the one command that checks it (with the issuer's key URL), what a
deed records and what the bundle does not show. The worked example in §8 has the exact text.

## 6. Determinism

The same deeds, keys and header MUST produce the same bytes:

- tar: USTAR headers (PAX only when a file passes 8 GiB − 1 byte), type regular file, mode 0644,
  uid and gid 0, empty user and group names, mtime = `created_at` truncated to the second, no
  directories, links or devices;
- gzip: no file name, mtime 0, OS 255 (unknown), the default compression level.

`internal/deed/archive` pins the SHA-256 of a 20-deed test bundle
(`internal/deed/archive/testdata/genesis-20.sha256`).

## 7. Reader rules

A reader MUST refuse the bundle with `bundle_invalid` and a one-sentence detail, before using any
byte of the offending entry, when it finds:

- an entry name that is not one of the five, or a name with `/`, `\`, `..` or a leading `.`;
- an entry that is not a regular file (a link, a directory, a device);
- a duplicate name, a sixth entry, or the files out of order;
- a file over its limit (by its header, before reading), or a stream that expands past the sum of
  the limits (a gzip bomb);
- a manifest that is not canonical, not closed, or not self-consistent;
- a file whose size or SHA-256 differs from the manifest, or a `deeds.jsonl` with another line
  count;
- a manifest whose range, dates, kinds, anchor or head differ from the deeds that verified.

A reader MUST NOT write any entry to disk. `deedglass verify` streams the deeds through the
verifier and reads the manifest after them; a deed that fails is reported first.

```
✕ This bundle contains an entry named "../../.ssh/authorized_keys". Nothing was extracted.
  next       Do not use this file. Ask the issuer for a new export.
```

## 8. Worked example

A three-deed bundle from the test vectors (org 2, deeds 1–3 of `testdata/deeds/chains/genesis-20.jsonl`,
sealed by test key b at T0 + 1 h). **Test keys: these bytes mean nothing outside tests.**

<!-- example:begin (generated by internal/deed/archive; go test ./internal/deed/archive -run TestBundleFormatDoc -update) -->

`README.txt`:

```
Deedglass deed bundle

3 deeds sealed by https://api.deedglass.example
org org_01m3xxf0m2ekmm72j2vvks65q5 · seq 1 to 3
2026-10-02

What is inside
  deeds.jsonl          one sealed deed per line, in seq order
  keys.json            the seal keys that sealed them
  manifest.json        counts, file hashes and the chain head
  manifest.seal.json   the exporter's seal over manifest.json

Check it (no account, no network except fetching the keys)
  deedglass verify <this file> \
    --keys https://api.deedglass.example/.well-known/deedglass/keys.json

What a deed records
  Which agent acted, for whom, what tool ran, which policy decided,
  and what the Witness saw. Payloads are not included, only digests.

What it does not show
  Whether an action was wise. Keys inside this bundle come from the
  exporter; pin the issuer's published keys for authenticity.

Spec: https://docs.deedglass.com/deeds/spec
```

`manifest.json` (stored as one canonical line; indented here):

```json
{
  "chain": {
    "anchor": {
      "body_hash": "sha256:936f436c12d714c554d0478e6130ef04918e4cb3c2aaa891335d9e60bd7d9fc5",
      "kind": "genesis",
      "seq": 0
    },
    "head": {
      "body_hash": "sha256:f1576718bb34de9cfd90689bb84974fe10d790e50646656451facfaeecbf8114",
      "deed_id": "deed_01m3xxn3y3smx2dr3kn4qn9eaz",
      "seq": 3
    }
  },
  "created_at": "2026-10-02T10:00:00.000Z",
  "files": {
    "README.txt": {
      "bytes": 909,
      "sha256": "952acdc23d9a1c115c6fc98f260aeb18bf3bba2be9bd8393dfa3f3bcba201ab7"
    },
    "deeds.jsonl": {
      "bytes": 5154,
      "lines": 3,
      "sha256": "3ce73b307f6f54d3b65f6615ddcb614ed3f7715b5fbdc28df08a9940683bdb12"
    },
    "keys.json": {
      "bytes": 1516,
      "sha256": "b81048ae50cd71e7f9158cd18c9d30033f1bd9f766abb81d9c32fe0c42a1deb1"
    }
  },
  "generator": {
    "commit": "4f2c1a9",
    "name": "deedglass",
    "version": "0.1.0-dev"
  },
  "issuer": "https://api.deedglass.example",
  "kinds": {
    "action": 3,
    "admin": 0,
    "amendment": 0
  },
  "object": "deed_bundle_manifest",
  "org": "org_01m3xxf0m2ekmm72j2vvks65q5",
  "range": {
    "count": 3,
    "first_seq": 1,
    "from": "2026-10-02T09:00:00.000Z",
    "last_seq": 3,
    "to": "2026-10-02T09:00:02.000Z"
  },
  "v": 1
}
```

`manifest.seal.json`:

```json
{"manifest_hash":"sha256:9daf8e23068b5421aa2a0664f3f5163c5c296378caab0e9083b0a983591cf771","object":"deed_bundle_seal","seal":"ed25519:vTSo5-A1JPjBfCRNkIfBSgG2yiZJdZC6psESbuafn7RGb_HNL0LceMKa6SlNCyxmV9ykfAlvnExOvTOv26EbBA","seal_key_id":"sk_01m3xxf0m2b2bf0pn7ssc7whbb"}
```

```
$ deedglass verify deedglass-org_01m3xxf0m2ekmm72j2vvks65q5-1-3.tar.gz --keys testdata/deeds/keys/test-keys.json
✓ 3 deeds · chain intact · 1 seal key · 2026-10-02 → 2026-10-02
✓ Bundle manifest matches · sealed by sk_01m3xxf0…
```
<!-- example:end -->
