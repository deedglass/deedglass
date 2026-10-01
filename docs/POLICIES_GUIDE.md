# Managing policies

**Status:** S12. Owners: S12 (versions, activation, the registry, `policy test`), S10 (the language), S11 (evaluation).
**Audience:** anyone who writes, reviews, tests or activates a policy for an org.

A policy decides, for every action an agent asks for, whether it runs, never runs, or waits for a person. In Deedglass you manage policies the way you manage code:

- **Versions are immutable.** Every push that changes something is a new version with its own `version_hash`.
- **Policies have tests.** Cases live next to the policy, and coverage shows which rules no case reaches.
- **Shadow before enforce.** A new version can decide every action next to the enforced one, and only record what it would have done.
- **Every activation is sealed as a deed.** Who changed the policy, to which exact version, and why.
- **Every instance switches within 2 seconds** of an activation.
- **Fail closed.** If an instance can't load and check the enforced version, it refuses every action in that org rather than fall back to an older one.

The language is specified in [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md). Ready-made rules are in [POLICY_COOKBOOK.md](POLICY_COOKBOOK.md), and the six starter policies are in [examples/policies/](../examples/policies/README.md).

Contents:

1. Your first policy in five minutes
2. How decisions are made
3. Test policies like code
4. Shadow mode
5. Activation and deeds
6. Versions
7. How a change reaches every instance
8. New orgs start with default-deny
9. One enforced policy per org
10. Troubleshooting
11. What policies don't do

## 1. Your first policy in five minutes

You need the `deedglass` binary and an org admin key. Admin keys are created by an admin in the Console, or on the server with `deedglass org keys create` ([AGENTS.md](AGENTS.md) section 4). The CLI reads the key from `--api-key` or `DEEDGLASS_API_KEY`, and the server from `--server` or `DEEDGLASS_SERVER`.

**Copy the starter.** Take postgres-safe and its cases into your own repository:

```
mkdir -p policies
cp examples/policies/postgres-safe.yaml examples/policies/postgres-safe.cases.yaml policies/
```

**Edit the allow-list.** In `policies/postgres-safe.yaml`, the rule `allow-listed-writes` names the tables your agent may write to without approval. Put your own tables in the list:

```yaml
      && sql.tables.all(t, t in ["public.support_notes", "public.order_tags"])
```

**Lint.** `lint` compiles the policy and reports every problem in one pass, with the line and a hint. `--voice` also checks that your reasons are calm, capitalised sentences that end with a period:

```
$ deedglass policy lint policies/postgres-safe.yaml --voice
✓ postgres-safe · 12 rules · 2 budgets · sha256:2c4c…04fd
```

**Test.** Add a case for each table you put in the list (section 3), then run the cases:

```
$ deedglass policy test policies/postgres-safe.yaml --require-coverage
✓ postgres-safe   28 cases · 12 of 12 rules
✓ 28 cases passed · 1 policy · every rule covered · 0.0 s
```

**Push.** This saves version 1 on the server. It is not active yet.

```
$ deedglass policy push policies/postgres-safe.yaml --notes "Orders DB, first cut"
✓ postgres-safe v1 saved · pv_01jb8m3c0d2f4g6h8j0k2m4n6p · sha256:2c4c…04fd · 12 rules · 2 budgets
  Next: deedglass policy test policies/postgres-safe.yaml
```

**Shadow.** The new version decides every action next to the enforced policy and only records what it would have done:

```
$ deedglass policy activate postgres-safe --version 1 --mode shadow --reason "Watch it for a day before enforcing"
◐ postgres-safe v1 is now the shadow policy · deed_01jb8p1a1c3fqx7m2k9d4e6w8r sealed
  It decides every action next to default-deny v1, and only records what it would have done.
```

**Enforce.** When you are ready, enforce the same version. On a terminal, the CLI says what will change and asks you to type the policy name back:

