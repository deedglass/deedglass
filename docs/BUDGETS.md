# Budgets and usage counters

**Status:** describes S11 (evaluation and the counter stores). The Gate wires it in S13; approvals re-check budgets in S22. Owner: S11.
**Audience:** anyone who sets a budget in a policy or on an agent, and anyone who runs Deedglass on more than one instance.

A budget caps how much an agent, or a whole org, can do in a window: calls per minute, calls per day, money per day. When an action would pass a budget, Deedglass denies it with the code `budget_exceeded`, exact numbers and the time the window resets.

## 1. Two places to set a budget

**On the agent** (S08, Console › Agents or `deedglass agent`):

```json
{"calls_per_minute": 60, "calls_per_day": 5000, "spend_per_day": {"amount": 5000, "currency": "usd"}}
```

**In the policy** (`budgets[]`, [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 4.3):

```yaml
budgets:
  - id: agent-rate
    window: minute
    max_calls: 60
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"
    measure: args.amount
```

**Both apply.** An action must fit every budget. The agent's budgets are checked first, then the policy's budgets in file order, and the first budget that would be passed decides. So the stricter limit always wins, and neither kind can loosen the other. In the trace and in `Decision.budget.id`, agent budgets are named `agent.calls_per_minute`, `agent.calls_per_day` and `agent.spend_per_day`.

## 2. Windows

| Window | Starts | Length |
|---|---|---|
| `minute` | the UTC minute (`12:18:00Z`) | 60 seconds |
| `hour` | the UTC hour (`12:00:00Z`) | 60 minutes |
| `day` | midnight in **the org's time zone**, stored as UTC (`Asia/Kolkata` midnight is `18:30:00Z` the day before) | the org's local calendar day |

- **Daylight saving time.** A day window is a calendar day, so on the days the clocks change it is 23 or 25 hours long.
- **Changing the org's time zone mid-day moves the window.** The next action reads the day window that starts at the new zone's midnight, which is a different counter row. Usage counted under the old zone stays in the old row and is not carried over, so the day after a change can allow up to one extra day's worth.
- **The clock is the app's.** `Input.time` is set by the instance that receives the action. Run NTP on every instance.

## 3. `per: agent` and `per: org`

- **`per: agent`** (the default) keeps one counter per agent. Ten agents with a 60-per-minute budget can make 600 calls a minute between them.
- **`per: org`** keeps one counter for the whole org. Ten agents share the 60. Use it when spreading work across agents would otherwise get around the limit.

Agent budgets (section 1) are always per agent.

## 4. Three kinds of budget

| Kind | How it counts | When it is checked |
|---|---|---|
| `max_calls: N` | 1 per action | before execution: `used + 1 > N` denies |
| `max_spend` with `measure` | the `measure` expression's value (an `int`, in minor units) | before execution: `used + measure > limit` denies. Exact |
| `max_spend` without `measure` | the cost the tool reports after it runs | checked before (`used >= limit` denies), counted after. It can overshoot (section 7) |

**A `measure` must return a non-negative whole number.** If it fails (a missing key, a wrong type, a negative value, a double), the action is denied with `policy_denied` and the rule id `budget:<id>`. A failing measure never lets an action through uncounted. Over the CEL cost limit, the reason is the cost-limit message ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 6).

**Agent budgets** map onto the same three kinds: `calls_per_minute` and `calls_per_day` are call budgets, and `spend_per_day` is a cost-based budget on the agent's reported spend.

## 5. The counters

Every counter is a row in one table, unchanged from migration 0003 ([DATA_MODEL.md](DATA_MODEL.md)):

```sql
deedglass.usage_counters (org_id, scope, counter, window_kind, window_start, value)
-- primary key: (org_id, scope, counter, window_kind, window_start)
```

