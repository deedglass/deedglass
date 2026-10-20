# Export bundle (edition 2)

> Licence: [CC-BY-4.0](../LICENSE) (the code that implements it is Apache-2.0). Part of the record
> spec, edition 2: see [spec/README.md](../README.md).

**Status:** normative · **Owner:** `internal/deed/archive` · **Changes:** below

The export bundle format is **unchanged** in edition 2 except for one optional entry: a bundle is
exactly what [`../v1/BUNDLE_FORMAT.md`](../v1/BUNDLE_FORMAT.md) describes (`"v": 1` in
`manifest.json`), and every bundle written before verifies the same way (the 2046 test,
`testdata/release-bundles/`). Edition 2 only says what a bundle may now hold:

- **`deeds.jsonl`** may hold Deed v2 lines beside v1 lines ([`DEED_SPEC.md`](DEED_SPEC.md) §9). A v1
  line is at most 16,384 bytes of `JCS(envelope)` and a v2 line at most 24,576 bytes; the file limits
  of v1 §2 are unchanged.
- **`keys.json`** holds every key the deeds name, of every algorithm: a v2 deed names one key per
  seal (`seal_keys`), so a dual-sealed bundle lists the issuer's Ed25519 and ML-DSA-65 keys. A key of
  an algorithm the reader doesn't implement is kept and never used (DEED_SPEC.md §10.2).
- **`manifest.seal.json`** stays an Ed25519 seal over `"deedglass/bundle/v1\n" + manifest_hash`,
  made with the exporter's active Ed25519 key, even when the deeds are dual-sealed. A second manifest
  seal is a follow-up (ADR-109 §9).
- **`timestamps.json`** (optional, TY-03, ADR-112) may sit between `keys.json` and `deeds.jsonl`:
  independent timestamps of chain heads (§9). A bundle without it is exactly a v1 bundle, byte for
  byte. A bundle with it has six entries at most, and its manifest lists it.

## 9. timestamps.json (optional)

### 9.1 Place in the bundle

The order of entries becomes `README.txt`, `keys.json`, `timestamps.json` (optional),
`deeds.jsonl`, `manifest.json`, `manifest.seal.json` (optional). The file comes before the deeds so
a reader knows which heads to compare while it streams. Its limit is 1 MiB (the reader refuses a
larger one by its header, `bundle_invalid`), and the gzip-bomb limit grows by the same amount.
`manifest.json`'s `files` then lists `timestamps.json` (with `bytes` and `sha256`, no `lines`) beside
the other three; it MUST list it when present and MUST NOT list it when absent. The manifest's `v`
stays 1. A reader that implements only edition 1 refuses such a bundle (an unknown entry); that is
the expected answer, not a fault.

### 9.2 The head and the file

A **head** is a statement about one sealed deed, every value copied from it:

```json
{"body_hash":"sha256:<64 hex>","deed_id":"deed_…","issued_at":"2026-10-20T09:29:58.120Z","object":"chain_head","org":"org_…","seq":1204,"v":1}
```

The **file** is `JCS` of one org's heads, `seq` strictly ascending, each with its evidence:

```json
{"heads":[{"head":{…},"rfc3161":[{"token":"<b64url DER>","tsa":"<url>"}],
  "scitt":[{"inclusion":"<b64url COSE_Sign1>","service":"<url>","service_key":{<JWK>},"statement":"<b64url COSE_Sign1>"}]}],
 "object":"head_timestamps","org":"org_…","v":1}
```

- The schema is closed at every level; the bytes MUST be `JCS` of the object (RFC 8785, the strict
  profile of `DEED_SPEC.md`); `org` of every head equals the file's `org`.
- Each head carries 0–8 tokens and 0–4 registrations, at least one in all; binary members are
  base64url without padding; URLs are http(s) with no user info, at most 512 characters.
- `service_key` is `{"kty":"EC","crv":"P-256","x","y"}` (ES256) or `{"kty":"OKP","crv":"Ed25519","x"}`
  (EdDSA), nothing else.
- Outside a bundle (`deedglass verify --timestamps`) the same file may hold up to 100,000 heads and
  256 MiB.

### 9.3 Evidence

- **RFC 3161 token.** A DER `TimeStampToken` (a CMS `ContentInfo` holding `SignedData` over
  `TSTInfo`). Its `messageImprint` MUST be SHA-256 over `JCS(head)`. It MUST carry signed attributes
  with the content type `id-ct-TSTInfo`, the message digest of the `TSTInfo`, and an ESS
  signing-cert attribute (v1 or v2) naming the signer; the signer MUST have the time-stamping
  extended key usage, and the signature MUST verify. A reader that holds roots for the TSA checks
  that the signer chains to one of them at `genTime`; without roots the token shows only that it is
  intact. The token's time is `genTime`.
- **Transparency-service registration** (following RFC 9943). `statement` is a tagged COSE_Sign1
  whose payload is exactly `JCS(head)`, protected header `{1: -8, 3:
  "application/vnd.deedglass.chain-head.v1+json", 4: <seal key id>, 15: {1: <issuer>, 2: <org>, 6:
  <iat>}}`, signed with that Ed25519 seal key of the key set (not revoked). `inclusion` is a tagged
  COSE_Sign1 with a detached payload, protected header `{1: -7 or -8, 395: 1}` and unprotected
  header `{396: {-1: [bstr .cbor [tree_size, leaf_index, [+ bstr]]]}}` (RFC 9942, RFC9162_SHA256).
  For each inclusion path the reader computes the root from the leaf `SHA-256(0x00 ‖ statement)` by
  RFC 9162 §2.1.3.2 and checks the signature over that root with `service_key` (or a key it holds).
  The registration's time is the statement's `iat`.

Every piece of evidence present MUST verify: one valid token never covers an invalid one.

### 9.4 Comparing heads with the deeds

After the deeds and the manifest verify, for each head whose evidence verified, with *T* the earliest
time its evidence gives:

| The head's seq | Result | Code |
|---|---|---|
| inside the intact deeds, same `body_hash` | matches | none |
| inside the intact deeds, another `body_hash` | the chain changed after *T* | `timestamp_mismatch` (exit 1) |
| after the last deed given | deeds were removed from the end, or the input stops early | `chain_tail_missing` (exit 1) |
| before the first deed given | not compared (a warning) | none |

Evidence that doesn't verify, or a head of another org than the deeds, is `timestamp_invalid` (exit
1). These three codes are verification results like `bundle_invalid`, never "cannot decide". A
reader may compare heads from files other than the bundle's own; the rules are the same.

## Changes

| Date | Change | Record |
|---|---|---|
| 2026-10-19 | Edition 2: v2 lines in `deeds.jsonl` (24 KiB each), keys of more than one algorithm in `keys.json`. No change to the format, its limits or the manifest seal. | ADR-109, TY-01 |
| 2026-10-20 | The optional `timestamps.json` entry (§9): head timestamps from RFC 3161 TSAs and transparency services, and the codes `timestamp_invalid`, `timestamp_mismatch` and `chain_tail_missing`. A bundle without it is unchanged. | ADR-112, TY-03 |
