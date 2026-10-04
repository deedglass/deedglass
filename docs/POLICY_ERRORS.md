# Policy diagnostics

A diagnostic is one finding the policy compiler reports about a policy file: an `error` means the policy is not accepted, and a `warning` means it is accepted but you should look at that line. Every diagnostic has a stable `code`, like `unknown_key`; codes are API, so new ones are added and existing ones are never renamed. In JSON a diagnostic has the fields `severity`, `code`, `message`, `hint` (the next step, when there is one), `file`, `line` and `column` (1-based; line 0 means the whole file), `end_line` and `end_column` (exclusive), `rule_id`, `path` (the YAML path, like `rules[0].when`) and `doc_url`, which points to the section of this page for that code. Run `deedglass policy lint policy.yaml` to check a file in your terminal, add `--json` for the result as JSON, or `--format github` to annotate a pull request in GitHub Actions.

A diagnostic in JSON, from `deedglass policy lint --json policy.yaml`:

```json
{
  "severity": "error",
  "code": "unknown_key",
  "message": "Unknown key \"decison\" in rule \"small-refunds\".",
  "hint": "Did you mean \"decision\"?",
  "file": "policy.yaml",
  "line": 10,
  "column": 5,
  "end_line": 10,
  "end_column": 12,
  "rule_id": "small-refunds",
  "path": "rules[0].decison",
  "doc_url": "https://docs.deedglass.com/policies/errors/unknown_key"
}
```

The same diagnostic in the terminal. `✕` marks an error and `○` a warning; code spans lose their backticks.

```text
✕ policy.yaml:10:5 · unknown_key
  Unknown key "decison" in rule "small-refunds".

    9 │     when: args.amount <= minor("50.00 usd")
   10 │     decison: allow
      │     ^^^^^^^
  hint: Did you mean "decision"?
```

And with `--format github`, one workflow command per diagnostic:

```text
::error file=policy.yaml,line=10,col=5::unknown_key: Unknown key "decison" in rule "small-refunds". Did you mean "decision"?
```

Each section below gives the severity, what the code means, a small policy that produces it, what the compiler prints and the fix. To keep them short, the printed output shows the first line, the message and the hint, and leaves out the source excerpt with carets that the terminal prints between the message and the hint. The language itself is described in [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md).

## file_too_large

**Severity:** error

The file is larger than 256 KiB. Deedglass checks the size before it reads any YAML, so a very large file can't slow down the compiler or the Console. A policy is rarely more than a few kilobytes; a file this size usually holds rules that differ only in a tool name.

```text
✕ policy.yaml · file_too_large
  This policy is 298 KiB; the limit is 256 KiB.
  hint: Split it into smaller policies, or replace repeated rules with globs.
```

**Fix:** replace repeated rules with one rule and a glob or a list, like `match: { tool: "stripe.*" }`, and remove large comment blocks.

## yaml_syntax

**Severity:** error

The file is not valid YAML, or it is not valid UTF-8. The most common cause is a `when` with ` : ` in it, which YAML reads as a new key.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: has(args.amount) ? args.amount <= 5000 : false
    decision: allow
```

```text
✕ policy.yaml:9:50 · yaml_syntax
  The YAML could not be read: mapping values are not allowed.
  hint: Quote the expression or write it as a block (when: >-), because YAML reads a colon followed by a space as a new key.
```

**Fix:** quote the expression.

```yaml
    when: 'has(args.amount) ? args.amount <= 5000 : false'
```

## yaml_tab_indent

**Severity:** error

A line is indented with a tab. YAML does not allow tabs for indentation, and a tab can look like any number of spaces in an editor, so Deedglass reports every such line. In the snippet below, line 5 starts with a tab.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
	reason: No rule allows this action.
```

```text
✕ policy.yaml:5:1 · yaml_tab_indent
  Line 5 is indented with a tab.
  hint: Use spaces; YAML does not allow tabs for indentation.
```

**Fix:** indent with spaces.

```yaml
defaults:
  decision: deny
  reason: No rule allows this action.
```

## yaml_alias

**Severity:** error

The file uses a YAML anchor (`&name`) or alias (`*name`). An alias makes one rule depend on text somewhere else in the file, so a reviewer can't read a rule on its own, and aliases can expand a small file into a very large one.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: &refund { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: large-refunds
    match: *refund
    decision: require_approval
    reason: Refunds over $50.00 wait for a person.
```

```text
✕ policy.yaml:8:12 · yaml_alias
  Anchors and aliases (& and *) are not allowed in policies.
  hint: Repeat the value, so each rule reads on its own.
