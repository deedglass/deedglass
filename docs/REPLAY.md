# Replay

**Status:** S27. Engine `internal/learn/replay.go`, jobs `internal/learn/replay_jobs.go`, the input scanner `internal/store/learnstore/inputs.go`.
**Audience:** anyone about to enforce a new policy version who wants to know what it would have done.

## The question it answers

"Had this version been enforced over that window, which recorded decisions would have come out differently?"

Replay re-decides every action the Gate recorded in a window, from the input the decision actually saw, under a candidate version. The answer is exact counts and the action ids behind them, never a sample and never a guess. When an input can't be replayed, the action is counted as `unknown` with its reason.

Replay never executes a tool, never reserves or touches usage counters, never seals a deed and never emits an action event. It evaluates each stored input with `policy.Evaluate` in shadow mode, which reserves nothing.

## Contents

1. [What it reads](#what-it-reads)
2. [Baselines](#baselines)
3. [Transitions and rule changes](#transitions-and-rule-changes)
4. [Human outcomes](#human-outcomes)
5. [Unknowns](#unknowns)
6. [sqlguard drift](#sqlguard-drift)
7. [The result digest](#the-result-digest)
8. [Determinism](#determinism)
9. [Limits and jobs](#limits-and-jobs)
10. [The API](#the-api)
11. [The CLI](#the-cli)
12. [What replay doesn't do](#what-replay-doesnt-do)

## What it reads

Every action of the org with a decision and an `evaluated_at` in the window `[from, to)` ([INSIGHTS.md](INSIGHTS.md#decision-time-and-seal-time) explains decision time). For each one: the stored `policy_input`, the recorded decision (`decision`, `decision_code`, `decision_source`, `decision_rule_id`, the enforcing `policy_version_id`), the status of its approval, the tool name, and the `sqlguard_version` recorded with the decision.

The stored input holds everything the policy saw: the agent's status and labels, the org's Stop state, the tool and its arguments, the on-behalf-of facts, the decision time, the usage counters and the anomaly facts ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 6.2). Anomaly facts replay as recorded: if anomaly facts were off for the org at the time, the stored input says `active: false`, and a candidate that reads `anomaly.active` sees false.

## Baselines

A replay compares the candidate with a baseline.

- **`recorded`** (the default) compares with the decision the Gate actually made at the time.
- **`version`** re-evaluates another stored version over the same inputs and compares with that. It isolates your change from everything else that differed at the time: a different enforced version, a policy that changed in between, or a newer sqlguard classifying the same SQL differently. Replaying a version against itself as the baseline gives `changed: 0` and `rule_changed: 0`.

## Transitions and rule changes

Each evaluated action ends up in exactly one of three places:

| Outcome | When |
|---|---|
| unchanged | the same decision, decided by the same rule |
| `rule_changed` | the same decision, decided by a different rule. Listed in `rule_changes`: `decision`, `from_rule`, `to_rule`, `count` and up to 20 `samples` |
| changed | a different decision. Listed in `transitions` by `(from, to)`: `from` is the baseline's decision, `to` the candidate's |

The summary always holds `actions = evaluated + unknown` and `evaluated = unchanged + changed + rule_changed`; a test asserts both.

Each transition carries `by_rule` (the candidate's deciding rule; `""` when the candidate's default decided), `by_tool`, and `samples`: the first 20 action ids by decision time. Transitions are ordered by count, largest first, then by `from` and `to`; breakdowns by count, then by key.

**Builtin `tool_unknown` decisions stay as decided.** An action whose tool no connector served was denied before any policy ran (`decision_source: builtin`, code `tool_unknown`). The tool didn't exist then, so no candidate could have decided otherwise: replay counts it as evaluated and unchanged, under any candidate and any baseline.

## Human outcomes

Every transition out of `require_approval` carries `human_outcome`: what people did with those actions, from the approval joined to each one.

| Field | Counts |
|---|---|
| `approved`, `denied`, `expired`, `cancelled` | approvals that ended that way |
| `pending` | approvals still pending, or actions with no approval row |
| `not_requested` | with a `version` baseline only: the baseline asked for approval, but the recorded decision didn't, so no person was asked. Left out when 0 |

This is what makes a loosening defensible: "require_approval → allow, 411 actions, humans approved 398, expired 5, pending 8, denied 0" (seed 7's loosen suggestion). A suggestion's guard reads exactly these counts ([SUGGESTIONS.md](SUGGESTIONS.md#the-six-generators)).

## Unknowns

An action replay can't evaluate is counted, by reason, and never guessed:

| Reason | Means |
|---|---|
| `input_pruned` | retention removed the stored input (`payload_pruned_at` is set; [RETENTION.md](RETENTION.md)) |
| `input_missing` | the action was recorded before inputs were stored (before S13), or without one |
| `input_version_unsupported` | the stored input's `v` isn't the format this build reads (`1`) |
| `input_unreadable` | the stored input doesn't decode |
| `evaluation_time_limit` | an evaluation hit its 2 s backstop (the candidate's or the baseline's) |

Guessing would mean inventing the facts a decision saw. A decision on a made-up input is not a replay of the past, so these actions stay out of every transition and are reported apart, with their own line in the CLI: `○ 14 unknown   12 inputs pruned by retention · 2 recorded before inputs were stored`.

The per-evaluation backstop is 2 s, not the Gate's 50 ms: replay runs many evaluations in parallel, and a wall-clock limit that tight would make the result depend on the machine's load. The CEL cost limit (S10) stays the real bound.

## sqlguard drift

The derived facts (every `sql.*` field and the time parts) are not stored; they are recomputed at each evaluation (S11). A replay under a newer classifier therefore classifies old SQL the new way. The result shows both sides in `sqlguard`: `recorded` (the sorted set of `sqlguard_version` values recorded with the decisions) and `current` (the version replaying now). A difference is shown, never hidden; the CLI prints `sqlguard   recorded 1 · replayed with 2` when they differ. To separate a classifier change from your policy change, replay with a `version` baseline: both sides then use the current classifier.

## The result digest

`result_digest` is `sha256:` plus the hex SHA-256 of the JCS (RFC 8785) canonical JSON of the result, with `elapsed_ms` and `result_digest` removed. The same inputs, the same candidate and the same sqlguard version always give the same digest.

The result's members, as they appear in the replay object: `candidate`, `baseline`, `window`, `summary`, `transitions`, `rule_changes`, `unknown` and `sqlguard`. The replay object leaves `transitions` and `rule_changes` out when they are empty; the result holds them as `[]`. Every number in a result is an integer and every key is ASCII, so sorted keys with no whitespace is the JCS form:

```sh
curl -s -H "Authorization: Bearer $DEEDGLASS_API_KEY" http://127.0.0.1:8080/v1/replays/rpl_01jbq3… > replay.json
python3 - replay.json <<'EOF'
import hashlib, json, sys
r = json.load(open(sys.argv[1]))
keys = ["candidate", "baseline", "window", "summary", "transitions", "rule_changes", "unknown", "sqlguard"]
res = {k: r.get(k, []) for k in keys}
body = json.dumps(res, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
print("sha256:" + hashlib.sha256(body.encode()).hexdigest())
EOF
jq -r .result_digest replay.json   # the two lines match
```

With jq alone, `jq -jcS '{candidate, baseline, window, summary, transitions: (.transitions // []), rule_changes: (.rule_changes // []), unknown, sqlguard}' replay.json | shasum -a 256` prints the same hex, without the `sha256:` prefix.

## Determinism

- Rows are scanned in `(evaluated_at, id)` order, in batches of 1,000, one short read-only org transaction per batch with a 5 s statement timeout.
- Each batch is evaluated by up to `GOMAXPROCS` workers into index-preserving slots, and the results are **folded in scan order**. So `samples` are always the first 20 by decision time, and the result is identical at any worker count and any batch size (a test runs `GOMAXPROCS` 1 and 8 and batch sizes 1, 7 and 1,000).
- Nothing in a result depends on the clock except `elapsed_ms`, which the digest leaves out.
- Ids, deed bytes and `issued_at` never enter a result.

## Limits and jobs

| Limit | Value | Refusal |
|---|---|---|
| Window | at most 90 days, `from` before `to` | 400 `replay_window_invalid` (`param: from` or `to`) |
| Actions in the window | at most 1,000,000, counted when the replay is created | 400 `replay_too_large`: "This replay covers 1,412,000 actions; the limit is 1,000,000. Narrow the window with from and to." |
| Created per org | 10 manual replays an hour | 429 `rate_limited`, `Retry-After: 300` |
| Running per org | one; the others queue | |
| Running per instance | two | |

- **Jobs.** Creating a replay queues a `replay_jobs` row and wakes the runner; every instance also looks for work every 2 s. `claim_replay_jobs` (a `SECURITY DEFINER` function, the only cross-org read of `replay_jobs`) leases at most one job per org, and never a job of an org that already has a live lease.
- **The lease** is 60 s and is renewed after every batch, with the progress. A runner that dies loses its lease; the next claim takes the job again with `attempts` + 1 and `processed` back at 0, and **restarts from zero**. That is safe because the engine is deterministic: the restarted run gives the same digest.
- **Cancel** (`POST /v1/replays/{id}/cancel`) marks a queued or running replay `cancelled` at once. A running one stops at its next batch, when its renewal finds the status changed. A finished replay is 409 `conflict`.
- **Statuses:** `queued` → `running` → `succeeded`, `failed` or `cancelled`. A failed replay's `error.code` is `candidate_invalid` (the stored version no longer compiles, or its hash changed: the replay fails closed), `replay_too_large`, `replay_window_invalid` or `internal_error` ("The replay stopped on a server error. Start it again; the result will be the same.").

## The API

| Route | Who |
|---|---|
| `POST /v1/policies/{id}/versions/{v}/replay` | admin session or org admin key; `Idempotency-Key` optional |
| `GET /v1/replays` | viewer and up, or an admin key; `?policy=pol_…&status=queued\|running\|succeeded\|failed\|cancelled`, `limit` (1 to 100, default 25), `cursor`; newest first |
| `GET /v1/replays/{id}` | viewer and up, or an admin key |
| `POST /v1/replays/{id}/cancel` | admin session or org admin key |

Agent keys get 403 on all four. `{id}` is a `pol_` id or the policy name; `{v}` is the version number (400 `validation_failed`, `param: v`, otherwise). A version that doesn't exist is 404 `not_found` with `param: version`: "No version 9 of acme-sim. Versions 1 to 3 exist. List them: deedglass policy versions acme-sim".

**Create.** The body is optional. `to` defaults to now and `from` to 7 days before `to`. `baseline` is `{"type":"recorded"}` (the default) or `{"type":"version","version":2}`.

```http
POST /v1/policies/acme-sim/versions/3/replay
Authorization: Bearer dg_live_…
Idempotency-Key: replay-v3-week-41
Content-Type: application/json

{"from":"2026-10-02T00:00:00Z","to":"2026-10-09T00:00:00Z","baseline":{"type":"recorded"}}
```

```json
202
{"object":"replay","id":"rpl_01jbq3…","status":"queued","purpose":"manual",
 "candidate":{"policy":"pol_01jb…","name":"acme-sim","version":3,"version_id":"pv_01jb…","version_hash":"sha256:5c1e…"},
 "baseline":{"type":"recorded"},
 "window":{"from":"2026-10-02T00:00:00Z","to":"2026-10-09T00:00:00Z"},
 "progress":{"processed":0,"total":5612,"percent":0},
 "requested_by":{"type":"api_key","id":"key_01jb…"},"attempts":0,
 "created_at":"2026-10-09T13:02:11Z","started_at":null,"finished_at":null,"error":null}
```

**Read.** While it runs, `progress` moves (`percent` is an integer). Once it succeeded, the result's fields are part of the object:

```json
GET /v1/replays/rpl_01jbq3…
200
{"object":"replay","id":"rpl_01jbq3…","status":"succeeded","purpose":"manual",
 "candidate":{"policy":"pol_01jb…","name":"acme-sim","version":3,"version_id":"pv_01jb…","version_hash":"sha256:5c1e…"},
 "baseline":{"type":"recorded"},
 "window":{"from":"2026-10-02T00:00:00Z","to":"2026-10-09T00:00:00Z"},
 "progress":{"processed":5612,"total":5612,"percent":100},
 "requested_by":{"type":"api_key","id":"key_01jb…"},"attempts":1,
 "created_at":"2026-10-09T13:02:11Z","started_at":"2026-10-09T13:02:11Z","finished_at":"2026-10-09T13:02:12Z","error":null,
 "summary":{"actions":5612,"evaluated":5612,"unchanged":5193,"changed":415,"rule_changed":4,"unknown":0},
 "transitions":[
   {"from":"require_approval","to":"allow","count":411,
    "human_outcome":{"approved":398,"denied":0,"expired":5,"cancelled":0,"pending":8},
    "by_rule":[{"rule_id":"allow-order-updates","count":411}],
    "by_tool":[{"tool":"postgres.execute","count":411}],
    "samples":["act_01jb9x…","act_01jb9y…"]},
   {"from":"allow","to":"deny","count":4,
    "by_rule":[{"rule_id":"refund-limit-strict","count":4}],
    "by_tool":[{"tool":"stripe.create_refund","count":4}],
    "samples":["act_01jbb2…"]}],
 "rule_changes":[{"decision":"deny","from_rule":"refund-limit","to_rule":"refund-limit-strict","count":4,"samples":["act_01jbc7…"]}],
 "unknown":{"input_pruned":0,"input_missing":0,"input_version_unsupported":0,"input_unreadable":0,"evaluation_time_limit":0},
 "sqlguard":{"recorded":["1"],"current":"1"},
 "result_digest":"sha256:9c1e…40ab","elapsed_ms":612}
```

**Cancel.**

```json
POST /v1/replays/rpl_01jbq3…/cancel
200
{"object":"replay","id":"rpl_01jbq3…","status":"cancelled","…":"…","finished_at":"2026-10-09T13:02:12Z"}
```

The numbers and ids are illustrative. The schemas are in `api/openapi.yaml`; the error codes in [api/errors.md](api/errors.md).

## The CLI

```
deedglass policy replay <name|pol_…> --version <n> [--from 7d] [--to now] [--baseline recorded|vN] [--wait] [--quiet] [--json]
```

`--from` is a number of days (`7d`, 1 to 90), a duration (`48h`) back from `--to`, or an RFC 3339 time; `--to` is `now` or an RFC 3339 time. `--baseline v2` re-evaluates version 2 as the baseline. Without `--wait` it prints the queued id and returns; with it, it polls until the replay ends, with a progress bar on a terminal. It uses an org admin key (`--api-key` or `DEEDGLASS_API_KEY`).

```
$ deedglass policy replay acme-sim --version 3 --from 7d --wait
◐ Replaying acme-sim v3 against 5,612 actions (2026-10-02 to 2026-10-09) · baseline: recorded decisions
  ████████████████████████████████ 100% · 0.6 s
✓ 415 decisions would change · 4 by a different rule · 5,193 unchanged · 0 unknown
  require_approval → allow     411   allow-order-updates · humans approved 398, expired 5, denied 0, pending 8
  allow → deny                   4   refund-limit-strict
  result sha256:9c1e…40ab · same inputs and version always give this digest
  next       deedglass policy activate acme-sim --version 3 --mode shadow --reason "…"
```

When some inputs can't be replayed, they get their own line:

```
  ○ 14 unknown   12 inputs pruned by retention · 2 recorded before inputs were stored
```

| Exit | When |
|---|---|
| 0 | the replay succeeded, or was queued (without `--wait`) |
| 1 | the server refused (`replay_too_large`, `replay_window_invalid`, `not_found`, `rate_limited`, …), or the replay failed (`replay_failed`) or was cancelled (`replay_cancelled`) |
| 2 | a usage error: no policy, no `--version`, or a `--from`, `--to` or `--baseline` it can't read |
| 3 | not authenticated |

## What replay doesn't do

- **It doesn't re-run anything.** No tool is called, no usage is reserved, no deed is sealed and no event is emitted. Replay answers what the policy would have decided, not what the tool would have done.
- **It doesn't guess.** A pruned, missing, newer-format or unreadable input is `unknown`, and so is an evaluation that hits its time limit. Nothing is filled in.
- **It doesn't predict the future.** It re-decides the past. Traffic next week can differ from the window you replayed.
- **It doesn't replay what people would do.** `human_outcome` is what people did with the approvals that were asked for; when the candidate asks for an approval the recorded decision didn't, nobody was asked, and replay can't know what they would have said.
- **It doesn't hide classifier changes.** SQL facts are recomputed with the current sqlguard; the result names the recorded and current versions rather than freezing the old classification.
- **It doesn't replay drafts over HTTP yet.** The engine replays an unsaved source: every suggestion's replay is one, stored with `purpose: suggestion` and readable with `GET /v1/replays/{id}` ([SUGGESTIONS.md](SUGGESTIONS.md#the-pass)). The API creates replays of stored versions only.
- **It doesn't change anything.** A replay result changes no policy and no setting. Activating a version is a separate step, by a person, sealed as a deed.
