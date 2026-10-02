# Policy cookbook

**Status:** S12. Every policy and case on this page is checked in CI by `docs/cookbook_test.go`: each policy compiles with zero warnings under `--voice`, and each case passes.
**Audience:** anyone writing a Deedglass policy who wants a starting point for a common need.

Eighteen recipes. Each one has the problem in one sentence, a complete policy you can lint and test as it stands, one test case, and a note. The language itself is in [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md); the workflow (lint, test, push, shadow, enforce) is in [POLICIES_GUIDE.md](POLICIES_GUIDE.md).

**How to try a recipe.** Save the policy as `recipe.yaml` and the case as `recipe.cases.yaml` in the same folder, then run:

```
deedglass policy test recipe.yaml --voice
```

**How to use a recipe.** An org enforces one policy at a time, so a recipe is rarely a policy on its own. Copy its rules into your policy, keep every rule id unique, and put deny rules above the allow rules they protect, because the first rule that matches decides ([POLICIES_GUIDE.md](POLICIES_GUIDE.md) section 9).

**Tool names.** The GitHub, Slack, Stripe and Gmail tool names follow the v1 connector plans, like the [starter policies](../examples/policies/README.md). The real connectors arrive in S29 and S30, and these names may change then.

Recipes:

1. Block destructive SQL
2. Require a WHERE clause
3. Allow reads, approve writes
4. Allow-list tables for writes
5. Cap refund size, and approve above the cap
6. A daily refund budget across all agents
7. Rate-limit a busy agent
8. Writes only in business hours
9. Four-eyes for merges
10. Only signed intents may move money
11. Different rules for prod and staging agents
12. Customer-facing actions need an on-behalf-of user
13. Slack channel allow-list
14. No `@channel`
15. Drafts, never send
16. Alert on every blocked destructive attempt
17. Tighten rules while an anomaly is active
18. Require two people to undo writes to billing tables

## 1. Block destructive SQL

**Problem.** One `DROP TABLE` from an agent can take a production table with it.

```yaml
deedglass: policy/v1
name: no-destructive-sql
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: block-destructive-sql
    match: { connector_type: postgres }
    when: sql.kind in ["drop", "truncate"]
    decision: deny
    reason: DROP and TRUNCATE are never allowed for agents. Ship schema changes as a reviewed migration.
  - id: allow-reads
    match: { tool: postgres.query }
    when: sql.category == "read" && !sql.writes
    decision: allow
```

```yaml
cases:
  - name: a comment does not hide DROP
    input: { tool: postgres.execute, sql: "/* tidy up */ DrOp TABLE public.orders" }
    expect: { decision: deny, rule: block-destructive-sql, reason_contains: DROP }
```

**Note.** The `sql` facts come from Deedglass's SQL classifier, which reads past comments and mixed case ([SQLGUARD.md](SQLGUARD.md)). The postgres-safe starter has this rule, plus rules for several statements at once and for SQL the classifier can't read with confidence.

## 2. Require a WHERE clause

**Problem.** An `UPDATE` or `DELETE` without a `WHERE` clause changes every row in the table.

```yaml
deedglass: policy/v1
name: bounded-writes
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: block-unbounded-writes
    match: { connector_type: postgres }
    when: sql.unbounded_write
    decision: deny
    reason: UPDATE and DELETE need a WHERE clause that names the rows to change. Add one and send the statement again.
  - id: writes-need-approval
    match: { tool: postgres.execute }
    when: sql.category == "write" && !sql.multi && !sql.ambiguous
    decision: require_approval
    reason: Writes to this database wait for a human. An admin can approve in Console › Approvals.
```

```yaml
cases:
  - name: WHERE 1=1 counts as no WHERE
    input: { tool: postgres.execute, sql: "UPDATE public.orders SET status = 'void' WHERE 1=1" }
    expect: { decision: deny, rule: block-unbounded-writes, reason_contains: WHERE clause }
  - name: a DELETE that names its rows waits for a human
    input: { tool: postgres.execute, sql: "DELETE FROM public.orders WHERE id = 3" }
    expect: { decision: require_approval, rule: writes-need-approval }
```