```

**Fix:** write the value out in each place.

```yaml
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a person.
```

## yaml_merge_key

**Severity:** error

The file uses a YAML merge key (`<<`). Like an alias, a merge key hides part of a rule somewhere else, so Deedglass refuses it.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: large-refunds
    <<: { decision: require_approval }
    match: { tool: stripe.create_refund }
    reason: Refunds wait for a person.
```

```text
✕ policy.yaml:8:5 · yaml_merge_key
  Merge keys (<<) are not allowed in policies.
  hint: Repeat the keys, so each rule reads on its own.
```

**Fix:** write the keys in the rule itself.

```yaml
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds wait for a person.
```

## yaml_tag

**Severity:** error

A value starts with `!`, which YAML reads as a tag. Policies use plain YAML values only; the YAML 1.2 core tags (`!!str`, `!!int` and so on) are the only ones allowed. The usual cause is a CEL condition that starts with `!`.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: single-statements
    match: { tool: "postgres.*" }
    when: !sql.multi
    decision: allow
```

```text
✕ policy.yaml:9:11 · yaml_tag
  !sql.multi is read by YAML as a tag, not as CEL.
  hint: Quote the expression: when: '!sql.multi'
```

**Fix:** quote the expression.

```yaml
    when: '!sql.multi'
```

## yaml_multi_document

**Severity:** error

The file holds more than one YAML document, separated by `---`. One file is one policy, so what you review is exactly what is stored.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
---
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
```

```text
✕ policy.yaml:6:1 · yaml_multi_document
  This file holds more than one YAML document.
  hint: Keep one policy per file, and remove the --- line.
```

**Fix:** move each policy into its own file, and remove the `---` line.

## yaml_duplicate_key

**Severity:** error

A key appears twice in the same mapping. Most YAML readers keep only one of the two values without telling you, so the file would mean something other than what it shows.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= 5000
    when: args.amount <= 50000
    decision: allow
```

```text
✕ policy.yaml:10:5 · yaml_duplicate_key
  Key "when" appears twice in this mapping (first on line 9).
  hint: Keep one of them; YAML would silently use only one.
```

**Fix:** keep one of the two lines. To combine both conditions, join them with `&&` in a single `when`.

## yaml_too_complex

**Severity:** error

The YAML nests more than 32 levels deep, or has more than 50,000 nodes (every key, value and list entry is a node). A real policy needs a few levels and a few thousand nodes; the limits keep the compiler fast on any input.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: deep
    decision: allow
    match:
      labels: [[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]
```

```text
✕ policy.yaml:10:43 · yaml_too_complex
  This policy nests more than 32 levels deep.
  hint: Flatten the structure; a policy needs only a few levels.
```

A file with more than 50,000 nodes gets a different message, at the node where counting stopped:

```text
✕ policy.yaml:731:79 · yaml_too_complex
  This policy has more than 50,000 YAML nodes; the limit is 50,000.
  hint: Split it into smaller policies, or replace repeated rules with globs.
```

**Fix:** flatten the structure, and replace repeated rules with globs, like `match: { tool: "stripe.*" }`.

## missing_marker

**Severity:** error

The file has no `deedglass: policy/v1` key, or it is empty. The marker names the format, so Deedglass and your editor know the file is a policy and which version of the language it uses. An empty file gets the message `This file is empty.`

```yaml
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
```

```text
✕ policy.yaml:1:1 · missing_marker
  This file has no deedglass: policy/v1 marker.
  hint: Add deedglass: policy/v1 as the first line.
```

**Fix:**

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
```

## unsupported_format

**Severity:** error

The marker names a format this version of Deedglass does not read, like `policy/v2`, or something that is not a policy format at all.

```yaml
deedglass: policy/v2
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
```

```text
✕ policy.yaml:1:12 · unsupported_format
  deedglass: policy/v2 is not supported by this version of Deedglass, which reads policy/v1.
  hint: Write deedglass: policy/v1, or upgrade Deedglass.
```

**Fix:** write `deedglass: policy/v1`, or upgrade Deedglass if the policy needs a newer format.

## unknown_key

**Severity:** error

A key is not part of the language at that place. Deedglass refuses unknown keys instead of ignoring them, because a misspelled key would otherwise do nothing without telling you. The hint suggests the closest valid key, or says where a key belongs (`tool` goes inside `match:`). A top-level `version: 1` gets this code too, with the hint to use `deedglass: policy/v1`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decison: allow
```

