---
title: "The security review pack"
description: "A security reviewer asks every team that ships AI agents the same question: how do you control what your agents do? Deedglass already holds the facts that…"
imported: "docs/SECURITY_PACK.md"
editUrl: false
---
A security reviewer asks every team that ships AI agents the same question: how do you control what
your agents do? Deedglass already holds the facts that answer it: which agents exist, which rules
they run under, who approved what, what Postgres really changed, what was undone, and a sealed
chain of records. The security review pack turns those facts into one document a reviewer can read
in ten minutes and check in two.

- **Readiness (free):** `deedglass security-pack readiness`, `GET /v1/security_pack/readiness` and
  Console › Settings › Security review. The 24 questions, each with a status and, for a gap, the
  fix. No evidence and no document.
- **The pack (`security_pack`, free while we grow):** `deedglass security-pack build`, `POST /v1/security_pack` and
  **Build the pack** in the same Console screen. A folder with a print-ready document, the same
  answers as Markdown and JSON, the period's records and a seal over every file.
- **The check (free, offline, no account):** `deedglass security-pack verify <folder>`.

The pack answers questions; it does not claim to meet any standard or framework. Each question
names the public framework items it relates to (OWASP Top 10 for LLM Applications 2025, NIST AI RMF
1.0) by id, so a reviewer can file the answer where it belongs.

A sample built from the Acme demo data is published with these docs:
index.html, answers.md,
answers.json and the rest of the folder under
`/samples/security-pack/`.

## Statuses

| Status | Meaning |
|---|---|
| In place | The rule over the evidence passed. |
| Partly | Some of it is in place; the fix says what is missing. |
| Not set up | None of it is in place. |
| Not applicable | The question doesn't apply to this setup (for example, no Postgres connector to watch). |
| Can't tell | The evidence couldn't be read. The answer says what and why. Never a guess, never In place. |

Every status is computed. The rules are deterministic and conservative: a share is rounded down, a
threshold is met only when reached, and the enforced rules are judged by what they do, not by what
they say (below).

## The 24 questions

Thresholds are named constants in `internal/securitypack/rules.go`; the wording is
`internal/securitypack/questions.yaml`.

### 1. Inventory

| Id | Question | In place when |
|---|---|---|
| `agents_listed` | Does every AI agent that acts have its own record, with a person responsible for it? | at least one agent, and every agent that isn't stopped has an owner (Partly when one doesn't) |
| `keys_scoped` | Does each agent use its own scoped key, and are unused keys cleaned up? | at least one live agent key, and none older than 30 days was never used |
| `keys_rotated` | Are API keys replaced at least every 90 days? | no live key (agent or admin) is older than 90 days; Partly when some are; Not set up when all are |
| `connectors_listed` | Is every system an agent can reach listed as a connector? | at least one connector, and no action in the period reached a tool through a wrapped MCP server instead |

### 2. Rules

| Id | Question | In place when |
|---|---|---|
| `rules_active` | Does a rule version you chose decide every agent action? | an enforced version other than the onboarding `default-deny` (Partly while only `default-deny` is enforced) |
| `default_deny` | Are destructive SQL and large money moves refused or held for a person by default? | the enforced version refuses or holds all four probes (below) |
| `limits_set` | Are agents limited in how often they act and how much they can spend? | the enforced version has a budget, or every active agent has its own |
| `rules_changes_recorded` | Is every change to the rules sealed with who made it and when? | every live activation and every activation or deactivation in the period has a record and an actor |

### 3. People in the loop

| Id | Question | In place when |
|---|---|---|
| `approvals_required` | Do risky actions wait for a person before they run? | the enforced version has a `require_approval` rule |
| `four_eyes` | Is the person who asked kept from approving their own request, with two approvers where it matters? | a rule asks for two or more approvers (the self-approval refusal applies to every approval) |
| `approvals_expire` | Do actions waiting for approval expire instead of waiting forever? | no approval is still pending past its expiry |

### 4. Stop

