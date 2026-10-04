# The simulator: `deedglass dev simulate`

**Status:** S27. Scenario `internal/learn/sim/scenario.go`, generator `generate.go`, runner `run.go`, seams `seams.go`; the CLI is `cmd/deedglass/dev_simulate.go`; the default scenario is `examples/sim/acme.yaml`.
**Audience:** anyone who needs honest history on a dev database: for Insights, the Console, tests, clips and demos.

`deedglass dev simulate` pushes a week of believable, seeded traffic through the real Gate into a fresh, clearly labelled simulated org. Policies, budgets, Stop, idempotency, approvals, the shadow pair and sealing all run for real. Only three things are scripted: the tools, the Witness and the clock. It runs only on a dev database.

```
deedglass dev simulate [--scenario examples/sim/acme.yaml] [--days 7] [--end now|-2d] [--seed 7]
                       [--org org_… | --org-slug acme-sim] [--append] [--pace 0s] [--quiet] [--json]
```

| Flag | Default | What it does |
|---|---|---|
| `--scenario` | the embedded `examples/sim/acme.yaml` | the scenario file; the policy files it names are read relative to it |
| `--days` | 7 | how many days to simulate, 1 to 90 |
| `--end` | `now` | when the window ends: `now`, a number of days back (`-2d`), a duration back (`-12h`), or an RFC 3339 time. Truncated down to the org-local hour |
| `--seed` | 7 | the same seed and scenario give the same plan |
| `--org-slug` | the scenario's `org.slug` | the fresh org's slug; with `--append`, the org to add to |
| `--org` | | with `--append` only: the simulated org's id (`org_…`) |
| `--append` | off | add days to an existing simulated org instead of creating one |
| `--pace` | `0s` | a pause between actions, for live demos (like `200ms`) |
| `--quiet` | off | print only the result line |
| `--json` | off | print a `simulation` object |

## Contents

