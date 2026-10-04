# Insights

**Status:** S27 (replay, shadow disagreements, stats, anomalies, the simulator). S28 adds suggestions and the Console screen.
**Audience:** anyone who wants to know whether their rules are right, and how Deedglass counts.

Insights reads what the Gate recorded and reports it: exact counts, every number with its window. It makes suggestions that people approve. **Nothing changes by itself.** Insights never activates a policy, never edits one, and never blocks an action. Its only input into decisions is the opt-in anomaly facts: an org can turn on `anomaly.active` for its policies, and then what an anomaly means is whatever that org's own rules say.

This page covers decision time, the stats, shadow disagreements and anomalies. Replay has its own page ([REPLAY.md](REPLAY.md)), and so does the simulator that seeds history for demos and tests ([SIMULATOR.md](SIMULATOR.md)).

## Contents

1. [Decision time and seal time](#decision-time-and-seal-time)
2. [Stats](#stats)
3. [Shadow disagreements](#shadow-disagreements)
4. [Anomalies](#anomalies)
5. [Anomaly facts in policies](#anomaly-facts-in-policies)
6. [Routes](#routes)
7. [The CLI](#the-cli)
8. [What Insights doesn't do](#what-insights-doesnt-do)

## Decision time and seal time

Insights buckets everything by **decision time**: `actions.evaluated_at`. It never buckets by seal time.

- **Action times come from the Gate's pipeline clock.** `evaluated_at` equals `policy_input.time` exactly: the instant the policy saw. Every other state time the Gate writes on `actions` (`pending_approval_at`, `executing_at` and the rest), every `action_events.at`, and `approvals.decided_at` come from the same clock, passed as a parameter, never SQL `now()`. In production the clock is the server's UTC time, which is why the server needs NTP.
- **Seal time comes from the database clock.** A deed's `issued_at` is when Deedglass sealed it, read under the chain lock (S04). It is never rewritten.
- **Simulated history shows the difference on purpose.** A simulated action from "day 2" has an `evaluated_at` six days ago and a deed issued today. Both are true: the decision is placed in virtual time, the seal happened when the simulation ran ([SIMULATOR.md](SIMULATOR.md)).

Bookkeeping columns such as `created_at` keep their database defaults; Insights doesn't read them.

## Stats

`GET /v1/insights/stats` returns an `insights_stats` object. Every number is deterministic SQL (`count`, `percentile_cont`) over `actions` joined to its approval, read as three statements that run side by side (counts; latency and approval-wait percentiles; costs), each in a read-only org transaction with a 5 s statement timeout and a custom plan. The totals' counts are the groups' sums; their percentiles come from the same statement as the groups'. With writes arriving during the read, the three statements can see a few milliseconds' difference. Rates are computed from the counts, rounded to 4 decimals, and the counts always come back next to them.

### The query

| Parameter | Values | Default |
|---|---|---|
| `window` | `7d` or `30d`, ending now | `7d` |
| `from`, `to` | RFC 3339 times, both or neither, instead of `window`; `from` before `to`, at most 90 days apart | |
| `by` | `rule`, `tool`, `agent` or `none` | `none` (the CLI defaults to `rule`) |
| `agent` | an agent id (`agt_…`) | every agent |
| `tool` | a tool name, or a glob with `*` like `stripe.*` (at most 200 characters) | every tool |
| `compare` | `previous` | no comparison |

A window over 90 days, or `from` not before `to`, is 400 `replay_window_invalid` with `param: from`. `window` together with `from` or `to` is 400 `validation_failed`. Any other parameter is 400 `validation_failed`.

### Every metric, exactly

An action is in the window when its `evaluated_at` is in `[from, to)` and it has a decision.

| Field | Definition |
|---|---|
| `actions` | actions with `evaluated_at` in the window |
| `decisions` | the recorded `allow`, `deny` and `require_approval` counts |
| `deny_rate` | deny decisions whose `decision_source` isn't `stop`, over actions whose `decision_source` isn't `stop`. Stop is a person's act, not a rule, so it is left out of both sides |
| `approval.requested` | `require_approval` decisions |
| `approval.approved`, `denied`, `expired`, `cancelled` | those of them whose approval ended that way |
| `approval.pending` | those whose approval is still pending, or has no approval row |
| `approval.rate` | approved / (approved + denied); `null` when neither happened. Expired and cancelled approvals are not decisions |
| `approval.median_seconds`, `p95_seconds` | `percentile_cont(0.5)` and `percentile_cont(0.95)` of `approvals.decided_at − actions.pending_approval_at`, over approved and denied approvals, rounded to whole seconds |
| `verdicts` | counts of `VERIFIED`, `MISMATCH` and `UNVERIFIABLE`, and `none` for actions with no verdict (not executed) |
| `levels` | `L0`, `L1`, `L2`: the witnessed level of the actions that have a verdict |
| `executed` | actions with an execution status (the tool ran, or may have) |
| `mismatch_rate`, `unverifiable_rate` | MISMATCH over `executed`, UNVERIFIABLE over `executed` |
| `latency_ms` | `p50` and `p95` (`percentile_cont`) of `latency_ms` over executed actions, rounded to whole milliseconds |
| `cost` | per currency: `total`, `p50` and `p95` of `cost_amount`, in integer minor units, over actions that carry a cost |
| `trend` | with `compare=previous`, below |
| `series` | below |
| `budgets` | below |

**Groups.** `by=rule` groups by `decision_rule_id`, across versions, and lists the version numbers seen in `versions`. A decision no rule made gets a key that starts with `@`: `@default` (the policy's default), `@stop`, `@budget` and `@builtin` (a decision made before any policy ran, like `tool_unknown`). `by=tool` groups by tool name, `by=agent` by agent id with the agent's name. Groups are ordered by `actions`, largest first, then by key. `totals` always has the whole window, read in its own query, so its percentiles are exact rather than a mix of the groups'.

**Trend.** With `compare=previous`, each group and the totals carry `trend`, comparing with the window of the same length that ends where this one starts. Counts get `delta_pct` (the change in percent), rates get `delta_pp` (the change in percentage points), each with `previous`, all rounded to 4 decimals. The trend covers `actions` (`delta_pct`), `deny_rate`, `mismatch_rate`, `unverifiable_rate` and, when both windows have one, `approval_rate` (`delta_pp`). When the previous window has fewer than 20 actions, `trend` holds only `{"actions":{"previous":null}}`: too few to compare.

**Series.** One row per org-local day (the org's time zone, `orgs.timezone`): `date`, `actions`, `denied` (every deny, Stop included), `verified`, `mismatch` and `unverifiable`. Days with no actions are left out.

**Budgets.** For each day budget, read from `usage_counters`' `day` windows (`calls`, `spend:<currency>` and `budget:<id>` counters) that overlap the window:

- the agent's own budgets: `agent.calls_per_day` and `agent.spend_per_day` (system agents left out);
- the enforced version's budgets with `window: day`: a `max_spend` budget is measured in money, a `max_calls` budget in calls; `per: org` gives one row with `agent: null`, a per-agent budget one row per agent that has a counter.

Each row has the `limit`, `days` (the date, the amount `used` and its `utilisation`, used over limit), `max_utilisation` (the highest day) and `days_over_80` (days with utilisation of 0.8 or more). Minute and hour budgets have no daily picture and are left out. The policy budgets are the ones enforced **now**, measured over past days.

**An empty window** carries `empty`: "No actions between 2026-10-02 and 2026-10-09. Send one: deedglass act fake.echo --args '{"x":1}'".

**Caching.** An answer is cached for 60 s per org and query (`by`, `agent`, `tool`, `compare`, and the window), and `generated_at` says when it was computed. A `window=7d` or `window=30d` query is keyed by its length, so a repeat within 60 s gets the same answer (its `from` and `to` are the first request's); an explicit `from` and `to` is keyed by those instants. The response has `Cache-Control: private, no-store`, and the route's timeout is 10 s.

```json
{"object":"insights_stats","window":{"from":"2026-10-02T13:05:00Z","to":"2026-10-09T13:05:00Z","days":7},
 "timezone":"Asia/Kolkata","by":"rule","filters":{"agent":null,"tool":null},
 "totals":{"actions":5612,"decisions":{"allow":4883,"deny":311,"require_approval":418},"…":"…"},
 "groups":[{"key":{"rule_id":"approve-order-updates"},"versions":[1],"actions":418,
            "decisions":{"allow":0,"deny":0,"require_approval":418},
            "approval":{"requested":418,"approved":401,"denied":0,"expired":3,"cancelled":0,"pending":14,
                        "rate":1,"median_seconds":1260,"p95_seconds":6840},
            "deny_rate":0,"verdicts":{"VERIFIED":392,"MISMATCH":0,"UNVERIFIABLE":6,"none":20},
            "levels":{"L0":120,"L1":200,"L2":78},"executed":398,"mismatch_rate":0,"unverifiable_rate":0.0151,
            "latency_ms":{"p50":41,"p95":180},"cost":[]}],
 "budgets":[{"agent":null,"budget":"refunds-per-day","kind":"policy","limit":{"amount":400000,"currency":"usd"},
             "max_utilisation":0.9512,"days_over_80":3,"days":[{"date":"2026-10-03","used":380480,"utilisation":0.9512}]}],
 "series":[{"date":"2026-10-03","actions":801,"denied":44,"verified":712,"mismatch":0,"unverifiable":27}],
 "generated_at":"2026-10-09T13:05:00Z","empty":null}
```

The numbers above are illustrative; `"…"` stands for the same fields as a group.

## Shadow disagreements

`GET /v1/insights/shadow` shows where the shadow policy and the enforced one disagree on live traffic ([POLICIES_GUIDE.md](POLICIES_GUIDE.md) explains shadow mode).

- **Selection.** Rows are the actions recorded with `shadow_version_id` equal to the shadow version, that have both a decision and a shadow decision. They are selected by that id, **never by activation time**, so appended simulated history (which lies in the past) counts too. `shadow_version=pv_…` picks a version; the default is the org's live shadow (404 `not_found` when there is none). `from` and `to` (RFC 3339) narrow it by decision time; both ends are optional.
- **Agreement.** An action agrees when the enforced decision and the shadow decision are the same decision; the rules may differ. `summary.agreement` is agree over evaluated, rounded to 4 decimals.
- **Transitions.** Every `(enforce, shadow)` pair that disagrees, ordered by count, with `by_rule` (the shadow's deciding rule), `by_tool`, `by_agent` (with names) and up to 20 `samples`, the first by decision time. A transition out of `require_approval` carries `human_outcome`: what people did with those actions (approved, denied, expired, cancelled, pending; an action with no approval row counts as pending).
- **`enforce`** lists the enforced versions that decided the report's actions, with their counts.
- **The sentence** comes from `internal/learn/copy.go`, so the CLI, the API and the Console say the same thing: "Shadow acme-sim v2 would have sent 41 actions to approval and denied 5 that v1 allowed (refund-approval-over-30: 41, slack-allowlist: 5)." It names at most three rules. With no disagreement it reads "Shadow acme-sim v2 agreed with v1 on all 3,207 actions.", and before any action "Shadow acme-sim v2 hasn't seen any actions yet."

```json
{"object":"shadow_report",
 "shadow":{"policy":"pol_01jb…","name":"acme-sim","version":2,"version_id":"pv_01jb…","live":true,"since":"2026-10-09T12:58:40Z"},
 "enforce":[{"name":"acme-sim","version":1,"version_id":"pv_01jb…","actions":3207}],
 "window":{"from":null,"to":null},
 "summary":{"evaluated":3207,"agree":3161,"disagree":46,"agreement":0.9857},
 "transitions":[{"enforce":"allow","shadow":"require_approval","count":41,
                 "by_rule":[{"rule_id":"refund-approval-over-30","count":41}],
                 "by_tool":[{"tool":"stripe.create_refund","count":41}],
                 "by_agent":[{"agent":"agt_01jb…","name":"support-bot","count":41}],"samples":["act_01jb…"]}],
 "sentence":"Shadow acme-sim v2 would have sent 41 actions to approval and denied 5 that v1 allowed (refund-approval-over-30: 41, slack-allowlist: 5)."}
```

## Anomalies

An anomaly is a signal for a person to look at, never a verdict. The Console renders anomalies amber or neutral, never red: red stays for deny, MISMATCH and destructive actions. Detection is six plain checks over hourly counts. There is no model and no training.

### What a check sees

For each agent that had at least one action in the window, a check sees:

- **The window:** the trailing 60 minutes (a live pass) or one org-local hour (a backfill). Counts in it: actions, denies (excluding `decision_source = stop`), MISMATCH verdicts, writes (side effect `write` or `destructive`), calls per tool with the highest side effect seen, and new on-behalf-of refs (refs this agent used for the first time in 30 days).
- **The baseline:** the 336 org-local hourly buckets (14 days) before the hour the window starts in, zero-filled. The volume, mismatch and user-ref checks use only the buckets that start at or after the agent's **effective creation**: the earlier of its row's `created_at` and its first decision, so simulated history older than the agent row still counts. μ and σ are the population mean and standard deviation of those buckets, and **σ′ = max(σ, √μ, 1)**, a floor so a flat baseline doesn't turn every bump into a spike.
- **Also:** the tools this agent used in the 30 days before, its 14-day total, the share of its 14-day volume in each org-local hour of the day, and the number of org-local days in the 14 with at least one action.

**Cold agents.** An agent with less than 72 hours of history (since its effective creation) gets only `new_tool`. The anomalies list names it under `baseline_building`: "support-bot has 41 hours of history. Volume and hours checks start after 72 hours."

### The six kinds

The thresholds are `DetectorVersion` "1" in `internal/learn/anomaly.go`; every episode records the version that found it.

| Kind | Fires when (all of) | Severity | Score |
|---|---|---|---|
| `new_tool` | the agent calls a tool it didn't use in the 30 days before, nor in its 14-day baseline; the agent is older than 24 hours; it has at least 20 actions in the 14-day baseline. One finding per new tool | `info` for a read, `warn` for a write (or an unknown side effect), `high` for a destructive call | the calls of that tool in the window |
| `volume_spike` | at least 72 hours of history; n ≥ 20, n ≥ μ + 3σ′ and n ≥ 3μ, where n is the window's actions | `warn`; `high` when the score is 10 or more | (n − μ) / σ′ |
| `off_hours` | the org-local hour the window starts in has under 1% of the agent's 14-day volume; at least 5 actions in the window; at least 200 actions over at least 5 active days in the 14 | `info`; `warn` when any action in the window is a write | the window's actions |
| `deny_spike` | n ≥ 20 and d ≥ 5, and d / n ≥ p + 3·√(p(1 − p) / n), where d is the window's denies and p = (D + 1) / (N + 2) over all 336 baseline buckets (D denies, N actions) | `warn` | (d / n − p) / √(p(1 − p) / n) |
| `mismatch_spike` | m ≥ 2 with a baseline mean μₘ under 0.25 an hour; or m ≥ 3 and m ≥ μₘ + 3σ′, where m is the window's MISMATCH verdicts | `high` | m in the first case; (m − μₘ) / σ′ in the second |
| `user_ref_burst` | r ≥ 10, r ≥ μᵣ + 3σ′ and r ≥ 3μᵣ, where r is the window's new on-behalf-of refs | `warn` | (r − μᵣ) / σ′ |

p starts at 0.5 for an agent with no history: one prior deny and one prior allow. Scores are rounded to 2 decimals. Findings come out in the order of the table, `new_tool` findings sorted by tool.

Each finding has a title from `internal/learn/anomaly_copy.go`, in the org's time zone:

| Kind | Title |
|---|---|
| `new_tool` | `first github.merge_pr (write), 15:00 to 16:00 IST` |
| `volume_spike` | `212 calls, 14:00 to 15:00 IST · 9.4× its usual 22.6` |
| `off_hours` | `12 calls, 02:00 to 03:00 IST · usually none at this hour` |
| `deny_spike` | `9 of 24 calls denied, 10:00 to 11:00 IST · usually 2%` |
| `mismatch_spike` | `3 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0` |
| `user_ref_burst` | `61 new on-behalf-of users, 16:00 to 17:00 IST · usually 2 an hour` |

### Evidence

Evidence holds numbers, hours, tool names and up to 10 action ids. **Never arguments, on-behalf-of refs, SQL or results.** A finding's evidence may only use these keys (`learn.EvidenceKeys`; a test fails on any other):

`window_start`, `window_end`, `hour`, `timezone`, `count`, `baseline_mean`, `baseline_stddev`, `factor`, `deny_rate`, `baseline_rate`, `mismatches`, `new_refs`, `writes`, `tool`, `side_effect`, `history_hours`, `share`, `action_ids`.

`action_ids` are the first 10 matching actions of the window in decision-time order: the denied ones for `deny_spike`, the MISMATCH ones for `mismatch_spike`, the ones with a new ref for `user_ref_burst`, that tool's calls for `new_tool`, and any action for the others. The episode adds `points`: the latest 48 hourly points, each `window_start`, `window_end`, `count` and `score`.

### Episodes

An episode is the unresolved series of findings for one agent, kind and tool. Its `episode_key` is `agent|kind|tool` (the tool is empty except for `new_tool`), and a unique index keeps at most one unresolved episode per key and org.

- **A finding with no unresolved episode** inserts a new `anm_` row (status `open`, `occurrences` 1) and publishes `anomaly.detected`.
- **A finding with an unresolved episode** (open or acknowledged) extends it: `window_end` moves to the new window's end, `score` and `severity` keep the highest seen, the evidence becomes the latest finding's plus the new point, and the title is the latest. When the episode's `active_until` had already passed before this window started, it is a **new occurrence**: `occurrences` goes up by one and `anomaly.detected` is published again. A window already in the episode (a re-run) is skipped.
- Either way, `active_until` = the window's end + `anomaly_active_minutes` (60 by default).

`anomaly.detected` goes out on `deedglass_events` in the episode's transaction, so the SSE stream (`GET /v1/events`) and the webhook outbox carry it. Its payload: `object: "anomaly"`, the episode `id`, `agent`, `kind`, `tool`, `severity` and `status: "open"`.

### Active and status: two separate ideas

- **Active** (`active_until` after now) is what `anomaly.active` sees. It ends on its own, 60 minutes after the last finding by default.
- **Status** (`open` → `acknowledged` → `resolved`) is the review inbox, and only a person moves it:
  - **Acknowledge** (admin): records who and when, and changes nothing else. Acknowledging again changes nothing. A resolved episode is 409 `conflict`.
  - **Resolve** (admin, a `reason` of 3 to 280 characters, required): closes the episode, ends any activity now (`active_until` becomes the earlier of itself and now), and **seals an admin deed `anomaly.resolved`** with the anomaly, kind, severity, occurrences, agent and tool. With anomaly facts on, resolving changes policy outcomes, which is why it is sealed. A resolved episode is 409 `conflict`; a new finding later opens a new episode.
  - **The 14-day close:** an episode with no new finding for 14 days (its `window_end` more than 14 days ago) closes itself, `resolved_by: "system"`, with the reason "No new finding for 14 days." and **no deed**, because nothing was decided.

So after a backfill of a past week, the inbox lists every episode as `open` while none of them is active.

### Detection

- **The loop.** Every `deedglass serve` instance runs a detection pass at start and then every 5 minutes, over each org with actions decided in the last 2 hours **or with anomaly facts on** (so an opted-in org is never stale just because it was quiet). The org list comes from `claim_learn_orgs`, which returns org ids only; each org's work runs in its own transaction under row-level security, holding the org's advisory lock (`pg_try_advisory_xact_lock(hashtextextended('deedglass/learn/detect/' || org, 0))`), with a 30 s statement timeout. An instance that finds the lock taken skips that org for this pass.
- **A pass** reads the trailing 60 minutes and the org's hourly buckets (cached per org for the hour), runs the six checks for every agent with actions in the window (the org's `console` system agent is left out), upserts episodes, closes 14-day-old episodes, prunes `detect` runs older than 7 days, and records a `learn_runs` row (`kind: detect`, `period_key` the 5-minute slot, like `2026-10-09T14:05Z`). Action ids for the evidence are read only when something fired.
- **Health.** `GET /v1/health` carries `checks.insights`: `ok`, or `degraded` when this instance's last pass failed or is more than 11 minutes old (two periods and a minute). It is informative and doesn't change the overall status: policies that read `anomaly.*` already fail closed.
- **Backfill (dev).** `deedglass insights detect --backfill 7d` walks a past range (1 hour to 30 days) one org-local hour at a time, each hour judged as if it were now, so simulated history gets its anomalies at the right virtual times. It refuses to run unless `DEEDGLASS_ENV=dev`. It is idempotent: a window already in an episode is skipped. Its `learn_runs` row has a `period_key` starting `bf:`, and never counts as a live pass for the staleness check below.

## Anomaly facts in policies

Policies can read `anomaly.active`, `anomaly.kinds` and `anomaly.severity` ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md#the-anomaly-variable)). They are **off by default**; an org turns them on.

```yaml
- id: approve-while-anomalous
  match: { side_effect: [write, destructive] }
  when: anomaly.active && anomaly.severity in ["warn", "high"]
  decision: require_approval
  reason: "This agent is behaving unusually right now. A person approves writes until the anomaly is resolved."
```

This is recipe 19 in [POLICY_COOKBOOK.md](POLICY_COOKBOOK.md), with its cases.

- **Off** (the default): every policy sees `{active: false}`. The Gate reads only the org's settings, cached for 60 s and dropped on a settings change (a read that fails fails closed, below).
- **On:** the Gate keeps a per-org map of active episodes in memory, loaded with one indexed query (`anomalies_active_idx`) and dropped on `NOTIFY deedglass_learn` (payload: the org id), which fires on every episode insert, extension, close and resolve and on every settings change, with a 10 s TTL as a safety net. A lookup that hits the cache does no database work. The facts are folded per agent at the decision time: `active` when any episode's `active_until` is later, `kinds` sorted and each once, `severity` the highest.
- **Fail closed.** If the last successful live detection pass for the org is older than `anomaly_stale_after_minutes` (15 by default), or detection never ran, every agent of the org gets `{active: true, kinds: ["detector_stale"], severity: "warn"}`. A failed lookup fails closed the same way, unless the org's last successful read showed the facts off: the read that failed is the one that would say whether the org opted in, so an org the Gate has never read successfully fails closed. An org that opted in has said "treat unusual as risky", and unknown is not usual.
- **The trace.** The decision records a trace line in `decision_detail`, shown as the action's `decision.anomaly`: `anomaly facts: volume_spike`, `anomaly facts: detector_stale (last run 22 min ago)`, `anomaly facts: detector_stale (detection hasn't run yet)` or `anomaly facts: detector_stale (the anomaly facts couldn't be read)`. It is absent when the facts were inactive.
- **Recorded for replay.** The facts are part of the stored `policy_input`, so a replay reproduces them exactly ([REPLAY.md](REPLAY.md)).
- **Undo actions** of the org's `console` system agent never get anomaly facts.

### Settings

The settings live in `orgs.settings -> 'insights'`:

| Field | Default | Range | What it does |
|---|---|---|---|
| `anomaly_facts` | `false` | | lets this org's policies read `anomaly.*` |
| `anomaly_active_minutes` | 60 | 5 to 1,440 | how long an episode stays active after its last finding |
| `anomaly_stale_after_minutes` | 15 | 10 to 240 | how old the last live detection pass may be before the facts fail closed |

`PATCH /v1/insights/settings` (admin) takes any of the three fields and a required `reason` (3 to 280 characters). A change **seals an admin deed `insights.settings_changed`** with the before and after values, because it changes what policies see, and sends `NOTIFY deedglass_learn`. A PATCH that changes nothing seals nothing and returns no `deed`.

```http
PATCH /v1/insights/settings
{"anomaly_facts": true, "reason": "Require approval while an agent spikes"}

200
{"object":"insights_settings","anomaly_facts":true,"anomaly_active_minutes":60,"anomaly_stale_after_minutes":15,
 "active_policy_reads_anomaly":true,"notice":null,"deed":"deed_01jbr…"}
```

`active_policy_reads_anomaly` is true when the enforced or the shadow version reads `anomaly` in a rule's `when` or a budget's `measure` (`policy.ReadsAnomaly` walks the checked expressions, so a mention in a string doesn't count). When a policy reads it while the facts are off, `notice` says so: "acme-sim v1 reads anomaly.active, but anomaly facts are off for this org, so it always sees false. Turn them on in Console › Insights › Settings." The anomalies list carries the same `notice`.

## Routes

Every route takes a Console session or an org admin key (scope `admin`, acting as admin). **Viewers read, admins write, and agent keys get 403 `insufficient_scope` on every Insights route**, reads included ("Agent keys can't read Insights. Use the Console or an org admin key."). Lists use the standard envelope with `limit` (1 to 100, default 25) and `cursor`.

| Route | Role | Notes |
|---|---|---|
| `POST /v1/policies/{id}/versions/{v}/replay` | admin | 202; optional `Idempotency-Key`; [REPLAY.md](REPLAY.md) |
| `GET /v1/replays`, `GET /v1/replays/{id}` | viewer | `?policy=pol_…&status=…` |
| `POST /v1/replays/{id}/cancel` | admin | 409 `conflict` once finished |
| `GET /v1/insights/stats` | viewer | the `insights_stats` object; 10 s timeout |
| `GET /v1/insights/shadow` | viewer | the `shadow_report`; 10 s timeout |
| `GET /v1/insights/anomalies` | viewer | `?status=open\|acknowledged\|resolved\|all&agent=&kind=&since=`; default open and acknowledged, the latest finding first |
| `GET /v1/insights/anomalies/{id}` | viewer | one `anomaly` |
| `POST /v1/insights/anomalies/{id}/acknowledge` | admin | no body |
| `POST /v1/insights/anomalies/{id}/resolve` | admin | `{"reason": "…"}`, 3 to 280 characters; seals `anomaly.resolved` |
| `GET /v1/insights/settings` | viewer | the `insights_settings` object |
| `PATCH /v1/insights/settings` | admin | seals `insights.settings_changed` on a change |

The anomalies list adds `active_now` (episodes active now), `anomaly_facts`, `notice` and `baseline_building` (agents with under 72 hours of history: `agent`, `name`, `hours`, `sentence`) to the list envelope. The `anomaly` object: `id`, `agent` (`id`, `name`), `kind`, `tool`, `severity`, `score`, `title`, `status`, `active`, `active_until`, `window` (`start`, `end`), `occurrences`, `evidence`, `detector_version`, `acknowledged` (`by`, `at`, or null), `resolved` (`by`, `at`, `reason`, `deed`, or null), `created_at`, `updated_at`. Errors and their codes are in [api/errors.md](api/errors.md); the schemas are in `api/openapi.yaml`.

## The CLI

The stats, shadow, anomalies and settings commands call the server with an org admin key (`--api-key` or `DEEDGLASS_API_KEY`). `insights detect` is an operator command that reads the database directly. Every command takes `--json`. Details and exit codes are in [CLI.md](CLI.md).

```
$ deedglass insights stats --window 7d --by rule
$ deedglass insights shadow
$ deedglass insights anomalies
$ deedglass insights detect --backfill 7d --org acme-sim
$ deedglass insights settings --anomaly-facts on --reason "Require approval while an agent spikes"
```

```
$ deedglass insights anomalies
5 anomalies · none active now · last 14 days
  high  mismatch_spike  ops-agent     3 MISMATCH verdicts, 11:00 to 12:00 IST · usually 0                  52h ago  anm_01jbx3k2…
  warn  new_tool        ops-agent     first github.merge_pr (write), 15:00 to 16:00 IST                    48h ago  anm_01jbx4d4…
  warn  volume_spike    growth-agent  212 calls, 14:00 to 15:00 IST · 9.4× its usual 22.6                  26h ago  anm_01jbx5p7…
  info  off_hours       growth-agent  12 calls, 02:00 to 03:00 IST · usually none at this hour             38h ago  anm_01jbx5h9…
  warn  user_ref_burst  support-bot   25 new on-behalf-of users, 16:00 to 17:00 IST · usually 0.1 an hour  1h ago   anm_01jbx6w3…
  Policies don't read anomalies in this org. Turn it on: deedglass insights settings --anomaly-facts on --reason "…"
```

The numbers are illustrative. Severities print amber (`info` plain), never red.

## What Insights doesn't do

- **No machine learning.** The checks are counts, means, standard deviations and fixed thresholds you can recompute with SQL. Nothing is trained, and the same history gives the same findings.
- **No automatic blocking, and no automatic policy change, ever.** Insights reads and reports. An anomaly blocks or holds nothing unless your org turned anomaly facts on and your own policy says what to do with them. Suggestions (S28) are applied only when a person accepts them.
- **No learning across orgs.** Every baseline is one agent's own history in one org. No org's data informs another's.
- **Baselines need 72 hours.** Until then an agent gets only `new_tool`; volume, hours, deny, mismatch and user-ref checks start after 72 hours of history.
- **A slow ramp can move a baseline.** An agent whose volume grows a little every day raises its own mean and never looks like a spike. Budgets and rate limits are the hard controls; anomalies are signals.
- **No time-of-day seasonality in the volume check.** `volume_spike` compares an hour with all of the agent's hours, not with the same hour on other days. A batch packed into one or two hours can read as a spike: spread it over more hours, or expect findings.
- **Detection runs on each instance's 5-minute loop** (and when an operator runs `deedglass insights detect`). A finding can arrive up to 5 minutes after the hour it describes, and there is no detection between passes.
