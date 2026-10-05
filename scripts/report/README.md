# The full scan report

One A4 page, rendered from `deedglass scan --json` and nothing else. It is the `$29` full scan
report's template (DL-039) and the page S48's `deedglass scan --full` renders on the buyer's
machine, with no person in the loop.

## From a scan to a PDF

```sh
deedglass scan --postgres-dsn-env DATABASE_URL --json --no-write > scan.json   # in your agent's project
node scripts/report/render.mjs --scan scan.json --out report.pdf                # one tagged A4 page
node --test scripts/report/render.test.mjs                                      # the renderer's tests
```

Add `--sample` only for the public sample (it labels the page `Sample, from the Deedglass demo`).
`--html <file>` also writes the filled page. Any other flag is refused, `--notes` included: the
report says only what the scan found.

The renderer drives the installed Google Chrome through `playwright-core` from `web/` (ADR-053), so
run `make web-install` once.

## What the page reads

| Part | Scan fields |
|---|---|
| Header | `dir`, `scanned_at`, a sha256 of the scan JSON, `score.database_checked`, `score.grade` |
| Three sentences | `postgres.tables[].can`, `undo_ready`, `summary.write_capable_tools`, the `unchecked_write_path` deductions |
| Grade | `score.grade`, `score.points`, every `score.deductions[]`, then `A means a small blast radius, not a safe agent.` |
| Table | `tools[]` with `side_effect: write` (routed tools share one row), then `postgres.tables[]` (`can`, `est_rows`, `triggers`, `undo_ready`) |
| Three policies | `policies[0..2]`: `why` and the rule's core lines from `yaml` |
| Looked at / didn't | `walk`, `configs`, `summary.mcp_servers`, `postgres`, `offline` |

A field the scan didn't fill shows `not checked`, never a guess. TRUNCATE is always `Can't be
undone`, because undo is built from row images and a TRUNCATE has none.

## Rules the template keeps

- Self-contained: Inter and Geist Mono (latin subsets) and the flat Split Orb are embedded; no
  script, no link, no network request (a test counts them). S48 can ship `report.html` inside the CLI.
- Light theme from `DESIGN.md` tokens; text 9 pt or larger; content inside 190 × 250 mm, so the
  page also prints on US Letter; every chip has an icon and a word, so colour never stands alone.
- Tagged PDF with an outline (`page.pdf({ tagged: true, outline: true })`), at most 300 KB, at most 3 s.

## Sealing (next release)

The footer says `sealing as a deed: next release`. `deedglass deeds seal-file` and
`deedglass verify --file` moved to S38 under S37's time box. Then the footer becomes
`Sealed as deed_… · sha256:… · check it with deedglass verify`.

## What it doesn't do

It reports what the scan could see in one directory and one database role. It doesn't read your
data, run your agent, or say the setup is safe.