1. [The scenario format](#the-scenario-format)
2. [The window](#the-window)
3. [The generator](#the-generator)
4. [Incidents](#incidents)
5. [The three seams](#the-three-seams)
6. [What runs for real](#what-runs-for-real)
7. [Determinism](#determinism)
8. [Honesty rules](#honesty-rules)
9. [Appending](#appending)
10. [The output](#the-output)
11. [Performance](#performance)
12. [What the simulator doesn't do](#what-the-simulator-doesnt-do)

## The scenario format

One YAML document. The parser is strict: an unknown key anywhere, a key given twice, a wrong type or a value out of range is an error with the file name and line, and with a suggestion when a known key or name is close (`acme.yaml:14: unknown key "per_dya" in agents[0]; did you mean "per_day"?`). Anchors, aliases and custom tags are refused; a second document in the file is refused.

```yaml
v: 1
org: { slug: acme-sim, name: "Acme (simulated)", timezone: Asia/Kolkata }
policies: { enforce: acme-sim.v1.yaml, shadow: { file: acme-sim.v2.yaml, from_day: 3 } }
approvers: [priya@example.test, sam@example.test]
agents:
  - name: support-bot
    hours: "09-19"
    per_day: 220
    user_refs: { pool: 400, prefix: cust_ }
    tools:
      - { tool: stripe.create_refund, weight: 30, args: { amount: { lognormal: { median: 2400, sigma: 0.8 } }, currency: usd } }
      - { tool: gmail.create_draft,    weight: 50 }
      - { tool: postgres.query,        weight: 20, sql: ["SELECT id, status FROM public.orders WHERE id = {id}"] }
    outcomes: { verified: 0.92, unverifiable: 0.05, failed: 0.03 }
    levels:   { L0: 0.3, L1: 0.5, L2: 0.2 }
incidents:
  - { day: 5, hour: 11, agent: support-bot, kind: first_use, tool: github.merge_pr, count: 2 }
```

### Top level

| Key | Required | Rules |
|---|---|---|
| `v` | yes | `1`, the only scenario format |
| `org` | yes | below |
| `policies` | yes | below |
| `approvers` | yes | 1 to 32 distinct lowercase addresses under the reserved `.test` domain, like `priya@example.test`. The first becomes the fresh org's owner; the others join as admins |
| `agents` | yes | 1 to 50 agents with distinct names |
| `incidents` | no | at most 200 |

### `org`

| Key | Rules |
|---|---|
| `slug` | 2 to 63 lowercase letters, digits and dashes, starting with a letter |
| `name` | 1 to 100 characters, not blank |
| `timezone` | an IANA zone, like `Asia/Kolkata` or `UTC` (`Local` is refused). Hours, days and budgets are read in it |

All three are required.

### `policies`

| Key | Required | Rules |
|---|---|---|
| `enforce` | yes | a policy file, relative to the scenario; it must compile. Pushed and enforced when the run starts |
| `shadow.file` | with `shadow` | a policy file that must compile |
| `shadow.from_day` | with `shadow` | 1 to 90: the shadow is activated when the first action of that day (or a later one) runs. A day past `--days` means no shadow |

### `agents[]`

| Key | Required | Default | Rules |
|---|---|---|---|
| `name` | yes | | like `org.slug`: 2 to 63 lowercase letters, digits and dashes |
| `hours` | yes | | two org-local hours with two digits each, `"HH-HH"`, start before end, end at most 24 (`"00-08"`, `"09-19"`) |
| `per_day` | yes | | 1 to 100,000 actions a day |
| `user_refs` | no | none | `pool` (1 to 9,000) and `prefix` (1 to 16 lowercase letters, digits and underscores, starting with a letter), both required. Refs look like `cust_4821` |
| `tools` | yes | | 1 to 50 tool specs, below |
| `outcomes` | no | `{ verified: 1 }` | fractions `verified`, `unverifiable`, `failed`, each 0 to 1, adding up to 1 (a missing key is 0) |
| `levels` | no | `{ L0: 1 }` | fractions `L0`, `L1`, `L2`, adding up to 1 |
| `approvals` | no | `approve: 1`, wait median `5m`, p95 `30m` | `approve`: the fraction of approvals that are approved (the rest are denied). `wait`: `median` and `p95`, both required, durations like `"20m"` or `"2h"`, above 0 and at most 7 days, p95 at or above the median |

### `agents[].tools[]`

| Key | Required | Rules |
|---|---|---|
| `tool` | yes | `<connector>.<tool>` in lowercase; the connector is `postgres`, `stripe`, `gmail`, `slack`, `notion` or `github` |
| `weight` | yes | above 0, at most 1,000,000; tools are picked in proportion to their weights |
| `args` | no | a mapping of argument names (lowercase letters, digits and underscores) to a string, an integer (at most 2^53 − 1 in size), a number, a boolean, or `{ lognormal: { median, sigma } }` (median 1 to 1e12, sigma 0 to 3), drawn and rounded to a whole number. Lists are refused |
| `sql` | no | Postgres tools only: 1 to 32 statements of 1 to 4,096 characters; one is picked per call. The placeholders `{id}` (an order id, 1000 to 9999) and `{sku}` (`'SKU-0001'` to `'SKU-0500'`) are filled in; any other placeholder is refused. `args.sql` is refused for Postgres tools: use `sql` |

A tool with neither `args` nor `sql` gets realistic generated arguments: a refund draws its amount from a lognormal around 2400 (sigma 0.8) in `usd`; `gmail.create_draft` a thread and a template; `slack.post_message` a channel (`#support` 70%, `#launches` 25%, `#random` 5%) and a length; `notion.append_block` a page and 1 to 3 blocks; `github.merge_pr` a repo and a PR number; a Postgres tool a statement from a default list (reads for `postgres.query`, an inventory update for `postgres.execute`).

The side effect is derived from the tool: `postgres.query` and `postgres.describe` read; `postgres.execute` writes, or is destructive when the statement starts with `DELETE`, `DROP` or `TRUNCATE`; other tools read when their name starts with `get_`, `list_`, `search_` or `read_`, are destructive with `delete_` or `remove_`, and write otherwise.

### `incidents[]`

| Key | Required | Rules |
|---|---|---|
| `kind` | yes | `mismatch`, `first_use`, `spike`, `off_hours`, `new_user_refs` or `budget_pressure` |
| `agent` | yes | one of the scenario's agents |
| `day` or `days` | one of them | a day 1 to 90, or a non-empty list of distinct days. Every day must fit in `--days` |
| `hour` | every kind except `budget_pressure` | the org-local hour, 0 to 23. `budget_pressure` refuses it: it spreads over the agent's hours |
| `tool` | `mismatch`, `first_use`, `spike` | a tool name, as above |
| `table` | `mismatch` | a schema-qualified table, like `public.inventory` |
| `count` | `mismatch`, `first_use`, `off_hours`, `new_user_refs` | 1 to 10,000 |
| `factor` | `spike` | above 1, at most 100 |
| `budget` | `budget_pressure` | a budget id in the enforce policy |
| `target` | `budget_pressure` | above 0, at most 1 |

A key that doesn't apply to the incident's kind is refused ("incidents[2].table does not apply to a spike incident; remove it."), and so is a missing one. Per kind:

- `mismatch`: the tool must be a Postgres tool, because the Witness compares table changes for Postgres only.
- `first_use`: the tool must not be one of the agent's tools.
- `off_hours`: the hour must be outside the agent's hours.
- `new_user_refs`: the agent needs `user_refs`.
- `budget_pressure`: the budget must be a `max_spend` budget with `window: day` that covers `stripe.create_refund` by that agent.

## The window

`--end` is truncated down to the start of its org-local hour (never later than `--end`, also at a daylight-saving change), and the window is the `--days` whole 24-hour spans before it. **Day d is the d-th 24-hour span**: `[start + (d − 1)·24 h, start + d·24 h)`. An incident's hour is the org-local hour inside that span. In a zone with daylight saving, a span that crosses a change is placed by its starting offset, so local hours can be off by one on those days.

## The generator

`sim.Plan(scenario, seed, window)` is pure: it decides every action before anything runs.

- **Seeded streams.** Randomness is `math/rand/v2`'s PCG, one stream per agent for its baseline traffic and one per incident, each seeded from `--seed` and the stream's index. Adding an incident never reshuffles the baseline.
- **Volume.** Each day, an agent makes `per_day` actions, give or take 10% (a uniform draw between 0.9 and 1.1 times).
- **A diurnal bell.** 97% of an agent's actions fall inside its hours, drawn from a normal curve centred on the middle of its hours with a standard deviation of a quarter of their width, cut at the edges. The other **3% fall outside its hours**, uniformly. An agent with hours `"00-24"` has none outside.
- **Tools** are picked by weight; arguments as above.
- **User refs** follow a Zipf curve (s = 1.2, v = 30) over the pool, so a few customers come back often and most rarely.
- **Outcomes and levels** are drawn per action from the agent's fractions. An `unverifiable` outcome is a timeout (the tool may have run) 30% of the time, and otherwise a tool that answered but couldn't be checked; a `failed` outcome is `upstream_error` at L0. Baseline traffic is never MISMATCH: only a `mismatch` incident makes one.
- **Latency** is drawn around a median per connector (Postgres 40 ms, Stripe 300, Gmail 250, Slack 180, Notion 350, GitHub 500; a timeout takes 30 s). A succeeded `stripe.create_refund` costs its amount, in its currency.
- **Approvals** are scripted for every action, used only if the policy asks: an approver picked from the list, a vote (approved with the agent's `approve` fraction), and a wait drawn from a lognormal through the agent's median and p95, at least 30 s.
- **Order.** Actions are sorted by time, then by stream; ties move forward by a millisecond, so every action has its own millisecond, in order, inside the window.

`sim.PlanDigest` hashes the plan, one canonical JSON line per action; the same scenario, seed and window give the same digest, byte for byte.

## Incidents

Each incident exists to make one check fire, in Insights or in S28's suggestions. The default scenario has six.

| Incident | What it adds | What it exercises |
|---|---|---|
| `mismatch` | `count` updates to `table` by the tool, in that hour, each witnessed MISMATCH at L2 (expected 1 row, observed 301) | the `mismatch_spike` anomaly; S28's tighten suggestion, because the inventory updates run under v1's `allow-db-writes` rule |
| `first_use` | `count` calls of a tool the agent never used, in that hour, each VERIFIED at L0 | the `new_tool` anomaly |
| `spike` | extra calls of the tool in that hour, so the hour holds about `factor` times its usual volume (`(factor − 1) × per_day ×` the hour's expected share) | the `volume_spike` anomaly |
| `off_hours` | `count` calls in an hour outside the agent's hours | the `off_hours` anomaly. In the default scenario it is on **day 6**, not earlier, because `off_hours` needs at least 5 days of history before it reports anything |
| `new_user_refs` | `count` on-behalf-of refs the agent never used. The calls already in that hour take the new refs first, and only the shortfall is added | the `user_ref_burst` anomaly. Taking over the hour's usual calls keeps the burst from also being a `volume_spike` |
| `budget_pressure` | refunds on each listed day, inside the agent's hours, until the budget's counter for the org-local calendar day that holds most of that day's span reaches `target` × its `max_spend`, counted the way the Gate reserves it (`policy.Evaluate`'s reservations against the enforce policy) | the stats' `budgets` row (`days_over_80`) and S28's `budget.near_limit` suggestion |

The anomaly thresholds are in [INSIGHTS.md](INSIGHTS.md#the-six-kinds). After a run, `deedglass insights detect --backfill 7d --org acme-sim` judges every past hour as if it were now, so the anomalies appear at their virtual times.

## The three seams

The runner builds the Gate the way `serve` does and swaps three of its seams. All three exist only in the simulator; `serve` never wires them.

- **Scripted tools** (`sim.Resolver`). The scenario's tools resolve to scripted executors that report the imitated connector type, so `postgres` tools turn on the SQL classifier and the `sql.*` facts are real. A Postgres tool declares the default schema `public`, and a `DELETE` raises its side effect to destructive, like the real connector (S17). Each call returns the planned outcome (output `{"ok":true,"simulated":true}`, the scripted cost, or the scripted error) and advances the virtual clock by the scripted latency. Any other tool name is `tool_unknown`.
- **The simulated witness** (`sim.Witness`, the Gate's `SimulatedWitness`). It builds the scripted reasons for the planned level and verdict and decides them through `witness.Decide`, like every verdict, with the method `"simulated"` at every level.
- **The virtual clock** (`sim.Clock`). Monotonic, set by the runner to each event's time and advanced by tool latencies. It is the Gate's pipeline clock, so `evaluated_at`, `policy_input.time`, every state time and `approvals.decided_at` are virtual ([INSIGHTS.md](INSIGHTS.md#decision-time-and-seal-time)).

## What runs for real

Everything else: the org's creation (`default-deny` and its bootstrap deed), agents, policy versions and their activation deeds, the policy decision and the shadow decision, budgets and their reservations, Stop, idempotency, the five action events, sealing and the deed chain.

**Approvals go through S22's service.** When the policy asks for approval, the runner schedules the approval script in virtual time. At `decision time + wait`, the scripted approver votes through `approve.Service.Decide` (via `console`, with the reason "Approved in the simulation." or "Denied in the simulation."), and an approved action runs through `Pipeline.Resume`. When the wait reaches the approval's `expires_at` first, the runner resumes the action at its expiry, and it expires. Votes and expiries that would fall after the window's end are left pending. Actions run **one at a time, in virtual-time order** (a decision due at the same instant as a new action goes first), so budgets see the same history every run.

## Determinism

The same scenario, seed and window give the same tools, arguments, user refs, virtual times, outcomes, levels, costs, latencies and approval scripts, and so the same decisions, counts and replay digests. Ids, deed bytes and `issued_at` differ between runs; nothing Insights computes depends on them.

## Honesty rules

Simulated history must never pass for real evidence.

- **Dev only.** The command refuses unless `DEEDGLASS_ENV=dev`, before it reads a scenario or opens a database, and again after loading the config. It prints the database host it writes to.
- **A flagged org.** A fresh org per run (`acme-sim`, then `acme-sim-2`, …), with `orgs.settings.simulated = true`, set in a transaction that seals an admin deed ("Marked as a simulated org (deedglass dev simulate)."). Its agents carry the label `simulated: "true"`; its approvers use `.test` addresses.
- **`witness.method` is `simulated`** in every verdict the simulator scripts, so the deed of every simulated action that ran says so.
- **Real seal times.** Deeds are sealed when the simulation runs: `issued_at` is the database clock, today, while `evaluated_at` is in the virtual past. The Console's banner for a simulated org (S28) reads: "Simulated history. These actions came from deedglass dev simulate. Their deeds were sealed when the simulation ran, and their witness method is simulated."
- **No email.** The approval notices and resume jobs the Gate queues are deleted at the end: the simulator decided the approvals itself.

## Appending

`--append` adds days to an existing org, only when:

- the org (`--org org_…` or `--org-slug …`) is flagged simulated, so a simulated action never lands in a real org: "acme is not a simulated org. dev simulate only appends to orgs it created.";
- its latest `evaluated_at` is at or before the new window's start, so virtual time never runs backwards inside an org: "acme-sim already has actions up to 2026-10-09 13:02 IST. The new days would overlap." The next step is `--end now` with `--days 1` or fewer, or a fresh org.

An appended run reuses the org's agents by name, adds any missing approver, and pushes a policy version only when its content changed; activating a version that is already active changes nothing.

## The output

```
$ deedglass dev simulate --days 7 --seed 7
◐ Simulating 7 days for a new org (acme-sim) · seed 7 · database 127.0.0.1:55432
  org        acme-sim (org_01jbq2…) · 2026-10-02 19:00 to 2026-10-09 19:00 IST
  agents     support-bot · ops-agent · growth-agent · nightly-etl
  policies   acme-sim v1 enforced · acme-sim v2 in shadow from day 3
✓ 5,612 actions through the Gate · 5,598 deeds sealed · chain intact
  ✓ 4,990 verified      L0 1,210 · L1 1,871 · L2 1,909   (simulated witness)
  ✕   311 denied        budget 258 · refund-limit 41 · block-destructive-sql 12
  ✕     3 mismatch      L2 · postgres.execute 3
  ○   188 unverifiable  …
  ·   100 failed        upstream_error 100
  ◐    14 pending       waiting for approval
  approvals  418 requested · 401 approved · 0 denied · 3 expired · median wait 21 min
  incidents  ops-agent 3 mismatches (day 5) · ops-agent first github.merge_pr (day 5) · growth-agent spike ×9 (day 6) · growth-agent 12 off-hours calls (day 6) · support-bot 25 new users (day 7) · support-bot budget pressure (days 2, 4, 6)
  next       deedglass insights detect --backfill 7d --org acme-sim   ·   deedglass insights stats --window 7d
```

The numbers are illustrative; a run prints its own, and the same seed prints the same counts. `deeds sealed` counts every deed in the org's chain: the sealed actions and the admin deeds (the org, its members, the policy activations, the approval votes). An action still waiting for approval has no deed yet. `--quiet` prints only the `✓` line. `--json` prints a `simulation` object: `org`, `slug`, `appended`, `seed`, `window`, `planned`, `plan_digest`, `actions`, `deeds`, `chain_intact`, `agents`, `policy`, `enforce_version`, `shadow_version`, `counts`, `approvals`, `elapsed_ms`.

| Exit | When |
|---|---|
| 0 | the run finished with the chain intact |
| 1 | outside dev (`dev_only`), `--append` into an org that isn't simulated (`not_simulated`) or would overlap (`simulation_overlap`), an org that doesn't exist (`not_found`), any other failure, or a chain that didn't check out |
| 2 | a usage error (`--days` outside 1 to 90, `--org` without `--append`, an `--end` it can't read) or an invalid scenario (`scenario_invalid`, with the file and line) |

## Performance

About 45 s for the default 7 days (about 5,600 actions) on a laptop. The simulator opens its own pool of 8 connections with async commit: on the throwaway dev database, commits don't wait for the disk. Every action is a full Gate round with real sealing, one at a time.

## What the simulator doesn't do

- **It doesn't run on anything but a dev database.** No flag turns the guard off.
- **It doesn't call real tools.** No connector runs, nothing leaves the process, and no external system changes.
- **It doesn't produce evidence.** Verdicts are scripted and say `simulated`; a simulated MISMATCH means the scenario asked for one.
- **It doesn't write into real orgs, or back in time.** It appends only to simulated orgs, and only after their latest decision.
- **It isn't a load test.** Actions run one at a time in virtual-time order, so the numbers say nothing about the Gate's throughput (`make bench-gate` does).
- **It doesn't model people beyond a vote and a wait.** Approvers approve or deny with a fixed fraction after a drawn wait.
- **It doesn't keep ids stable.** Ids, deed bytes and seal times differ between runs; the plan, the counts and the replay digests don't.
- **It doesn't detect anomalies by itself.** Run `deedglass insights detect --backfill` afterwards.
