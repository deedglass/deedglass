# The FHIR write-integrity benchmark

How often does an AI agent's FHIR write "succeed" (a 200 or a 201) while the server stores
something other than what the agent reports? And which checks see it? This benchmark answers both
on synthetic data, against a real FHIR server, in a way anyone can run again. It is the healthcare
version of the [corpus benchmark](../BENCHMARK.md) and scores the same three things.

It checks **what was stored against what the agent reported**. It never judges whether clinical
content is right: every resource is synthetic, and the scenarios are about writes, not medicine.

Deedglass wrote the scenarios and the runner, and Deedglass is one of the subjects. That is a
conflict of interest, so the method is written down here before the results, the scenarios
Deedglass misses are in the benchmark on purpose, and anyone can run it, change it, and send a
result or a scenario.

```sh
make fhir-bench                                      # none, http-status and deedglass, every scenario (Docker, a few minutes)
SUBJECTS=none,http-status make fhir-bench            # the baselines only
SUBJECTS='none,cmd:mine=./my-checker' make fhir-bench   # your checker beside the baseline
RUN=conditional make fhir-bench                      # the scenarios whose id contains "conditional"
OUT=corpus/fhir/results/latest.json make fhir-bench  # where the results go (and the .md table beside it)
```

`make fhir-bench` starts Medplum 5.2.6 on PostgreSQL 18.6 (its database in memory) with Redis in
Docker on free localhost ports, runs every scenario once per subject, and removes the containers at
the end. The latest committed run is [`results/latest.md`](results/latest.md) (the table) and
[`results/latest.json`](results/latest.json) (everything the runner saw). The live run needs Docker
and isn't part of CI; CI replays the committed run instead (below).

## The scenarios

Each scenario is one directory under [`scenarios/`](scenarios/): `scenario.json` (the story, the
server behaviour and its sources, the setup, the calls, what the agent reports) and `expected.json`
(what the server stored with no gateway in front, recorded by the runner from the server's database
and reviewed by hand). [`README.md`](README.md) lists them.

**Every scenario is documented behaviour, never a bug.** Each one is standard FHIR R4 semantics, an
RFC the server follows, or what the vendor's own documentation describes, and `scenario.json` cites
the source. Behaviour that looks like a server bug, or that contradicts the vendor's documentation,
is left out of the benchmark and goes to the vendor privately first.

