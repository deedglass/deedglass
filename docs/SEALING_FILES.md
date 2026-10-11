# Sealing files

`deedglass deeds seal-file` seals a file you hold, such as a report you send to someone, as a deed.
Anyone with the deed, the file and your public seal keys can check later that the file is the one
you sealed, offline.

```
$ deedglass deeds seal-file sample-blast-radius-report.pdf --label "Full scan report · sample · Acme demo"
✓ Sealed sample-blast-radius-report.pdf · 214,388 bytes · sha256:3f9a…c21e
  deed_01k6…  written to sample-blast-radius-report.pdf.deed.json
  Footer: Sealed as deed_01k6… · sha256:3f9a…c21e · check it with deedglass verify

$ deedglass verify sample-blast-radius-report.pdf.deed.json --file sample-blast-radius-report.pdf --keys keys.json
✓ 1 deed · seals intact · chain not checked · 1 seal key
✓ File matches · sample-blast-radius-report.pdf · 214,388 bytes · sha256:3f9a…c21e
  label      Full scan report · sample · Acme demo

$ deedglass verify sample-blast-radius-report.pdf.deed.json --file edited.pdf --keys keys.json
✓ 1 deed · seals intact · chain not checked · 1 seal key
✕ edited.pdf doesn't match the deed. Its sha256 is sha256:77b0…91d4 (214,388 bytes); the deed sealed another file labelled "Full scan report · sample · Acme demo".
  next       Check that you have the file that was sealed. One changed byte gives a different sha256.
```

(Numbers and hashes are placeholders.)

## How it works

1. The CLI reads the file and computes its SHA-256 and size. **The file never leaves your machine.**
2. It sends four fields to `POST /v1/deeds/files` with an org admin key: `sha256`, `bytes`,
   `media_type` (from the extension, or `--media-type`: `application/pdf`, `text/markdown`,
   `application/json`, `text/plain`, `text/html`) and `label` (1 to 120 characters, no line breaks).
3. The server seals the admin deed `file.sealed` in your org's deed chain. The label is the deed's
   `admin.reason`; `admin.data_digest` is the SHA-256 of the JCS of the four fields
   (`{"bytes":…,"label":…,"media_type":…,"sha256":"sha256:…"}`). The target is your org.
4. The CLI writes the deed envelope (its canonical JSON) to `<path>.deed.json`, or `--out`.

`deedglass verify <deed.json> --file <path>` first checks the deed exactly as `verify` always does
(the seal, the body hash, the key; [VERIFYING_DEEDS.md](VERIFYING_DEEDS.md)). Then it hashes the
file, takes the label from the deed, and recomputes `data_digest` for each of the four media types.
A match prints `File matches`; no match exits 1 (`file_mismatch`). A deed that isn't `file.sealed`
exits 2 (`not_a_sealed_file`). `--json` adds a `file` object (`matches`, `sha256`, `bytes`,
`media_type`, `label`, `deed`) to the verification report. The deed file is an ordinary deed
envelope, so the browser verifier and the SDK verifiers check its seal too.

## Who may seal

An org admin key, or an admin+ Console session. Agent keys are refused (403 `insufficient_scope`):
an agent's actions are sealed by the Gate, never by the agent.

## The report footer

The full scan report prints `Sealed as deed_… · sha256:… · check it with deedglass verify` from
`seal-file`'s output. It never says the findings are complete or official.

## What it doesn't do

- It seals the file's hash, size, type and label. It says nothing about whether the file's
  contents are right.
- The deed shows that the org's seal key sealed this hash at `issued_at`. It doesn't show who wrote
  the file, or that nobody had it before.
- A mismatch can't show the sealed file's hash: the deed holds only the digest of the four fields.
  It shows the label, so you can ask for the right file.
- It doesn't store the file. Keep the file and its `.deed.json` together.