**Note.** `sql.unbounded_write` is true for a missing `WHERE`, a trivial one like `WHERE 1=1` or `WHERE true`, and an unbounded write inside a CTE. The approval rule leaves out `approval:`, so it uses the defaults: one admin, within 30 minutes.

## 3. Allow reads, approve writes

**Problem.** You want your agent to read freely, and a person to look at every write first.

```yaml
deedglass: policy/v1
name: reads-free-writes-approved
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: allow-reads
    match: { tool: postgres.query }
    when: >-
      sql.category == "read" && !sql.writes && !sql.locks_rows
      && size(sql.unknown_functions) == 0
    decision: allow
  - id: writes-need-approval
    match: { tool: postgres.execute }
    when: sql.category == "write" && !sql.multi && !sql.unbounded_write && !sql.ambiguous
    decision: require_approval
    reason: Writes to this database wait for a human. An admin can approve in Console › Approvals.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
    notify: [email, slack]
    verify: L2
```

```yaml
cases:
  - name: reads run freely
    input: { tool: postgres.query, sql: "SELECT id, status FROM public.orders WHERE id = 42" }
    expect: { decision: allow, rule: allow-reads }
  - name: writes to orders wait for a human
    input: { tool: postgres.execute, sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42" }
    expect: { decision: require_approval, rule: writes-need-approval, verify: L2, approval: { required: 1 } }
```

**Note.** `allow-reads` refuses a "read" that calls a function the classifier doesn't know, because an unknown function might write. Everything that is neither a clean read nor a single bounded write falls to the default and is refused.

## 4. Allow-list tables for writes

**Problem.** Your agent should write to a few low-risk tables on its own, and nowhere else without a person.

```yaml
deedglass: policy/v1
name: allow-listed-writes
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: allow-listed-writes
    description: Writes to these tables run without approval. Edit the list for your schema.
    match: { tool: postgres.execute }
    when: >-
      sql.kind in ["insert", "update"] && !sql.multi && !sql.cte_write && !sql.unbounded_write
      && !sql.ambiguous && sql.tables_complete && size(sql.unknown_functions) == 0
      && sql.tables.all(t, t in ["public.support_notes", "public.order_tags"])
    decision: allow
    verify: L2
  - id: other-writes-need-approval
    match: { tool: postgres.execute }
    when: sql.category == "write" && !sql.multi && !sql.unbounded_write && !sql.ambiguous
    decision: require_approval
    reason: Writes outside the allow-listed tables wait for a human. An admin can approve in Console › Approvals.
```

```yaml
cases:
  - name: an insert into an allow-listed table runs
    input: { tool: postgres.execute, sql: "INSERT INTO public.support_notes (order_id, note) VALUES (42, 'Called the customer')" }
    expect: { decision: allow, rule: allow-listed-writes, verify: L2 }
  - name: an update to orders waits for a human
    input: { tool: postgres.execute, sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42" }
    expect: { decision: require_approval, rule: other-writes-need-approval }
```

**Note.** `sql.tables` holds every table the statement touches, at every depth, so a write that also reads another table is not allow-listed. `sql.tables_complete` is false when the classifier met something it can't follow; requiring it means a doubtful statement goes to a person instead.

## 5. Cap refund size, and approve above the cap

**Problem.** Small refunds should run on their own, and large ones should wait for a person.

```yaml
deedglass: policy/v1
name: refund-cap
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: refunds-need-an-amount
    description: A refund without an amount refunds the whole charge, so the amount is required.
    match: { tool: stripe.create_refund }
    when: '!has(args.amount) || type(args.amount) != int || args.amount <= 0'
    decision: deny
    reason: Refunds need an explicit amount in minor units, like 2000 for $20.00.
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
    verify: L1
  - id: large-refunds-need-approval
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human. An admin can approve in Console › Approvals.
    approval: { approvers: [role:admin], required: 1, expires_in: 2h }
    notify: [slack]
    verify: L1
```

