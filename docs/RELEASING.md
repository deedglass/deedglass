# Releasing Deedglass

The founder cuts every release. Claude prepares and checks; nothing becomes public without the
founder's hand on it. The workflow is `.github/workflows/release.yml`, the config `.goreleaser.yaml`.

## Cut a release candidate

1. Set the version everywhere: `sdk/typescript/package.json` and `npm/*/package.json` to
   `0.1.0-rc.1`, `sdk/python/src/deedglass/_version.py` to `0.1.0rc1`, and add `## [0.1.0-rc.1]` at
   the top of `CHANGELOG.md`.
2. `scripts/check-versions.sh v0.1.0-rc.1` → every line `✓`.
3. `make ci && make integration && make attack && make release-check` → all green, ending with
   `✓ 5 binaries · 5 archives · checksums · 5 SBOMs · image linux/amd64 + linux/arm64 · reproducible`.
4. `make bench-quick && BENCH_QUICK=1 make bench-check` → `6 targets met · 0 missed`.
5. Commit, then tag and push the tag:

   ```sh
   git tag -a v0.1.0-rc.1 -m "Deedglass 0.1.0-rc.1" && git push origin v0.1.0-rc.1
   ```

6. Watch the run: `gh run watch --exit-status $(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')`.
   `verify` runs every gate (and records the attack results against the tag), `release` builds and
   pushes the image and makes a **draft** release, `sdk-build` attaches the npm and Python packages.
7. Review the draft on GitHub: notes, five archives, `checksums.txt`, five SBOMs, and (public repo
   only) `checksums.txt.sigstore.json`. Publish it as a prerelease, or delete it.

## Cut 0.1.0

The same steps with `0.1.0` (Python `0.1.0`), then publish the draft as the latest release. The
image gets `0.1.0`, `0.1` and `latest`.

## After publishing

- `docker run --rm ghcr.io/deedglass/deedglass:0.1.0 version` → `0.1.0`, and the commit.
- `deedglass version --check` from an older build → `○ deedglass 0.1.0 is available.`
- Set the GHCR package to public (GitHub › Packages › deedglass › Package settings) when the repo is.

## Turn on publishing (founder only)

npm and PyPI jobs exist and are off. To turn one on: GitHub › Settings › Secrets and variables ›
Actions › Variables › New repository variable `DEEDGLASS_PUBLISH_NPM` (or `DEEDGLASS_PUBLISH_PYPI`) =
`true`. Each job runs in its own environment (`npm`, `pypi`); add yourself as a required reviewer
there once the repo is public. Both use OIDC trusted publishing, never a stored token. npm's first
publish of a new package is yours, by hand, before trusted publishing can be configured on its
settings page. The docs deploy job waits for `DEEDGLASS_DEPLOY_DOCS` and the `CLOUDFLARE_API_TOKEN`
secret.

## Signing

While the repository is private, the workflow skips signing: keyless signatures and GitHub artifact
attestations write the repository and workflow identity into a public log. When the repository is
public, the same workflow signs `checksums.txt` and the image digest with cosign and records build provenance with GitHub's artifact attestations.

## Homebrew

The cask goes to `deedglass/homebrew-tap` only when the `HOMEBREW_TAP_TOKEN` secret exists (a
fine-grained token with contents write on that one repository). Without it the step is skipped.

## What this doesn't cover

macOS notarization, Windows code signing, OS packages and the MCP Registry submission (S37).
