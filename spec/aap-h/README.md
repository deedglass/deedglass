# Agent Action Provenance for Health (AAP-H)

**v0.1.0-draft, 2026-10-26.** An open, vendor-neutral draft for recording what an AI agent did to
health data: which agent and model, what the policy decided, who approved it, what the agent
claimed, and what was checked afterwards by reading the system of record. At its top level the
record is signed and chained, so a third party can check it offline.

Any EHR, payer, health-tech or AI vendor may implement it. Deedglass is one implementation (the
reference one); nothing in the spec requires Deedglass.

## Read

| File | What it is |
|---|---|
| [`SPEC.md`](SPEC.md) | The specification: record model, FHIR R4 profiles, the JSON envelope, BALP mapping, signing, verification rules, conformance levels L1 to L3, the Deedglass mapping, open questions |
| [`RESEARCH.md`](RESEARCH.md) | What exists (FHIR Provenance and AuditEvent, IHE BALP, HL7 AI Transparency, SMART, Da Vinci PAS, ABDM and NHCX, W3C VC, C2PA) and the gap, with sources |
| [`fhir/`](fhir/) | FHIR R4 StructureDefinitions (2 profiles, 7 extensions), CodeSystems and ValueSets, canonical base `https://deedglass.com/fhir/aap-h` |
| [`schema/aaph-envelope.schema.json`](schema/aaph-envelope.schema.json) | JSON Schema 2020-12 for the envelope |
| [`schema/aaph-fhir.schema.json`](schema/aaph-fhir.schema.json) | A lightweight JSON Schema check of the AAP-H FHIR resources (not a FHIR validator) |
| [`examples/`](examples/) | Synthetic examples: an NHCX pre-authorisation (India), a Da Vinci PAS submission whose read-back doesn't match (US), a refused ABDM record write, a converted Deedglass deed, their FHIR views, a test key set, and envelopes the schema must refuse |
| [`tools/`](tools/) | `genfhir.py` writes `fhir/`; `envelope2fhir.py` turns an envelope into its FHIR views. Python standard library only |
| [`aaph_test.go`](aaph_test.go) | The checks below |

## Check

```sh
go test ./spec/aap-h/
```

The test validates every example against the schemas, checks the FHIR conformance resources and
views against each other (every extension, sub-extension and bound code defined; each view says what
its envelope says), verifies the signed examples and their chains, refuses tampered copies, and
converts every action deed in `testdata/deeds/` (v1 and v2) to an envelope, checks the deed with the
Go verifier and checks the envelope against it. No FHIR validator, Java or network is needed. Run a
FHIR validator over `fhir/` and `examples/fhir/` for full FHIR validation; that isn't part of this
check yet.

`go test ./spec/aap-h/ -update` re-signs the `aaph-jcs` examples with the public test key and rewrites
`examples/keys.json` and `examples/deedglass-native.envelope.json`. Then regenerate the FHIR views:

```sh
cd spec/aap-h
for n in nhcx-preauth pas-submit abdm-denied; do python3 tools/envelope2fhir.py examples/$n.envelope.json $n examples/fhir; done
```

**The test key is not a key.** Its seed is public (SHA-256 of `aap-h/test-key/1`). A record it signs
means nothing outside these examples.

## Licences

- **The specification text** (`SPEC.md`, `RESEARCH.md`, this README) is licensed under the
  **Creative Commons Attribution 4.0 International licence (CC-BY-4.0)**, the same licence as the
  rest of `spec/` ([`../LICENSE`](../LICENSE)). Copy, translate, adapt and republish it, including in
  a commercial product or a standards submission, with credit.
- **The schemas, StructureDefinitions, CodeSystems, ValueSets, examples, tools and test**
  (`schema/`, `fhir/`, `examples/`, `tools/`, `aaph_test.go`) are licensed under the **Apache License
  2.0**, the repository's code licence ([`../../LICENSE`](../../LICENSE)), so they can go straight into
  products and FHIR packages.

Credit it as:

> Agent Action Provenance for Health (AAP-H) v0.1.0-draft, by Arjun Prabhakaran and the Deedglass
> contributors, https://github.com/deedglass/deedglass/tree/main/spec/aap-h. Text CC-BY-4.0;
> schemas and profiles Apache-2.0.

## Status and changes

This is a draft for comment. Anything may change before v1.0, including names, canonical URLs and
codes. Each published version stays in this folder's history; a version that changes what a
verifier does gets a new `aaph` value in the envelope and a new signature tag.

Comments: open an issue or a pull request, or reply on the chat.fhir.org thread. Section 13 of the
spec lists the questions we most want answered.

## Where this goes

The draft lives here, next to the code and test vectors that keep it honest. The aim is a neutral
home: we will propose it to the relevant HL7 work groups (Security, and the group that owns the AI
Transparency IG) as input to an HL7 FHIR implementation guide. If HL7 or another standards body
takes it on, the canonical URLs move to that body's namespace, this draft's URLs stay as aliases,
and the licences stay CC-BY-4.0 and Apache-2.0 or become more open.