```text
✕ policy.yaml:10:5 · unknown_key
  Unknown key "decison" in rule "small-refunds".
  hint: Did you mean "decision"?
```

**Fix:**

```yaml
    decision: allow
```

## missing_key

**Severity:** error

A required key is missing: `name` or `defaults` at the top level, `decision` in `defaults`, `id` or `decision` in a rule, or `id`, `window` or a limit (`max_calls` or `max_spend`) in a budget.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
```

```text
✕ policy.yaml:7:5 · missing_key
  Rule "small-refunds" has no decision.
  hint: Add decision: allow, deny or require_approval.
```

**Fix:**

```yaml
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
```

## wrong_type

**Severity:** error

A value has the wrong shape: text where a mapping belongs, a list where a single value belongs, a quoted number where a whole number belongs, or an empty `when`. The message names what was expected and what was found. Some `wrong_type` messages have no hint, because the message already says what to write.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: stripe.create_refund
    decision: allow
```

```text
✕ policy.yaml:8:12 · wrong_type
  match must be a mapping, like { tool: "stripe.*" }; found a string.
```

**Fix:**

```yaml
    match: { tool: stripe.create_refund }
```

## conflicting_keys

**Severity:** error

Two keys can't be used together. A budget limits either calls (`max_calls`) or spending (`max_spend`), not both, and `measure` only works with `max_spend`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    window: day
    max_calls: 100
    max_spend: "500.00 usd"
```

```text
✕ policy.yaml:11:16 · conflicting_keys
  Budget "refunds-per-day" sets both max_calls and max_spend.
  hint: Keep one; use two budgets to limit both.
```

**Fix:** use two budgets.

```yaml
budgets:
  - id: refund-calls-per-day
    match: { tool: stripe.create_refund }
    window: day
    max_calls: 100
  - id: refund-spend-per-day
    match: { tool: stripe.create_refund }
    window: day
    max_spend: "500.00 usd"
```

## invalid_name

**Severity:** error

The policy `name` is not a slug. A name is 2 to 63 characters of lowercase ASCII letters, digits and dashes, starting with a letter, so it reads the same in a URL, a log line and the Console.

```yaml
deedglass: policy/v1
name: Refunds Policy
defaults:
  decision: deny
  reason: No rule allows this action.
```

```text
✕ policy.yaml:2:7 · invalid_name
  Policy name "Refunds Policy" is not a slug.
  hint: Use lowercase letters, digits and dashes, starting with a letter, like refunds-policy.
```

**Fix:**

```yaml
name: refunds-policy
```

## invalid_id

**Severity:** error

A rule or budget `id` is not a slug. Ids follow the same rules as names, because they appear in deny messages, in deeds and in the Ledger.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small_refunds
    match: { tool: stripe.create_refund }
    decision: allow
```

```text
✕ policy.yaml:7:9 · invalid_id
  Rule id "small_refunds" is not a slug.
  hint: Use lowercase letters, digits and dashes, starting with a letter, like small-refunds.
```

**Fix:**

```yaml
  - id: small-refunds
```

## duplicate_id

**Severity:** error

Two rules, two budgets, or a rule and a budget share an id. Budgets and rules share one set of ids, so a decision always names exactly one of them.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a person.
```

```text
✕ policy.yaml:11:9 · duplicate_id
  Rule id "refunds" is used twice (first on line 7).
  hint: Give each rule its own id.
```

**Fix:** give each rule its own id, like `small-refunds` and `large-refunds`.

## too_many_rules

**Severity:** error

The policy has more than 500 rules. Rules are read top to bottom, and a person reviewing the file has to be able to follow them. A policy with 501 rules gets:

```text
✕ policy.yaml:7:3 · too_many_rules
  This policy has 501 rules; the limit is 500.
  hint: Combine rules with match globs and lists, or split the policy.
```

**Fix:** combine rules that differ only in a tool or an agent into one rule with a glob or a list, like `match: { tool: ["github.create_issue", "github.create_comment"] }`.

## too_many_budgets

**Severity:** error

The policy has more than 50 budgets. Every budget is checked for every action, so the limit keeps evaluation fast. A policy with 51 budgets gets:

```text
✕ policy.yaml:7:3 · too_many_budgets
  This policy has 51 budgets; the limit is 50.
  hint: Combine budgets with match globs, or split the policy.
