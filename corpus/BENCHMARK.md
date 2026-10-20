# The corpus benchmark

A neutral, reproducible way to compare tools that sit between an AI agent and a database: gateways,
proxies, checkers, audit layers. It plays each scenario of the [intent-vs-effect corpus](README.md)
through a tool (a *subject*), reads what Postgres really committed from its own change log, and
scores the tool against that, never against what the tool or the agent says.

Deedglass wrote the corpus and the runner, and Deedglass is one of the subjects. That is a conflict
of interest, so the method is written down before the results, the scenarios Deedglass misses are
in the corpus on purpose, and anyone can run it, change it, and send a result or a scenario.

```sh
make corpus-bench                                  # deedglass and none, all scenarios (Docker, about a minute)
SUBJECTS=none make corpus-bench                    # the baseline only
SUBJECTS='none,cmd:mine=./my-checker' make corpus-bench   # your checker beside the baseline
RUN=trigger make corpus-bench                      # one scenario
OUT=corpus/results/latest.json make corpus-bench   # where the results go (and the .md table beside it)
```

`make corpus-bench` starts PostgreSQL 18.6 in Docker on a free localhost port, runs every scenario on
a fresh database per subject, and removes the container at the end. The latest committed run is
[`results/latest.md`](results/latest.md) (the table) and [`results/latest.json`](results/latest.json)
(everything the runner saw).

## What a run does

For each subject and each scenario:

1. Create a fresh database and run `setup.sql` as a superuser.
2. Let the subject prepare (a gateway sets up its connector here).
3. Start a logical replication slot (`test_decoding`) and, when the scenario names NOTIFY channels,
   a listener on them.
4. Run the steps in order. An agent step goes to the subject: a gateway runs each statement itself,
   as the agent would send it; an observer hands it back and the runner runs it on a direct
   connection. Another writer's step always runs on a direct connection.
5. Read what committed: every transaction's row changes from the change log (attributed to the
   session that ran the step), row counts before and after, and NOTIFY messages per channel.
6. Ask the subject for its report, then drop the database.

A gateway's own bookkeeping in the scenario's database (for Deedglass, the `deedglass` schema) is
left out of the facts.

## The three scores

