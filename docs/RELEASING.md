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
   pushes the image and makes a **draft** release, `sdk-build` attaches the npm and Python packages,
   and `npm-stage` builds the seven npm packages from this run's archives and prints the publish
   plan. With npm publishing on (below), `publish-npm` then puts them on npm; a release candidate
   goes to the `next` dist-tag, so `npx deedglass` keeps the last release.
7. Review the draft on GitHub: notes, five archives, `checksums.txt`, five SBOMs, and (public repo
   only) `checksums.txt.sigstore.json`. Publish it as a prerelease, or delete it.

## Cut 0.1.0

The same steps with `0.1.0` (Python `0.1.0`), then publish the draft as the latest release. The
image gets `0.1.0`, `0.1` and `latest`.

## After publishing

- `docker run --rm ghcr.io/deedglass/deedglass:0.1.0 version` → `0.1.0`, and the commit.
- `deedglass version --check` from an older build → `○ deedglass 0.1.0 is available.`
- Set the GHCR package to public (GitHub › Packages › deedglass › Package settings) when the repo is.

## npm: seven packages from one tag

One tag push publishes every npm package of the release from GitHub Actions, in this order:

1. the five platform packages `@deedglass/cli-darwin-arm64`, `@deedglass/cli-darwin-x64`,
   `@deedglass/cli-linux-arm64`, `@deedglass/cli-linux-x64` and `@deedglass/cli-win32-x64`, one
   binary each;
2. the wrapper `deedglass` (what `npx deedglass` runs), whose `optionalDependencies` pin the five at
   the same version and which carries `mcpName`;
3. `@deedglass/sdk`.

`npm-stage` (every release, read-only) runs `scripts/npm-publish.mjs stage`: every `package.json`
names the tag's version, the wrapper pins the five and its `mcpName` equals `server.json`'s name,
no package has an install script, each GoReleaser archive of this run matches `checksums.txt`, and
each binary (the `.exe` out of the Windows `.zip`) is built for its package's OS and CPU. It packs
the seven tarballs and prints the plan. `publish-npm` publishes exactly those tarballs with
`scripts/npm-publish.mjs publish`, over npm trusted publishing: GitHub's OIDC token, no npm token, no
`npm login` and no two-factor prompt per package. While the repository is public each package gets
npm provenance, linking it to this workflow run. A version npm already has is skipped, so
**Re-run failed jobs** after a fix publishes only what is missing; it stops at the first failure, so
the wrapper never reaches npm before its five binaries. npm packages go out when the run reaches
`publish-npm`, before you publish the draft GitHub release.

To try the packaging on your machine: `make release-check`, then `make npm-dry-run` stages the
snapshot's binaries the same way and prints the plan (nothing reaches npm).
`node --test scripts/npm-publish.test.mjs` (in `make workflows-check` and CI) covers the refusals.

## Turn on publishing (founder only)

npm and PyPI jobs exist and are off. Both use OIDC trusted publishing, never a stored token.

**npm, once:**

1. On npmjs.com, for each of the seven packages above: the package › Settings › Trusted
   publishing › GitHub Actions, with organization or user `deedglass`, repository `deedglass`,
   workflow filename `release.yml` and environment `npm`. A trusted publisher can only be added to a
   package that exists; all seven were published at 0.1.0 by hand.
2. GitHub › Settings › Secrets and variables › Actions › Variables › New repository variable
   `DEEDGLASS_PUBLISH_NPM` = `true`.

From then on a tag is the whole release step. Once one CI publish has worked, npm suggests setting
each package's Publishing access to require two-factor authentication and disallow tokens; trusted
publishing keeps working with that setting. If `publish-npm` stops with npm refusing the login,
the package named in the error is missing its trusted publisher (step 1).

**PyPI:** the repository variable `DEEDGLASS_PUBLISH_PYPI` = `true`, with a trusted publisher for
`release.yml` and environment `pypi` on PyPI.

Each job runs in its own environment (`npm`, `pypi`). A required reviewer on an environment pauses
that job for your approval on every release; leave it off to keep releases to a tag push. The docs
deploy job waits for `DEEDGLASS_DEPLOY_DOCS` and the `CLOUDFLARE_API_TOKEN` secret.

## MCP Registry

The MCP Registry lists `io.github.deedglass/deedglass` from `server.json`, and it checks that the
npm package `deedglass` at `server.json`'s version carries the same `mcpName`, so it can only run
after `publish-npm`. When the `mcp-registry` workflow is on `main`, run it by hand once npm serves
the release: `gh workflow run mcp-registry.yml`. `release.yml` doesn't start it: GitHub can't chain
one workflow's job to another's, and a registry entry is public, so it stays a deliberate step.

## Signing

While the repository is private, the workflow skips signing: keyless signatures and GitHub artifact
attestations write the repository and workflow identity into a public log. When the repository is
public, the same workflow signs `checksums.txt` and the image digest with cosign and records build provenance with GitHub's artifact attestations.

## Homebrew

The cask goes to `deedglass/homebrew-tap` only when the `HOMEBREW_TAP_TOKEN` secret exists (a
fine-grained token with contents write on that one repository). Without it the step is skipped.

## What this doesn't cover

macOS notarization, Windows code signing and OS packages.
