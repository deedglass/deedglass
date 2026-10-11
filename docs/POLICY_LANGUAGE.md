# The Deedglass policy language (`deedglass: policy/v1`)

**Status:** normative for policy/v1 (S10). Owners: S10 (language, compiler, `lint`, `fmt`), S11 (evaluation), S12 (storage and versions).
**Audience:** anyone who writes, reviews or audits a policy, and anyone writing a compatible compiler.

## 1. In ten seconds

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: block-destructive-sql
    match: { tool: "postgres.*" }
    when: sql.kind in ["drop", "truncate"]
    decision: deny
    reason: DROP and TRUNCATE are never allowed for agents.
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: large-refunds-need-approval
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human. An admin can approve in Console › Approvals.
```

A policy decides, for every action an agent asks for, whether it runs (`allow`), never runs (`deny`) or waits for a person (`require_approval`).
Rules are read top to bottom, and **the first rule that matches decides**; when none matches, the `defaults` decide.
**An error never widens access:** a condition that can't be evaluated never lets an action through.

## 2. Contents

1. In ten seconds
2. Contents
3. The file format
4. Fields
5. Globs, money and durations
6. Evaluation order and the two invariants (6.1 The decision, 6.2 The input, 6.3 The trace, 6.4 Messages, 6.5 `AlwaysDenies`, 6.6 `verify:` and the Witness)
7. The CEL environment
8. Limits
9. Normalization and `version_hash`
10. How the language is versioned
11. Formatting (`deedglass policy fmt`)
12. What policies can't do
13. Coming from OPA/Rego, Cedar or Kubernetes CEL
14. Later
15. FAQ
16. Diagnostics at a glance

The diagnostic codes (`unknown_key`, `cel_type`, …) are listed in section 16 and, with examples and fixes, in [POLICY_ERRORS.md](POLICY_ERRORS.md). The CLI is in [CLI.md](CLI.md#policies-policy-lint-policy-fmt-policy-evaluate-policy-test-policy-push-and-policy-activate).

## 3. The file format

A policy is one YAML document in UTF-8 (a byte-order mark and CRLF line endings are accepted). It is a mapping with these top-level keys:

```yaml
# Comments are kept by fmt and ignored by the hash.
deedglass: policy/v1              # format marker; required
name: postgres-safe               # slug, unique per org
description: Reads run freely. Writes wait for a human. Schema changes never run.

defaults:                         # applies when no rule matches
  decision: deny                  # allow | deny | require_approval
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
  verify: L0                      # minimum witness level for allowed writes (S19 enforces it)
  approval:                       # used by require_approval rules that omit fields
    approvers: [role:admin]
    required: 1
    expires_in: 30m

budgets:                          # checked before rules
  - id: agent-rate
    per: agent                    # agent (default) | org
    window: minute                # minute | hour | day (day = the org's local calendar day)
    max_calls: 60
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"       # money is always a string "<decimal> <currency>"
    measure: args.amount          # CEL giving int minor units; omit it to count the reported cost

rules:                            # first match wins
  - id: block-destructive-sql
    description: DROP and TRUNCATE never run through agents.
    match:                        # every present field must match; a list means "any of"
      tool: "postgres.*"          # glob on the full tool name <connector>.<tool>
      connector_type: postgres
      side_effect: [write, destructive]
      agent: ["support-*"]        # glob on the agent's name, or an exact agt_ id
      labels: { env: prod }       # agent labels; every pair must match exactly
    when: sql.kind in ["drop", "truncate"]   # CEL returning bool; omitted means true
    decision: deny
    reason: DROP and TRUNCATE are never allowed for agents.
    notify: [slack]
  - id: large-refunds-need-approval
    match: { tool: stripe.create_refund }
    when: args.amount > minor("50.00 usd")
    decision: require_approval
    reason: Refunds over $50.00 wait for a human. An admin can approve in Console › Approvals.
    approval: { approvers: [role:admin, "user:priya@example.com"], required: 1, expires_in: 2h }
    verify: L1