**Does the scenario diverge?** A scenario diverges when what the agent reports
(`expected.json`'s `agent_reports.changes`) differs from what the database commits on the agent's
behalf with no gateway in front (`expected.json`'s facts, which `make corpus` reproduces). The
effect compared is: row changes per table and operation in the agent's transactions; rows that
vanished from a table with no row change in the log (a DROP); and NOTIFY messages per channel.
Eleven of the twelve scenarios diverge. `04-another-writer` doesn't: the agent's report is right,
and a tool that flags it raises a false alarm.

**(a) Caught.** The subject's flag for the scenario, the strongest of its actions:

| Flag | Means | Counts as caught |
|---|---|---|
| `before` | an agent action was stopped or held before it ran (a policy refusal, an approval hold) | yes |
| `after` | an action ran, and the subject's verdict says what committed doesn't match what was asked | yes |
| `unverifiable` | the subject said it can't tell | **no** |
| `none` | none of these | no |

A scenario is *caught* when it diverges and the flag is `before` or `after`. A *false alarm* is a
`before` or `after` flag on a scenario that doesn't diverge. Saying "I can't tell" is honest and a
person should look, but it doesn't say what went wrong, so it isn't a catch.

**(b) Reported the true effect.** The subject's own account of what committed for the agent equals
what committed in this run, by the same effect as above. A gateway that stops an action changes
what commits, so each run is compared with its own facts. A subject that doesn't say what
committed scores no. The baseline's account is the agent's report.

**(c) Left a record a third party can check.** The subject left a record of the scenario that
someone else can check without trusting the subject's server: the check passes and the record
covers every action. For Deedglass: the deeds are exported as a bundle (`deedglass deeds export`),
`deedglass verify` checks it offline, and every action of the scenario has a deed whose decision,
execution and witness verdict are the ones the API showed. For a command subject: the check
command it names exits 0.

The scores are per scenario and per subject. There is no single overall number: a gateway that
stops a bad write and one that reports it afterwards are both useful, and the table shows which is
which.

## The subjects

- **`none`**, the baseline: no gateway or checker. The statements run on a direct connection, and
  the only account of what happened is the agent's report.
- **`deedglass`**: `deedglass serve` built from the commit under test, with a control plane on the
  same throwaway server. Each agent statement goes through the Gate as its own action
  (`postgres.execute`), to a Postgres connector set up with `deedglass connector postgres init` and
  witnessed at L2 (Proven) on the scenario's watched tables, with default connector settings. The
  policy is [`bench/cmd/corpus-bench/policy.yaml`](../bench/cmd/corpus-bench/policy.yaml): the
  postgres-safe starter's refusals word for word, then writes allowed with an L2 check where
  postgres-safe would hold them for a person to approve (the benchmark doesn't count a routine
  approval as a catch). An agent that calls an HTTP API goes through a REST connector, which
  records the call at L0 (Claimed). Its flag comes from its decisions and verdicts; its account of
  what committed is the sum of the L2 counts of the actions that ran.
- **`cmd:<name>=<command>`**: any observer run as a command (below).

## Add your gateway or checker

Two ways, depending on whether your tool runs the agent's statements.

**An observer** (it watches, the statements run directly): write a command. The runner calls it
twice per scenario, the way [`make corpus`'s `CORPUS_TOOL`](README.md#run-your-tool-against-it)
does: `<command> before` after `setup.sql`, and `<command> after` once the steps ran, with
`CORPUS_SCENARIO`, `CORPUS_DIR`, `CORPUS_DSN` (the scenario's database, superuser) and
`CORPUS_REPORT` (the agent's report as JSON) set. The last line it prints after the steps is its
answer, one JSON object:

```json
{"flag": "after",
 "notes": ["301 rows changed in public.orders, the statement named 1"],
 "effect": {"changes": {"public.orders": {"update": 301}}, "notifications": {}},
 "record": {"kind": "signed log", "check": "my-checker verify run.log"}}
```

`flag` is `none`, `before`, `after` or `unverifiable`. `effect` (what your tool says committed for
the agent) and `record` may be `null`. The runner runs `record.check` itself, in the scenario's
directory; exit 0 means the record checks. Then:

```sh
SUBJECTS='none,cmd:mine=./my-checker' OUT=corpus/results/mine.json make corpus-bench
```

**A gateway** (the agent's statements go through it): implement `bench.Subject` in Go
([`internal/corpus/bench/bench.go`](../internal/corpus/bench/bench.go)). `Start` begins a scenario;
the session's `Prepare` gets the scenario's database after `setup.sql`, `Agent` runs each agent
step's statements through your gateway and returns what the agent's client saw, and `Finish`
returns your report (flag, notes, effect, record). The Deedglass adapter,
[`bench/cmd/corpus-bench/deedglass.go`](../bench/cmd/corpus-bench/deedglass.go), is a worked example;
register yours in `open` in [`bench/cmd/corpus-bench/main.go`](../bench/cmd/corpus-bench/main.go).

### Submitting a result

Open a pull request with your adapter (the command or the Go code, or a link to where it lives and
the exact version), the results file from your run (`corpus/results/<tool>.json` and its `.md`), and
how your tool was configured. Use your tool's defaults, or say what you changed and why. We run it
again on our machine before it goes on the leaderboard, and we publish results where Deedglass
loses exactly as we publish the rest. If you think a scoring rule is unfair to your tool, say so in
the pull request: the method changes in the open, with a new version number in the results file.

New scenarios are welcome the same way ([`README.md`](README.md#add-a-scenario)); scenarios that
other tools catch and Deedglass doesn't are especially welcome.

## Limits

- **The scenarios are small and few.** Twelve shapes, one table or two each, one Postgres version
  (18.6). Real incidents add connection pools, ORMs, retries and several agents.
- **The agent's report is fixed text,** written in each `expected.json`, not a live model's output.
  The benchmark scores tools, not agents.
- **The effect counts rows, not values.** A trigger that rewrites the value the agent set, with the
  same row count, isn't in v1; a later version can compare values too.
- **NOTIFY is the only effect outside rows** the runner counts. An email sent by an HTTP call from a
  trigger, a file written by `COPY TO PROGRAM`, or a message on a queue outside Postgres isn't
  measured, and no row-based check would see it.
- **Configuration matters.** Deedglass runs with default connector settings and the policy above.
  Its `witness.strict_triggers` setting, off by default and off here, would turn
  `09-unwatched-table` from VERIFIED into a gap (UNVERIFIABLE): still not a catch by these rules.
  A different policy would stop or hold different actions.
- **`06-timeout-unknown`'s slow function** stands in for a lock wait. Deedglass can't derive the
  footprint of a statement that calls an unknown function, so its verdict there is UNVERIFIABLE for
  that reason, not because of the double credit. Its L2 check judges each statement against what
  that statement said, and each credit statement changed the one row it named, so a statement it
  can read wouldn't flag the double credit either (that variant isn't in this run).
- **Through the Gate, an agent's client timeout doesn't cut the Gate's own connection,** so the
  execution in 06 is recorded as succeeded, not unknown. The unknown-outcome path is pinned by
  `internal/gate`'s `TestL2UnknownExecution`, not by this benchmark.
- **One run, no timing.** The scores are deterministic for a given commit (each scenario is one
  fixed sequence of statements); the run doesn't measure latency.

## Versions

The results file carries `"version": 1`, the commit, the PostgreSQL version and each subject's
version and configuration. A change to the scoring rules bumps the version, and results of
different versions aren't compared in one table.

The corpus is published under CC-BY-4.0 ([`LICENSE`](LICENSE)); the runner (`internal/corpus`,
`bench/cmd/corpus-bench`, `scripts/corpus_bench.sh`) is part of the Deedglass code, under Apache-2.0.