```

**Fix:** combine budgets with the same window and limit into one budget whose `match` uses a glob or a list.

## too_many_values

**Severity:** error

A list has more than 32 entries. The limit applies to each `match` list (`tool`, `connector_type`, `side_effect`, `agent`), to `labels`, to `approvers` and to `notify`. A rule whose `match.agent` lists 33 agent names gets:

```text
✕ policy.yaml:9:14 · too_many_values
  agent lists 33 values; the limit is 32.
  hint: Use a glob, or split the rule.
```

**Fix:** use a glob, like `agent: ["support-*"]`, or give the agents a label and match on it, like `labels: { team: support }`.

## value_too_long

**Severity:** error

A `description` is longer than 1,000 characters. A policy whose description is 1,011 characters gets:

```text
✕ policy.yaml:3:14 · value_too_long
  The description is 1,011 characters; the limit is 1,000.
  hint: Shorten it.
```

**Fix:** keep the description to a few sentences, and put longer explanations in comments (`#`), which `fmt` keeps and the hash ignores.

## invalid_decision

**Severity:** error

A `decision` is not `allow`, `deny` or `require_approval`. The hint maps common words (`block`, `reject`, `approve`, `permit` and others) to the decision you probably meant.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: block-drop
    match: { tool: "postgres.*" }
    when: sql.kind == "drop"
    decision: block
    reason: DROP is never allowed for agents.
```

```text
✕ policy.yaml:10:15 · invalid_decision
  block is not a decision.
  hint: Did you mean deny? Decisions are allow, deny and require_approval.
```

**Fix:**

```yaml
    decision: deny
```

## invalid_value

**Severity:** error

A field that takes one of a fixed set of values has something else: `verify` (`L0`, `L1`, `L2`), a budget's `per` (`agent`, `org`) or `window` (`minute`, `hour`, `day`), or a `notify` channel (`email`, `slack`, `webhook`).

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-week
    match: { tool: stripe.create_refund }
    window: week
    max_calls: 100
```

```text
✕ policy.yaml:9:13 · invalid_value
  week is not a budget window.
  hint: Use minute, hour or day.
```

**Fix:** use a supported window. For a weekly limit, set a daily one.

```yaml
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    window: day
    max_calls: 15
```

## out_of_range

**Severity:** error

A whole number is outside its range: `max_calls` is 1 to 1,000,000,000, and an approval's `required` is 1 to 5.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: agent-rate
    window: minute
    max_calls: 0
```

```text
✕ policy.yaml:9:16 · out_of_range
  max_calls: 0 is out of range.
  hint: Use a number from 1 to 1,000,000,000.
```

**Fix:** to stop every call, write a rule with `decision: deny` instead of a budget of zero; otherwise set a limit of at least 1.

```yaml
    max_calls: 60
```

## reason_missing

**Severity:** error

A rule (or `defaults`) denies or requires approval but has no `reason`. The reason is the one sentence the agent, and the person who approves, will see; without it a denial can't be explained.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: block-drop
    match: { tool: "postgres.*" }
    when: sql.kind == "drop"
    decision: deny
```

```text
✕ policy.yaml:10:15 · reason_missing
  Rule "block-drop" denies but has no reason.
  hint: Write the one sentence the agent will see, like "DROP is never allowed for agents."
```

**Fix:**

```yaml
    decision: deny
    reason: DROP is never allowed for agents.
```

## control_character

**Severity:** error

A string contains a control character, a bidirectional control character or an invisible formatting character (like U+200B, a zero-width space). These can make text display differently from what it says, so a reviewer could approve something other than what runs. Tabs are allowed in descriptions only.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: "No rule allows​ this action."
```

```text
✕ policy.yaml:5:11 · control_character
  The reason contains an invisible character (U+200B).
  hint: Remove it. It can make text display differently from what it says.
```

**Fix:**

```yaml
  reason: No rule allows this action.
```

## invalid_glob

**Severity:** error

A pattern in `match` (or in a `glob()` call) is not a valid glob. Globs use `*` and `?` only, with letters, digits and `_ . : -`; they are at most 200 characters and never empty. An `agent:` entry that starts with `agt_` must be a whole agent id.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: "stripe/create_refund" }
    when: args.amount <= minor("50.00 usd")
    decision: allow
```

```text
✕ policy.yaml:8:20 · invalid_glob
  stripe/create_refund is not a valid pattern.
  hint: Patterns use * and ? only, with letters, digits and _ . : -.
```

**Fix:** tool names are `<connector>.<tool>`.

```yaml
    match: { tool: stripe.create_refund }
```

## invalid_side_effect

**Severity:** error

A `side_effect` in `match` is not `read`, `write`, `destructive` or `unknown`. These are the values tools declare, so no globs are allowed here.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: reads
    match: { tool: "postgres.*", side_effect: reads }
    decision: allow
