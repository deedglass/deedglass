# Lockdown mode

Lockdown is one switch for the strictest setup Deedglass has, for hospitals and security teams that
want every agent write to wait for a person and every safeguard on at once (ADR-117). It sits on top
of your policy: the policy decides first, then lockdown tightens the decision. It never loosens one.

Lockdown helps hospitals and security teams show who approved each agent write and what Deedglass
could check about it. It doesn't make Deedglass, or how you use it, meet any law or standard.
[What it doesn't do](#what-it-doesnt-do) is listed below.

## Turn it on

For every org on a server, in `deedglass.yaml` (or `DEEDGLASS_LOCKDOWN=true`), then restart:

```yaml
lockdown: true
```

For one org, on the server (with its database URL), as the operator:

```sh
deedglass lockdown on --reason "Ward pilot"
deedglass lockdown status
```

`lockdown on` seals an `org.updated` record (`fields: ["settings.lockdown"]`). When the org has a FHIR
connector and PHI mode is off, it turns [PHI mode](PHI_MODE.md) on in the same record.

A new local stack can start in lockdown:

```sh
deedglass init --lockdown
```

That sets `lockdown: true` for the local server, adds an ML-DSA-65 seal key beside the Ed25519 one,
enforces `postgres-safe`, labels the quickstart agent `connector=postgres`, and turns the org's
setting on. The smoke `UPDATE` then waits for a person; on a dev stack init countersigns it as the
local owner.

`GET /v1/whoami` shows `"lockdown": true` while the org's own setting is on. `deedglass doctor` shows
whether lockdown is on, who set it, and what weakens it.

## What it does

For every action of an org in lockdown, after the policy's decision, in this order (the first refusal
wins; each refusal is a deny with `policy_denied`, sealed, naming the rule):

| Rule | What happens |
|---|---|
| `lockdown.one-connector` | An agent reaches only the connector its `connector` label names. An agent without the label is refused. Every API key belongs to one agent, so every key reaches one connector. A person's own Console action (an undo) is exempt. |
| `lockdown.phi-mode` | Calls to a FHIR connector are refused while the org's PHI mode is off. |
| `lockdown.unclear-sql` | Native Postgres SQL that Deedglass can't read with confidence, or several statements, is refused. |
| `lockdown.no-drop-truncate` | `DROP` and `TRUNCATE` are refused. |
| `lockdown.no-schema-change` | Schema and permission changes (`ALTER`, `CREATE`, `GRANT`, `REVOKE` and the like) are refused. |
| `lockdown.no-server-code` | Server-side code, `COPY … PROGRAM`, and file, network or session functions are refused. |
| `lockdown.no-unbounded-write` | `UPDATE` and `DELETE` without a `WHERE` are refused. |
| `lockdown.cant-check-hold` | While the agent has an action that ended Can't check in lockdown and no person has acknowledged it, its writes are refused. Reads still run. |
| `lockdown.write-needs-person` | Every other write waits for a person. |

The SQL rules apply whatever the policy says, using the same classifier policies use
([SQLGUARD.md](SQLGUARD.md)).

**Writes.** A write is any tool whose side effect isn't `read` (an MCP tool's `unknown` counts as a
write), and SQL that writes. When the policy allows a write, lockdown turns it into an approval for
`role:admin` (rule `lockdown.write-needs-person`). When the policy already asks for an approval,
lockdown keeps its approvers and its rule.

