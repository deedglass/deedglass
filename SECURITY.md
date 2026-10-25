# Security policy

Deedglass records what AI agents did, so people rely on it when something goes wrong. If you find
a way to make it record the wrong thing, run something it shouldn't, or show one org's data to
another, we want to hear about it, and we are grateful when you tell us first.

## Reporting

- **Email `security@deedglass.com`.** The address is being set up. Until this note is removed,
  also send your report through GitHub private vulnerability reporting (below), so it can't be
  lost.
- **GitHub private vulnerability reporting** on `github.com/deedglass/deedglass` (Security ›
  Report a vulnerability): <https://github.com/deedglass/deedglass/security/advisories/new>.
- Please don't open a public issue, pull request or discussion for a security problem.

No encryption key is published yet. If your report holds something sensitive, say so in a first
message and we will agree a channel.

## What to include

- What you found and what an attacker could do with it.
- The steps to reproduce it, from a fresh `deedglass init` if you can.
- The version (`deedglass version`), the operating system, and the Postgres version.
- Whether you want to be credited, and how.

## What we will do

We are a small team, so these are the times we can keep:

- **Acknowledge** your report within 3 business days.
- **Assess** it within 7 business days: whether we can reproduce it and how severe it is (the
  scale in `docs/SECURITY_REVIEW_2026-10.md`).
- **Fix or mitigate** a high-severity issue within 30 days, or tell you why it takes longer and
  what we are doing meanwhile. Medium and low issues are scheduled and you hear when.
- **Tell you** before we publish a fix, and credit you in the release notes and the security
  review if you want credit.

There is **no bug bounty** at this stage. We can't pay for reports yet, and we won't pretend
otherwise.

## Scope

- The `deedglass` binary: `serve`, the Gate, policies, connectors, the vault, the Witness and the
  decoder, deeds and `deedglass verify`.
- The Console and the approval pages it serves.
- The CLI, including `deedglass init`, `doctor`, `scan`, `mcp wrap`, `a2a proxy`, `witness debezium`
  and `guard shell`.
- The independent verifiers in `verifiers/` and the browser verifier.
- Release artifacts: the archives, `checksums.txt` and its signature, the SBOMs, the image on GHCR,
  the npm packages, and `scripts/install.sh`.
- The SDKs: `@deedglass/sdk` (TypeScript) and `deedglass` (Python).
- docs.deedglass.com and deedglass.com.
- The hosted beta, when it exists.

## Out of scope

- Volumetric denial of service, and load tests against anything you don't run yourself.
- Social engineering of anyone, and physical attacks.
- Findings that need a compromised Deedglass host, database superuser or seal key first.
- Missing headers on static pages with no sensitive data, without a demonstrated impact.
- Rate-limit reports without demonstrated harm.
- How often a model falls for a prompt injection. Deedglass limits and records what a manipulated
  model's tool calls can do; it doesn't measure the model.

## Safe harbour

We won't pursue or support legal action against research done in good faith within this policy.
In return, please:

- Test against your own installation, or your own org on a hosted instance.
- Don't access, change or keep anyone else's data. If you see someone else's data, stop, and tell
  us what you saw without copying it further.
- Give us a reasonable time to fix a problem before you talk about it publicly.

## Supported versions

The latest minor release gets security fixes. Before 1.0, that is the latest release.

## Checking a release

Every release since v0.1.0 signs `checksums.txt` with keyless cosign from this repository's release
workflow; each archive has an SPDX SBOM beside it, and the npm packages carry npm provenance (from
0.1.1; the first two npm versions predate it).
[INSTALL.md](docs/INSTALL.md#check-what-you-downloaded) has the commands. `scripts/install.sh`
checks the signature itself when cosign is installed.

## How Deedglass is built to resist attack

- [Threat model](docs/THREAT_MODEL.md): assets, trust boundaries, attackers, and the control and
  test for each threat, with what stays possible.
- [Attack suite](docs/ATTACK_SUITE.md): every case's expected outcome written down before its code,
  and the latest run (`make attack`).
- [Security review](docs/SECURITY_REVIEW_2026-10.md): what was checked, what was found and what was
  done.
- [Limitations](docs/LIMITATIONS.md): every known limit.
- Static analysis: CodeQL (Go, JavaScript and TypeScript, Python, and the workflows) on every pull
  request and weekly (`.github/workflows/codeql.yml`), beside `govulncheck`, the fuzz targets and the
  in-repo guards. Every workflow action is pinned to a commit SHA.
