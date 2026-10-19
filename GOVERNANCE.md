# Governance

## Today

Deedglass has one maintainer, Arjun Prabhakaran, who reviews and merges every change and decides the
roadmap. That will change as people contribute; this file will say how when it does.

## How decisions are made

- **Architecture and dependencies** are decided in writing as ADRs in [`docs/adr/`](docs/adr/): the
  problem, the options, the choice and why. An ADR starts as Proposed and becomes Accepted or Rejected.
- **The deed format** is a public spec ([`spec/v1/DEED_SPEC.md`](spec/v1/DEED_SPEC.md), with
  [`spec/v1/BUNDLE_FORMAT.md`](spec/v1/BUNDLE_FORMAT.md), under CC-BY-4.0: [`spec/README.md`](spec/README.md)).
  Changing it needs an ADR and a new version tag in the format, so old deeds keep verifying.
- **Security** reports follow [`SECURITY.md`](SECURITY.md).

## Continuity

You should be able to rely on Deedglass even if this project stops:

- The core, the SDKs and the CLI are Apache-2.0. Anyone can fork and keep them going.
- The deed format is a public spec, and `deedglass verify` and the in-browser verifier check deeds
  offline with nothing but the public key. Your deeds stay checkable without us.
- Self-hosted, your data, your keys and your deeds stay on your machines. Nothing in the free
  product phones home.