**Four-eyes.** A high-risk write needs two different people: a destructive tool, a delete (SQL
`DELETE`, a FHIR delete, an HTTP `DELETE`), a database function call (`postgres.call`), an undo, and
anything on a Stripe connector. One person voting twice is refused (`already_countersigned`), as for
any approval with `required: 2` ([APPROVALS.md](APPROVALS.md#who-can-countersign)). An org with fewer
than two owners or admins can't approve these: they are refused with `approval_unsatisfiable`.

**Short approvals.** Every approval expires within 15 minutes. Timeout means no.

**The strongest check.** Each write is checked at the strongest level its connector reaches: Proven
(L2) where the connector is witnessed at Proven for the call (the Postgres connector with
`witness.enabled`, a FHIR connector set up for Proven), otherwise Checked (L1) when the tool has a
read-back, otherwise Claimed (L0). A policy that asks for less is raised; one that asks for more keeps
its level. The action's `decision_detail.lockdown` records the source and what changed:

| Flag | Meaning |
|---|---|
| `approval_added` | The policy allowed the write; lockdown made it wait. |
| `four_eyes` | Two different people approve it. |
| `expiry_shortened` | The policy's approval waited longer than 15 minutes. |
| `proven_required` | The connector reaches Proven, so the write is checked there. |
| `proven_unavailable` | The connector can't reach Proven for this call: Checked at best. |
| `claimed_only` | Nothing checks this tool's result: Claimed only. |
| `verify_raised` | The policy asked for a lower level. |

**The Can't check hold.** When an action admitted in lockdown ends Can't check (UNVERIFIABLE), that
agent's writes are refused until a person acknowledges the action. The hold covers the whole agent,
not one `Deedglass-Session`: the agent names its own session, so a hold per session could be stepped
around with a new header.

```sh
deedglass lockdown status                     # lists the held agents and the action holding each
deedglass lockdown ack act_01k6w4… --as priya@acme.test --reason "Read the row back by hand"
```

`ack` seals `lockdown.acknowledged` (target the action) and the agent's writes can run again.

**Seals and timestamps.** With `lockdown: true` in the config, prod refuses to start without
`DEEDGLASS_SEAL_PQ_KEY_FILE`, so every record carries both seals, Ed25519 and ML-DSA-65
([SEAL_KEYS.md](SEAL_KEYS.md)); dev starts and warns. Both refuse `timestamps.at_export: false`, so
exports carry outside timestamps whenever TSAs are configured ([TIMESTAMPS.md](TIMESTAMPS.md)).
`deedglass doctor` warns when no TSA is configured.

## Two choices to know about

**A Can't check result holds the agent's writes in every session.** Lockdown doesn't scope the hold
to one `Deedglass-Session`. The agent chooses its own session ref, so a hold per session could be
stepped around by sending a new header. After an action ends Can't check, none of that agent's writes
run, in any session, until a person runs `deedglass lockdown ack`. Its reads still run, and other
agents aren't affected.

**Keys reach one connector through the agent's `connector` label.** Every API key that can act
belongs to one agent, so lockdown scopes keys by scoping agents: an agent labelled
`connector=orders-db` reaches only the `orders-db` connector, and every key it holds does too. An
agent without the label is refused until an admin sets one (`PATCH /v1/agents/{id}` with
`{"labels": {"connector": "orders-db"}}`, or `deedglass agent create <name> --label
connector=orders-db` for a new agent). Changing the label is an admin action, sealed as
`agent.updated`. Org admin keys can't act, so they need no label. The key itself carries no
connector: to give one connector to a different set of keys, use a different agent.

## Turn it off

Turning lockdown off needs two people, both active owners or admins of the org, on the server:

```sh
deedglass lockdown off --as priya@acme.test --reason "Pilot over"     # asks; sealed lockdown.off_requested
deedglass lockdown off --countersign --as sam@acme.test               # a different person, within 30 minutes
```

The second step seals `org.updated` with `settings.lockdown: false`, naming who asked, who
countersigned and the request's record. The person who asked can't countersign
(`countersign_same_person`); lockdown stays on. There is no API route that turns lockdown off:
`PATCH /v1/org` refuses `settings.lockdown`.

With `lockdown: true` in the server's config, `lockdown off` is refused
(`lockdown_pinned_by_config`): remove the key on the server and restart first. That change isn't a
sealed record; `deedglass doctor` and the server's start log (`lockdown`) show it.

## What weakens it

`deedglass lockdown status` and `deedglass doctor` list these:

- no ML-DSA-65 seal key (records carry one seal);
- no TSAs configured (no outside timestamps);
- fewer than two owners or admins (high-risk writes are refused);
- agents without a `connector` label (all their calls are refused);
- a FHIR connector while PHI mode is off (its calls are refused).

## What it doesn't do

- **The server CLI can't tell who is typing.** `lockdown on`, `off` and `ack` run on the server as
  the operator; the people named with `--as` are recorded as given. Anyone with a shell on the server
  and its database URL can name two people, or change the database directly. The two-person rule
  stops one admin key or one Console session from turning lockdown off; it doesn't stop someone who
  controls the server.
- **No Console screen yet.** Lockdown is turned on and off from the server CLI or the config.
- **Turning off the config key isn't sealed.** Only the org setting's changes are records.
- **It doesn't judge a write.** A person still decides whether the change is right; lockdown makes
  sure one does.
- **The SQL rules cover the native Postgres connector.** SQL sent through another tool (an MCP
  server's `execute_sql`, for example) isn't classified; the person approving sees it.
- **Proven is only where the connector reaches it.** Other connectors are checked at Checked or
  Claimed, and the record says so (`proven_unavailable`, `claimed_only`).
- **High risk is a fixed list.** A write outside it needs one person. Your policy can ask for two
  (`required: 2`) on anything else.
- **No timestamps without TSAs.** Lockdown keeps export timestamps on, but adds no TSA and doesn't
  timestamp the head on a schedule; run `deedglass deeds timestamp --every 15m` for that.
- **Replays show the policy's decision.** `deedglass policy replay` evaluates the policy alone, not
  lockdown.
- **One person can still acknowledge a Can't check.** The hold needs one owner or admin, not two.
- **Approver groups don't exist yet.** Four-eyes uses `role:admin` (or the policy's own approvers).