```yaml
cases:
  - name: a $20.00 refund runs
    input: { tool: stripe.create_refund, args: { charge: ch_3PqR8s2eZvKYlo2C1a2b3c4d, amount: 2000 } }
    expect: { decision: allow, rule: small-refunds, verify: L1 }
  - name: $50.01 waits for a human
    input: { tool: stripe.create_refund, args: { charge: ch_3PqR8s2eZvKYlo2C1a2b3c4d, amount: 5001 } }
    expect: { decision: require_approval, rule: large-refunds-need-approval, approval: { expires_in: 2h } }
```

**Note.** Amounts are in minor units, so `minor("50.00 usd")` is `5000`. The first rule refuses a missing, fractional or string amount before the cap is read; this is the stripe-refunds starter's logic.

## 6. A daily refund budget across all agents

**Problem.** Ten agents that each refund a little can still refund too much in one day.

```yaml
deedglass: policy/v1
name: refund-budget
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
budgets:
  - id: refunds-per-day
    description: At most $500.00 of refunds a day, across every agent in the org.
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"
    measure: 'has(args.amount) && type(args.amount) == int ? args.amount : 0'
rules:
  - id: refunds-need-an-amount
    match: { tool: stripe.create_refund }
    when: '!has(args.amount) || type(args.amount) != int || args.amount <= 0'
    decision: deny
    reason: Refunds need an explicit amount in minor units, like 2000 for $20.00.
  - id: refunds
    match: { tool: stripe.create_refund }
    decision: allow
    verify: L1
```

```yaml
cases:
  - name: a refund that would pass the daily cap is refused
    input:
      tool: stripe.create_refund
      args: { charge: ch_3PqR8s2eZvKYlo2C1a2b3c4d, amount: 2000 }
      org: { timezone: Asia/Kolkata }
      usage: { refunds-per-day: 49000 }
    expect: { decision: deny, code: budget_exceeded, rule: "budget:refunds-per-day", reason_contains: "$510.00 of $500.00" }
```

**Note.** `per: org` keeps one counter for the whole org, and the day starts at midnight in the org's time zone. The `measure` is in single quotes because it contains ` : `, which plain YAML reads as a new key. Because the amount comes from `measure`, the check is exact across instances ([BUDGETS.md](BUDGETS.md) section 7).

## 7. Rate-limit a busy agent

**Problem.** One agent in a loop can make thousands of calls before anyone notices.

```yaml
deedglass: policy/v1
name: report-bot-rate
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
budgets:
  - id: report-bot-rate
    description: The report agent makes at most 10 calls a minute.
    match: { agent: report-bot }
    window: minute
    max_calls: 10
rules:
  - id: allow-reads
    match: { tool: postgres.query }
    when: sql.category == "read" && !sql.writes
    decision: allow
```

```yaml
cases:
  - name: the 11th call in a minute is refused
    input:
      tool: postgres.query
      sql: "SELECT count(*) FROM public.orders WHERE status = 'open'"
      agent: { name: report-bot }
      usage: { report-bot-rate: 10 }
    expect: { decision: deny, code: budget_exceeded, rule: "budget:report-bot-rate", reason_contains: "10 of 10 calls this minute" }
```

**Note.** A budget's `match` takes the same fields as a rule's, so `agent:` limits it to one agent (by name glob or `agt_` id). You can also set `calls_per_minute` on the agent itself in Console › Agents; both apply, and the stricter one decides ([BUDGETS.md](BUDGETS.md) section 1).

## 8. Writes only in business hours

**Problem.** Writes at 3 a.m. happen when nobody is watching, so you want them only during the working day.

