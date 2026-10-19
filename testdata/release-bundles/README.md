# Release bundles: the 2046 test (TY-11)

One sealed export bundle from every Deedglass release, kept forever. On every pull request the
`release-bundles` CI job checks each of them with the CLI built from that pull request
(`deedglass verify --strict`, manifest and manifest seal included) and with the browser verifier
(`web/verify/test/release-bundles.test.ts`). A change that stops any release's records from
verifying fails the job, which is required on `main` (`.github/required-checks.txt`).

| Path | What it is |
|---|---|
| `<version>/bundle.tar.gz` | 12 deeds of one test org (actions verified, approved and denied, a Unicode agent name, an admin stop, two amendments; key a seals 1–10, key b 11–12 after the rotation; key b seals the manifest), in the export bundle format of `docs/BUNDLE_FORMAT.md` |
| `<version>/keys.json` | the key set a reader trusts out of band (the published test keys) |
| `<version>/origin.json` | the release tag and commit, and the release's own CLI that verified the bundle when it was made |
| `<version>/SHA256SUMS` | the three files' SHA-256 (also in `testdata/SHA256SUMS`) |

**How a bundle is made.** `scripts/release-bundles.sh add vX.Y.Z` checks the tag out in a throwaway
worktree, builds that release's `deedglass`, copies `internal/deed/deedtest/cmd/genrelease` in and
runs it there, so the deeds, the bundle writer and the seals are that release's code. The output is
byte-identical every run (fixed keys, ids and times). The release's own CLI must verify it before
anything is written. Then the files are committed and never made again.

**Test keys only.** The bundles are sealed by the published test-vector keys
(`testdata/deeds/keys/test-keys.json`), so they mean nothing outside tests and every verifier
warns `test_keys`.

**What it doesn't cover.** A server export from a live database (the bundles come from the deed and
bundle code, not from `deedglass serve`), and COSE exports. `internal/deed` is the same from v0.1.0
to v0.4.0, so the first six bundles hold the same deeds and differ only in the version their
manifests name; the test earns its keep at the first format change (deed v2, TY-01 and TY-02).