```
$ deedglass policy activate postgres-safe --version 1 --mode enforce --reason "Guard the orders database before the pilot"
  From now on, every agent action in acme is decided by postgres-safe v1 (replacing default-deny v1).
  The change is sealed as a deed.
  Type the policy name to confirm: postgres-safe
✓ postgres-safe v1 enforced · deed_01jb8p2k7v3n5q9s1t3w5y7m4x sealed · shadow cleared
```

In scripts, where there is no terminal to type into, add `--yes`. Without a terminal and without `--yes`, the CLI refuses to enforce.

**Every activation needs a reason**, from 3 to 280 characters on one line. It goes into the deed, so write it for the person who reads the Ledger in six months: what changed, and why it is safe.

**`default_allow` needs an acknowledgement.** A policy whose `defaults.decision` is `allow` lets through every action no rule names. The compiler warns about it (`default_allow`), and enforcing such a policy is refused until you acknowledge the warning: `--ack default_allow` on the CLI, or `"acknowledge_warnings": ["default_allow"]` in the API. The acknowledgement is recorded in the deed. Without it the API answers `400 validation_failed`:

```
This policy allows every unmatched action (default_allow). To enforce it anyway, repeat the request with acknowledge_warnings: ["default_allow"].
```

## 2. How decisions are made

For every action, Deedglass checks Stop first (a stopped org, a stopped or suspended agent), then budgets (the agent's own, then the policy's), then the rules from top to bottom. **The first rule that matches decides.** When no rule matches, `defaults` decides. A condition that fails while it runs never lets an action through.