```yaml
deedglass: policy/v1
name: business-hours-writes
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: writes-only-in-business-hours
    match: { tool: postgres.execute }
    when: time.weekday in ["sat", "sun"] || time.hour < 9 || time.hour >= 18
    decision: deny
    reason: Writes run only from 09:00 to 18:00, Monday to Friday, in the org's time zone. Try again during those hours.
  - id: business-hours-writes
    match: { tool: postgres.execute }
    when: sql.category == "write" && !sql.multi && !sql.unbounded_write && !sql.ambiguous
    decision: allow
    verify: L2
```

```yaml
cases:
  - name: a write on Monday evening is refused
    input:
      tool: postgres.execute
      sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
      org: { timezone: Asia/Kolkata }
      time: 2026-10-05T20:30:00+05:30
    expect: { decision: deny, rule: writes-only-in-business-hours }
  - name: the same write on Monday morning runs
    input:
      tool: postgres.execute
      sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
      org: { timezone: Asia/Kolkata }
      time: 2026-10-05T11:00:00+05:30
    expect: { decision: allow, rule: business-hours-writes }
```

**Note.** `time.hour` and `time.weekday` are in the org's time zone (set in Console › Settings), not the server's. The time is an input recorded with each action, so a replay decides the same way.

## 9. Four-eyes for merges

**Problem.** A merge into your main branch should need two people, not one.

```yaml
deedglass: policy/v1
name: four-eyes-merges
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: merges-need-two-approvals
    match: { tool: github.merge_pull_request }
    decision: require_approval
    reason: Merging needs two people. Two admins can approve in Console › Approvals.
    approval: { approvers: [role:admin], required: 2 }
    notify: [slack]
    verify: L1
```

```yaml
cases:
  - name: a merge waits for two admins
    input: { tool: github.merge_pull_request, args: { owner: acme, repo: web, pull_number: 77 } }
    expect: { decision: require_approval, rule: merges-need-two-approvals, approval: { required: 2, approvers: [role:admin] } }
```

**Note.** `required` is 1 to 5. A role counts as any number of people; with only `user:` approvers, `required` can't be more than the number of people listed. Without `expires_in` the request uses the default of 30 minutes; add `expires_in: 24h` to give reviewers a day. The github-read-mostly starter denies merges outright instead.

## 10. Only signed intents may move money

**Problem.** An API key alone shouldn't be enough to move money; the agent's runtime should sign each call.

```yaml
deedglass: policy/v1
name: signed-money
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: money-needs-a-signed-intent
    match: { tool: [stripe.create_refund, stripe.create_payout, stripe.create_transfer] }
    when: '!request.intent_signed'
    decision: deny
    reason: Actions that move money need a signed intent. Sign each call from the agent's runtime, and set its intent mode to required.
  - id: small-signed-refunds
    match: { tool: stripe.create_refund }
    when: has(args.amount) && type(args.amount) == int && args.amount > 0 && args.amount <= minor("50.00 usd")
    decision: allow
    verify: L1
```

```yaml
cases:
  - name: an unsigned refund is refused
    input: { tool: stripe.create_refund, args: { charge: ch_3PqR8s2eZvKYlo2C1a2b3c4d, amount: 2000 } }
    expect: { decision: deny, rule: money-needs-a-signed-intent }
  - name: a signed refund runs
    input:
      tool: stripe.create_refund
      args: { charge: ch_3PqR8s2eZvKYlo2C1a2b3c4d, amount: 2000 }
      request: { mode: gateway, intent_signed: true }
    expect: { decision: allow, rule: small-signed-refunds }
```

**Note.** `request.intent_signed` is true only when the Gate has checked the call's signed intent ([INTENTS.md](INTENTS.md)). The `when` is quoted because YAML reads a leading `!` as a tag.

## 11. Different rules for prod and staging agents

**Problem.** A staging agent can write freely, and a prod agent with the same code should not.

