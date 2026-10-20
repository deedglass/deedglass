# Independent verifiers

Two more ways to check Deedglass records without the `deedglass` binary, written from the record
spec ([`../spec/`](../spec/)) rather than ported from the Go code (TEN_YEAR TY-10). If the reference
verifier is ever wrong, gone, or simply not something you want to run, these give a second opinion
that shares no code with it.

| | Python | JavaScript |
|---|---|---|
| Where | [`python/deedglass_verify/`](python/deedglass_verify/) | [`js/deedglass-verify.mjs`](js/deedglass-verify.mjs), one file |
| Runs on | Python 3.10+ | Node 20+, and browsers (tested in Chromium on /verify-offline) |
| Dependencies | none (standard library) | none |
| Ed25519 | pure Python (RFC 8032) | WebCrypto, with a BigInt fallback where a browser lacks Ed25519 |
| ML-DSA-65 | pure Python (FIPS 204), on `hashlib`'s SHAKE | in the file (FIPS 204), with its own Keccak |
| Bundles | `tarfile` and `gzip`, read in order, nothing written to disk | `DecompressionStream` and a small tar reader, nothing written |

There are no packages to install on purpose: every dependency in this repository needs an ADR, and
a verifier meant to outlive the project is better with none. The ML-DSA-65 seal of a v2 record is
checked by both, so a dual-sealed record gets the same answer as from `deedglass verify`.

## Use

```sh
# Python (from verifiers/python, or with it on PYTHONPATH)
python3 -m deedglass_verify bundle.tar.gz --keys keys.json
python3 -m deedglass_verify deeds.jsonl --keys keys.json --strict --json

# JavaScript (Node)
node verifiers/js/deedglass-verify.mjs bundle.tar.gz --keys keys.json
```

```js
// JavaScript (browser or Node)
import { parseKeySet, verifyBundle, reportJSON, exitCode } from './deedglass-verify.mjs';
const keys = await parseKeySet(new Uint8Array(await (await fetch('keys.json')).arrayBuffer()));
const rep = await verifyBundle(new Uint8Array(await file.arrayBuffer()), keys);
console.log(reportJSON(rep).result); // valid | invalid | cannot_determine
```

The input is an export bundle (`.tar.gz`), a chain of records (`.jsonl`) or one record (`.json`).
Exit codes are the spec's: 0 everything verifies, 1 a failure, 2 cannot decide (no keys, a newer
format, an algorithm the verifier doesn't implement). With `--strict`, records must also be in
canonical form, and keys taken from inside a bundle aren't enough (exit 2): pass the issuer's keys.
`--json` prints a report with the same `result`, failure `code`, `seq` and `field`, and bundle
`manifest` and `manifest_seal` fields as `deedglass verify --json`.

What both check: the strict JSON profile, JCS (RFC 8785), the closed schema of each record version,
the body hash, every seal (Ed25519 and ML-DSA-65; keys of other algorithms are kept and never used),
key validity and revocation, the accept policy by `issued_at`, the chain (org, seq, links, time
order), the key set's safety rules (small-order and non-canonical Ed25519 keys refused), and, for a
bundle, the reader rules, the manifest against the files and the records, and the manifest's seal.

Not covered: the optional COSE_Sign1 encoding (spec §12), `--anchor`, `--all`, `--pin`, key URLs,
directories of records, and **head timestamps** (TY-03, `spec/v2/BUNDLE_FORMAT.md` §9). A bundle that
carries `timestamps.json` is read and the file is checked against the manifest like any other, with
the warning `timestamps_not_checked`; its RFC 3161 tokens and transparency-service inclusion proofs
are not checked, and heads are not compared with the chain (so a cut chain that only a timestamp
shows passes here). Use `deedglass verify` for those.

## How they are kept in agreement

`python3 verifiers/crosscheck.py <deedglass binary>` (or `make independent-verifiers`; CI workflow
`independent-verifiers`, not a required check yet) runs three verifiers side by side and fails on
any difference:

- **Release bundles.** Every bundle in [`../testdata/release-bundles/`](../testdata/release-bundles/)
  (one per release, the 2046 test), with the issuer's keys, with `--strict`, and with the bundle's
  own keys, plus 23 tampered copies of each: a changed record, a flipped seal, records removed,
  swapped or repeated, a blank line, the manifest's seal removed or broken, a non-canonical or wrong
  manifest, an edited README or key set, path traversal, an extra, duplicated, reordered or missing
  entry, a symlink, a damaged or cut gzip stream, and a key set missing the key that sealed the last
  records. The verdict compared is the result, the first failure's code, seq and field, and the
  manifest and manifest-seal status.
- **A bundle with timestamps.json** ([`../testdata/timestamps/`](../testdata/timestamps/), TY-03), with
  the issuer's keys, `--strict` and its own keys, plus five tampered copies: the file edited, removed,
  moved after the records, repeated, and dropped from an unsealed manifest.
- **Record vector files.** Every record vector file in [`../testdata/deeds/`](../testdata/deeds/)
  through all three command lines, with its key set (the valid ones also with `--strict`).
- **Expected answers.** Each verifier also runs every vector in [`../testdata/deeds/`](../testdata/deeds/)
  (v1 and v2: valid, lenient, chains and invalid, each also under `--strict`) and both parity corpora
  (500 v1 cases and 200 v2 cases) against the expected answers, which come from the reference. The two
  COSE vectors are skipped.

On 2026-10-20 all 288 cases agree (197 bundle cases, 91 vector files), and both verifiers match 848
of 848 vector checks.

One class is compared loosely: a damaged gzip stream. Where a damaged deflate stream is noticed
depends on how much each reader has buffered (a CRC mismatch only shows at the end), so all three must
refuse it, but the first failure they name can differ. For some such copies `deedglass verify` stops
with an input error (exit 2, `keys_invalid`) because the bundle's damaged `keys.json` doesn't parse,
even when `--keys` is given; the independent verifiers say `bundle_invalid` (exit 1).

## What "written from the spec" means here

The first run, written from the spec text and nothing else, matched 841 of 848 vector checks. The
seven differences were all about which `field` or `seq` a failure names, which the text doesn't fully
pin. They were settled from the vectors (and by reading the reference's order to understand them),
and are now written down so the next implementer doesn't need the code:

1. **The order inside check 5** (the closed schema, DEED_SPEC.md §11.1). First the envelope's
   members (unknown, then missing), then the types of its members; a v2 envelope's `seals` are
   checked completely at this point (members, types, `alg`, seal shape, key id). Then the body's
   members, then the types of its members in a fixed reading order, each nested object's members
   checked as it is read (`seal_keys`, `agent`, `action`, `policy`, `approval`, `execution`,
   `witness`, `cost`, `admin` with `actor` then `target`). Then the body's values (enums, patterns,
   lengths, presence by kind), with `org` checked after `seal_key_id`/`seal_keys`. Then `body_hash`,
   then (v1) `seal` and `seal_key_id`. So an unknown member anywhere is named before a bad value
   anywhere.
2. **The `seq` a failure names** is the body's `seq` whenever the body is an object and `seq` is a
   positive integer, even when that deed fails the schema; it is null for `malformed` and for a wrong
   `object`.
3. **Extra entries in `expected.json`.** A vector may name its own key set (`"keys"`), relative to
   `testdata/deeds/`.

The spec's change log records 1 and 2 as an erratum. Everything else (the seal inputs, the accept
policy, the chain rules, the bundle rules) was implemented from the text as written and agreed with
the reference the first time.

**Test keys.** The release bundles and vectors are sealed by Deedglass's published test keys. Both
verifiers warn `test_keys` when they see them; such records mean nothing outside tests.