| Id | Question | In place when |
|---|---|---|
| `stop_all` | Can more than one person stop every agent at once? | two or more active owners or admins (Partly with one) |
| `stop_used` | Has Stop been used or tried recently? | an `agent.stopped` or `org.stopped_all` record in the period (Partly when only earlier) |

### 5. Checking results

| Id | Question | In place when |
|---|---|---|
| `results_checked` | Are agents' write actions checked against the system they changed? | at least 80% of the period's executed writes are Checked or Proven; Partly from 20%; Not applicable without writes |
| `mismatches_handled` | When a check finds the result doesn't match, is there a follow-up? | every action that ended in Doesn't match has an undo started for it |
| `watched_sessions` | Are changes made to your databases outside Deedglass recorded too? | observe mode is on for every Postgres connector |

A write is an executed action whose tool isn't a read by name (`query`, `get_*`, `list_*`,
`search*`, `read_*`, `describe*`). An unknown tool counts as a write, so it needs a check to count
as checked.

### 6. Undo

| Id | Question | In place when |
|---|---|---|
| `undo_available` | Can agents' database writes be undone from the record? | every connector that writes is Postgres |
| `undos_done` | When something was undone, was the undo itself checked? | every undo in the period ended in Matches and every session undo finished |

### 7. Secrets and network

| Id | Question | In place when |
|---|---|---|
| `secrets_encrypted` | Are connector credentials encrypted at rest and never shown again? | every credential is envelope-encrypted and was set or rotated in the last year |
| `agents_never_see_secrets` | Do agents act without ever holding database passwords or API keys? | every action in the period ran through a Deedglass connector (an MCP server behind `mcp wrap` holds its own credentials) |
| `egress_limited` | Do connectors' outbound calls go through one door that refuses private networks? | the server refuses private networks (Partly with the dev override or `DEEDGLASS_EGRESS_ALLOW_CIDRS`) |

### 8. Records

| Id | Question | In place when |
|---|---|---|
| `records_sealed` | Does every action have a sealed record in an unbroken chain? | the chain check over the period is intact and no finished action is without its record after 5 minutes |
| `records_verifiable` | Can a reviewer check the records offline, without an account? | the server has a seal key and none was revoked |
| `product_tested` | Has this version of the control layer been attacked on purpose, and with what result? | the attack suite result this binary carries is for this version and nothing differed |

## The probes behind `default_deny`

The enforced version is compiled and evaluated with the same evaluator the Gate uses, on four fixed
inputs: `DROP TABLE public.orders`, `TRUNCATE public.orders` and `DELETE FROM public.orders` on the
org's Postgres connector's `execute` tool, and a $5,000.00 USD refund on its Stripe connector's
`create_refund` (a charge Stripe confirmed, undisputed, with that much left to refund). A rule that
allows any of them shows in the answer by name. An evaluation that fails is Can't tell, never a
refusal.

## What the pack folder holds

```
security-pack-acme-2026-10-18/
  index.html            print-ready (A4 and Letter), self-contained: no script, no font or image request
  answers.md            the same answers in Markdown
  answers.json          security_pack v1 (a closed schema)
  records.tar.gz        the period's records, export bundle v1 (left out with --no-records)
  README.txt            what's here, how to check it, what not to send
  pack.seal.json        a file.sealed record over each file above, and the public seal keys
  aliases.private.json  only with --aliases: which alias is which real name. Don't send it.
```

Print `index.html` or save it as PDF from any browser; each group starts a page and a running
footer names the org, the period and `answers.json`'s digest. No PDF engine is involved.

`answers.json`:

```json
{"object":"security_pack","v":1,"org":"org_…","org_name":"Acme","period":{"from":"…","to":"…"},
 "generated_at":"…","deedglass_version":"…",
 "summary":{"in_place":17,"partly":4,"not_set_up":2,"not_applicable":1,"cant_tell":0},
 "answers":[{"id":"keys_rotated","group":"inventory","question":"…","status":"partly","answer":"…",
   "evidence":{"live_keys":5,"older_than_90_days":2,"oldest_on":"2026-06-02"},"check":"…",
   "refs":[{"framework":"NIST AI RMF 1.0","id":"MEASURE 2.7"}]}],
 "chain":{"intact":true,"head":"sha256:…"},
 "records":{"file":"records.tar.gz","deeds":4212,"sha256":"sha256:…"}}
```