```

**Why `deedglass: policy/v1` and not `version: 1`.** Everywhere else in the API, "version" means a stored policy version (`pv_…`, `"version": 3`). The marker names the *format*, the way `openapi: 3.1.0` does, and it lets editors recognise the file. A file with `version: 1` gets `unknown_key`, with the hint to use `deedglass: policy/v1`. `fmt` puts the marker first; the compiler reads it anywhere in the top-level mapping.

**YAML subset.** Policies use plain YAML with these refusals, each checked before anything is expanded:
- no anchors (`&a`), aliases (`*a`) or merge keys (`<<`);
- no tags except the YAML 1.2 core schema (`!!str`, `!!int`, `!!float`, `!!bool`, `!!null`, `!!map`, `!!seq`);
- exactly one document;
- no tab in the indentation;
- no key twice in one mapping.

## 4. Fields

The compiler checks every row. "Slug" means `^[a-z][a-z0-9-]{1,62}$` (ASCII lowercase letters, digits and dashes; 2 to 63 characters; starts with a letter).

### 4.1 Top level

| Field | Type | Rules |
|---|---|---|
| `deedglass` | string | required; exactly `policy/v1` |
| `name` | slug | required; unique per org (S12 enforces uniqueness) |
| `description` | string | optional; at most 1,000 characters |
| `defaults` | mapping | required |
| `budgets` | list | optional; at most 50 |
| `rules` | list | optional; at most 500. A policy with no rules is valid: the default decides everything |

### 4.2 `defaults`

| Field | Type | Rules |
|---|---|---|
| `decision` | enum | required: `allow`, `deny` or `require_approval`. `allow` gives the warning `default_allow` |
| `reason` | string | required unless the decision is `allow`; at most 280 characters (longer is the warning `reason_too_long`) |
| `verify` | enum | `L0` (default), `L1` or `L2` |
| `approval` | mapping | optional; see 4.5. Missing fields default to `approvers: [role:admin]`, `required: 1`, `expires_in: 30m` |
| `notify` | list | optional; `email`, `slack` or `webhook` |

### 4.3 `budgets[]`

| Field | Type | Rules |
|---|---|---|
| `id` | slug | required; unique across budgets **and** rules |
| `description` | string | optional; at most 1,000 characters |
| `match` | mapping | optional; as in 4.4. Omitted means every action |
| `per` | enum | `agent` (default) or `org` |
| `window` | enum | required: `minute`, `hour` or `day` (the org's local calendar day) |
| `max_calls` | int | 1 to 1,000,000,000. Exactly one of `max_calls` and `max_spend` |
| `max_spend` | money | more than zero |
| `measure` | CEL | only with `max_spend`; must type-check to `int` or `dyn`; omitted means the cost the tool reports after it runs |

### 4.4 `rules[]` and `match`

| Field | Type | Rules |
|---|---|---|
| `id` | slug | required; unique across rules and budgets; ASCII only |
| `description` | string | optional; at most 1,000 characters |
| `match` | mapping | optional; omitted means every action |
| `when` | CEL string | optional (omitted means true); at most 4,096 characters; type-checks to `bool` or `dyn`; within the cost limit (section 8) |
| `decision` | enum | required: `allow`, `deny` or `require_approval` |
| `reason` | string | required for `deny` and `require_approval`; at most 280 characters (longer is a warning); no control or bidirectional characters |
| `approval` | mapping | `require_approval` only (elsewhere it is the warning `ignored_field`); merged over `defaults.approval` field by field |
| `notify` | list | `email`, `slack` or `webhook`; for `deny` (an alert) and `require_approval` (the request). On `allow` it is ignored, with a warning |
| `verify` | enum | `L0`, `L1` or `L2`; for `allow` and `require_approval`. Omitted means `defaults.verify`. On `deny` it is ignored, with a warning |

`match` fields. Every field that is present must match; a list means "any of"; a single value is a list of one.

| Field | Matches | Values |
|---|---|---|
| `tool` | the full tool name `<connector>.<tool>` | globs |
| `connector_type` | the connector's type | globs; a value that isn't a known type gets the warning `unknown_connector_type`. Known: `postgres`, `rest`, `mcp`, `mcp_http`, `sdk`, `github`, `slack`, `stripe`, `gmail`, `notion`, `fake` (`sdk` is what S31's SDK edges declare in proxy mode) |
| `side_effect` | what the tool declares | `read`, `write`, `destructive`, `unknown` (no globs) |
| `agent` | the agent's name (glob), or its id (an exact `agt_…`) | globs or ids |
| `labels` | the agent's labels | a mapping; every pair must be equal. Keys as in S08 (`^[a-z0-9][a-z0-9._/-]{0,62}$`), values at most 128 printable characters. YAML values are read as text, so `tier: 1` matches the label value `"1"` |

Each list has at most 32 entries, and each glob at most 200 characters (S13's tool-name limit). A `match` field with no value (`match: { tool: }`) is `wrong_type`: it would otherwise silently match everything. An agent id is `agt_` and 26 lowercase letters and digits (contract §3); an `agent:` entry that starts with `agt_` but isn't an id, and has no `*` or `?`, is `invalid_glob`.

### 4.5 `approval`

| Field | Type | Rules |
|---|---|---|
| `approvers` | list | `role:owner`, `role:admin` or `user:<email>`. Never `role:viewer`: viewers can't approve |
| `required` | int | 1 to 5, and at most the number of distinct approvers when every approver is a `user:` (`approvals_unsatisfiable`). A role counts as any number of people, so a list with a role is never unsatisfiable |
| `expires_in` | duration | from 1m to 7d |

### 4.6 Strings

Every string (names, ids, descriptions, reasons, globs) is valid UTF-8 with no control characters (U+0000 to U+001F except tab in descriptions, U+007F, U+0080 to U+009F) and no bidirectional or invisible formatting characters (U+061C, U+200B to U+200F, U+202A to U+202E, U+2060 to U+2069, U+FEFF). These can make text display differently from what it says ("Trojan Source"), so they are refused with `control_character`.

## 5. Globs, money and durations

**Globs.**
- `*` matches any run of characters, dots included, and `?` matches exactly one character.
- Nothing else is special. Matching is case-sensitive and anchored at both ends.
- The alphabet is `[A-Za-z0-9_.*?:-]`. Upper case is allowed because MCP tool names can use it.
- An `agt_` id in `agent:` matches that id exactly.

| Pattern | Matches | Doesn't match |
|---|---|---|
| `postgres.*` | `postgres.execute`, `postgres.a.b` | `postgresql.execute`, `postgres` |
| `stripe.create_?efund` | `stripe.create_refund` | `stripe.create_refunds` |
| `*` | everything, including the empty string | |

**Money** is a string, `"<decimal> <currency>"`, like `"50.00 usd"`.
- The currency is an active ISO 4217 code: case-insensitive on input, lowercase after `fmt`.
- The decimals may not exceed the currency's minor unit (usd 2, jpy 0, kwd 3). `"50 usd"` is fine; `"50.005 usd"` is `money_precision`.
- The amount is not negative, and in minor units it is at most 2^53 − 1.
- A YAML number (`max_spend: 50.00`) is `money_float`: write money as a string, so no precision is lost.
- Inside CEL, `minor("50.00 usd")` is `5000`, the amount in minor units. Arguments in `args` are compared in minor units too.

**Durations** are a whole number and one lowercase unit, `s`, `m`, `h` or `d`: `30m`, `2h`, `1d`, `90s`. Approval expiry is from 1m to 7d, so `60s` is fine and `0m` and `8d` are `invalid_duration`.

## 6. Evaluation order and the two invariants

Evaluation is one pass with no I/O, no clock and no randomness: `policy.Evaluate(ctx, compiled, input, options)` in `internal/policy/evaluate.go`. It returns a decision and never a Go error. Every failure becomes a deny, and no failure ever becomes an allow.

```mermaid
flowchart TD
  A[Action] --> P{Input readable?}
  P -- no --> D0[deny: policy_denied, rule invalid-input]
  P -- yes --> S{Org stopped, agent stopped or suspended?}
  S -- yes --> D1[deny: org_stopped / agent_stopped / agent_suspended]
  S -- no --> AB{Any agent budget would be passed?}
  AB -- yes --> D2[deny: budget_exceeded, id agent.*]
  AB -- no --> B{Any policy budget would be passed?}
  B -- yes --> D3[deny: budget_exceeded]
  B -- no --> R{First rule whose match matches and whose when is true}
  R -- found --> O[That rule's decision]
  R -- none --> DF[defaults.decision]
```

The algorithm, as implemented:

```
if the policy is nil                     → deny policy_unavailable
a panic anywhere in evaluation           → deny policy_unavailable ("evaluator error")
Prepare(input)                           → on error: deny policy_denied, source builtin, rule "invalid-input"
1  stop     org stopped                  → deny org_stopped
            agent stopped / suspended    → deny agent_stopped / agent_suspended
1b agent budgets (S08, on the agent row), in this order:
            calls_per_minute, calls_per_day: used + 1 > limit → deny budget_exceeded, id agent.calls_per_minute / agent.calls_per_day
            spend_per_day (cost based):  used >= limit        → deny budget_exceeded, id agent.spend_per_day
            otherwise the standard calls reservations (minute, hour, day) carry the agent's limits
2  policy budgets, in file order, each whose match applies:
            amount := 1 (max_calls) | measure (max_spend with measure) | 0 (max_spend, cost based)
            measure error, or a result that isn't a non-negative int
                                         → deny policy_denied, rule "budget:<id>"
            cost based:  used >= limit       → deny budget_exceeded
            otherwise:   used + amount > limit → deny budget_exceeded
            else collect a reservation (or a cost target, for a cost-based budget)
3  rules, in file order:
            match doesn't cover the action → no_match; next rule
            no when                        → the rule decides
            when → false                   → no_match; next rule
            when → true                    → the rule decides
            when over the cost limit       → deny policy_denied, whatever the rule's decision
            other when error, allow rule   → error; next rule (invariant 1)
            other when error, deny or require_approval rule → the rule decides, reason + suffix
            when returns something that isn't a bool → treated as an error, as above
4  default                               → defaults.decision
decide: allow and require_approval keep the reservations (not in shadow mode); deny drops them
the 50 ms deadline, or a CEL internal error, anywhere → deny policy_unavailable
```

1. **Prepare.** The input is decoded and checked once: the version, the decision time, the agent status, the org's time zone and `args` (a JSON object of at most 256 KiB, nested at most 32 deep). The SQL classifier runs here ([SQLGUARD.md](SQLGUARD.md)). An input that can't be read is denied with `policy_denied`, source `builtin`, rule `invalid-input`.
2. **Stop.** Org stopped → `org_stopped`. Agent stopped → `agent_stopped`. Agent suspended → `agent_suspended`. The identity layer already refuses these when the key is checked; this step is defence in depth, and it keeps replay honest.
3. **Budgets.** First the agent's own budgets (S08), then the policy's `budgets` in file order. Every budget applies, the first one that would be passed decides, and so the stricter limit always wins. A `measure` that fails is a deny with the rule id `budget:<id>`: an action is never let through uncounted. [BUDGETS.md](BUDGETS.md) has the counters, windows and accuracy.
4. **Rules**, in file order. The first rule whose `match` matches and whose `when` is true decides.
5. **The default.**

**`policy_unavailable`** is always a deny, with the message `Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies.` It covers a missing policy, a panic, an internal CEL error (a panic inside a function) and the evaluation deadline. The deadline is a backstop of 50 ms per action (`EvalOptions.Deadline`); the CEL cost limit is the real bound. If the deadline passes after a rule has already decided deny, the deny stands; otherwise the decision becomes `policy_unavailable`, because a later deny rule might have matched.

**Invariant 1. An error never widens access.**
- If a `when` fails at run time (a missing key, a wrong type, a result that isn't a bool), an `allow` rule counts as *not matching*.
- A `deny` or `require_approval` rule counts as *matching*, and its reason gets the suffix ` (Deedglass could not evaluate this rule: <short error>.)`.
- **Going over the runtime cost limit (100,000 units) is a deny for any rule**, `allow`, `deny` or `require_approval`, and for a budget `measure`. The decision is `policy_denied` with this reason: `Deedglass stopped evaluating this rule at its cost limit of 100,000, so the action did not run. Send smaller arguments, or ask an admin to simplify the rule in Console › Policies.` Skipping an allow rule would let the next rule decide, and an agent could then choose arguments large enough to skip any rule it likes; a clear deny closes that. (This was decided after S10, in DL-024; it is stricter than the first bullet.)
- Guard optional arguments with `has()`: `has(args.amount) && args.amount > 5000`.

Every runtime error is also listed in the decision's `errors`, as `"<rule id>: <short error>"` (`"budget:<id>: …"` for a measure).

**Invariant 2. Same input, same decision.** There is no clock, randomness or network inside evaluation. The time and the usage counters are inputs, recorded with the action. Running the same input 1,000 times in parallel gives identical decisions (a test does it). That is what makes shadow mode and replay (S27) exact. The one value that varies is the elapsed time, which is not part of the decision's JSON.

**Shadow mode.** `EvaluatePair` evaluates the enforced policy and, when there is one, the shadow policy over the same prepared input. The shadow decision is computed independently and never carries reservations or cost budgets, so it never counts against a budget.

**Why first match wins, and not deny-overrides.** A policy reads top to bottom like a firewall: to know what happens to an action, read until the first rule that matches. The alternative, used by Cedar and by many OPA policies, evaluates every rule and lets any deny win; it is order-independent, but a reader has to hold the whole file in mind to predict a decision. Deedglass chooses readability, and the compiler warns when an earlier rule makes a later one unreachable (`rule_unreachable`).

### 6.1 The decision

`Evaluate` returns a `Decision` (`internal/policy/decision.go`). In JSON:

| Field | Meaning |
|---|---|
| `decision` | `allow`, `deny` or `require_approval` |
| `code` | empty for allow; otherwise `policy_denied`, `approval_required`, `budget_exceeded`, `agent_stopped`, `agent_suspended`, `org_stopped` or `policy_unavailable` |
| `source` | what decided: `stop`, `budget`, `rule`, `default` or `builtin` (invalid input, unavailable) |
| `rule_id` | the rule id, a budget id (`agent-rate`, `agent.calls_per_minute`), `budget:<id>` for a failed measure, `invalid-input` or `unavailable`; empty for the default and for Stop |
| `rule_line` | the rule's line in the policy file |
| `reason` | the policy's own sentence, with the error suffix when a rule failed |
| `message` | what the agent sees (section 6.4). Empty for allow |
| `approval` | for `require_approval`: `approvers`, `required`, `expires_in` (seconds), the rule's approval merged over `defaults.approval` |
| `verify` | for `allow` and `require_approval`: `L0`, `L1` or `L2` (the rule's, else `defaults.verify`, else `L0`) |
| `notify` | the rule's `notify`, for deny and require_approval |
| `budget` | for `budget_exceeded`: `id`, `per`, `window`, `limit`, `used`, `unit` (`calls` or a currency), `resets_at`, `retry_after_s` |
| `reservations` | for allow and require_approval in enforce mode: the counter increments the Gate makes (`budget`, `scope`, `counter`, `window`, `window_start`, `amount`, `limit`, `resets_at`) |
| `cost_budgets` | the counters the reported cost is added to after execution (`budget`, `scope`, `window`, `window_start`, `currency`) |
| `facts` | the derived facts: `sql` (section 7.1) and `time` (`now`, `hour`, `minute`, `weekday`, `date`) |
| `errors` | runtime errors, `"<rule id>: <short error>"` |
| `trace` | the explain trace (section 6.3), only when asked for |
| `sqlguard_version` | the classifier version, when SQL was classified |

The caller adds which policy decided (S12's registry and S13's Gate add its id, name, version and mode). `deedglass policy evaluate --json` adds `object`, `policy` and `evaluated_in_us`. From `cmd/deedglass/testdata/policy/evaluate_json.golden` (`DELETE FROM orders` against the postgres-safe policy; the trace is trimmed to four of its ten steps):

```json
{
  "object": "policy_decision",
  "decision": "deny",
  "code": "policy_denied",
  "source": "rule",
  "rule_id": "block-unbounded-writes",
  "rule_line": 55,
  "reason": "UPDATE and DELETE need a WHERE clause that names the rows to change.",
  "message": "Blocked by policy \"block-unbounded-writes\": UPDATE and DELETE need a WHERE clause that names the rows to change.",
  "facts": {
    "sql": {
      "present": true, "kind": "delete", "category": "write", "kinds": ["delete"],
      "statements": 1, "multi": false, "writes": true, "write_kinds": ["delete"],
      "has_where": false, "unbounded_write": true, "where_trivial": false, "has_returning": false,
      "cte_write": false, "select_into": false, "copy_program": false, "explain_analyze": false,
      "locks_rows": false, "tables": ["public.orders"], "tables_complete": true,
      "functions": [], "dangerous_functions": [], "write_functions": [], "unknown_functions": [],
      "ambiguous": false, "reasons": [], "bytes": 18
    },
    "time": { "now": "2026-10-04T10:00:46+05:30", "hour": 10, "minute": 0, "weekday": "sun", "date": "2026-10-04" }
  },
  "trace": [
    { "step": "stop", "result": "pass", "detail": "agent active · org running" },
    { "step": "budget", "id": "agent-rate", "result": "pass", "detail": "0 of 60 calls this minute" },
    { "step": "rule", "id": "block-unbounded-writes", "line": 55, "result": "match", "detail": "when → true", "cost": 2 },
    { "step": "rule", "result": "not_evaluated", "detail": "6 more rules not evaluated" }
  ],
  "sqlguard_version": "1",
  "policy": {
    "name": "postgres-safe",
    "version_hash": "sha256:2c4c3f1058e5b52c0c96469dcde7354e1047cd4f238e785c074e5566efc504fd"
  },
  "evaluated_in_us": 41
}
```

### 6.2 The input

The Gate builds one `Input` per action (S13) and stores it canonically with the action, so a decision can be replayed (S27). `deedglass policy evaluate --input` reads the same shape (`cmd/deedglass/testdata/policy/evaluate_input.json`):

```json
{"v":1,
 "org":{"id":"org_01jb8k0000000000000000000a","timezone":"Asia/Kolkata","stopped":false},
 "agent":{"id":"agt_01jb8k0000000000000000000a","name":"support-bot","labels":{"env":"prod"},"status":"active",
          "budgets":{"calls_per_minute":60,"calls_per_day":5000,"spend_per_day":{"amount":5000,"currency":"usd"}}},
 "user":{"ref":"cust_8812","sub":"","iss":""},"session":{"ref":"sess_4410"},
 "tool":{"name":"postgres.execute","connector":"postgres","connector_type":"postgres","side_effect":"write","default_schema":"public"},
 "args":{"sql":"UPDATE public.orders SET status = 'shipped' WHERE id = 42"},
 "request":{"mode":"gateway","bytes":71,"intent_signed":true,"digest":"sha256:5c1e"},
 "time":"2026-10-04T04:30:00Z",
 "usage":{"calls_minute":3,"calls_hour":40,"calls_today":212,"spend_today":0,"currency":"usd","budgets":{"agent-rate":3,"agent-daily":212}},
 "anomaly":{"active":false}}
```

| Key | Holds |
|---|---|
| `v` | the input format, `1` |
| `org` | `id`, `timezone` (IANA; empty means UTC), `stopped` |
| `agent` | `id`, `name`, `labels`, `status` (`active`, `suspended`, `stopped`), and `budgets` (S08's limits; CEL never sees them) |
| `user`, `session` | the on-behalf-of facts (S09) |
| `tool` | `name`, `connector`, `connector_type`, `side_effect`, and `default_schema` for the SQL classifier |
| `args` | the tool arguments, a JSON object of at most 256 KiB, nested at most 32 deep. Integers become `int`, other numbers `double` |
| `request` | `mode`, `bytes`, `intent_signed`, `digest` |
| `time` | the decision instant, set by the caller, in UTC |
| `usage` | the counters for the current windows ([BUDGETS.md](BUDGETS.md)) |
| `anomaly` | `active`, and `kinds` and `severity` when active ([the `anomaly` variable](#the-anomaly-variable)). Filled by Insights only when the org turns anomaly facts on; otherwise `{"active":false}` |

**Only raw facts are stored.** The derived facts (every `sql.*` field and the time parts `hour`, `minute`, `weekday`, `date`) are recomputed at each evaluation, so a replay under a newer classifier reflects it, and the decision records `sqlguard_version`. `Input.Digest()` is `sha256:` plus the hex SHA-256 of the input's canonical JSON.

### 6.3 The trace

With `explain` (`deedglass policy evaluate --explain`, the Console), the decision carries a trace: one step per thing evaluation looked at. Without it, evaluation builds no trace and allocates nothing for it.

| Field | Meaning |
|---|---|
| `step` | `stop`, `budget`, `rule` or `default` |
| `id` | the rule or budget id |
| `line` | the rule's line in the policy file |
| `result` | below |
| `detail` | one short line: `when → true`, `when → false`, `match doesn't cover this action`, `when error: no such key: amount`, `0 of 60 calls this minute`, `currency_mismatch: usage is in eur`, `no rule matched; defaults.deny` |
| `cost` | the actual CEL cost of the condition or measure |

| Result | Meaning |
|---|---|
| `pass` | Stop checked and clear; a budget checked and within its limit |
| `match` | this rule (or the default) decided |
| `no_match` | the `match` doesn't cover the action, or `when` was false |
| `error` | `when` or `measure` failed at run time; the detail says whether the rule still decided |
| `exceeded` | Stop applies, or a budget would be passed: this step decided |
| `reserved` | declared for budgets; `Evaluate` doesn't emit it in S11 (a budget that fits is `pass`, and its increment is in `reservations`) |
| `not_evaluated` | the rules after the deciding one, summarised in one step: `… 6 more rules not evaluated` |

Rules after the deciding one are not evaluated, so they are not listed one by one.

### 6.4 Messages

`message` is what the agent sees. Each message is exact and ends with a next step. The templates are constants in `internal/policy/decision.go`, with tests.

| Case | Message |
|---|---|
| Deny by rule | `Blocked by policy "block-destructive-sql": DROP and TRUNCATE are never allowed for agents.` |
| Deny by default | `Blocked by policy "postgres-safe": No rule allows this action. Add a rule in Console › Policies to allow it.` |
| Approval | `Waiting for approval under policy "writes-need-approval": Writes to this database wait for a human. An admin can approve in Console › Approvals.` |
| Budget (calls) | `Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 14 s.` |
| Agent budget (S08) | `Budget "calls_per_minute" for agent "support-bot" reached: 60 of 60 calls this minute. Try again in 14 s.` |
| Budget (money, org) | `Budget "refunds-per-day" reached: $500.00 of $500.00 across all agents today. It resets at midnight Asia/Kolkata (in 6 h 12 m).` |
| Rule error on a deny | `Blocked by policy "cap-refunds": Refunds over $50.00 wait for a human. (Deedglass could not evaluate this rule: no such key: amount.)` |
| Cost limit | `Blocked by policy "cap-refunds": Deedglass stopped evaluating this rule at its cost limit of 100,000, so the action did not run. Send smaller arguments, or ask an admin to simplify the rule in Console › Policies.` |
| Measure error | `Blocked by policy "budget:refunds-per-day": Deedglass could not measure this action for budget "refunds-per-day" (no such key: amount), so it did not run. An admin can check the budget's measure in Console › Policies.` |
| Invalid input | `Deedglass could not read this action's input (the arguments must be a JSON object), so it did not run. Check the arguments the agent sent.` |
| Org stopped | `All agents in this org are stopped. An owner can start them again in Console › Settings.` |
| Agent stopped | `Agent "support-bot" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.` |
| Agent suspended | `Agent "support-bot" is suspended. An admin can resume it in Console › Agents.` |
| Unavailable | `Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies.` |

The rule form `Blocked by policy "<rule id>": <reason>` names the rule, because in Deedglass's words a rule is a policy; the structured fields carry both the policy name and the rule id. The Stop messages are S08's error-catalog strings, and a test keeps them equal. A budget message quotes the usage recorded before this action, and rounds the wait up.

### 6.5 `AlwaysDenies`

`AlwaysDenies(policy, tool, agent)` answers one static question: is every call to this tool by this agent denied, whatever the arguments? S14 uses it to hide such tools from an MCP client's `tools/list`.

It walks the rules in order and stops at the first rule whose `match` (tool, connector type, side effect, agent, labels) covers the tool:
- a rule with a `when` can't be decided statically, so the answer is false;
- otherwise the answer is whether that rule's decision is `deny`.

If no rule covers the tool, the answer is whether `defaults.decision` is `deny`. A missing policy always denies. It ignores budgets and Stop, because they change from minute to minute.

### 6.6 `verify:` and the Witness

`verify:` sets the lowest witness level an allowed action must reach to be `VERIFIED` (S19,
[WITNESS.md](WITNESS.md)). The decision carries it, and the Gate records it on the action as
`required_level` when the decision is made, so a later policy change doesn't alter it.

- **It applies to tools that may write:** side effect `write`, `destructive` or `unknown`. A read
  always requires `L0`, whatever the rule says.
- **The default is `L0`:** the rule's `verify`, else `defaults.verify`, else `L0`.
- **It never weakens a verdict, and it is never lowered.** A violation at any level is still
  `MISMATCH`. A clean level below the required one gives `UNVERIFIABLE` with the reason
  `level_below_required`, at that level: `verify: L1` with a clean L0 and a failed read-back is
  `L0 · UNVERIFIABLE`.
- **A higher level never needs to be clean to keep a lower claim, unless the policy asks.** With
  `verify: L0`, a clean L1 read-back and an L2 gap give `L1 · VERIFIED`, with the gap listed. With
  `verify: L2`, the same evidence gives `L1 · UNVERIFIABLE`.
- **It doesn't block the call.** The tool runs when the decision allows it; the required level
  only decides what the verdict may say afterwards. To refuse writes that can't be confirmed, deny
  them in the policy.
- **Ask only for what can run.** `L1` needs a read-back on the tool (connector config, see
  [CONNECTORS.md](CONNECTORS.md#read-backs)) or a Postgres single-row update by primary key; `L2`
  needs the Postgres change stream (S21). Otherwise every such action is `UNVERIFIABLE`.

## 7. The CEL environment

Conditions (`when`) and measures (`measure`) are [CEL](https://cel.dev) expressions: typed, not Turing-complete and cost-bounded. Policy/v1 uses `cel.dev/cel-go` v0.32.0 with one fixed environment.

### 7.1 Variables

Every structured variable is declared field by field, so a typo like `sql.knid` is a compile error (`cel_no_field`), never a silent `false`. Strings are empty and lists are empty when a value is absent.

| Variable | Type | Fields |
|---|---|---|
| `tool` | object | `name`, `connector`, `connector_type`, `side_effect` (strings); since S16 also `type` (the connector type again, like `rest`) and `op` (the tool's name after the first dot, like `update_status` in `orders.update_status`). Since S17 the Postgres connector raises `side_effect` per call (never lowers it): `destructive` for a `postgres.execute` whose SQL deletes, and the function's own side effect for `postgres.call`; its first allowed schema is the classifier's default schema, so `sql.tables` names unqualified tables as the `search_path` will resolve them |
| `args` | `map(string, dyn)` | the tool arguments as JSON: integers are `int`, other numbers are `double`, objects are maps, arrays are lists |
| `request` | object | `mode` (`gateway` or `proxy`), `bytes` (int), `intent_signed` (bool), `digest` (string); since S16 `http`, an object with `method`, `path` and `host` (strings), filled by the REST connector before the decision with the request it will send: `request.http.path` is the path with the arguments filled in, like `/v1/orders/42`. Empty strings for every other connector, and when the arguments don't build a request (the call then fails as `validation_failed` and nothing is sent). Since S29 also `github` and `slack`, below. |
| `sql` | object | below; filled by the SQL classifier (S11) when the tool is a Postgres tool with an `args.sql` string, otherwise `present` is false and `kind` is `none` |
| `agent` | object | `id`, `name`, `labels` (map string to string), `status` |
| `user` | object | `ref`, `sub`, `iss` (empty strings when absent; from `Deedglass-On-Behalf-Of` or a signed intent, S09) |
| `session` | object | `ref` (from `Deedglass-Session` or a signed intent) |
| `org` | object | `id`, `timezone` (IANA name) |
| `time` | object | `now` (timestamp), `hour` (0 to 23), `minute` (0 to 59), `weekday` (`"mon"` … `"sun"`), `date` (`"2026-10-04"`), all in the org's time zone |
| `usage` | object | `calls_minute`, `calls_hour`, `calls_today`, `spend_today` (int, minor units), `currency`, `budgets` (map from budget id to its value in the current window) |
| `anomaly` | object | `active` (bool), `kinds` (list(string)), `severity` (string): what Insights says about this agent at decision time; below |

#### `request.github` and `request.slack` (S29)

The GitHub and Slack connectors fill these before the decision, from the call's arguments and one cached lookup (5 minutes). For every other tool all fields are empty strings, `false` and `0`. When the lookup fails, `enriched` is `false` and the other fields may be empty: write rules so that a missing fact is treated as the risky case, like the starter does with `!request.github.enriched || request.github.protected_branch`.

| Field | Type | Meaning |
|---|---|---|
| `request.github.repo`, `owner`, `name` | string | the repository (`acme/web`, `acme`, `web`) |
| `request.github.default_branch` | string | the repository's default branch, from GitHub |
| `request.github.branch` | string | the branch a write goes to: a commit's `branch`, or the default branch when the call names none |
| `request.github.base`, `head` | string | a pull request's base and head |
| `request.github.path` | string | a file's path |
| `request.github.protected_branch` | bool | `branch` (or `base` for a pull request) matches one of the connector's `protected_branches` globs (`main`, `master`, `release/*` by default) |
| `request.github.is_default_branch` | bool | the write goes to the default branch |
| `request.github.touches_workflows` | bool | `path` is under `.github/workflows/` or `.github/actions/` (files that run with the repository's secrets) |
| `request.github.enriched` | bool | the connector looked the repository up |
| `request.slack.channel_id` | string | the channel's id (`C0AGENTUPD`), resolved from a `#name`. Compare ids: a renamed channel keeps its id |
| `request.slack.channel_name` | string | the channel's name without `#` |
| `request.slack.is_private`, `is_im`, `is_ext_shared` | bool | a private channel, a direct message, a channel shared with another workspace |
| `request.slack.broadcast` | bool | the text holds `<!channel>`, `<!here>` or `<!everyone>` |
| `request.slack.user_mentions` | int | how many people the text mentions (`<@U…>`) |
| `request.slack.enriched` | bool | the connector looked the channel up |

```yaml
- id: workflow-files-need-approval
  match: { tool: github.create_or_update_file }
  when: request.github.touches_workflows
  decision: require_approval
  reason: Changes under .github/workflows run with your repository's secrets, so a person approves them.
```

In policy test cases, set them under `request`: `input: { tool: github.create_pr, args: {…}, request: { github: { enriched: true, base: main, protected_branch: true } } }`.

#### `request.stripe` and `request.email` (S30)

The Stripe connector reads the charge (or the PaymentIntent's latest charge, or the subscription)
from Stripe before the decision, cached 30 seconds per object; the Gmail connector reads the
recipients from the arguments and the connector's `internal_domains`, with no call. Amounts are
minor units of `request.stripe.currency`, from Stripe, never from the agent's arguments. When a
read fails, `enriched` is `false`: the starter `stripe-refunds` sends those refunds to a person.

| Field | Type | Meaning |
|---|---|---|
| `request.stripe.charge`, `payment_intent`, `customer` | string | ids from Stripe |
| `request.stripe.currency` | string | the charge's currency, lowercase ISO 4217 |
| `request.stripe.charge_amount`, `amount_refunded`, `refundable` | int | minor units; `refundable = charge_amount − amount_refunded` |
| `request.stripe.disputed` | bool | the charge is disputed |
| `request.stripe.charge_age_days` | int | whole days since the charge |
| `request.stripe.amount_display` | string | `args.amount` for people, like `$120.00` or `¥5,000` |
| `request.stripe.subscription`, `status`, `current_period_end`, `amount_per_period`, `interval` | string, string, int (Unix seconds), int, string | for `stripe.cancel_subscription` |
| `request.stripe.enriched` | bool | Stripe was read |
| `request.email.recipient_count` | int | to + cc + bcc |
| `request.email.domains`, `external_domains` | list(string) | lowercase; external means not in the connector's `internal_domains` |
| `request.email.has_external`, `has_bcc`, `is_reply` | bool | |
| `request.email.enriched` | bool | the recipients could be read |

#### `request.mail` (S40, ops build only)

The outbound-mail connector exists only in the ops build (`make ops-build`) and only for the org
ids in `connectors.outbound_mail.orgs`. For `mail.send` it counts these in the message itself, so a
rule never depends on the sender's own counts. Zero (`enriched` false) for every other tool.

| Fact | Type | Meaning |
|---|---|---|
| `request.mail.from`, `from_domain` | string | the authorised mailbox and its domain |
| `request.mail.to_domain` | string | the recipient's domain, lowercase |
| `request.mail.words`, `links`, `bare_domains`, `questions` | int | words above the signature, `http(s)://` links, domains Gmail would turn into links, `?` |
| `request.mail.has_html` | bool | the body holds `<html` (the connector refuses it anyway) |
| `request.mail.step`, `threaded` | int, bool | the sequence step; a reply in a thread |
| `request.mail.unsubscribe_https` | bool | the one-click unsubscribe URL is https |
| `request.mail.enriched` | bool | the message could be read |

A budget's `measure` sees the same variables as `when`, `request` included (S30 confirmed it with
the starter's `refunds-per-day`):

```yaml
measure: 'request.stripe.enriched && request.stripe.currency == "usd" && has(args.amount) && type(args.amount) == int ? args.amount : 0'
```

In policy test cases: `request: { stripe: { enriched: true, currency: usd, refundable: 14900 } }`.

#### The `anomaly` variable

`anomaly` is what Insights' anomaly detection says about the acting agent at the decision instant ([INSIGHTS.md](INSIGHTS.md)).

| Field | Type | Meaning |
|---|---|---|
| `active` | bool | the agent has at least one anomaly episode whose `active_until` is after the decision time |
| `kinds` | list(string) | the kinds of those episodes, sorted, each once: `new_tool`, `volume_spike`, `off_hours`, `deny_spike`, `mismatch_spike`, `user_ref_burst`, or `detector_stale` alone when the facts fail closed. Empty when not active |
| `severity` | string | the highest severity of those episodes: `info`, `warn` or `high`; `""` when not active |

How they are filled:

- **Opt-in, off by default.** Only an org that turns on anomaly facts (`PATCH /v1/insights/settings` with `"anomaly_facts": true`, or `deedglass insights settings --anomaly-facts on --reason "…"`) gets them. Otherwise every policy sees `active` false, `kinds` empty and `severity` `""` (unless the lookup itself fails, below).
- **Fail closed.** For an org that opted in, when the last successful detection pass for the org is older than `anomaly_stale_after_minutes` (15 by default), every agent gets `active` true, `kinds` `["detector_stale"]` and `severity` `"warn"`. Unknown is not usual. A lookup that fails fails closed the same way, unless the org's last successful read showed the facts off, because the failed read is the one that would say whether the org opted in.
- **Recorded.** The facts are part of the stored input (`"anomaly":{"active":true,"kinds":["volume_spike"],"severity":"warn"}`; `kinds` and `severity` are left out when empty), so a replay sees exactly what the decision saw ([REPLAY.md](REPLAY.md)). The action's `decision.anomaly` carries the trace line, like `anomaly facts: volume_spike` or `anomaly facts: detector_stale (last run 22 min ago)`, and is absent when the facts were inactive.

```yaml
- id: approve-while-anomalous
  match: { side_effect: [write, destructive] }
  when: anomaly.active && anomaly.severity in ["warn", "high"]
  decision: require_approval
  reason: "This agent is behaving unusually right now. A person approves writes until the anomaly is resolved."
```

The complete policy with its cases is recipe 19 in [POLICY_COOKBOOK.md](POLICY_COOKBOOK.md). When an enforced or shadow policy reads `anomaly` while the facts are off, `GET /v1/insights/settings` and `GET /v1/insights/anomalies` say so in `notice`.

The `sql` fields:

| Field | Type | Meaning |
|---|---|---|
| `present` | bool | the action carries SQL |
| `kind` | string | the first statement's kind (`select`, `update`, `drop`, …; `empty` for only comments; `none` without SQL) |
| `category` | string | the most severe category across statements (`read`, `write`, `ddl`, `privilege`, `server_code`, `unknown`, …) |
| `kinds` | list(string) | every statement's kind |
| `statements` | int | the number of non-empty statements |
| `multi` | bool | more than one statement |
| `writes` | bool | any statement, CTE or function writes |
| `write_kinds` | list(string) | the kinds that write |
| `has_where` | bool | the top-level UPDATE or DELETE has its own WHERE |
| `unbounded_write` | bool | any UPDATE or DELETE, including inside a CTE, lacks a WHERE or has a trivial one |
| `where_trivial` | bool | a WHERE like `true` or `1 = 1` |
| `has_returning` | bool | a RETURNING clause |
| `cte_write` | bool | a data-modifying CTE |
| `select_into` | bool | `SELECT … INTO` |
| `copy_program` | bool | `COPY … PROGRAM` |
| `explain_analyze` | bool | `EXPLAIN ANALYZE` (it runs the statement) |
| `locks_rows` | bool | `FOR UPDATE`, `FOR SHARE` or `LOCK` |
| `tables` | list(string) | `schema.table` names at every depth |
| `tables_complete` | bool | false when the classifier met something it can't follow |
| `functions` | list(string) | every function called |
| `dangerous_functions` | list(string) | file, network, backend-control and settings functions |
| `write_functions` | list(string) | functions that write |
| `unknown_functions` | list(string) | functions the classifier doesn't know |
| `ambiguous` | bool | the classifier isn't sure; treat it as dangerous |
| `reasons` | list(string) | why it is ambiguous |
| `bytes` | int | the SQL's size |

Adding a field later is additive. Renaming or removing one needs a new language version.

A REST rule that lets an agent read orders and change only their status (S16):

```yaml
rules:
  - id: order-reads
    match: { tool: ["orders.*"] }
    when: tool.type == "rest" && request.http.method == "GET"
    decision: allow
  - id: order-status
    match: { tool: [orders.update_status] }
    when: request.http.method == "PATCH" && request.http.path.startsWith("/v1/orders/")
    decision: allow
```

In policy test cases, set them as `tool: {name: orders.get, type: rest, op: get}` and
`request: {http: {method: GET, path: /v1/orders/42, host: orders.example.com}}`. `type` defaults to
`connector_type` and `op` to the name after the first dot.

### 7.2 Functions

- **The CEL standard library**, with cross-type numeric comparisons on, so `args.amount <= 5000` works whether the JSON said `5000` or `5000.0`.
- **Extensions, pinned:** strings (`ext.Strings`, version **5**), sets (`ext.Sets`, version **0**) and lists (`ext.Lists`, version **4**). A library upgrade can't change what a stored policy means, because the versions are fixed here and recorded in every normalized policy.
- **`glob(value, pattern) → bool`**, with the glob rules of section 5. The pattern must be a constant string.
- **`minor(money) → int`**: `minor("50.00 usd") == 5000`. The argument must be a constant string, and the compiler checks it.
- **`matches`** (RE2, linear time), in both forms: `tool.name.matches("^stripe\\.")` and `matches(tool.name, "^stripe\\.")`. The pattern must be a constant string of at most 256 characters, and it is checked when the policy compiles. **Patterns come from the policy author, never from the agent.**
- **`glob`** is a global function only: `glob(t, "public.*")`, also inside `all`/`exists` over a list.
- **Timestamps and durations:** `time.now > timestamp("2026-10-04T00:00:00Z")` and `duration("1h")` work; a malformed literal is refused at compile time (`cel_type`).
- **`usage.budgets["refunds-per-day"]`** reads a budget's current value. A budget id that doesn't exist is an evaluation error at run time, which never widens access.

**Not available:** `now()` (use `time.now`), network, files, user-defined functions and macros beyond the standard set (`has`, `all`, `exists`, `exists_one`, `map`, `filter`).

**The values of `sql.kind`** are the statement keywords in lowercase: `select`, `insert`, `update`, `delete`, `merge`, `drop`, `truncate`, `alter`, `create`, `grant`, `revoke`, `copy`, `do`, `call`, `show`, `explain`, `begin`, `commit`, `set` and so on, plus `empty` and `none`. **`sql.category`** is one of `none`, `read`, `maintenance`, `session`, `transaction`, `write`, `ddl`, `privilege`, `server_code`, `unknown`. S11's classifier spec has the full table.

### 7.3 Types and `dyn`

`args` is a map of `dyn`, so `args.flag` type-checks as `dyn` and is accepted as a condition; a value that turns out not to be a bool at run time is an evaluation error, which never widens access (section 6). The same applies to a `measure` that returns `dyn`: S11 checks for an `int` at run time.

A condition whose static type is not `bool` (`when: '"yes"'`) is `cel_not_bool`. A measure whose static type is not `int` is `cel_not_int`. Comparing a value with a quoted number using `<`, `<=`, `>` or `>=` (`args.amount <= "5000"`) is `cel_type`: a quoted number is a string, and strings compare letter by letter.

## 8. Limits

These are constants in `internal/policy/limits.go`. Changing one needs an ADR.

| Limit | Value | On breach |
|---|---|---|
| Source size | 256 KiB | `file_too_large` |
| YAML nodes / depth | 50,000 / 32 | `yaml_too_complex` |
| Anchors, aliases, merge keys `<<`, non-core tags, multiple documents | none allowed | `yaml_alias`, `yaml_merge_key`, `yaml_tag`, `yaml_multi_document` |
| Rules / budgets | 500 / 50 | `too_many_rules` / `too_many_budgets` |
| Entries in one `match` list, labels, approvers, notify | 32 | `too_many_values` |
| `description` | 1,000 characters | `value_too_long` |
| `reason` | 280 characters | `reason_too_long` (warning) |
| A glob | 200 characters | `invalid_glob` |
| `when` and `measure` length | 4,096 characters | `cel_too_long` |
| CEL parser recursion | 64 | `cel_syntax` |
| Estimated maximum cost per `when` or `measure` | 100,000 units | `cel_cost` |
| Runtime cost per expression | 100,000 units | deny `policy_denied` with the cost-limit reason, for any rule (section 6) |
| Evaluation time per action | 50 ms | deny `policy_unavailable` (section 6) |
| Comprehension nesting | 2 | `cel_nesting` |
| `matches()` pattern | 256 characters | `cel_regex` |
| Diagnostics per file | 50, then one "and N more" entry | |

Cost estimation uses these size hints, because CEL can't know how big a value will be. Without them every list would count as unbounded.

| Value | Hint |
|---|---|
| a string from `args` or built by the expression | 262,144 bytes |
| a declared fact string (names, ids, kinds, table and function names) | 256 bytes |
| a label value | 128 bytes |
| `args` | 1,024 keys |
| a value inside `args` (`dyn`) | 1,024 elements |
| a list | 1,024 |
| `sql.tables` | 64 |
| `sql.functions` and the other function lists | 128 |
| `agent.labels` | 32 |
| `usage.budgets` | 50 |

The estimate is an upper bound for the hinted sizes. A real value larger than its hint is still caught at run time by the cost limit.

In practice this means one loop over a list is fine (`sql.tables.all(t, …)` estimates a few thousand units), while a loop inside a loop over `args` values (1,024 × 1,024) is over the limit. Guarding with `size(…) <= 20 &&` does not lower a static estimate; iterate a smaller list or split the rule.

## 9. Normalization and `version_hash`

Compiling turns the file into a **normalized document**: a JSON value with every default filled in, and nothing about how the file was written.

```
bytes ─► size check ─► one YAML document
      ─► refuse anchors, aliases, merge keys, non-core tags, tabs; count nodes and depth
      ─► tree with positions ─► schema checks (with did-you-mean)
      ─► CEL: parse → check → validators (regex, constants, nesting) → cost estimate
      ─► programs ─► normalize ─► JCS ─► version_hash
      ─► warnings (unreachable rules, default allow, ignored fields, secret-looking strings)
```

The normalized document:

```json
{
  "lang": "deedglass.policy/v1",
  "cel": {"lists": 4, "sets": 0, "strings": 5},
  "name": "refunds",
  "description": "",
  "defaults": {
    "decision": "deny", "reason": "No rule allows this action. …", "verify": "L0", "notify": [],
    "approval": {"approvers": ["role:admin"], "required": 1, "expires_in": 1800}
  },
  "budgets": [
    {"id": "refunds-per-day", "description": "", "match": {"tool": ["stripe.create_refund"]},
     "per": "org", "window": "day", "max_spend": {"amount": 50000, "currency": "usd"}, "measure": "args.amount"}
  ],
  "rules": [
    {"id": "small-refunds", "description": "", "match": {"tool": ["stripe.create_refund"]},
     "when": "args.amount <= minor(\"50.00 usd\")", "decision": "allow", "reason": "", "verify": "L0"}
  ]
}
```

- `lang` and the pinned CEL extension versions come first.
- Budgets and rules keep file order, because order is meaning.
- Each `when` and `measure` is the canonical unparse of its checked expression (`cel.AstToString`), so spaces, line breaks and quote style inside an expression don't matter. An omitted `when` is `"true"`.
- Durations are seconds; money is `{amount, currency}` in minor units.
- `match` lists are sorted and de-duplicated; `notify` and `approvers` are sorted and de-duplicated. Absent `match` fields are left out.
- A rule carries only the fields its decision uses: `approval` for `require_approval`; `notify` for `deny` and `require_approval`; `verify` for `allow` and `require_approval`, with the default merged in. Ignored fields never reach the document.

Comments, key order, quoting, indentation and flow-versus-block style never reach it.

`version_hash = "sha256:" + hex(sha256(JCS(normalized)))`, with the deed digest helper (contract §7). The hash goes into every deed as `policy.version_hash`, so anyone can tie an action to the exact rules that decided it.
- The same rules re-indented, re-quoted, commented or with keys reordered give the **same** hash.
- Swapping two rules, editing a reason, changing `60` to `61` or adding `verify: L1` to an allow rule give a **different** hash.
- `source_hash` is the SHA-256 of the raw bytes, for "is this the file I pushed?".

`deedglass policy lint --json` prints `version_hash` for a valid file.

## 10. How the language is versioned

- The marker `deedglass: policy/v1` names the language. `lang` in the normalized document is `deedglass.policy/v1`.
- **Additive changes stay in v1:** a new variable field, a new function, a new optional key, a new diagnostic code, a new warning. An existing valid policy keeps compiling, and keeps its `version_hash` unless it uses the new thing.
- **Anything that can change the meaning or the hash of an existing policy is v2:** renaming or removing a field, changing a default, a new CEL extension version, a different normalization.
- Deedglass will read v1 for as long as any stored policy uses it. A newer marker in an older Deedglass is `unsupported_format`.
- Diagnostic codes are API: they are added, never renamed.

## 11. Formatting (`deedglass policy fmt`)

`fmt` rewrites a valid policy into one layout, keeping every comment:
- key order: `deedglass, name, description, defaults, budgets, rules`; in a rule `id, description, match, when, decision, reason, approval, notify, verify`; in a budget `id, description, match, per, window, max_calls, max_spend, measure`; in `defaults` `decision, reason, verify, approval, notify`; in `approval` `approvers, required, expires_in`;
- two-space indentation, block style for the top-level lists;
- flow style (`{ … }`, `[ … ]`) for `match`, `approval` and short lists when they fit in 100 columns;
- lowercase currencies in `max_spend`;
- a `when` that needs quoting gets single quotes, and a `when` over 80 characters becomes a folded block (`>-`).

`fmt` refuses a file that doesn't compile; it never changes meaning. The comment above the first key stays at the top of the file; a comment after a `match` or `approval` stays on that key's line. Two properties are tested over every valid fixture: `fmt(fmt(x)) == fmt(x)`, and `version_hash(fmt(x)) == version_hash(x)`.

## 12. What policies can't do

- **No loops and no functions of your own.** CEL has bounded comprehensions (`all`, `exists`, `map`, `filter`), nested at most two deep.
- **No network lookups, files or clocks.** Everything a rule can see is in section 7. The time is an input.
- **No state beyond `usage`.** A rule can't remember the previous action; budgets and the `usage` counters are the only memory.
- **No composition across files in v1.** One enforce policy is active per org (S12 explains why); there is no `include:`.
- **CEL doesn't understand SQL.** The `sql` facts come from the S11 classifier, which is conservative and is not a full Postgres parser. When it isn't sure it says `ambiguous`, and good policies deny ambiguous SQL.
- **No anomaly facts unless the org opts in.** `anomaly.*` stays inactive until an admin turns anomaly facts on, and what an anomaly means is whatever your rules say; Insights never blocks on its own ([INSIGHTS.md](INSIGHTS.md)).
- **A policy decides; it doesn't execute.** Approvals (S22), notifications (S22, S29) and witness levels (S19) are carried out by other parts of Deedglass.
- **The cost estimate is an estimate.** It uses the size hints in section 8; the runtime cost limit is the backstop.

## 13. Coming from OPA/Rego, Cedar or Kubernetes CEL

| If you know | What is similar | What differs |
|---|---|---|
| OPA / Rego | Policy as code, reviewed in pull requests; a default decision | Deedglass rules are ordered and the first match decides; conditions are CEL, not Rego |
| Cedar | `permit`/`forbid` maps to `allow`/`deny`; nothing is allowed by default unless you say so | Cedar evaluates every policy and any forbid wins; Deedglass reads top to bottom |
| Kubernetes CEL (ValidatingAdmissionPolicy) | The same expression language, with cost estimation and limits | Deedglass adds `match`, decisions, approvals and budgets around the expression |

The sources for the sentences about other products are in `CLAIMS_REGISTER.md` (rows C-024 onward).

## 14. Later

Not in v1, and planned:
- composition across files (`include:`);
- per-rule shadow mode (v1 has policy-level shadow mode, S12);
- a language server (v1 has `lint`, a JSON Schema and the Console editor, S26).

## 15. FAQ

**Why YAML and not a language of its own?** YAML reads in ten seconds and diffs cleanly; the part that needs a real language, the condition, uses CEL, which is typed and cost-bounded.

**Why does `when: !sql.multi` fail?** YAML reads a leading `!` as a tag. Quote the expression: `when: '!sql.multi'`.

**Why does `when: x ? a : b` fail?** YAML reads ` : ` in a plain value as a new key. Quote it, or write it as a block (`when: >-`).

**Is `args.amount` in dollars or cents?** In whatever the tool takes. Stripe takes minor units, so `5000` is $50.00; `minor("50.00 usd")` writes the same number readably.

**Why did my `allow` rule not match when an argument was missing?** Invariant 1: an error never widens access. Guard it: `has(args.amount) && args.amount <= 5000`.

**Can two policies share a `version_hash`?** Only if they normalize to the same document, which means they decide every action the same way and read the same.

## 16. Diagnostics at a glance

Every finding is a **diagnostic** with a stable `code`, a `severity` (`error`: the policy is not accepted; `warning`: it is accepted, but look), a one-sentence `message`, a `hint` (the next step) and a position. [POLICY_ERRORS.md](POLICY_ERRORS.md) has one section per code.

**Positions.** `line` and `column` are 1-based, and columns count characters (runes), not bytes. A diagnostic about a value points at the value (for a quoted value, at its opening quote); about a key (`unknown_key`, `yaml_duplicate_key`, `invalid_label`), at the key; about a whole list or mapping written as a block (`too_many_rules`, `wrong_type` on `rules:`), at its key; about something missing (`missing_key`), at the start of the mapping that lacks it. Inside a `when` or `measure`, it points at the offending token in the file, in every YAML style (plain, quoted, `|`, `>`). Line 0 means the whole file (`file_too_large`).

**One pass.** The compiler reports every problem it can in one run, sorted by position, at most 50 (then one `more_diagnostics` entry). Two things stop it early, because nothing after them can be read safely: problems reading the YAML itself (size, syntax, tabs, aliases, merge keys, several documents, duplicate keys, too many nodes) and a missing or unreadable top-level mapping. A non-core tag is reported and checking continues. A misspelled key that looks like a required one (`desicion`) is reported once, as `unknown_key`, not also as `missing_key`.

| Stage | Errors | Warnings |
|---|---|---|
| Reading the file | `file_too_large`, `yaml_syntax`, `yaml_tab_indent`, `yaml_alias`, `yaml_merge_key`, `yaml_tag`, `yaml_multi_document`, `yaml_duplicate_key`, `yaml_too_complex` | |
| Structure | `missing_marker`, `unsupported_format`, `unknown_key`, `missing_key`, `wrong_type`, `conflicting_keys` (both `max_calls` and `max_spend`, or `measure` without `max_spend`), `invalid_name` (the policy name), `invalid_id` (rule and budget ids), `duplicate_id`, `too_many_rules`, `too_many_budgets`, `too_many_values`, `value_too_long` | |
| Values | `invalid_decision`, `invalid_value` (enums: `per`, `window`, `verify`, `notify`), `out_of_range` (`required`, `max_calls`), `reason_missing` (including an empty reason), `control_character`, `invalid_glob`, `invalid_side_effect`, `invalid_label`, `invalid_duration`, `money_float`, `invalid_money`, `unknown_currency`, `money_precision`, `invalid_approvers`, `approvals_unsatisfiable` | `reason_too_long`, `unknown_connector_type`, `ignored_field` |
| CEL | `cel_syntax`, `cel_undeclared`, `cel_no_field`, `cel_type`, `cel_not_bool`, `cel_not_int` (a `measure`), `cel_cost`, `cel_nesting`, `cel_too_long`, `cel_regex`, `cel_constant_required` (and `invalid_glob`, `invalid_money`, `unknown_currency`, `money_precision` for constant arguments of `glob()` and `minor()`) | |
| Whole policy | | `rule_unreachable`, `default_allow`, `secret_in_policy`, `reason_style` (only with `--voice`) |
| The compiler | `more_diagnostics` (an error when any hidden diagnostic is an error, else a warning), `internal_compiler_error` | |

**`rule_unreachable`** is conservative, with no false positives: rule B is unreachable when an earlier rule A has no `when`, and every field of A's `match` covers B's. A field A doesn't set covers anything. A glob list covers another when each of B's globs is covered by one of A's: `*`, the identical pattern, or `prefix*` covering a literal or a longer `prefix…*`. `side_effect` and agent ids cover by set inclusion; A's `labels` must all appear in B's. When unsure, it doesn't warn.

**`secret_in_policy`** looks for `dg_live_`, `dg_test_`, `sk_live_`, `rk_live_`, `ghp_`, `github_pat_` and `xoxb-` followed by at least 8 more characters, and `AKIA` followed by 16 capitals or digits, anywhere in the file, comments included. The message shows only the prefix.

**`reason_style`** (opt-in with `deedglass policy lint --voice`) checks reasons against the Deedglass voice: a capital first letter, a final period, no exclamation mark, and none of the words in `scripts/banned_words.txt`. Customers write their own domain words, so it is off by default; S12's starter policies are tested with it on.