The full order, the CEL variables, the messages your agent sees and the two invariants are in [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 6. Budgets are in [BUDGETS.md](BUDGETS.md), and the SQL facts are in [SQLGUARD.md](SQLGUARD.md).

## 3. Test policies like code

**Cases.** A cases file sits next to the policy, named `<name>.cases.yaml`, and `deedglass policy test` pairs the two automatically. Each case is an input and what you expect:

```yaml
policy: postgres-safe.yaml            # optional; resolved relative to this file
defaults:                             # merged into every case's input
  agent: { name: support-bot, labels: { env: prod } }
  org: { timezone: Asia/Kolkata }
  time: 2026-10-04T10:00:00+05:30
cases:
  - name: a comment does not hide DROP
    input: { tool: postgres.execute, sql: "/* tidy up */ DrOp TABLE public.orders" }
    expect: { decision: deny, rule: block-destructive-sql, reason_contains: DROP }
  - name: writes to orders wait for a human
    input: { tool: postgres.execute, sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42" }
    expect: { decision: require_approval, rule: writes-need-approval, verify: L2, approval: { required: 1 } }
  - name: the minute budget stops a burst
    input: { tool: postgres.query, sql: "SELECT 1", usage: { calls_minute: 60 } }
    expect: { decision: deny, code: budget_exceeded, rule: "budget:agent-rate" }
```

- **`input`** takes `tool` (a name, or `{name, connector_type, side_effect, default_schema}`), `args` or the `sql` shorthand, `agent`, `org`, `user`, `session`, `request`, `time`, `usage` and `anomaly`. It is the same input format as `POST /v1/policies/evaluate`.
- **`expect`** needs `decision`. It can also check `rule` (a rule id, `default`, `budget:<id>` or `stop`), `code`, `reason_contains`, `message`, `verify`, `approval` (`required`, `approvers`, `expires_in`) and `notify`.
- A case with no `time` runs at `2026-10-04T10:00:00Z`, and usage is zero unless you give it.
- Tests are pure: no database and no server. They run the same evaluation the Gate runs.

**Coverage.** A rule is covered when it decided at least one case. The default and every budget are reported too. A rule no case reaches is either dead or untested, and both are worth knowing about:

```
$ deedglass policy test policies/postgres-safe.yaml --require-coverage
✕ postgres-safe   28 cases · 12 of 13 rules
✕ 28 passed · 0 failed · uncovered: allow-explain
```

A failing case shows what you expected and what happened:

```
$ deedglass policy test examples/policies/postgres-safe.yaml
✕ a comment does not hide DROP · postgres-safe.cases.yaml:31
    expected  deny · block-destructive-sql
    got       allow · allow-reads
✕ 23 passed · 1 failed · uncovered: allow-describe
```

**Flags.**

| Flag | Does |
|---|---|
| `--cases <file>` | use this cases file instead of `<name>.cases.yaml` |
| `--dir <dir>` | test every policy in a directory; each needs a cases file |
| `--require-coverage` | fail when any rule decides no case |
| `--voice` | also fail on `reason_style` warnings |
| `--run <regexp>` | run only the cases whose name matches |
| `--json` | print the results as JSON |

Exit codes: 0 when everything passes, 1 for any failed case or uncovered rule, 2 for a usage error. `POST /v1/policies/test` returns the same result as JSON.

**In CI.** Keep your policies in a `policies/` directory with their cases, and run the tests on every change. As a GitHub Actions workflow step, with the `deedglass` binary installed on the runner in an earlier step:

```yaml
      - name: Test Deedglass policies
        run: deedglass policy test --dir policies --require-coverage
```

## 4. Shadow mode

A shadow policy runs next to the enforced one. For every action, Deedglass evaluates both over the same input. The enforced policy decides; the shadow policy's decision is only recorded.

- **What it records.** The shadow decision in full: the decision, the rule that decided, the reason, and the policy's name, version and `version_hash`. It is computed independently of the enforced decision.
- **It never counts against a budget.** A shadow decision carries no reservations, so watching a policy changes no counters.
- **At most one shadow per org.** Activating another version in shadow replaces the current one (with a deed).
- **The enforced version can't also be the shadow** (`409 conflict`). Shadow a newer version instead.
- **To stop shadowing,** switch the shadow off with a reason: `deedglass policy activate postgres-safe --version 2 --clear --reason "…"`. This is sealed as `policy.deactivated`.
- **To promote,** enforce the shadow version (section 5).

**Seeing where they disagree.** Insights (S27) will compare shadow and enforced decisions over time and show you where they differ. **It isn't built yet.** Until then, you can try a version against single actions with `deedglass policy evaluate --server` (section 10). The Gate stores each shadow decision with its action from S13.

## 5. Activation and deeds

Every activation and deactivation is sealed as an admin deed in the same transaction that changes the policy. There is no activation without a deed.

| Deed field | Holds |
|---|---|
| `action` | `policy.activated` or `policy.deactivated` |
| `actor` | who did it: a person, an org admin key, or `system` for onboarding |
| `target` | the version: `{type: "policy_version", id: "pv_…"}` |
| `reason` | the reason you gave |
| `data_digest` | a digest over `{policy_id, name, version, version_id, version_hash, source_hash, mode, activation_id, previous_version_id, acknowledged_warnings}` |

- **The YAML is never in the deed.** The deed holds `version_hash` (what the rules mean) and `source_hash` (the exact bytes you pushed), so anyone holding the version can match it to the deed.
- **The deed's `policy` block is null.** An admin deed records a change to the org; the version it activated is in the data.
- **A promote seals two deeds.** Enforcing the version that was in shadow ends the shadow (`policy.deactivated`) and enforces it (`policy.activated`), in one transaction.
- **Replacing the enforced policy seals one deed** for the new version; the old activation ends in the same transaction.

In the Console's Ledger, an activation reads like this:

```
policy.activated · postgres-safe v3 · enforce · by Priya · "Guard the orders database before the pilot"
```

**Check a deed offline.** Export it, then verify its seal against your server's published seal keys:

```
deedglass deeds show <deed> --json > deed.json
deedglass verify deed.json --keys https://<your-server>/.well-known/deedglass/keys.json
✓ 1 deed · seal valid
```

[VERIFYING_DEEDS.md](VERIFYING_DEEDS.md) explains what `verify` checks and every failure it reports.

## 6. Versions

- **Immutable.** A version is never edited: the database refuses an UPDATE or DELETE on a version row. To change a policy, push a new version.
- **Numbered per policy:** v1, v2, v3. Each has a `pv_` id, its `version_hash`, its `source_hash`, its author, the time, and your `--notes`.
- **Identical pushes save nothing.** When the pushed YAML compiles to the same `version_hash` as the latest version, the API answers `200` with the existing version instead of `201`, and the CLI says so:

  ```
  $ deedglass policy push policies/postgres-safe.yaml
  ○ postgres-safe v1 already has this content · nothing saved
    Next: deedglass policy activate postgres-safe --version 1 --mode shadow --reason "…"
  ```

  Comments, indentation, quoting and key order don't change `version_hash` ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 9), so reformatting a file never creates a version.
- **A push compiles first.** A policy with errors is refused with `400 policy_invalid` and the same diagnostics `lint` prints. A push is not sealed as a deed: the version row is the record of what was saved, and deeds are for changes that take effect.
- **Names are fixed per policy.** Pushing YAML named `stripe-refunds` to the policy `postgres-safe` is refused with `400 validation_failed`. Push it as a new policy, or change the name.
- **Rolling back is activating an older version.** There is no separate rollback: `deedglass policy activate postgres-safe --version 2 --mode enforce --reason "Roll back v3, which blocked the nightly sync"`. It is sealed like any other activation.

## 7. How a change reaches every instance

Each Deedglass instance keeps the org's enforced and shadow policies in memory, compiled and checked. An activation reaches all of them like this:

1. The activation commits. In the same transaction, Deedglass sends a Postgres `NOTIFY` on the `deedglass_policy` channel with the org's id. Postgres delivers it only after the commit.
2. Every instance is listening. Each one reloads that org in the background and swaps the new policies in. Actions already being evaluated finish with the version they started with.
3. **The target is 2 seconds** from commit to every instance deciding with the new version.

Two backstops cover a lost notification:

- **A safety poll every 30 seconds** compares each cached org with the database.
- **A reconnect reloads everything.** When an instance's listening connection drops and comes back, it drops its whole cache and reloads each org on its next action.

**Fail closed.** On every load, an instance does not trust the stored rows. For the enforced version and the shadow it:

1. compiles the stored YAML again;
2. checks that the result has the stored `version_hash` and `source_hash`;
3. checks the activation deed's seal against the org's seal keys, that the deed targets this version, and that its data digest matches this activation.

If any check fails for the enforced version, **every action in that org is refused with `policy_unavailable`**, the failure is logged (never the YAML), and `GET /v1/health` reports `"policies": "degraded"`. The instance tries the load again every 5 seconds. **It never falls back to an older version.** If an admin has just enforced a stricter policy in an emergency, quietly serving the older, looser one would be the worst outcome. A shadow version that fails its checks is logged and dropped, and the enforced policy keeps deciding.

## 8. New orgs start with default-deny

When an org is created, Deedglass saves `default-deny` v1 for it and enforces it, with the actor `system` and the reason "New orgs start with default-deny." That activation is sealed as a deed like any other. So nothing runs until an admin activates a policy that allows it, and the very first policy change in your Ledger is this one.

`default-deny` has no rules: its default refuses every action, and its `agent-rate` budget holds each agent to 60 calls a minute. Its reason tells the agent what to do next:

```
Blocked by policy "default-deny": Every new org starts with default-deny. Activate a starter policy in Console › Policies to allow actions.
```

Orgs created before S12 have no activation. `deedglass dev seed` enforces default-deny in every org that has none, and saves the other five starters as inactive policies. Until an org has an activation, the server refuses its actions with a builtin rule, `no-active-policy`.

## 9. One enforced policy per org

An org has exactly one enforced policy, plus at most one shadow. The database enforces it with a unique index.

**Why one.** With first-match-wins, you can read one file from top to bottom and know what happens to any action. Two policies at once would need a rule for which one wins, and then no single file would tell you. So there are no includes in v1, and every decision names one policy version, which is also the one in the deed.

**Combining starters.** To cover Postgres and Stripe with one policy, build one file from both starters:

1. Start from one starter, and copy the other's rules and budgets into it.
2. **Keep every id unique** across rules and budgets. The compiler refuses a duplicate (`duplicate_id`).
3. **Put denies first.** A deny rule only protects the allow rules below it, because the first rule that matches decides. The compiler warns (`rule_unreachable`) when an earlier rule hides a later one.
4. Keep one `defaults` block, and keep its decision `deny`.
5. Copy both cases files into one, run `deedglass policy test --require-coverage`, and add cases until every rule is covered.

## 10. Troubleshooting

**`policy_unavailable`.** Your agent sees: `Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies.`

- If every action in the org gets it and `/v1/health` says `degraded`, an instance could not load or check the enforced version (section 7). Check the server log for `the enforced policy failed its checks`. The entry names the check that failed. A version that no longer compiles to its stored hash means its row was changed outside Deedglass: push the policy again as a new version and enforce it.
- If only some actions get it, evaluation itself failed for those actions, for example by running past its time limit of 50 ms per action ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 6).

