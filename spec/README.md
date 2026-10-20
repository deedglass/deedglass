# The Deedglass record spec

A Deedglass record (a *deed*) is the sealed record of one thing an AI agent did, was refused, or was
later found to have done differently. This directory holds the formats exactly enough that anyone
can write a verifier from them alone, in any language, and match the test vectors byte for byte.

The text in this directory is published under the **Creative Commons Attribution 4.0 International
licence (CC-BY-4.0)**, in [`LICENSE`](LICENSE). The code that implements it (the rest of this
repository, outside `ee/`) is Apache-2.0. You may copy, translate, adapt and republish the spec,
including in a commercial product or a standards submission, as long as you give credit (below).

## Editions

Each edition is a directory. An edition is never rewritten: a later change lands in a new edition or,
for a fix that doesn't change what a verifier does (a typo, a clearer sentence, a broken link), as a
dated erratum in [`CHANGELOG.md`](CHANGELOG.md). Every edition stays here for good, so a record
written in 2026 can still be checked against the text it was written under in 2036.

| Edition | Contents | Status | Since |
|---|---|---|---|
| [`v1/`](v1/) | [Deed v1](v1/DEED_SPEC.md) (body, canonical form, seal, chain, envelope, keys, verification, COSE) and [Export bundle v1](v1/BUNDLE_FORMAT.md) | Normative | 2026-10-01; CC-BY-4.0 since 2026-10-19 |
| [`v2/`](v2/) | [Deed v1 and v2](v2/DEED_SPEC.md): every seal names its algorithm, the accept policy, ML-DSA-65 seals beside Ed25519 (ADR-109); [Export bundle](v2/BUNDLE_FORMAT.md) (unchanged format) | Normative (ADR-109, proposed) | 2026-10-19 |

The record format has its own version number inside every record (`"v": 1` or `"v": 2`). The edition
number of this directory follows it: edition 1 describes `v: 1` records and v1 bundles; edition 2
describes `v: 1` and `v: 2` records and v1 bundles.

**Test vectors** are in [`../testdata/deeds/`](../testdata/deeds/) (deeds, key sets, expected
results) and [`../testdata/release-bundles/`](../testdata/release-bundles/) (one sealed bundle from
every release, checked on every pull request). Vectors are kept for every version, for good.

## Implementations

- `deedglass verify`, in Go (`internal/deed`), the reference verifier.
- The browser verifier (`web/verify`), dependency-free TypeScript, which runs offline.
- The verifiers in the TypeScript and Python SDKs (`sdk/typescript/src/verify`,
  `sdk/python/src/deedglass/verify`).

### Independent verifiers

Two verifiers written from this text rather than ported from the Go code (TY-10), with no
dependencies: a Python one ([`../verifiers/python/`](../verifiers/python/), standard library only)
and a single-file JavaScript one for Node 20+ and browsers
([`../verifiers/js/deedglass-verify.mjs`](../verifiers/js/deedglass-verify.mjs)). Both check v1 and v2
records, Ed25519 and ML-DSA-65 seals, chains and export bundles. A CI workflow runs them beside
`deedglass verify` on every release bundle, tampered copies of each and every vector, and fails on any
different verdict. [`../verifiers/README.md`](../verifiers/README.md) says how to run them and what the
text left to the vectors.

If you write another verifier, open an issue or a pull request that adds it to this list with how
you ran the vectors. An implementation outside this project is the best check that the text is
complete.

## Where the spec lives

The spec lives in this repository for now, next to the code and vectors that keep it honest: a
change to the format, its vectors and both verifiers land in one pull request. If someone outside
the project implements it, we will consider offering it to a neutral home (for example a Linux
Foundation project, the IETF, or OASIS). Any move keeps CC-BY-4.0 or a more open licence, keeps
every edition and its vectors, and keeps old records verifiable.

## How to credit it

> Deedglass record spec, edition 1 (`spec/v1/`), by Arjun Prabhakaran and the Deedglass
> contributors, https://github.com/deedglass/deedglass/tree/main/spec/v1, licensed under CC-BY-4.0.

Say whether you changed it. A translation or an adapted copy should link back to the edition it is
based on.

## Related, not part of the spec

- Record exports to other formats (OpenTelemetry GenAI, OCSF, W3C PROV-JSON, FHIR) are views of a
  record, not the record: [`../docs/EXPORTS.md`](../docs/EXPORTS.md).
- The intent-vs-effect corpus, the scenarios a record's check is meant to catch, is in
  [`../corpus/`](../corpus/), also under CC-BY-4.0.