| Scenario | The server's behaviour | Sources |
|---|---|---|
| `01-conditional-create-match` | A conditional create with one match stores nothing and answers 200 | [FHIR R4 conditional create](https://hl7.org/fhir/R4/http.html#ccreate) |
| `02-conditional-update-creates` | A conditional update with no match creates the resource (201) | [FHIR R4 conditional update](https://hl7.org/fhir/R4/http.html#cond-update), [Medplum: updating data](https://www.medplum.com/docs/fhir-datastore/updating-data) |
| `03-transaction-as-batch` | Without the `transaction-bundles` feature, Medplum processes a transaction as a batch, with no error | [Medplum: batch requests](https://www.medplum.com/docs/fhir-datastore/fhir-batch-requests), [FHIR R4 transaction](https://hl7.org/fhir/R4/http.html#transaction) |
| `04-transaction-atomic` | With the feature on, the same transaction is refused whole (400) | as 03 |
| `05-readonly-field-dropped` | An AccessPolicy read-only element is dropped from a create answered 201 | [Medplum: read-only elements](https://www.medplum.com/docs/access/access-policies#read-only-elements) |
| `06-client-id-replaced` | A create ignores the id the client sent | [FHIR R4 create](https://hl7.org/fhir/R4/http.html#create) |
| `07-author-replaced` | Medplum records the writing client as `meta.author`, not the author the client sent | [Medplum: on behalf of](https://www.medplum.com/docs/auth/on-behalf-of) |
| `08-server-meta-added` | The server adds its own `meta`; nothing else changes | [FHIR R4 create](https://hl7.org/fhir/R4/http.html#create) |
| `09-lost-update` | An update without `If-Match` overwrites a version the client never read | [FHIR R4 concurrency](https://hl7.org/fhir/R4/http.html#concurrency), [Medplum: updating data](https://www.medplum.com/docs/fhir-datastore/updating-data) |
| `10-stale-if-match` | With `If-Match` on a stale version, the update is refused (412) | as 09 |
| `11-patch-add-replaces-array` | JSON Patch `add` on an existing array replaces it | [RFC 6902 §4.1](https://www.rfc-editor.org/rfc/rfc6902#section-4.1), [Medplum: updating data](https://www.medplum.com/docs/fhir-datastore/updating-data) |
| `12-patch-missing-path` | JSON Patch `replace` on a missing element is refused (400) | [RFC 6902 §4.3](https://www.rfc-editor.org/rfc/rfc6902#section-4.3) |
| `13-dangling-reference` | Without `checkReferencesOnWrite`, a reference to a missing Patient is stored | [Medplum: Project](https://www.medplum.com/docs/api/fhir/medplum/project), [Medplum discussion 1221](https://github.com/medplum/medplum/discussions/1221) |
| `14-reference-checked` | With `checkReferencesOnWrite` on, the same create is refused (400) | [Medplum: Project](https://www.medplum.com/docs/api/fhir/medplum/project) |
| `15-delete-history-kept` | A delete keeps every earlier version readable; `$expunge` removes them | [FHIR R4 delete](https://hl7.org/fhir/R4/http.html#delete), [Medplum: deleting data](https://www.medplum.com/docs/fhir-datastore/deleting-data) |
| `16-retry-duplicate` | Create isn't idempotent: a retry after a lost answer stores a second resource | [FHIR R4 conditional create](https://hl7.org/fhir/R4/http.html#ccreate) |
| `17-other-writer-status` | Another client's later update is that client's version, not the agent's | [FHIR R4 update](https://hl7.org/fhir/R4/http.html#update) |

Six scenarios are controls, where the agent's report is right: 04, 08, 10, 12, 14 and 17. A tool
that flags them raises a false alarm, so a tool that flags everything scores as badly as one that
flags nothing.

## What a run does

For each subject and each scenario, on one server:

1. Put the project's settings for the scenario (Medplum: `Project.features`,
   `Project.checkReferencesOnWrite`), and make the agent a fresh client (Medplum: a
   ClientApplication, with an AccessPolicy when the scenario names read-only elements).
2. Seed the scenario's resources with another client. Every identifier carries a value unique to
   the run, so runs never see each other's data.
3. Let the subject prepare (a gateway sets up its connectors here).
4. Run the steps in order. An agent call goes to the subject: a gateway sends it itself, as the agent
   would; an observer hands it back and the runner sends it straight to the server with the agent's
   token. Another writer's call always goes straight to the server.
5. Before and after every step, read **the server's own version tables** in its database (Medplum:
   `"<Type>_History"`) and keep every version the step stored, with its full content. After the
   steps, look up whether each resource the agent's versions reference exists, and how many earlier
   versions each resource the agent deleted still holds.
6. Ask the subject for its report.

## What "stored" means

The runner computes what the server stored on the agent's behalf from those versions alone, never
from the server's answers or a subject. Five parts, compared as a whole:

| Part | What it counts |
|---|---|
| changes | the versions the agent's calls stored, per resource type and kind (create, update, delete) |
| differs | the top-level elements of a version the agent stored that aren't what the agent believes it stored: missing, or another value. By default the agent believes the server stored what it sent; a scenario can say otherwise for a patch. Elements only the server has are fine; `id` counts only for a create that named one; `meta` only for `author`, `tag`, `security` and `profile` the agent set |
| overwrote | versions another writer stored that the agent's update replaced without the agent having read them |
| dangling | references in what the agent stored to resources that don't exist |
| history kept | resources the agent deleted whose earlier versions can still be read |

## The three scores

These are the corpus benchmark's rules ([`corpus/BENCHMARK.md`](../BENCHMARK.md#the-three-scores)),
with "commit" read as "the server stored".

**Does the scenario diverge?** When what the agent reports (`agent_reports.effect`) differs from
what the server stores with no gateway in front (`expected.json`).

**(a) Caught.** The subject's strongest flag: `before` (it stopped or held the call before it ran),
`after` (it ran, and the subject's verdict says what was stored doesn't match what was asked),
`unverifiable` (it said it can't tell: not a catch), or `none`. Caught means the scenario diverges
and the flag is `before` or `after`; a false alarm is `before` or `after` on a control.

**(b) Reported the true effect.** The subject's own account of what was stored equals what was
stored in this run, all five parts. A gateway that stops a call changes what is stored, so each run
is compared with its own facts. A subject that doesn't say what was stored scores no; the
baseline's account is the agent's report.

**(c) Left a record a third party can check.** As in the corpus benchmark: for Deedglass, the deeds
exported as a bundle, `deedglass verify` passing offline, and every action of the scenario with a
deed whose decision, execution and verdict match what the API showed; for a command subject, the
check it names exits 0.

There is no single overall number.

## The subjects

- **`none`**, the baseline: the calls go straight to the server, and the only account of what
  happened is the agent's report.
- **`http-status`**: trusts the status code, the check most orchestration code makes. A 2xx answer
  the agent saw means the call did what it asked (a create created one resource, a conditional
  update updated one, a Bundle did every entry); anything else did nothing. It reads nothing back
  and never raises a flag.
- **`deedglass`**: `deedglass serve` built from the commit under test. Each agent call goes through
  the Gate as its own action to a `fhir` connector with default settings (`if_match: when_known`):
  every write is read back at the version the server reported (Checked, L1) and Proven (L2) from
  Medplum's change log, with `meta.author` checked against the agent's client
  ([docs/connectors/fhir.md](../../docs/connectors/fhir.md)). A Bundle or a conditional update goes
  through a REST connector, because the `fhir` connector takes one resource on one type and id per
  call; that records the call at L0 (Claimed). The policy is
  [`bench/cmd/fhir-bench/policy.yaml`](../../bench/cmd/fhir-bench/policy.yaml): the healthcare-fhir
  pack's refusals word for word, then writes allowed at L1 where the pack would hold them for a
  person (a routine approval isn't counted as a catch). Its flag comes from its decisions and
  verdicts. Its account of what was stored: each action it checked (L1 or L2) stored the one version
  its tool names, except one whose check says nothing new was stored (`fhir_conditional_matched`,
  `fhir_no_new_version`), with the elements its Doesn't match reasons name; an action that only
  reached L0 means it doesn't say.
- **`cmd:<name>=<command>`**: any observer run as a command (below).

## Add your checker or gateway

**An observer** (it watches; the calls go straight to the server): a command, called twice per
scenario, `<command> before` ahead of the steps and `<command> after` once the facts were read,
with these set:

| Variable | What it holds |
|---|---|
| `FHIRBENCH_SCENARIO`, `FHIRBENCH_DIR` | the scenario's id and directory |
| `FHIRBENCH_SERVER`, `FHIRBENCH_BASE_URL`, `FHIRBENCH_TOKEN` | the server, its FHIR base and the agent's bearer token |
| `FHIRBENCH_DSN` | the server's database, as its owner (a throwaway) |
| `FHIRBENCH_REPORT` | a file with the agent's report as JSON |
| `FHIRBENCH_RUN` | after the steps: a file with each call and what the client saw (never the facts) |

The last line it prints after the steps is its answer:

```json
{"flag": "after",
 "notes": ["Observation/123 was stored without note"],
 "effect": {"changes": {"Observation": {"create": 1}}, "differs": ["Observation.note"]},
 "record": {"kind": "signed log", "check": "my-checker verify run.log"}}
```

**A gateway** (the agent's calls go through it): implement `fhirbench.Subject` in Go
([`internal/corpus/fhirbench/subjects.go`](../../internal/corpus/fhirbench/subjects.go)); the
Deedglass adapter, [`bench/cmd/fhir-bench/deedglass.go`](../../bench/cmd/fhir-bench/deedglass.go),
is a worked example. Register it in `open` in [`bench/cmd/fhir-bench/main.go`](../../bench/cmd/fhir-bench/main.go).

Send a result the way the corpus benchmark takes one ([Submitting a result](../BENCHMARK.md#submitting-a-result)):
the adapter, the results file, and how your tool was configured. We run it again before it is
listed, and publish results where Deedglass loses the same way as the rest. Scenarios other tools
catch and Deedglass doesn't are especially welcome.

## CI: the golden replay

`make fhir-bench` needs Docker and a few minutes, so it isn't a required CI check.
`TestGoldenReplay` (in `internal/corpus/fhirbench`, part of `make test`) replays the committed run
instead: it recomputes every fact from the versions recorded in `results/latest.json`, the
baselines' reports from the recorded calls and answers, every score from those facts and today's
`expected.json`, the totals, and the markdown table, and fails on any difference. A change to the
scoring rules, a scenario's expected facts or the table that the committed run doesn't support fails
there; the Deedglass subject's report is replayed as recorded (its live behaviour is pinned by the
connector's own tests and `make fhir-proven-it`).

## Limits

- **One server.** This version runs on Medplum 5.2.6 only. HAPI FHIR JPA 8.x is next: its
  scenarios differ (it keeps the version for an update that changes nothing, can create placeholder
  reference targets, and has its own conditional and transaction behaviour), and they will be added
  once they run live here. Managed EHR APIs aren't in it.
- **Small and few.** Seventeen scenarios, one or two resources each, on one server version with one
  set of project settings. Real deployments add retries in client libraries, subscriptions, bots and
  several agents.
- **The agent's report is fixed text** in each `scenario.json`, not a live model's output. The
  benchmark scores tools, not agents.
- **Top-level elements.** "differs" compares top-level elements (and four `meta` elements), so a
  change deep inside an element shows as that element, not its path.
- **Configuration matters.** Deedglass runs with default connector settings and the policy above.
  `if_match: required` (off here) makes the connector refuse an update without `If-Match`; this run
  doesn't measure that setting. The pack's `no-delete-clinical` refusal is what catches 15: Deedglass's check
  for a delete only confirms that a read answers 410 ([docs/connectors/fhir.md](../../docs/connectors/fhir.md#the-check-checked-l1)),
  which is true there. In 07 the read-back catches the replaced author; its reason names `meta`
  because Medplum shows `meta.author` only in its extended mode, so Deedglass's account of the
  element isn't exact.
- **A different project setup changes the outcome.** 03 and 04, and 13 and 14, are the same calls
  with one project setting changed; that is the point of the pairs.
- **One run, no timing.** The facts are deterministic for a given server version and settings; the
  run doesn't measure latency.

## Versions

The results file carries `"version": 1`, the commit, each server's version, image digest and
PostgreSQL version, and each subject's version and configuration. A change to the scoring rules
bumps the version.

The scenarios are published under CC-BY-4.0 ([`../LICENSE`](../LICENSE)); the runner
(`internal/corpus/fhirbench`, `bench/cmd/fhir-bench`, `scripts/fhir_bench.sh`) is part of the
Deedglass code, under Apache-2.0.
