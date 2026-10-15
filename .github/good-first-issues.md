# Good first issues

The drafts that used to live here were filed on GitHub on 2026-10-15, each checked against the
code first and given the files involved:
[good first issue](https://github.com/deedglass/deedglass/labels/good%20first%20issue) ·
[help wanted](https://github.com/deedglass/deedglass/labels/help%20wanted).

| Issue | What | Labels |
|---|---|---|
| #11 | serve: cap open connections (KI-004) | good first issue, help wanted, area: server |
| #12 | doctor: warn when the open-file limit is low (KI-004) | good first issue, area: doctor |
| #13 | tests: use acme.test instead of acme.com | good first issue, area: tests |
| #14 | scan: add MongoDB's MCP server to the catalog | good first issue, help wanted, area: scan |
| #15 | scan: add GitHub's MCP server to the catalog | good first issue, help wanted, area: scan |
| #16 | policies: pin TRUNCATE variants in postgres-safe's cases (KI-006) | good first issue, area: policies |
| #17 | report: a US Letter page size for the full scan report | good first issue, area: report |
| #18 | demo: show each scene's time in agent-script --step | good first issue, area: demo |
| #19 | play: print the policy evaluate command behind each live decision | good first issue, area: cli |
| #20 | doctor: check observe mode | help wanted, area: doctor |

Dropped from the old drafts, because the code already does them: `verify --summary-json` (the
global `--json` prints the verification report) and a release-notes link in `version --check` (it
prints the release's page). The roadmap is #21.

New ones follow the same shape: the context, where to look (files and functions), what "done"
means, and the test command. Data-file changes (a scan catalog entry, a policy case) make the best
first issues.