**`budget_exceeded`.** A budget would be passed. The message names the budget, the numbers and when the window resets, for example `Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 14 s.` The agent can wait and retry. To change the limit, edit the budget in the policy (or the agent's own budgets in Console › Agents), push and activate. [BUDGETS.md](BUDGETS.md) explains the windows and counters.

**"Why was my agent blocked?"** Ask the server to evaluate the same action against the active policies, with the trace:

```
deedglass policy evaluate --server --tool postgres.execute --sql "DELETE FROM public.orders" --agent agt_01jb… --explain
```

The trace shows Stop, every budget, and each rule up to the one that decided, with its line. `--agent agt_…` uses that agent's real name, labels, status and budgets. Add `--policy postgres-safe --version 3` to try a stored version instead, or `--policy policies/postgres-safe.yaml` to try a local file against the server's agents. A dry run never counts against a budget.

**Agent keys can't call policy routes**, not even to read: `403 insufficient_scope`, "Agents cannot read or test policies. Use the Console or an admin key." An agent that could read its policy could probe it for gaps. Use an org admin key or the Console.

**Other refusals you may see:**

| Answer | Means |
|---|---|
| `409 conflict` "postgres-safe v3 is already the enforced policy. Nothing changed." | nothing to do |
| `409 conflict` "The enforced policy can't be switched off. Activate another version or default-deny instead." | an org always has an enforced policy |
| `400 validation_failed` on `reason` | the reason is missing, or not 3 to 280 characters on one line |
| `400 validation_failed` on `acknowledge_warnings` | the version has `default_allow`; see section 1 |
| `403 insufficient_role` | viewers can read and test policies, but only admins and owners can push or activate |

## 11. What policies don't do

- **No includes.** One policy is one readable file. Combine starters by copying their rules (section 9).
- **No per-agent policies.** One policy decides for every agent in the org. Use `match.agent` and agent labels to give agents different rules ([POLICY_COOKBOOK.md](POLICY_COOKBOOK.md) recipe 11).
- **No scheduled activations.** An activation takes effect when you make it. Rules can use the time of day (`time.hour`, `time.weekday`, recipe 8).
- **`notify` and approvals are decided, not yet delivered.** A decision says who to notify and who may approve. Email delivery and approvals arrive in S22, and Slack in S29.
- **Shadow results become visible from S27,** when Insights shows where shadow and enforced decisions disagree.
- **Tool names are provisional.** The GitHub, Slack, Stripe and Gmail names in the starters follow the v1 connector plans; the real connector tool names arrive in S29 and S30.
- **No editor yet.** The Console's policy editor, dry-run panel and version diff arrive in S26. Until then, use files, the CLI and the API.
