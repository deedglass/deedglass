> Sample built from the Acme demo data

# How Acme controls its AI agents

Answers built from Acme's own Deedglass records, 17 July to 15 October 2026. Every number below is counted from those records; the last section shows how to check them.

**16 in place · 4 partly · 3 not set up · 1 not applicable**

- Org: `org_01m50d5zr94ahs13tkv9txwz2q`
- Built: 15 October 2026 by Deedglass 0.1.0-dev
- Records: 626 sealed records in records.tar.gz, sha256 faa2ad6644f8
- Chain: Intact, head 6724ff3c769a

## 1. Inventory

### Does every AI agent that acts have its own record, with a person responsible for it?

**Partly.** 4 agents are registered and an agent without a record can't call any tool, but 4 agents have no named owner.

Evidence: `agents 4 · active 4 · stopped 0 · without owner 4`

How to check: deedglass agent list, or Console › Agents

Related: NIST AI RMF GOVERN 1.6 · OWASP LLM06 Excessive Agency

### Does each agent use its own scoped key, and are unused keys cleaned up?

**In place.** 1 live agent key belongs to exactly one agent with action scopes only; agent keys can never hold admin rights. Every key has been used.

Evidence: `agent keys 1 · admin keys 1 · unused keys 0`

How to check: deedglass agent keys list &lt;agent&gt;, or Console › Agents › Keys

Related: OWASP LLM06 Excessive Agency · NIST AI RMF MEASURE 2.7

### Are API keys replaced at least every 90 days?

**In place.** No live API key is older than 90 days (2 live keys).

Evidence: `live keys 2 · older than 90 days 0 · oldest 15 Oct 2026`

How to check: deedglass agent keys list &lt;agent&gt; and deedglass org keys list (created dates)

Related: NIST AI RMF MEASURE 2.7

### Is every system an agent can reach listed as a connector?

**In place.** 1 system is listed as a connector. Every action in the period went through one of them.

Evidence: `connectors 1 · active connectors 1 · proxy actions 0`

How to check: deedglass connector list, or Console › Connectors

Related: NIST AI RMF GOVERN 1.6 · OWASP LLM06 Excessive Agency

## 2. Rules

### Does a rule version you chose decide every agent action?

**In place.** acme-demo version 2 (18 rules) decides every action. It was activated by key\_01m50d8maknvw5wvx6ejcw5zkr on 15 Oct 2026, sealed in deed\_01m50d8mzyza8a62bdejtc5fm6.

Evidence: `enforced acme-demo · version 2 · version id pv_01m50d8mz5d48c3gezv1g3v0dy · version hash sha256:6f9139ab353170c9d93c274c0d86c721b897fd21a48cdf741c25d3c0045fe668 · activated by key_01m50d8maknvw5wvx6ejcw5zkr · activated 15 Oct 2026 · activation deed deed_01m50d8mzyza8a62bdejtc5fm6 · rules 18`

How to check: Console › Policies shows the enforced version; deedglass verify checks its activation record

Related: OWASP LLM06 Excessive Agency · NIST AI RMF GOVERN 1.4

### Are destructive SQL and large money moves refused or held for a person by default?

**In place.** The enforced rules were run on 4 fixed inputs (DROP TABLE, TRUNCATE, DELETE with no WHERE, a $5,000 refund): 4 refused, 0 held for a person, none allowed.

Evidence: `probes 4 · refused 4 · held 0 · allowed 0`

How to check: deedglass policy evaluate --server --tool &lt;connector&gt;.execute --sql 'DROP TABLE public.orders'

Related: OWASP LLM06 Excessive Agency · NIST AI RMF MANAGE 1.3

### Are agents limited in how often they act and how much they can spend?

**In place.** The enforced rules set 1 budget on calls or spend, and 0 of 4 active agents have their own budget, so a looping agent stops at its limit.

Evidence: `rule budgets 1 · agents with budgets 0 · active agents 4`

How to check: deedglass agent list --json shows each agent's budgets; Console › Policies shows the enforced version's

Related: OWASP LLM10 Unbounded Consumption · NIST AI RMF MANAGE 1.3

