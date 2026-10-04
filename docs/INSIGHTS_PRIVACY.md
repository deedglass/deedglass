# Insights phrasing and your data

**Status:** S28. Code `internal/learn/privacy.go` (what may be sent), `phrase.go` (when), `phrase_claude.go` (the client), `phrase_validate.go` (the checks on a reply), `prices.go` (the price table); the wiring in `cmd/deedglass/serve_insights.go`. Decision: ADR-059.
**Audience:** owners and operators deciding whether to let Claude reword suggestions, and anyone who needs to know exactly what would leave the server.

Every suggestion ([SUGGESTIONS.md](SUGGESTIONS.md)) is written by Deedglass's own templates, and that text is complete on its own. An org can choose to have Claude, Anthropic's model, reword a suggestion's title and rationale. **It is off by default.** When it is on, only placeholders, product-defined tool names, decisions and numbers leave the server, and the reply is used only if every number in it matches the evidence.

## Contents

1. [Two switches, both off by default](#two-switches-both-off-by-default)
2. [Exactly what is sent](#exactly-what-is-sent)
3. [What is never sent](#what-is-never-sent)
4. [Placeholders](#placeholders)
5. [The request](#the-request)
6. [Checking the reply](#checking-the-reply)
7. [Cost and limits](#cost-and-limits)
8. [Anthropic's data terms](#anthropics-data-terms)
9. [Turning it off](#turning-it-off)

## Two switches, both off by default

A phrasing request is made only when all of these hold:

1. **The operator set a key.** `DEEDGLASS_ANTHROPIC_API_KEY` is set on the server (it must start with `sk-ant-`). Without it, no client is built and no request can be made; the settings object reports `phrasing_available: false`. `DEEDGLASS_ANTHROPIC_MODEL` picks the model (default `claude-sonnet-5-5`; a Claude model id without a date suffix).
2. **The org opted in.** An admin sets `llm_phrasing: true` with `PATCH /v1/insights/settings` (or in Console › Insights › Settings), with a reason; the change is sealed as an `insights.settings_changed` deed. Turning it on while the operator has no key is 400 `validation_failed` on `llm_phrasing`: "Claude phrasing needs DEEDGLASS_ANTHROPIC_API_KEY on the server. Deedglass keeps using its own wording until then."
3. **The org is under its limits:** fewer than 20 phrasing calls today (UTC) and less spent this calendar month (UTC) than `llm_monthly_cap_cents` (default 200, $2.00).

Phrasing runs after the suggestion is stored, in the background, one call at a time per org. It never delays or blocks a suggestion, and any failure leaves the template text in place.

## Exactly what is sent

One type crosses to the phrasing client: `PhrasingInput`. The JSON keys it may send are pinned in `learn.PhrasingInputKeys`, and `TestPhrasingInputKeysMatchStruct` fails if the struct grows a field the list doesn't name:

| Key | Holds |
|---|---|
| `kind` | `loosen`, `tighten`, `budget`, `explicit_rule` or `advisory` |
| `generator` | like `loosen.approval_rate` |
| `tool` | a product-defined tool name (`postgres.execute`, `stripe.create_refund`); a tool your org named itself is a placeholder |
| `connector_type` | a product connector type (`postgres`, `stripe`, …), or left out |
| `before`, `after` | decisions or levels, like `require_approval` and `allow` |
| `window_days` | 14 |
| `refs` | each placeholder and its kind (`agent`, `rule`, `table`, `budget`, `policy`, `tool`); never its value |
| `numbers` | each number the template text uses: `{key, value, unit}`, with unit `count`, `seconds`, `rate`, `days` or `minor:<currency>` |
| `template_title`, `template_rationale` | the template text, with a placeholder in place of every name your org chose |

## What is never sent

Arguments, SQL text, tool results, on-behalf-of and session refs, email addresses, org and user names, agent, rule, table, budget and policy names, action ids, the policy YAML, the diff, the evidence JSON and the risk note. `TestPrivacySentinels` builds a suggestion from evidence full of sentinel values (in arguments, SQL, user refs, emails, agent names, rule ids, the org name and action ids), captures the request body and fails if any sentinel appears, if a key outside the allow-list appears, or if the body reaches 8 KiB. `TestPrivacyProperty` does the same over random evidence. `TestHostileEnv` sets `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` to hostile values and checks that the request still goes only to the pinned host, carrying only the Deedglass key, and that with the Deedglass key empty no request is made.

```
go test ./internal/learn/ -run 'NeverEnforce|Privacy|HostileEnv' -count=1 -v
```

## Placeholders

Every name your org chose becomes a placeholder before anything leaves the server: `{agent_1}`, `{table_1}`, `{rule_1}`, `{budget_1}`, `{policy_1}`, `{tool_1}`. The same name gets the same placeholder within one suggestion. The values stay in memory on the server and are never serialized. When a reply comes back, Deedglass puts the names back itself. A reply can't add a placeholder (it fails the checks below), so text in a name can never reach the model, and the model can't make Deedglass print a name that wasn't in the template.

## The request

The client is a small standard-library client (ADR-059), not an SDK. It reads nothing from the environment: the key and model come only from Deedglass's own configuration, and the HTTP client is the egress door's first-party client with proxies off, so `HTTPS_PROXY` can't redirect it. One `POST https://api.anthropic.com/v1/messages` (the host is pinned in code) with the headers `x-api-key` (the Deedglass key), `anthropic-version: 2023-06-01` and `content-type: application/json`. No `temperature`, `top_p` or assistant prefill.

This is the body `ClaudePhraser.RequestBody` builds for seed 7's loosen suggestion ([SUGGESTIONS.md](SUGGESTIONS.md#a-worked-example-seed-7)), generated by the code from a fixture with the same numbers (a median wait of 1,140 seconds stands in for the run's 19 minutes). The wire form is compact JSON, 1,385 bytes; it is indented here for reading:

```json
{
  "model": "claude-sonnet-5-5",
  "max_tokens": 1024,
  "system": "You reword short notes for Deedglass, a tool that records what AI agents did. Rules: calm, exact, second person. Use only the numbers given. Keep every {placeholder} unchanged. No exclamation marks, no emoji, no hype words. Title ≤ 90 characters, sentence case. Rationale ≤ 400 characters, at most 2 sentences.",
  "messages": [
    {
      "role": "user",
      "content": "{\"kind\":\"loosen\",\"generator\":\"loosen.approval_rate\",\"tool\":\"postgres.execute\",\"before\":\"require_approval\",\"after\":\"allow\",\"window_days\":14,\"refs\":{\"{agent_1}\":\"agent\",\"{rule_1}\":\"rule\",\"{table_1}\":\"table\"},\"numbers\":[{\"key\":\"approved\",\"value\":398,\"unit\":\"count\"},{\"key\":\"concluded\",\"value\":403,\"unit\":\"count\"},{\"key\":\"median_wait\",\"value\":1140,\"unit\":\"seconds\"},{\"key\":\"window_days\",\"value\":14,\"unit\":\"days\"}],\"template_title\":\"Allow updates to {table_1} without approval\",\"template_rationale\":\"People approved 398 of 403 updates to {table_1} by {agent_1} in the last 14 days and denied none. Each waited a median of 19 minutes.\"}"
    }
  ],
  "output_config": {
    "effort": "low",
    "format": {
      "type": "json_schema",
      "schema": {
        "additionalProperties": false,
        "properties": {
          "rationale": { "maxLength": 400, "type": "string" },
          "title": { "maxLength": 90, "type": "string" }
        },
        "required": ["title", "rationale"],
        "type": "object"
      }
    }
  }
}
```

The `system` string is `learn.PhrasingSystemPrompt`, verbatim, and holds no org data. The user message is the `PhrasingInput` as JSON, nothing else: `public.orders`, `ops-agent` and `approve-order-updates` went out as `{table_1}`, `{agent_1}` and `{rule_1}`.

**Outcomes.** A reply with `stop_reason: end_turn` goes to the checks below. Anything else keeps the template, with the reason stored in `phrasing.reason`: the stop reason itself (`refusal`, `max_tokens`, …); a reply that isn't exactly `{title, rationale}` (`validation:schema`); a 4xx (`http_400`, …); a timeout after 10 s (`timeout`); a network failure (`network`). A 429 is retried once after its `retry-after` when that is 10 s or less; a 529 or another 5xx is retried once after 1 s.

## Checking the reply

A reply is used only if every check passes; the first failure keeps the template with `phrasing.reason` set to `validation:<check>`:

| Check | Fails when |
|---|---|
| `empty` | the title or rationale is empty |
| `title_length` | the title is over 90 characters, or more than one line |
| `rationale_length` | the rationale is over 400 characters |
| `sentences` | the rationale has more than 2 sentences |
| `control_characters`, `emoji`, `dash` | it holds a control character, an emoji, or a long dash (U+2014, U+2013) |
| `exclamation` | it holds `!` |
| `placeholder_unknown` | it uses a placeholder the input didn't give |
| `placeholder_dropped` | it leaves out a placeholder the template used |
| `banned_word` | it uses a word on Deedglass's banned list (the same list CI checks) |
| `first_person` | it says I, me, my, we, our, us or their contractions |
| `number_not_in_input` | it holds a number that isn't one of the input's numbers in a normalized form: a count as written (`1,204` and `1204` match), a rate as `97.3%` or `97%`, seconds as seconds, minutes, hours or days rounded, money as `$48.00`, `48.00` or minor units, or a number already in the template text |

When a reply passes, the suggestion shows it as its title and rationale, with `phrasing.source: "claude"`, the model, and the template title it replaced in `phrasing.template_title`. The Console adds: "Worded by claude-sonnet-5-5 from the numbers above. Deedglass checked that every number matches the evidence." The template text stays stored with the suggestion.

## Cost and limits

| Limit | Value |
|---|---|
| Monthly cap per org | `llm_monthly_cap_cents`, default 200 ($2.00), 0 to 10,000 ($100.00). 0 stops every call |
| Calls per org | at most 20 a day (UTC), one at a time |
| Per call | `max_tokens` 1024, a 10 s timeout, at most one retry |

Before each call Deedglass adds up what the org's calls cost this month and how many it made today; at either limit it stops and logs `cap_reached`. Tokens a failed call used (a refusal, for example) count toward the cap. Each call logs `insights.phrase` with the model, input and output tokens, `cost_usd_micros`, the duration and the outcome; never the text. The suggestion's `phrasing` object stores the same numbers.

Prices come from `internal/learn/prices.go`, per million tokens, **checked 2026-10-04** against Anthropic's pricing page (https://platform.claude.com/docs/en/about-claude/pricing):

| Model | Input | Output |
|---|---|---|
| `claude-sonnet-5-5` (the default) | $2 | $10 |
| `claude-sonnet-5` | $2 | $10 |
| `claude-sonnet-5-5` | $2 | $10 |
| `claude-haiku-4-5` | $1 | $5 |
| `claude-opus-5` | $5 | $25 |
| `claude-opus-5-5` | $4 | $20 |

A model not in the table is counted at $5 and $25, the most expensive row, so the cap errs on the side of spending less. For scale, a call of 900 input and 120 output tokens on `claude-sonnet-5-5` costs $0.003. The default is Claude Sonnet 5.5 because on 2026-10-04 Anthropic listed Claude Sonnet 5 as a legacy model at the same price. The cap here is Deedglass's own; set a spend limit on the Anthropic workspace too.

## Anthropic's data terms

The request goes to Anthropic under the operator's own API key, so the operator's agreement with Anthropic governs it. What Anthropic's public pages said when we checked them on **2026-10-04**:

- **Training.** Anthropic's Commercial Terms of Service (effective June 17, 2025), section B, say: "Anthropic may not train models on Customer Content from Services." (https://www.anthropic.com/legal/commercial-terms). Anthropic's privacy center says the same for its commercial products by default, unless the customer gives feedback or otherwise opts in (https://privacy.claude.com/en/articles/7996868-is-my-data-used-for-model-training).
- **Retention.** Anthropic's privacy center says that, by default, API inputs and outputs are deleted within 30 days. The exceptions it lists: inputs and outputs flagged for a usage policy violation (kept up to 2 years, with safety scores up to 7 years), feedback the customer submits (5 years), what the law requires, services where the customer controls retention (such as the Files API), and different terms agreed with Anthropic (https://privacy.claude.com/en/articles/7996866-how-long-do-you-store-my-organization-s-data).

Deedglass sends no feedback to Anthropic and uses no Files API. These are Anthropic's statements about its own service, not something Deedglass can check; read the pages yourself before you turn phrasing on, because they can change.

## Turning it off

- **For one org:** `PATCH /v1/insights/settings {"llm_phrasing": false, "reason": "…"}` (sealed as `insights.settings_changed`), or switch it off in Console › Insights › Settings. New suggestions keep the template wording. Suggestions Claude already worded keep that wording, marked `phrasing.source: "claude"`.
- **For a cap only:** set `llm_monthly_cap_cents` to 0.
- **For the whole server:** unset `DEEDGLASS_ANTHROPIC_API_KEY` and restart. No client is built, no request can be made, and every org reads `phrasing_available: false`.