```

```text
✕ policy.yaml:8:47 · invalid_side_effect
  reads is not a side effect.
  hint: Did you mean read? Use read, write, destructive or unknown.
```

**Fix:**

```yaml
    match: { tool: "postgres.*", side_effect: read }
```

## invalid_label

**Severity:** error

A label in `match.labels` is not valid. Keys use lowercase letters, digits and `. _ / -` (up to 63 characters, starting with a letter or digit), and values are at most 128 printable characters, the same rules as agent labels.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: prod-refunds
    match: { tool: stripe.create_refund, labels: { Env: prod } }
    decision: require_approval
    reason: Refunds in production wait for a person.
```

```text
✕ policy.yaml:8:52 · invalid_label
  Env is not a label key.
  hint: Label keys use lowercase letters, digits and . _ / -, up to 63 characters, like env or team.
```

**Fix:**

```yaml
    match: { tool: stripe.create_refund, labels: { env: prod } }
```

## invalid_duration

**Severity:** error

An approval's `expires_in` is not a duration, or is outside 1m to 7d. A duration is a whole number and one unit: `s`, `m`, `h` or `d`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds wait for a person.
    approval: { approvers: [role:admin], expires_in: 2 hours }
```

```text
✕ policy.yaml:11:54 · invalid_duration
  expires_in: 2 hours is not a duration.
  hint: Use a number and one unit: 30m, 2h or 1d (from 1m to 7d).
```

**Fix:**

```yaml
    approval: { approvers: [role:admin], expires_in: 2h }
```

## money_float

**Severity:** error

An amount of money is a YAML number. YAML numbers are floating point, and `0.1 + 0.2` is not `0.3` in floating point, so money is always a string with a currency.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: 500.00
```

```text
✕ policy.yaml:11:16 · money_float
  max_spend: 500.00 is a YAML number.
  hint: Write money as a string, like "50.00 usd", so no precision is lost.
```

**Fix:**

```yaml
    max_spend: "500.00 usd"
```

## invalid_money

**Severity:** error

A money string is not `"<decimal> <currency>"`, or the amount is negative, zero (for `max_spend`) or larger than 9,007,199,254,740,991 in minor units. The same check applies to `minor()` inside a condition.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "$500"
```

```text
✕ policy.yaml:11:16 · invalid_money
  $500 is not an amount of money.
  hint: Write money as a string with a currency, like "50.00 usd".
```

**Fix:**

```yaml
    max_spend: "500.00 usd"
```

## unknown_currency

**Severity:** error

The currency is not an active ISO 4217 code. The code is three letters, in any case; `fmt` writes it in lowercase.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usx"
```

```text
✕ policy.yaml:11:16 · unknown_currency
  usx is not an active ISO 4217 currency.
  hint: Did you mean "usd"? Use a three-letter code, like usd, eur or inr.
```

**Fix:**

```yaml
    max_spend: "500.00 usd"
```

## money_precision

**Severity:** error

The amount has more decimals than the currency's minor unit: 2 for usd and inr, 0 for jpy, 3 for kwd. Deedglass never rounds money for you, so the amount you read is the amount that is enforced.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.005 usd"
```

```text
✕ policy.yaml:11:16 · money_precision
  500.005 usd has more decimals than usd allows (2).
  hint: Round it to 2 decimals, like "500.01 usd".
```

**Fix:**

```yaml
    max_spend: "500.01 usd"
```

## invalid_approvers

**Severity:** error

An entry in `approvers` is not `role:owner`, `role:admin` or `user:<email>`. `role:viewer` is refused because viewers can't approve.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds wait for a person.
    approval: { approvers: [priya@acme.com] }
```

```text
✕ policy.yaml:11:29 · invalid_approvers
  priya@acme.com is not an approver.
  hint: Write it as user:priya@acme.com.
```

**Fix:**

```yaml
    approval: { approvers: ["user:priya@acme.com"] }
```

## approvals_unsatisfiable

**Severity:** error

An approval needs more approvals than the people it names. When every approver is a `user:`, `required` can be at most the number of distinct people, or no action could ever be approved.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds wait for two people.
    approval: { approvers: ["user:priya@acme.com"], required: 2 }
```

```text
✕ policy.yaml:11:63 · approvals_unsatisfiable
  This rule needs 2 approvals but names only 1 person.
  hint: Lower required, or add people or a role to approvers.
```

**Fix:**

```yaml
    approval: { approvers: ["user:priya@acme.com", "user:sam@acme.com"], required: 2 }