### Is every change to the rules sealed with who made it and when?

**In place.** 5 rule changes (the live activations and the period's changes), each sealed with who made it and when; 5 changes happened in the period.

Evidence: `changes 5 · sealed 5 · with who 5 · changes in period 5`

How to check: deedglass deeds list --kind admin lists each policy.activated record; deedglass verify checks them

Related: NIST AI RMF GOVERN 1.4 · NIST AI RMF MEASURE 2.8

## 3. People in the loop

### Do risky actions wait for a person before they run?

**In place.** 4 enforced rules hold actions for a person. In the period 1 action waited: 0 approved, 0 denied.

Evidence: `approval rules 4 · approvals 1 · approved 0 · denied 0`

How to check: deedglass approvals list, or Console › Approvals

Related: OWASP LLM06 Excessive Agency · NIST AI RMF MAP 3.5

### Is the person who asked kept from approving their own request, with two approvers where it matters?

**In place.** Deedglass refuses an approval from the person who asked, on every approval. 1 rule asks for two or more people; 0 approvals in the period needed two.

Evidence: `approval rules 4 · two person rules 1 · two person approvals 0`

How to check: Console › Approvals shows each approver; the approval's record lists who decided

Related: NIST AI RMF MAP 3.5

### Do actions waiting for approval expire instead of waiting forever?

**In place.** Every waiting action has an expiry. In the period 0 approvals expired, and none is waiting past its expiry.

Evidence: `approvals 1 · expired 0 · overdue 0 · median minutes 0`

How to check: deedglass approvals list --status expired

Related: NIST AI RMF MAP 3.5

## 4. Stop

### Can more than one person stop every agent at once?

**In place.** 2 people (owners and admins) can stop every agent at once with deedglass org stop-all or Console › Stop all.

Evidence: `people 2 · admin keys 1`

How to check: Console › Settings › Members; deedglass org stop-all --help

Related: NIST AI RMF MANAGE 2.4 · OWASP LLM06 Excessive Agency

### Has Stop been used or tried recently?

**Not set up.** Stop has never been used or tried in this org.

Evidence: `stops in period 0`

How to check: deedglass deeds list --kind admin lists the agent.stopped and org.stopped\_all records

Related: NIST AI RMF MANAGE 2.4

## 5. Checking results

### Are agents' write actions checked against the system they changed?

**Partly.** 67% of the period's 419 write actions were checked against the system of record (151 Checked, 132 Proven); 136 rest on the tool's own answer (aim for 80%).

Evidence: `writes 419 · checked 151 · proven 132 · claimed 136 · share percent 67`

How to check: deedglass actions list --level L0 lists the actions that rest on the tool's own answer

Related: NIST AI RMF MEASURE 2.4

### When a check finds the result doesn't match, is there a follow-up?

**Not set up.** 38 actions ended in Doesn't match in the period and none has a follow-up: act\_01m50d8j5r9cv4fn30nqserq4x, act\_01m50d8jah53j1hj5j3c9xkkas, act\_01m50d8jb80zgpyqtvxx65mpzc, act\_01m50d8jm25481w9e1hnyby0he, act\_01m50d8jnkpx1psehmet50fvtg and 33 more.

Evidence: `mismatches 38 · followed up 0 · open act_01m50d8j5r9cv4fn30nqserq4x, act_01m50d8jah53j1hj5j3c9xkkas, act_01m50d8jb80zgpyqtvxx65mpzc, act_01m50d8jm25481w9e1hnyby0he, act_01m50d8jnkpx1psehmet50fvtg and 33 more`

How to check: deedglass actions list --verdict MISMATCH

Related: NIST AI RMF MANAGE 4.3

### Are changes made to your databases outside Deedglass recorded too?

**Not set up.** Observe mode is off, so changes made outside Deedglass aren't recorded.

Evidence: `postgres connectors 1 · watched 0 · sessions 0 · caught 0`

How to check: deedglass observe list

Related: NIST AI RMF MEASURE 2.4

## 6. Undo

### Can agents' database writes be undone from the record?

**In place.** Every connector that writes (1) is Postgres, where Deedglass can undo a write from what Postgres committed.

Evidence: `write connectors 1 · undo connectors 1`

How to check: deedglass undo preview &lt;act\_…&gt; on any write

Related: NIST AI RMF MANAGE 2.3

### When something was undone, was the undo itself checked?

**Not applicable.** Nothing was undone in the period.

Evidence: `undos 0 · matched 0 · session undos 0`

How to check: deedglass actions show &lt;act\_…&gt; on the undo

Related: NIST AI RMF MANAGE 2.3

## 7. Secrets and network

### Are connector credentials encrypted at rest and never shown again?

**In place.** 1 credential is stored with A256GCM-ENV-1 envelope encryption and never returned by the API or Console. Each was set or rotated in the last year.

Evidence: `credentials 1 · alg A256GCM-ENV-1 · stale 0`

How to check: deedglass connector show &lt;connector&gt; shows the credential's version and dates, never its value

Related: OWASP LLM02 Sensitive Information Disclosure · NIST AI RMF MEASURE 2.7

### Do agents act without ever holding database passwords or API keys?

**In place.** All 469 actions in the period ran through Deedglass connectors, which hold the credentials; the agent sent only the tool and its arguments.

Evidence: `gateway actions 469 · proxy actions 0`

How to check: deedglass actions list shows each action's mode (gateway or proxy)

Related: OWASP LLM02 Sensitive Information Disclosure · OWASP LLM06 Excessive Agency · NIST AI RMF MEASURE 2.7

### Do connectors' outbound calls go through one door that refuses private networks?

**Partly.** Every connector call leaves through one outbound door, but private networks are reachable (allowed in dev, 0 ranges listed).

Evidence: `env dev · private networks allowed in dev · allowed ranges 0`

How to check: deedglass config print (egress keys)

Related: OWASP LLM06 Excessive Agency · NIST AI RMF MEASURE 2.7

## 8. Records

### Does every action have a sealed record in an unbroken chain?

**In place.** 571 finished actions in the period each have a sealed record. The chain of 626 records is unbroken.

Evidence: `deeds checked 626 · chain intact · chain head sha256:6724ff3c769af6d2d7216889e44146d08ef159796631119afff65a24cdcc6873 · finished 571 · unsealed 0`

How to check: deedglass ledger check; deedglass verify records.tar.gz

Related: NIST AI RMF MEASURE 2.8

### Can a reviewer check the records offline, without an account?

**In place.** Records are sealed with Ed25519 seal key sk\_01m50d5zr4txnqpj78yszjexmm; the public keys ship with the records, and deedglass verify checks them offline.

Evidence: `seal keys sk_01m50d5zr4txnqpj78yszjexmm · signer sk_01m50d5zr4txnqpj78yszjexmm · revoked 0`

How to check: deedglass security-pack verify &lt;folder&gt;

Related: NIST AI RMF MEASURE 2.8 · NIST AI RMF MEASURE 1.3

### Has this version of the control layer been attacked on purpose, and with what result?

**Partly.** The attack suite ran against Deedglass 0.1.0 on 15 Oct 2026: 70 cases matched, 0 differed. This server runs 0.1.0-dev.

Evidence: `running 0.1.0-dev · tested version 0.1.0 · matched 70 · differs 0 · run 15 Oct 2026`

How to check: docs/ATTACK\_SUITE.md, or make attack in the Deedglass repo

Related: NIST AI RMF MEASURE 2.6 · NIST AI RMF MEASURE 2.7

## What this pack doesn't show

- Agents or tools that never go through Deedglass.
- Changes on databases or tables Deedglass doesn't watch.
- Whether the rules suit your business: the pack shows what the enforced rules do on four fixed inputs, not on every input.
- Anything before 17 July 2026 or outside this org, and anything about models or prompts.
- An outside opinion: Deedglass wrote this pack from the org's own records; no third party reviewed it.

## How to check this pack

Run `deedglass security-pack verify <folder>` (no account needed). It checks every file against its sealed record in pack.seal.json and every record in records.tar.gz. Pin the org's own keys with `--keys keys.json`.
