# The intent-vs-effect corpus

Six small, reproducible cases in which **what an AI agent reports**, **what the database commits**
and **what a checking tool should say** come apart. Each one is plain SQL on PostgreSQL, with the
expected outcome written down. Run them against Deedglass, against your own tool, or against
nothing at all to see the gap for yourself.

The corpus is published under **CC-BY-4.0** ([`LICENSE`](LICENSE)): copy it, change it, put it in
your own test suite or paper, and say where it came from. The runner that executes it
(`internal/corpus`, `scripts/corpus.sh`) is part of the Deedglass code, under Apache-2.0.

## The scenarios

| Scenario | The agent reports | Postgres commits | Deedglass |
|---|---|---|---|
| [`scenarios/01-trigger`](scenarios/01-trigger/) | 1 row updated | 301 rows updated: a trigger ships the rest of the batch | MISMATCH at L2 |
| [`scenarios/02-cascade`](scenarios/02-cascade/) | 1 customer deleted | 1 customer and 12 orders deleted: `ON DELETE CASCADE` | MISMATCH at L2 |
| [`scenarios/03-orm-reset`](scenarios/03-orm-reset/) | development database reset, 3 rows seeded | 500 rows gone; the change log shows only the 3 inserts | denied by policy, never runs |
| [`scenarios/04-another-writer`](scenarios/04-another-writer/) | 1 row updated | exactly that, then another writer changes 5 rows | VERIFIED at L2 (the other writer isn't the agent) |
| [`scenarios/05-partial-failure`](scenarios/05-partial-failure/) | the transfer failed, nothing moved | the debit committed before the failure | statement 1 VERIFIED; statement 2 failed |
| [`scenarios/06-timeout-unknown`](scenarios/06-timeout-unknown/) | credited once after a timeout | credited twice: the timed-out statement committed | both attempts VERIFIED, one with an unknown outcome |

Two of the six are cases where the right answer is *not* a mismatch (04 and 06): a tool that
flags everything is as wrong as one that flags nothing.

## Run it

```sh
make corpus                         # Docker; under a minute
RUN=trigger make corpus             # one scenario
KEEP=1 make corpus                  # keep the Postgres container and the databases, and print the DSN
```

`make corpus` starts PostgreSQL 18.6 in Docker on a free localhost port (with `wal_level=logical`),
runs each scenario in a fresh database, and removes the container at the end. For each scenario it
prints what the agent reports, what its client saw, what committed and the expected Deedglass
outcome, and fails if any database fact differs from `expected.json`. What each scenario saw is also
written to `.tmp/corpus/results.json`.

To run against a Postgres you already have (a throwaway one: each scenario creates and drops a
database and a logical slot, and 03 drops a schema):

```sh
go run ./internal/corpus/cmd/corpus -dsn 'postgres://postgres@127.0.0.1:5433/postgres' -dir corpus
```

## What is in a scenario

| File | What it is |
|---|---|
| `setup.sql` | the tables and rows before the agent acts (`REPLICA IDENTITY FULL` on every table, so a change log carries whole rows) |
| `agent.sql` | the statements the agent runs, one at a time, each in its own transaction (as `psql` runs a file) |
| `other-writer.sql` | where there is one: what another session runs |
| `scenario.json` | the steps in order: which session runs which file, whether it stops at the first error, its client timeout |
| `expected.json` | `agent_reports` (what the agent says it changed), `agent_saw` (what its client was told), `database_commits` (each committed transaction's row changes by session, row counts before and after, probe values) and `deedglass` (the expected Deedglass outcome per action, what it assumes, and the test that pins it) |
| `README.md` | the story in a few lines |

The runner reads what committed from Postgres's own change log (`test_decoding`, one entry per
committed transaction) and from row counts, never from what the agent or its client said.

## Run your tool against it

Any tool that claims to check what an agent did can be pointed at the corpus. Set `CORPUS_TOOL` to a
command; the runner calls it twice per scenario:

```sh
CORPUS_TOOL=./my-checker make corpus
```

- `my-checker before`: after `setup.sql`, before the agent acts. Start watching here.
- `my-checker after`: after every step. Print your verdict as the last line of output.

Both calls get `CORPUS_SCENARIO` (the id), `CORPUS_DIR` (the scenario directory), `CORPUS_DSN`
(the scenario's database) and `CORPUS_REPORT` (a JSON file with the agent's report:
`{"summary": …, "changes": {"public.orders": {"update": 1}}}`). The runner prints your verdict
next to the expected Deedglass outcome and records it in `results.json`. The database facts are
checked the same way whatever your tool says.

Comparing verdicts across tools: Deedglass says `VERIFIED` (what committed is what the statement
said), `MISMATCH` (it isn't), or `UNVERIFIABLE` (it can't tell). If your tool uses other words,
map them in your write-up and say how.

## What the Deedglass column means, and what it doesn't

The Deedglass outcome in each `expected.json` is stated with its basis: the attack case or test
that pins it (`make attack`, `go test ./internal/gate …`). `make corpus` itself doesn't run
Deedglass: it reproduces the database facts and shows the expected outcome beside them. Each
`deedglass.assumes` says how Deedglass is set up for that outcome (the statement goes through the
Gate to a Postgres connector checked at L2, the tables are watched).

The scenarios are small on purpose. Real incidents add retries, connection pools, ORMs, several
agents and many tables; the six shapes here are the ones those come down to.

## Add a scenario

A pull request with a new directory under `scenarios/` is welcome: the next number, the files
above, an `expected.json` whose database facts `make corpus` reproduces, and a README. Say where the
case comes from (a public incident, a bug report) if you can. `go test ./internal/corpus` checks
that every scenario is complete and that a Deedglass outcome uses the product's own reason codes.