```

## cel_syntax

**Severity:** error

A `when` or `measure` is not valid CEL. The hint covers the two most common slips: `=` instead of `==`, and `and`, `or` or `not` instead of `&&`, `||` or `!`. An expression nested more than 64 levels deep gets this code too.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: exact-refund
    match: { tool: stripe.create_refund }
    when: args.amount = 5000
    decision: allow
```

```text
✕ policy.yaml:9:23 · cel_syntax
  Syntax error in when: unexpected token '='.
  hint: Compare with ==
```

**Fix:**

```yaml
    when: args.amount == 5000
```

## cel_undeclared

**Severity:** error

An expression uses a name that is not one of the policy variables: `tool`, `args`, `request`, `sql`, `agent`, `user`, `session`, `org`, `time`, `usage` and `anomaly`. There is no `now()`; use `time.now`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: arg.amount <= 5000
    decision: allow
```

```text
✕ policy.yaml:9:11 · cel_undeclared
  Unknown name "arg".
  hint: Did you mean "args"? Available: tool, args, request, sql, agent, user, session, org, time, usage and anomaly.
```

**Fix:**

```yaml
    when: args.amount <= 5000
```

## cel_no_field

**Severity:** error

An expression reads a field that its variable does not have. Every variable except `args` is declared field by field, so a typo is an error when you lint, never a condition that is quietly false at run time.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: block-drop
    match: { tool: "postgres.*" }
    when: sql.knid == "drop"
    decision: deny
    reason: DROP is never allowed for agents.
```

```text
✕ policy.yaml:9:15 · cel_no_field
  sql has no field "knid".
  hint: Did you mean "kind"?
```

**Fix:**

```yaml
    when: sql.kind == "drop"
```

## cel_type

**Severity:** error

An expression does not type-check: an operator or function is applied to values of the wrong types. The most common case is comparing a value with a quoted number using `<`, `<=`, `>` or `>=`: a quoted number is a string, and strings compare letter by letter, so `"10000" < "5000"`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= "5000"
    decision: allow
```

```text
✕ policy.yaml:9:26 · cel_type
  args.amount <= "5000" compares a number with a string.
  hint: Compare numbers with numbers: args.amount <= 5000 (minor units, so 5000 is $50.00).
```

**Fix:**

```yaml
    when: args.amount <= 5000
```

## cel_not_bool

**Severity:** error

A `when` returns something other than true or false, like a string. A condition whose type is only known at run time (`dyn`, like `args.flag`) is accepted, and a value that turns out not to be a bool counts as an evaluation error, which never widens access.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: selects
    match: { tool: "postgres.*" }
    when: sql.kind
    decision: allow
```

```text
✕ policy.yaml:9:11 · cel_not_bool
  when must be true or false; this expression returns a string.
  hint: Compare it with something, like args.status == "open"
```

**Fix:**

```yaml
    when: sql.kind == "select"
```

## cel_not_int

**Severity:** error

A budget's `measure` returns something other than a whole number. A spending budget counts minor units, so `measure` must return an `int` (or `dyn`, which is checked at run time).

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"
    measure: string(args.amount)
```

```text
✕ policy.yaml:12:14 · cel_not_int
  measure must be a whole number of minor units; this expression returns a string.
  hint: Return an int, like args.amount, or guard it: has(args.amount) ? args.amount : 0
```

**Fix:**

```yaml
    measure: args.amount
```

## cel_cost

**Severity:** error

The estimated worst-case cost of a `when` or `measure` is more than 100,000 units. Deedglass evaluates every condition inline, before the action runs, so the cost of each one is bounded when the policy compiles. CEL can't know how long a list will be, so the estimate assumes up to 1,024 entries for each list in `args`; a loop inside a loop over arguments is about a million steps.

```yaml
deedglass: policy/v1
name: orders
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: distinct-items
    match: { tool: shop.create_order }
    when: args.items.all(a, args.items.all(b, a != b))
    decision: allow
```

```text
✕ policy.yaml:9:11 · cel_cost
  This condition could cost up to 113,252,355 units per call; the limit is 100,000.
  hint: Iterate a smaller list (sql.tables has at most 64 entries), avoid a loop inside a loop over args, or split the rule.
