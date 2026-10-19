# Making the repo public (launch morning, founder only)

An ordered checklist. Nothing here runs by itself: each step is the founder's, in this order, on
launch morning (`~/txv/ops/launch/launch_day_runbook.md`). The repo goes public **before** the
`v0.1.0` tag is pushed, because keyless signing needs the public repo.

## 1. Gates (07:00 IST)

```sh
scripts/rc.sh --quick           # ci, integration, attack, demo, site, release: every line ✓
scripts/prepublic_audit.sh      # "No blockers"; read the ○ findings below
```

If the audit reports a **secret**, stop. Rotate it first; whether to rewrite history is your call.

### Triage of the audit's ○ findings (S37, 2026-10-05)

| Category | What it found | Decision needed |
|---|---|---|
| email (commit metadata) | Every commit's author and committer is your personal Gmail address. It becomes public with the history | Keep it, or rewrite history to `you@users.noreply.github.com` before the switch (destructive: every commit hash changes). Recommendation: keep the history; set `git config user.email` to a `@deedglass.com` or GitHub noreply address for every commit after the switch |
| email (content) | Test and example addresses (`priya@acme.com`, `owner@acme.example.test`, …). `acme.com` is a real domain | Fine to publish; good-first-issue 2 moves them to `acme.test` |
| path | `~/txv/ops/...` and `/Users/you/...` in CLAUDE.md, docs and snippets | Fine to publish (they name your layout, not secrets). CLAUDE.md is the builder's rulebook; keep or trim it |
| plan | `prompts-v7` session references in tests, attack metadata and docs | Fine to publish; they explain where a rule came from |

The secret-shaped strings in tests and docs are examples, sentinels and published test vectors;
`scripts/prepublic_audit.allow` names each one with its reason.

## 2. Wording (07:30)

```sh
git apply ~/txv/ops/launch/public-switch.patch                 # product: "open source" leaves the banned lists
git -C ~/txv/site apply ~/txv/ops/launch/public-switch-site.patch   # site: the same, and REPO_PUBLIC = true
make ci && (cd ~/txv/site && npm run check)
```

Then change "Self-hosted · Apache-2.0" wording to "Open source" only where you want it, and add the
README badges if you want them. Approve CLAIMS row P-007 before any copy says "open source".

## 3. GitHub settings (08:00)

In github.com/deedglass/deedglass › Settings:

1. **General:** visibility public; description `An agent said it updated 1 row. Postgres changed
   301. Deedglass checks what AI agents really did, enforces your rules, keeps signed records and has
   undo.` (set 10-14); website `https://deedglass.com`; topics `ai-agents`, `mcp`,
   `model-context-protocol`, `postgres`, `postgresql`, `logical-decoding`, `agent-security`,
   `ai-safety`, `audit-log`, `policy-engine`, `golang`; social preview: the site's
   `github-social-1280x640.png`; Discussions on, with an **I ran it** category and **Q&A**.
2. **Code security:** private vulnerability reporting on; secret scanning and push protection on;
   Dependabot alerts and security updates on; CodeQL default setup on.
3. **Rules:** a ruleset on `main` (required status checks: the CI jobs listed in
   `.github/required-checks.txt`, `release-bundles` among them; no force push; no deletion)
   and one on tags `v*` (only you can create them).
4. **Environments:** `npm` and `pypi`, each with you as the required reviewer.
5. **Packages:** GHCR package `deedglass` public and linked to the repo.
6. **Teams:** create `deedglass/maintainers` with yourself, so `.github/CODEOWNERS` resolves.
7. **Variables:** `DEEDGLASS_PUBLISH_PYPI=true` (leave npm off; its first publish is by hand).
8. **Labels:** `i-ran-it`, `good first issue`, and the `area: …` labels used in
   `.github/good-first-issues.md`; then file those ten issues.

## 4. Then

Push `main`, push the tag `v0.1.0`, and follow the runbook from 08:30.

## What this doesn't do

It doesn't check the site or the docs site for private notes; `scripts/rc.sh` runs the banned-words
check over both. Once the history is public it stays public: anything you want gone must go before
step 3.