```yaml
deedglass: policy/v1
name: prod-and-staging
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: staging-reads-and-writes
    match: { connector_type: postgres, labels: { env: staging } }
    when: sql.category in ["read", "write"] && !sql.multi && !sql.unbounded_write && !sql.ambiguous
    decision: allow
  - id: prod-reads
    match: { tool: postgres.query, labels: { env: prod } }
    when: sql.category == "read" && !sql.writes
    decision: allow
  - id: prod-writes-need-approval
    match: { tool: postgres.execute, labels: { env: prod } }
    when: sql.category == "write" && !sql.multi && !sql.unbounded_write && !sql.ambiguous
    decision: require_approval
    reason: Writes from prod agents wait for a human. An admin can approve in Console › Approvals.
    verify: L2
```

```yaml
cases:
  - name: a staging agent writes on its own
    input:
      tool: postgres.execute
      sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
      agent: { name: sync-bot, labels: { env: staging } }
    expect: { decision: allow, rule: staging-reads-and-writes }
  - name: a prod agent waits for a human
    input:
      tool: postgres.execute
      sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42"
      agent: { name: sync-bot, labels: { env: prod } }
    expect: { decision: require_approval, rule: prod-writes-need-approval }
```

**Note.** Labels are set on the agent in Console › Agents, and every label change is sealed as an admin deed, so a relabel that moves an agent between rules is on the record. An agent with neither label falls to the default.

## 12. Customer-facing actions need an on-behalf-of user

**Problem.** When your agent writes to a customer, you want to know which customer it was acting for.

```yaml
deedglass: policy/v1
name: on-behalf-of
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: drafts-for-a-customer
    match: { tool: [gmail.create_draft, gmail.update_draft] }
    when: user.ref != ""
    decision: allow
    verify: L1
  - id: drafts-need-a-customer
    match: { tool: [gmail.create_draft, gmail.update_draft] }
    decision: deny
    reason: Drafts to customers must name the customer they serve. Send the Deedglass-On-Behalf-Of header with the customer's id.
```

```yaml
cases:
  - name: a draft for a named customer runs
    input:
      tool: gmail.create_draft
      args: { to: [customer@example.com], subject: Your refund }
      user: { ref: cust_8812 }
    expect: { decision: allow, rule: drafts-for-a-customer }
  - name: a draft with no customer is refused
    input: { tool: gmail.create_draft, args: { to: [customer@example.com], subject: Your refund } }
    expect: { decision: deny, rule: drafts-need-a-customer }
```

**Note.** `user.ref` comes from the `Deedglass-On-Behalf-Of` header or from a signed intent ([IDENTITY.md](IDENTITY.md)), and it is an empty string when neither is present. The deed records it, so the Ledger shows whom each draft was for.

## 13. Slack channel allow-list

**Problem.** Your agent should post on its own in its status channel, and ask before posting anywhere else.

```yaml
deedglass: policy/v1
name: slack-channels
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: allow-listed-channels
    match: { tool: [slack.post_message, slack.update_message, slack.schedule_message] }
    when: args.channel in ["#agent-updates", "C0AGENTUPD"]
    decision: allow
    verify: L1
  - id: other-channels-need-approval
    match: { tool: [slack.post_message, slack.update_message, slack.schedule_message] }
    decision: require_approval
    reason: Posts outside the allow-listed channels wait for a human. An admin can approve in Console › Approvals, or add the channel in Console › Policies.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
    notify: [slack]
    verify: L1
```

```yaml
cases:
  - name: "a post to #agent-updates runs"
    input: { tool: slack.post_message, args: { channel: "#agent-updates", text: Nightly sync finished. } }
    expect: { decision: allow, rule: allow-listed-channels }
  - name: "a post to #general waits for a human"
    input: { tool: slack.post_message, args: { channel: "#general", text: The weekly report is ready. } }
    expect: { decision: require_approval, rule: other-channels-need-approval }
```

**Note.** List each channel by its name and by its id, because an agent may send either. A post with no `channel` makes the `when` fail, and a failing `when` never lets an allow rule match, so it goes to approval.

## 14. No `@channel`

**Problem.** An agent that mentions a whole channel interrupts everyone in it.