```

**Fix:** iterate one list at a time, like `when: args.items.all(i, i.sku != "gift-card")`. The estimate uses the size hints, not the conditions around the loop, so adding `size(args.items) <= 20 &&` in front of a nested loop does not lower it.

## cel_nesting

**Severity:** error

Comprehensions (`all`, `exists`, `exists_one`, `map`, `filter`) are nested more than 2 deep. Deeply nested loops are hard to read and multiply the cost of a condition.

```yaml
deedglass: policy/v1
name: orders
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: no-gift-tags
    match: { tool: shop.create_order }
    when: args.orders.all(o, o.items.all(i, i.tags.all(t, t != "gift")))
    decision: allow
```

```text
✕ policy.yaml:9:52 · cel_nesting
  Comprehensions are nested 3 deep; the limit is 2.
  hint: Flatten the condition, or split it into two rules.
```

**Fix:** check a flatter value, like a flag the tool sets on each order: `when: args.orders.all(o, !o.gift)`.

## cel_too_long

**Severity:** error

A `when` or `measure` is longer than 4,096 characters. A long expression is usually a long list of values written inline. A `when` of 5,281 characters gets:

```text
✕ policy.yaml:9:11 · cel_too_long
  This when is 5,281 characters; the limit is 4,096.
  hint: Split the rule, or move a list into match.
```

**Fix:** move lists of tools or agents into `match`, where globs can shorten them, or split the condition across two rules.

## cel_regex

**Severity:** error

The pattern in `matches()` is not a constant string, is longer than 256 characters, or is not valid RE2. Patterns come from the policy author, never from the agent's arguments, and they are checked when the policy compiles.

```yaml
deedglass: policy/v1
name: email
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: acme-recipients
    match: { tool: gmail.send }
    when: args.to.matches(args.pattern)
    decision: allow
```

```text
✕ policy.yaml:9:27 · cel_regex
  The pattern in matches() must be a constant string.
  hint: Patterns come from the policy, never from an agent's arguments: write the pattern in quotes.
```

**Fix:**

```yaml
    when: args.to.matches("^[a-z.]+@acme[.]com$")
```

## cel_constant_required

**Severity:** error

`glob()` was given a pattern, or `minor()` an amount, that is not a constant string. Like `matches()` patterns, these come from the policy, so an agent can't choose what a rule matches.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: allowed-tools
    when: glob(tool.name, args.pattern)
    decision: allow
```

```text
✕ policy.yaml:8:27 · cel_constant_required
  glob() needs a constant pattern.
  hint: Patterns come from the policy, never from an agent's arguments: write it in quotes, like glob(tool.name, "stripe.*")
```

**Fix:**

```yaml
    when: glob(tool.name, "stripe.*")
```

## reason_too_long

**Severity:** warning

A `reason` is longer than 280 characters. The policy is accepted, but agents and approvers see at most 280 characters, so the end of the reason is cut off. A reason of 311 characters gets:

```text
○ policy.yaml:10:13 · reason_too_long
  This reason is 311 characters. Agents and approvers see at most 280.
  hint: Shorten it to one sentence; the rest is cut off.
```

**Fix:** write one sentence that says what was refused and what to do next, like `Refunds over $50.00 wait for a person. An admin can approve in Console › Approvals.`

## unknown_connector_type

**Severity:** warning

A `connector_type` in `match` is not a known connector type. The policy is accepted, because new connector types can be added later, but a misspelled type matches nothing. Known types: `postgres`, `rest`, `mcp`, `mcp_http`, `sdk`, `github`, `slack`, `stripe`, `gmail`, `notion` and `fake`.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: reads
    match: { connector_type: postgress, side_effect: read }
    decision: allow
```

```text
○ policy.yaml:8:30 · unknown_connector_type
  postgress is not a known connector type.
  hint: Did you mean postgres?
```

**Fix:**

```yaml
    match: { connector_type: postgres, side_effect: read }
```

## ignored_field

**Severity:** warning

A rule sets a field its decision does not use: `approval` on anything but `require_approval`, `notify` on `allow`, or `verify` on `deny`. The policy is accepted and the field has no effect, which is usually not what the author meant.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund }
    when: args.amount <= minor("50.00 usd")
    decision: allow
    notify: [slack]
```

```text
○ policy.yaml:11:5 · ignored_field
  notify is ignored because the decision is allow.
  hint: Remove it; notifications go out for denials and approval requests.
```

**Fix:** remove `notify: [slack]`, or change the decision to `require_approval` if a person should see these refunds.

## rule_unreachable

**Severity:** warning

An earlier rule with no `when` matches everything a later rule matches, so the first match always comes from the earlier rule and the later one never decides anything. The policy is accepted. Deedglass only warns when this is certain from `match`.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: stripe-reads
    match: { tool: "stripe.*" }
    decision: allow
  - id: block-refunds
    match: { tool: stripe.create_refund }
    decision: deny
    reason: Refunds are never made by agents.