## How a reviewer checks it

```sh
deedglass security-pack verify security-pack-acme-2026-10-18
# ✓ Checked: every file matches its sealed record; 4,212 records, chain intact
```

It checks, offline: that `pack.seal.json` is well formed; that each `file.sealed` record verifies
(Ed25519 seal, body hash, key validity) and belongs to the pack's org; that each file's bytes match
the sha256 and size the record seals; that `answers.json` is a valid closed `security_pack` v1 of
the same org whose records digest matches; that every file the pack needs is sealed; that the
records bundle was sealed by the server that sealed the files; and every record in
`records.tar.gz`, with `deedglass verify`'s own code. One changed byte in any file fails the check
and names the file. Files in the folder that aren't sealed are listed as such.

Without `--keys`, the keys inside the pack are used: that shows the folder is whole and consistent,
not who sealed it. To check that the records were sealed by the org's own server, get its public
keys from the org (`/.well-known/deedglass/keys.json` on its server) and pin them:

```sh
deedglass security-pack verify security-pack-acme-2026-10-18 --keys keys.json
```

## Privacy

The pack is built on your own server and nothing reaches deedglass.com. Evidence holds ids,
counts, hashes, dates and the names of your agents, connectors and rules; never action arguments or
results, row values, emails or credential values (`TestSecurityPackLive` sweeps every file, the
records inside `records.tar.gz` included, for canaries in each).

With `--aliases`, agent, connector and rule names become stable aliases (`agent-1`, `connector-2`,
`rules-1`, numbered in creation order) across the folder, and the mapping is written only to
`aliases.private.json`. `records.tar.gz` is left out with `--aliases`, because the sealed records
inside it carry the real names and can't be renamed without breaking their seals; `--records`
includes it anyway. The rules are probed with the real names before the aliases apply.

## Who can do what

| | Readiness | Build | Verify |
|---|---|---|---|
| Viewer | yes | no | yes (offline) |
| Admin, owner, org admin key | yes | yes | yes |
| Agent key | 403 | 403 | n/a |

Without the `security_pack` feature, the build answers 403 `pro_required` and the readiness view
keeps working.

## What the pack doesn't show

- Agents or tools that never go through Deedglass: an agent with its own credentials to a system
  Deedglass doesn't front leaves no record.
- Changes on databases or tables Deedglass doesn't watch.
- Whether the rules suit your business: the probes are four fixed inputs, not every input.
- Anything before the period or outside the org, and anything about models or prompts.
- An outside opinion: Deedglass writes the pack from your own records; no third party reviews it.
- The readiness view's chain check covers the last day (the pack checks the whole period), so a
  break older than a day shows only in the pack or in `deedglass ledger check`.

## Framework references

Each question names related items by id only (never their text), re-read on 2026-10-15:

- OWASP Top 10 for LLM Applications 2025 (genai.owasp.org/llm-top-10): LLM02 Sensitive Information
  Disclosure, LLM06 Excessive Agency, LLM10 Unbounded Consumption.
- NIST AI RMF 1.0 core (airc.nist.gov AI RMF Core, section 5): GOVERN 1.4, GOVERN 1.6, MAP 3.5,
  MEASURE 1.3, MEASURE 2.4, MEASURE 2.6, MEASURE 2.7, MEASURE 2.8, MANAGE 1.3, MANAGE 2.3,
  MANAGE 2.4, MANAGE 4.3.

A reference says where a reviewer might file the answer. It is not a claim that any item is met.

:::note[Source]
This page is `docs/SECURITY_PACK.md` in the Deedglass repo, imported at build time.
:::
