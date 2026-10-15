# The playground (deedglass.com/play)

The playground shows the 1-row-versus-301 moment in a browser tab: a real Postgres (PGlite), the
Acme demo tables and trigger, a scripted agent, a 6-line policy, Undo and records sealed in the tab.

**Honesty line, shown on the page:** "The playground compares table snapshots in your browser. The
real Witness reads Postgres's own change log."

## In your terminal: `deedglass play`

`npx deedglass play` tells the same story offline, with no Docker and nothing sent anywhere. Every
line says where it comes from:

- **live:** the policy decisions come from `policy.Evaluate` on the Acme demo policy
  (`examples/demo/policies/acme-demo.yaml`), the engine `deedglass serve` runs. The records are
  sealed with a key made for the run and checked by the code behind `deedglass verify`; then one
  byte is changed and the check fails.
- **replay:** Postgres can't run inside the binary, so its lines are replayed from
  `docs/assets/demo-hero.cast.json` (a recorded `make demo-hero`, commit and date printed) and the
  undo from `examples/demo/testdata/launch.golden`. Some diff rows are skipped, and the output says
  how many. `internal/play`'s tests keep both copies equal to their sources and check that every
  replayed line is in the recording.

`--fast` prints everything at once; off a terminal there are no pauses. To see the scene live on
your own machine: `npx deedglass init --yes`, then `npx deedglass init --trigger`.

## How it's built

- `go run ./cmd/playgen` writes `examples/play/`: `acme.sql` (`examples/demo/sql` 01, 02 and 10,
  with only psql meta-commands and the role clause removed; a test checks every other line is
  byte-equal), `fixtures.json` (eight statements with `sqlguard`'s facts, computed in Go),
  `policies.json` (acme-demo and postgres-safe exported to CEL by `internal/policy/celexport`, and
  the minor() currency table) and `parity.golden.json` (40 cases with `policy.Evaluate`'s answer).
- The site copies them (`site/src/play/scripts/sync.mjs`) and bundles the engine with PGlite 0.5.8
  and cel-js 8.0.0 into `site/public/play/` (`npm run build` in `site/src/play`). Everything is
  served from deedglass.com; nothing loads from another domain.
- The browser never parses SQL or compiles YAML to decide anything. The editor reads a subset:
  `rules[].{id, match.tool, match.agent, when, decision, reason}`, put in front of acme-demo's rules.
- `site/tests/unit/parity.test.ts` runs the 40 cases through cel-js: 40 of 40 must agree.
- `/play` alone has `'wasm-unsafe-eval'` in its CSP (`public/_headers`); never `'unsafe-eval'`.

## Limits

- Snapshots, not the change log: the check diffs `public.orders` and `public.refunds` before and
  after each statement. Records are Checked (L1) with method `playground_snapshot`, never Proven.
- No budgets, approvers, clocks or verify levels: those parts of a policy are ignored.
- Undo puts rows back, not tables: after an allowed `DROP`, run the scene again.
- Records are sealed with a key made for the visit; they verify only against that key.
- About 5.6 MB on the wire (gzip; less where the CDN serves Brotli), loaded on the first click.