```

```text
○ policy.yaml:10:5 · rule_unreachable
  Rule "block-refunds" can never match: rule "stripe-reads" (line 7) matches everything it does and has no when.
  hint: Move rule "block-refunds" above rule "stripe-reads", or narrow rule "stripe-reads".
```

**Fix:** put the narrower rule first.

```yaml
rules:
  - id: block-refunds
    match: { tool: stripe.create_refund }
    decision: deny
    reason: Refunds are never made by agents.
  - id: stripe-reads
    match: { tool: "stripe.*" }
    decision: allow
```

## default_allow

**Severity:** warning

`defaults.decision` is `allow`, so every action no rule matches runs, including tools added after you wrote the policy. The policy is accepted, but enforcing it needs an explicit acknowledgement.

```yaml
deedglass: policy/v1
name: refunds
defaults:
  decision: allow
rules:
  - id: block-refunds
    match: { tool: stripe.create_refund }
    decision: deny
    reason: Refunds are never made by agents.
```

```text
○ policy.yaml:4:13 · default_allow
  defaults.decision: allow lets every unmatched action run.
  hint: Deedglass recommends deny. Enforcing this policy will need an explicit acknowledgement.
```

**Fix:** deny by default, and allow what you need with rules.

```yaml
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
```

## secret_in_policy

**Severity:** warning

A line looks like an API key or token (for example a Deedglass, Stripe, GitHub, Slack or AWS key). Policies are stored, versioned and shown to admins, so a secret in a policy is a secret shared with everyone who can read it. The policy is accepted; the message shows only the key's prefix.

```yaml
deedglass: policy/v1
name: storage
description: Uses key AKIAIOSFODNN7EXAMPLE for uploads.
defaults:
  decision: deny
  reason: No rule allows this action.
```

```text
○ policy.yaml:3:23 · secret_in_policy
  Line 3 looks like a secret (AKIA…).
  hint: Policies are stored and shown to admins. Keep secrets in the connector vault.
```

**Fix:** remove the key from the policy, keep it in the connector vault, and rotate it if the file was ever shared.

## reason_style

**Severity:** warning

A `reason` does not follow the Deedglass voice: it has an exclamation mark, starts with a lowercase letter, does not end with a period, or uses a word Deedglass copy never uses. This check runs only when you ask for it, with `deedglass policy lint --voice`.

```yaml
deedglass: policy/v1
name: postgres-safe
defaults:
  decision: deny
  reason: No rule allows this action.
rules:
  - id: block-drop
    match: { tool: "postgres.*" }
    when: sql.kind == "drop"
    decision: deny
    reason: DROP is never allowed for agents!
```

```text
○ policy.yaml:11:13 · reason_style
  This reason has an exclamation mark.
  hint: Say it calmly, without an exclamation mark.
```

**Fix:**

```yaml
    reason: DROP is never allowed for agents.
```

## more_diagnostics

**Severity:** error or warning

The file has more than 50 diagnostics. Deedglass reports the first 50, sorted by position, and then this one entry, which counts the rest. Its severity is `error` when any of the hidden diagnostics is an error, and `warning` when they are all warnings, so a file with only warnings is still accepted. A file with 60 invalid rule ids gets, after the first 50:

```text
✕ policy.yaml · more_diagnostics
  And 10 more (10 errors, 0 warnings).
  hint: Fix the ones above and run lint again.
```

**Fix:** fix the diagnostics shown, starting from the top, and run `deedglass policy lint` again. Many come from one cause, like a wrong indentation or a repeated mistake.

## internal_compiler_error

**Severity:** error

The compiler itself failed on this file. This is a bug in Deedglass, not in your policy. A policy that can't be compiled is never accepted, so a compiler failure can't let an action through.

```text
✕ policy.yaml · internal_compiler_error
  Deedglass could not compile this policy (an internal error), so it was not accepted.
  hint: Report it with the file; it is a bug in Deedglass.
```

**Fix:** report it to Deedglass with the policy file and the output of `deedglass policy lint --json`. Remove any secrets from the file first.

## See also

- [POLICY_LANGUAGE.md](POLICY_LANGUAGE.md): the policy language, its limits and the CEL environment.
- [CLI.md](CLI.md#policies-policy-lint-policy-fmt-policy-evaluate-policy-test-policy-push-and-policy-activate): `deedglass policy lint` and `deedglass policy fmt`.