| Column | Values |
|---|---|
| `scope` | an agent id (`agt_…`), or `org` for a `per: org` budget |
| `counter` | `calls`, `spend:<currency>` (for example `spend:usd`) or `budget:<budget id>` |
| `window_kind` | `minute`, `hour` or `day` (the column isn't called `window`, a reserved word in SQL) |
| `window_start` | the window's start in UTC (section 2) |
| `value` | a count, or money in minor units; never negative |

- **Standard counters** exist for every action that is allowed: `calls` per agent for the minute, hour and day, and `spend:<currency>` per agent for the day, from the reported cost. They feed the `usage.*` variables in CEL and the Console's agent strip. **They are also the agent-budget counters:** when the agent has `calls_per_minute` or `calls_per_day`, that row's increment carries the limit.
- **Budget counters** are `budget:<id>`, one per policy budget, scoped to the agent or to `org` according to `per`.

Row-level security keeps each org's counters to that org. **Pruning** (`Usage.Prune`, a maintenance job) deletes minute rows older than 2 hours, hour rows older than 3 days and day rows older than 400 days.

## 6. Reservations and release

Evaluation is pure: it reads a snapshot of the counters and returns a decision. It never writes. When the decision is `allow` or `require_approval`, it carries `reservations`, the increments the Gate makes next, and `cost_budgets`, the counters the reported cost will be added to. A deny, and a shadow-mode decision, carry neither.

The lifecycle the Gate follows (S13 implements it):

```
Snapshot ─► Evaluate ─► (allow | require_approval) ─► Reserve ─┬─ ExceededError ─► deny budget_exceeded
                                                               └─ ok ─► execute ─► RecordCost(cost budgets)
action ends without executing (approval denied/expired, cancelled, failed before the upstream call) ─► Release
approved later ─► S22 re-checks Stop and re-evaluates budgets before it executes
```

- **`Reserve`** adds every reservation in one transaction, all or nothing.
- **`Release`** takes them back (never below 0) when the action ends without executing. The Gate calls it once per action.
- **`RecordCost`** adds the reported cost to each cost target whose currency matches, after the tool ran.

So an action waiting for approval holds its reservation: a budget of 10 calls with 10 actions waiting denies the 11th at once.

## 7. Accuracy across instances

**Call budgets and `measure` budgets are exact across any number of instances.** Postgres decides, not the instance. `Reserve` writes each counter with one conditional upsert:

```sql
INSERT INTO deedglass.usage_counters AS u (org_id, scope, counter, window_kind, window_start, value)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id, scope, counter, window_kind, window_start)
DO UPDATE SET value = u.value + EXCLUDED.value, updated_at = now()
WHERE u.value + EXCLUDED.value <= $7
RETURNING u.value;
```

No row back means the limit was hit: the whole transaction rolls back and the Gate denies with `budget_exceeded`. An integration test runs 400 concurrent `Reserve` calls against a limit of 60 from two separate connection pools (two instances); exactly 60 succeed, 340 are refused, and the row reads 60.

The snapshot an evaluation reads can be a few milliseconds old. That only matters for the message; the upsert is the authority.

**Cost-based budgets can overshoot.** A budget without `measure` (and an agent's `spend_per_day`) is checked before execution against what has been recorded, and the cost is added only after the tool reports it. Every action already in flight when the limit is reached still runs. With 5 actions in flight at $20 each, a $50 budget at $40 can end the day at $140. Use a `measure` when the amount is in the arguments.

## 8. Currency

A `max_spend` budget counts only costs in its own currency. A `"500.00 usd"` budget ignores a cost reported in eur. **There is no FX conversion.** For an agent's `spend_per_day`, when the snapshot's spend is in another currency, the trace says `currency_mismatch` and that usage doesn't count against the limit.

Money is in minor units everywhere: `5000` is $50.00, and `--usage spend_today=5000` means $50.00.

## 9. Messages and Retry-After

A budget deny carries a message with exact numbers and a next step (the templates are constants in `internal/policy/decision.go`):

| Case | Message |
|---|---|
| Calls, minute or hour | `Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 14 s.` |
| Agent budget | `Budget "calls_per_minute" for agent "support-bot" reached: 60 of 60 calls this minute. Try again in 14 s.` |
| Money, `per: org`, day | `Budget "refunds-per-day" reached: $500.00 of $500.00 across all agents today. It resets at midnight Asia/Kolkata (in 6 h 12 m).` |

The numbers are the usage recorded before this action and the limit. A wait is rounded up, so the message never promises a reset earlier than the real one.

`Decision.budget` holds the same facts for machines: `id`, `per`, `window`, `limit`, `used`, `unit` (`calls` or a currency), `resets_at` (the end of the window) and `retry_after_s` (whole seconds to `resets_at`, at least 1). The Gate turns it into HTTP 429 with a `Retry-After` header (S13).

## 10. Trying a budget locally

`deedglass policy evaluate` runs with no store. Pass usage with `--usage` ([CLI.md](CLI.md#deedglass-policy-evaluate)):

```
$ deedglass policy evaluate --policy postgres-safe.yaml --tool postgres.query --sql "SELECT 1" --usage calls_minute=60
✕ deny · budget agent-rate · 9 µs
  Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 14 s.
```

`--usage` takes `calls_minute`, `calls_hour`, `calls_today`, `spend_today`, `currency`, or a budget id (`--usage refunds-per-day=49000`). A per-agent call budget with no value of its own takes the standard counter for its window (`calls_minute`, `calls_hour` or `calls_today`); that is what `policy.FillLocalUsage` does, and why `agent-rate` above reads 60. A server never does this: its store reads each budget's own counter.

## 11. Worked examples

### 11.1 An agent rate of 60 calls a minute

```yaml
budgets:
  - id: agent-rate
    window: minute
    max_calls: 60
```

Each agent gets its own counter (`per: agent`), `budget:agent-rate`, for the current UTC minute. The 60th call in a minute is allowed and reserves 1 (the counter reads 60); the 61st is denied:

```
Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 14 s.
```

If the agent also has `calls_per_minute: 30` on its own row, the agent budget is checked first and the 31st call is denied with `Budget "calls_per_minute" for agent "support-bot" reached: 30 of 30 calls this minute. …`.

### 11.2 A $500 a day refund budget for the whole org

```yaml
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"
    measure: args.amount
```

The org is in `Asia/Kolkata`, so the day starts at 18:30 UTC. At 17:48 local time the org has refunded $490.00 today, across all its agents, and an agent asks for a $20.00 refund (`args.amount` is 2000). 49,000 + 2,000 is more than 50,000, so it is denied before it runs:

```
✕ deny · budget refunds-per-day
  Budget "refunds-per-day" reached: this action would bring today's total across all agents to $510.00 of $500.00. It resets at midnight Asia/Kolkata (in 6 h 12 m).
```

A budget with a `measure` names the total the action would reach, with this action included, not only the usage before it: "$490.00 of $500.00" alone would read like room to spare. The decision's `budget` object carries both `used` (49,000) and `projected` (51,000), and the trace line reads `$490.00 + $20.00 = $510.00 of $500.00 today`. Call budgets and cost-based budgets (no `measure`) keep the plain "reached: 60 of 60" form, because there the usage before the action is already at or over the limit.

A $10.00 refund would have been allowed, with a reservation of 1,000 on `budget:refunds-per-day` (scope `org`, limit 50,000). Because the amount comes from `measure`, the check is exact across instances. Only refunds count: the `match` limits the budget to `stripe.create_refund`.

### 11.3 An agent spend limit of $50 a day, counted after execution

The agent row has `"spend_per_day": {"amount": 5000, "currency": "usd"}`. There is no `measure`: the cost is whatever the tool reports after it runs, added to the agent's `spend:usd` counter for its day.

- At $42.00 spent, a call is allowed (`used >= limit` is false), runs, and reports $15.00. The counter becomes $57.00: the budget overshot, because the cost was only known afterwards.
- The next call sees $57.00 and is denied before it runs:

```
Budget "spend_per_day" for agent "support-bot" reached: $57.00 of $50.00 today. It resets at midnight Asia/Kolkata (in 6 h 12 m).
```

Two calls in flight at once can both pass the check at $42.00. If the overshoot matters, cap the per-call amount with a rule, or use a policy budget with a `measure`.

## 12. What budgets don't do

- **No FX.** A budget counts only its own currency.
- **No exact cost-based limits.** Without a `measure`, a budget can be passed by the actions already in flight.
- **No rolling windows.** Windows are fixed (this minute, this hour, today); 60 calls at 12:18:59 and 60 more at 12:19:00 are both within a 60-per-minute budget.
- **No carry-over** between windows, and none when the org's time zone changes.
- **No budgets across orgs**, and none per user or per session in v1.
- **They don't stop an action already running.** A budget is checked before execution; Stop is the control for work in progress, and it can't un-run a call either.
