# Releasing Deedglass

Pushing a `v*` tag on `main` is the decision to release: the founder's hand, or Claude's acting for
him. Claude prepares and checks first. With npm publishing on, that tag push alone puts the seven
npm packages on npm (a stable version moves `latest`) before anyone looks at the draft GitHub
release, so tag only a commit you would ship. The workflow is `.github/workflows/release.yml`, the
config `.goreleaser.yaml`.

## Cut a release candidate

1. Set the version everywhere: `sdk/typescript/package.json` and `npm/*/package.json` to
   `0.1.0-rc.1`, `sdk/python/src/deedglass/_version.py` to `0.1.0rc1`, and add `## [0.1.0-rc.1]` at
   the top of `CHANGELOG.md`.
2. `scripts/check-versions.sh v0.1.0-rc.1` → every line `✓`.
3. `make ci && make integration && make attack && make release-check` → all green, ending with
   `✓ 5 binaries · 5 archives · checksums · 5 SBOMs · image linux/amd64 + linux/arm64 · reproducible`.
4. `make bench-quick && BENCH_QUICK=1 make bench-check` → `6 targets met · 0 missed`.
5. Merge to `main` after green CI, then tag `main` and push the tag (`verify` refuses a tag whose
   commit isn't on `main`):

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
   only) `checksums.txt.sigstore.json`. Publish it as a prerelease. With npm publishing on,
   deleting the draft does **not** undo npm: the packages are already there and npm versions can't
   be replaced. To back out, see [Rollback](#rollback-npm).

## Cut 0.1.0

The same steps with `0.1.0` (Python `0.1.0`), then publish the draft as the latest release. The
image gets `0.1.0`, `0.1` and `latest`.

## After publishing

- **Add the release's bundle (the 2046 test, TY-11)** in the first PR after the tag, for a stable
  release (a prerelease keeps none): `git fetch --tags && scripts/release-bundles.sh add v0.1.0`
  → `✓ v0.1.0 · testdata/release-bundles/0.1.0 · sealed by v0.1.0's code, verified by deedglass
  0.1.0 …`. Its `bundle.tar.gz` has the same SHA-256 as the one in the release run's
  `release-bundle` artifact (the bundle is byte-identical wherever it is made). Commit
  `testdata/release-bundles/0.1.0/` and the lines it added to `testdata/SHA256SUMS`. Until that PR
  merges, the required `release-bundles` job fails on every other PR (`✕ no release bundle for:
  v0.1.0`). See [The 2046 test](#the-2046-test).
- `docker run --rm ghcr.io/deedglass/deedglass:0.1.0 version` → `0.1.0`, and the commit.
- `deedglass version --check` from an older build → `○ deedglass 0.1.0 is available.`
- Set the GHCR package to public (GitHub › Packages › deedglass › Package settings) when the repo is.

## The 2046 test

Every release leaves one sealed export bundle in `testdata/release-bundles/<version>/`, made from
that release's own code and kept forever (`testdata/release-bundles/README.md`). On every PR the
required CI job `release-bundles` verifies all of them with the CLI built from the PR
(`deedglass verify --strict`, manifest seal included) and with the browser verifier, so a change
that would stop an old release's records from verifying can't merge. `make ci` runs the CLI half
(`make release-bundles`) and `make web-verify-test` the browser half. The release run's `verify` job
runs the same check against the tag, then makes the tag's bundle and uploads it as the
`release-bundle` artifact. A committed bundle is never made again or edited; if a format change
means an old bundle no longer verifies, the change is wrong, not the bundle.

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
the wrapper never reaches npm before its five binaries. A platform package npm already has must
hold this run's binary (the same sha256 inside npm's tarball); if it holds another build's (a tag
deleted and pushed again on a new commit), nothing is published and the version has to be bumped.
npm packages go out when the run reaches `publish-npm`, before you publish the draft GitHub release.

To try the packaging on your machine: `make release-check`, then `make npm-dry-run` stages the
snapshot's binaries the same way and prints the plan (nothing reaches npm).
`node --test scripts/npm-publish.test.mjs` (in `make workflows-check` and CI) covers the refusals.

## Turn on publishing (founder only)

npm and PyPI jobs exist and are off. Both use OIDC trusted publishing, never a stored token.

npm and PyPI check only the repository owner, the repository, the workflow filename and the
environment name, never the git ref, and GitHub creates a missing environment, unprotected, the
first time a job names it. So the environments themselves must allow `v*` tags only; otherwise a
pushed branch that rewrites `release.yml` (keeping `environment: npm`, dropping the gate) could
publish all seven packages with real-looking provenance.

**Once, before the first variable is set:**

1. The environments `npm` and `pypi`, each limited to `v*` tags: GitHub › Settings › Environments ›
   New environment › `npm` › Deployment branches and tags › Selected branches and tags › Add
   deployment branch or tag rule › Ref type **Tag** › `v*`; the same for `pypi`. Or:

   ```sh
   for e in npm pypi; do
     gh api -X PUT repos/deedglass/deedglass/environments/$e --input - >/dev/null \
       <<<'{"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}'
     gh api -X POST repos/deedglass/deedglass/environments/$e/deployment-branch-policies \
       -f name='v*' -f type=tag >/dev/null
   done
   ```

   Check, for `npm` and `pypi`:
   `gh api repos/deedglass/deedglass/environments/npm --jq .deployment_branch_policy` →
   `{"custom_branch_policies":true,"protected_branches":false}`, and
   `gh api repos/deedglass/deedglass/environments/npm/deployment-branch-policies --jq '.branch_policies[] | [.name, .type]'`
   → `["v*","tag"]` and nothing else. Leave required reviewers off: one would pause every release
   for your approval. Optional: a tag ruleset (Settings › Rules › Rulesets › New tag ruleset,
   target `v*`, restrict creations, bypass for repository admins) so only admins can push a
   release tag.