```yaml
deedglass: policy/v1
name: no-channel-mentions
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: no-broadcast-mentions
    match: { tool: [slack.post_message, slack.update_message, slack.schedule_message] }
    when: >-
      has(args.text) && (args.text.contains("<!channel>") || args.text.contains("<!here>")
      || args.text.contains("<!everyone>"))
    decision: deny
    reason: Agents never notify a whole channel. Remove the channel mention and send the message again.
  - id: allow-listed-channels
    match: { tool: [slack.post_message, slack.update_message, slack.schedule_message] }
    when: args.channel in ["#agent-updates", "C0AGENTUPD"]
    decision: allow
    verify: L1
```

```yaml
cases:
  - name: "<!channel> is refused, even in #agent-updates"
    input: { tool: slack.post_message, args: { channel: "#agent-updates", text: "<!channel> the sync is down." } }
    expect: { decision: deny, rule: no-broadcast-mentions }
```

**Note.** The rule checks the message text for `<!channel>`, `<!here>` and `<!everyone>`, the same forms the slack-post-with-approval starter checks. That starter sends these messages to a person for approval instead of refusing them; pick the one that fits your team.

## 15. Drafts, never send

**Problem.** Your agent writes good replies, and you still want a person to press send.

```yaml
deedglass: policy/v1
name: drafts-never-send
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: block-sending
    match: { tool: ["gmail.*send*", "gmail.*forward*", "gmail.*reply*"] }
    decision: deny
    reason: Agents write drafts here. A person reviews and sends every email.
  - id: drafts
    match: { tool: [gmail.create_draft, gmail.update_draft] }
    decision: allow
    verify: L1
  - id: reads
    match: { tool: ["gmail.get_*", "gmail.list_*", "gmail.search_*"] }
    decision: allow
```

```yaml
cases:
  - name: sending a draft never runs
    input: { tool: gmail.send_draft, args: { draft_id: r-4482910 } }
    expect: { decision: deny, rule: block-sending }
  - name: writing a draft runs
    input: { tool: gmail.create_draft, args: { to: [customer@example.com], subject: Your refund } }
    expect: { decision: allow, rule: drafts, verify: L1 }
```

**Note.** The globs catch any Gmail tool with `send`, `forward` or `reply` in its name. Matching is case-sensitive, so a tool named in another case falls to the default, which also refuses it. This is the gmail-drafts-only starter without its rule for destructive actions.

## 16. Alert on every blocked destructive attempt

**Problem.** A refused `DROP` is good news, and you still want to hear about it, because the agent tried.

```yaml
deedglass: policy/v1
name: alert-on-destructive
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: block-destructive-sql
    match: { connector_type: postgres }
    when: sql.kind in ["drop", "truncate"] || sql.unbounded_write
    decision: deny
    reason: Destructive SQL never runs through agents. Ship it as a reviewed migration, or name the rows to change.
    notify: [email, slack]
  - id: block-destructive-tools
    match: { side_effect: destructive }
    decision: deny
    reason: Destructive actions stay with people. Make the change yourself, or ask an admin to add a rule in Console › Policies.
    notify: [email, slack]
```

```yaml
cases:
  - name: a DROP is refused and raises an alert
    input: { tool: postgres.execute, sql: "DROP TABLE public.orders" }
    expect: { decision: deny, rule: block-destructive-sql, notify: [email, slack] }
  - name: deleting a customer is refused and raises an alert
    input: { tool: { name: stripe.delete_customer, side_effect: destructive }, args: { customer: cus_QhR8s2eZvKYlo2 } }
    expect: { decision: deny, rule: block-destructive-tools, notify: [email, slack] }
```

**Note.** On a deny, `notify` asks for an alert; on `require_approval` it carries the approval request. Today the decision records who should be told, and nothing is sent yet: email delivery arrives in S22 and Slack in S29. `side_effect` is what the tool declares, so the second rule only catches tools that declare themselves destructive.

## 17. Tighten rules while an anomaly is active