**npm, once:**

1. On npmjs.com, for each of the seven packages above, `https://www.npmjs.com/package/<name>/access`
   › Trusted Publisher › GitHub Actions: organization or user `deedglass`, repository `deedglass`,
   workflow filename `release.yml`, environment name `npm`, and under Allowed actions tick
   **npm publish** (leave npm dist-tag off). Since 3 September 2026 a new trusted publisher allows
   only `npm stage publish` unless npm publish is ticked, and a staged package waits for a
   maintainer's two-factor approval. The command-line equivalent, after `npm login`, is
   `npm trust github <name> --file release.yml --repo deedglass/deedglass --env npm --allow-publish`
   (npm 11.15.0 or later). A trusted publisher can only be added to a package that exists; all seven
   were published at 0.1.0 by hand. A new trusted publisher expires unless it publishes within two
   days, so tag a release within two days of adding them.
2. GitHub › Settings › Secrets and variables › Actions › Variables › New repository variable
   `DEEDGLASS_PUBLISH_NPM` = `true` (only after the environment check above passes).

From then on a tag is the whole release step. Once one CI publish has worked, npm suggests setting
each package's Publishing access to require two-factor authentication and disallow tokens; trusted
publishing keeps working with that setting. If `publish-npm` stops with npm refusing the login,
the package named in the error is missing its trusted publisher, or npm publish isn't among its
allowed actions (step 1).

**PyPI, once** (the `publish-pypi` job uploads the wheel and sdist that `sdk-build` made, with
`pypa/gh-action-pypi-publish` over OIDC; nothing reaches PyPI until both steps are done):

1. No `deedglass` project exists on PyPI yet, so add a *pending* trusted publisher, which creates
   the project on its first upload: pypi.org › Your account › Publishing › Add a new pending
   publisher › GitHub: PyPI project name `deedglass`, owner `deedglass`, repository `deedglass`,
   workflow name `release.yml`, environment name `pypi` (the `pypi` environment limited to `v*`
   tags first, as above). A pending publisher doesn't reserve the name until it is used, so tag a
   release soon after.
2. The repository variable `DEEDGLASS_PUBLISH_PYPI` = `true`. The next `vX.Y.Z` tag publishes.
3. After the first upload, switch the install lines from the GitHub form back to
   `pip install deedglass`: `README.md`, `sdk/python/README.md`, `docs/SDK_PYTHON.md`,
   `docs-site/src/content/docs/get-started/python-sdk.mdx`, `examples/python/README.md` and the
   `sdk-python` line in `internal/setup/init.go`.

The docs deploy job waits for `DEEDGLASS_DEPLOY_DOCS` and the `CLOUDFLARE_API_TOKEN` secret.

## Rollback (npm)

npm never lets a version be replaced, and deleting the draft GitHub release leaves npm as it is.
A release candidate only moved `next`; leave it, and fix forward with `-rc.N+1`. For a bad stable
version, point `latest` back at the previous one and deprecate the bad one (an npm login with
two-factor; trusted publishing can't do this while npm dist-tag isn't an allowed action):

```sh
npm dist-tag add deedglass@0.1.0 latest
npm dist-tag add @deedglass/sdk@0.1.0 latest
npm deprecate deedglass@0.1.1 "0.1.1 is broken; use 0.1.0 or 0.1.2"
npm deprecate @deedglass/sdk@0.1.1 "0.1.1 is broken; use 0.1.0 or 0.1.2"
```

The five platform packages need nothing: the wrapper pins exact versions. Then fix forward with
the next patch version.

## MCP Registry

The MCP Registry lists `io.github.deedglass/deedglass` from `server.json`, and it checks that the
npm package `deedglass` at `server.json`'s version carries the same `mcpName`, so it can only run
after `publish-npm`. `release.yml`'s `mcp-registry` job calls the `mcp-registry` workflow after
`publish-npm` on every `vX.Y.Z` tag (never a release candidate), behind the same
`DEEDGLASS_PUBLISH_NPM` gate: it waits up to 10 minutes for npm to serve the version, then publishes
`server.json` over GitHub OIDC, so the registry entry follows npm. To republish by hand (for example
after a failed run): `gh workflow run mcp-registry.yml`.

## Signing

The repository is public, so every release signs `checksums.txt` and the image digest with keyless
cosign (GitHub OIDC; no signing key or secret is stored) and records build provenance with GitHub's
artifact attestations; both write the repository and workflow identity into a public log. The
workflow would skip signing only if the repository were made private again. `scripts/install.sh`
checks `checksums.txt.sigstore.json` when cosign is installed (identity
`https://github.com/deedglass/deedglass/.github/workflows/release.yml@refs/tags/v*`, issuer
`https://token.actions.githubusercontent.com`) and prints the command otherwise; the manual check is
in [INSTALL.md](INSTALL.md#check-what-you-downloaded).

## Homebrew

The cask goes to `deedglass/homebrew-tap` only when the `HOMEBREW_TAP_TOKEN` secret exists (a
fine-grained token with contents write on that one repository). Without it the step is skipped.

## What this doesn't cover

macOS notarization, Windows code signing and OS packages.