**Problem.** When an agent starts behaving unusually, you want its writes to wait for a person until someone has looked.

```yaml
deedglass: policy/v1
name: anomaly-tightening
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: anomaly-holds-writes
    match: { tool: postgres.execute }
    when: anomaly.active
    decision: require_approval
    reason: Deedglass sees unusual activity, so writes wait for a human. An admin can approve in Console › Approvals.
    notify: [slack]
    verify: L2
  - id: allow-reads
    match: { tool: postgres.query }
    when: sql.category == "read" && !sql.writes
    decision: allow
  - id: allow-listed-writes
    match: { tool: postgres.execute }
    when: >-
      sql.kind == "insert" && !sql.multi && !sql.ambiguous && sql.tables_complete
      && sql.tables.all(t, t in ["public.support_notes"])
    decision: allow
    verify: L2
```

```yaml
cases:
  - name: an allow-listed write waits for a human during an anomaly
    input:
      tool: postgres.execute
      sql: "INSERT INTO public.support_notes (order_id, note) VALUES (42, 'Called the customer')"
      anomaly: { active: true }
    expect: { decision: require_approval, rule: anomaly-holds-writes, verify: L2 }
```

**Note.** `anomaly.active` is live from S27, when Insights starts detecting anomalies. **Until then, `anomaly.active` is always false**, so this rule never decides an action on a server; the case sets it by hand. Put the rule above the allow rules it should tighten, because the first rule that matches decides.

## 18. Require two people to undo writes to billing tables

**Problem.** Putting rows back in your billing database changes money records again, so it should need two people, not one.

```yaml
deedglass: policy/v1
name: billing-undo-four-eyes
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: billing-undo-needs-two
    match: { agent: console, tool: billing.undo, connector_type: postgres }
    decision: require_approval
    reason: Undoing a write to the billing tables changes money records again, so two people countersign it first.
    approval: { approvers: [role:admin], required: 2 }
    verify: L2
  - id: undo-needs-approval
    match: { agent: console, tool: "*.undo", connector_type: postgres }
    decision: require_approval
    reason: Undoing a write changes data again, so a human confirms it first.
    approval: { approvers: [role:admin], required: 1 }
    verify: L2
```

```yaml
cases:
  - name: an undo on the billing database waits for two admins
    input:
      tool: { name: billing.undo, connector_type: postgres }
      agent: { name: console }
      args: { undoes: act_01jb8m2x7k4q9r3t5v6w8y0z1a, plan_digest: "sha256:c0a4", allow_triggers: false, reason: A sync job overwrote 12 invoices. }
    expect: { decision: require_approval, rule: billing-undo-needs-two, approval: { required: 2, approvers: [role:admin] }, verify: L2 }
  - name: an undo on another Postgres connector waits for one admin
    input: { tool: { name: postgres.undo, connector_type: postgres }, agent: { name: console }, args: { undoes: act_01jb8m2x7k4q9r3t5v6w8y0z1a } }
    expect: { decision: require_approval, rule: undo-needs-approval, approval: { required: 1 } }
```

**Note.** An undo ([UNDO.md](UNDO.md)) is an action of your org's `console` system agent on behalf of the person who asked for it, with the tool `<connector>.undo`. Its arguments name the action it undoes and the plan digest, not the tables, so scope by connector: give the billing database its own Postgres connector (here named `billing`), and its undos are `billing.undo`. The requester never counts toward the two: S22's self rule asks for two *other* admins or owners, so the org needs at least three. Keep the general `*.undo` rule below the billing one, because the first rule that matches decides; without any undo rule, the default denies every undo.

## See also

- [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md): every field, the CEL variables and the evaluation order.
- [POLICIES_GUIDE.md](POLICIES_GUIDE.md): lint, test, push, shadow, enforce and roll back.
- [POLICY_ERRORS.md](POLICY_ERRORS.md): every diagnostic code, with a fix.
- [The starter policies](../examples/policies/README.md): six complete policies with their cases.
