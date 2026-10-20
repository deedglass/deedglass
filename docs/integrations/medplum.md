# Medplum and Deedglass

Put Deedglass between an AI agent (or a Medplum Bot) and your Medplum project, and every write the
agent makes is decided by your policy, read back at the version Medplum reported, and sealed as a
deed you can check later. On a self-hosted Medplum on Postgres, Deedglass can also read Medplum's
own change log and confirm that the transaction behind that version holds what the agent sent
(Proven). This page covers the setup, what each write gets, and the limits. The runnable example
is `examples/medplum` (`make medplum-example`).

Use synthetic data while you try it. Deedglass doesn't look at or change clinical content: it
records what was sent and compares it with what Medplum stored.

## Why a check after the write

A FHIR server can answer `201 Created` and store something other than what the client sent. With
Medplum 5.2.6, an AccessPolicy that lists a field in `readonlyFields` does exactly that for a
create: the write succeeds, the field is dropped, and the answer is the stored resource without it.
An agent that only reads the status code reports success. The example runs into this on purpose:

```text
✕ (b) · medplum.create · executed · Proven · Doesn't match
  L1 fhir_field_changed: medplum.create reported success, but Observation/2aca30b2-… version 10… shows note as null, not [{"text":"Synthetic note: measured after a short walk."}].
  L2 version_field_changed: The server's database committed Observation/2aca30b2-… version 109fbce1-… with note different from what medplum.create sent.
```

The same check catches any other change to a field the agent set between the call and the stored
version: a server setting, a proxy, or a bug in the client's own tool. Fields only Medplum
adds (`meta.author`, `meta.project`, `meta.compartment` and the like) are allowed.

## Set it up

1. **A ClientApplication for the connector**, with an AccessPolicy limited to the types the agent
   needs. Its access token is the connector's bearer token (`auth.kind: bearer`). An access token
   expires; renew the credential when it does.
2. **The connector**: the [FHIR connector](../connectors/fhir.md) with `base_url` set to
   your project's `…/fhir/R4`, the types in `allowed_resource_types`, and the interactions you
   allow (reads by default; add `create`, `update`, `patch` explicitly).
3. **Proven, if you run Medplum yourself**: `wal_level = logical` on Medplum's database, a
   `LOGIN REPLICATION` role, a publication on the history tables of the allowed types, and
   `witness: { enabled: true, server: medplum, author: ClientApplication/<id> }`. The steps are in
   [Proven on HAPI FHIR JPA and Medplum](../connectors/fhir.md#proven-l2-on-hapi-fhir-jpa-and-medplum).
   On a Medplum whose database you can't reach, writes are Checked.
4. **A policy.** Start from the [healthcare-fhir pack](../guides/healthcare-fhir-pack.md) or the
   example's `policy.yaml`: reads and Observation creates run and are checked, updates wait for a
   person (`require_approval`), everything else is refused.

## What each write gets

| Write | Checked (read-back) | Proven (Medplum's change log) |
|---|---|---|
| create, update, patch | yes: the version in `ETag` is read back and compared field by field | yes, when the witness is on: bound by the `versionId` Medplum reported, `meta.author` compared with `witness.author` |
| delete | yes: a later read must answer 410 or 404 | no: Medplum's answer names no version to bind |
| an update that changed nothing | the note `fhir_no_new_version`, and the comparison decides | no new version, so nothing to bind |
| a 5xx or a timeout | Can't check, unless one history version carries the action's binding token | no |

`verify: L1` in a rule accepts Checked and still records Proven where it runs; `verify: L2` makes a
write that only reaches Checked come out as Can't check, never Matches.

## Approvals

A rule with `decision: require_approval` holds the write until a person countersigns in
Console › Approvals (or the emailed link, or Slack). Nothing reaches Medplum before that. The
deed of the write names the approval, who decided and when. See [Approvals](../APPROVALS.md).

## Medplum Bots

Teams that automate with Medplum Bots can send a bot's writes through the same gate: the bot
calls `@deedglass/sdk` instead of `medplum.createResource`, with the Deedglass URL and an agent key
in the project's secrets.

```ts
import type { BotEvent, MedplumClient } from '@medplum/core';
import { Deedglass } from '@deedglass/sdk';

export async function handler(_medplum: MedplumClient, event: BotEvent) {
  const resource = event.input as { resourceType: string };
  const dg = new Deedglass({
    baseUrl: event.secrets['DEEDGLASS_SERVER']?.valueString,
    apiKey: event.secrets['DEEDGLASS_API_KEY']?.valueString,
  });
  const a = await dg.act('medplum.create', { resource_type: resource.resourceType, resource });
  return { action: a.id, status: a.status, check: a.witness?.verdict ?? 'none' };
}
```

The full handler, which waits for the deed to be sealed and logs a Doesn't match, is
`examples/medplum/bot/deedglass-gate-bot.ts`; `bot/run-local.ts` calls it on your machine without
deploying it. Bundle `@deedglass/sdk` into the bot's dist file (Medplum's bot layer supplies
`@medplum/core`, not this SDK); the SDK has no runtime dependency and needs Node 22.12 or newer.
The bot runtime must reach your Deedglass server over https. Because the connector makes the
write, Medplum records the connector's ClientApplication as `meta.author`, not the bot.

## Limits

- **Beta.** Built and tested against Medplum 5.2.6 on PostgreSQL 18.6; Proven reads Medplum 5.x's
  table layout only. Another layout gives the gap `fhir_schema_unknown` on writes that asked for L2.
- **Bearer tokens only in the example.** Deedglass doesn't renew a bearer token.
- **No transaction or batch Bundles** through the connector yet.
- **The action history stores arguments and results as sent**, including any clinical values,
  until you prune them; sealed deeds hold digests only. See [PHI mode](../PHI_MODE.md).
- The [FHIR connector's limits](../connectors/fhir.md#what-it-doesnt-do-yet) apply.

## Sources (checked 2026-10-20)

- https://www.medplum.com/docs/bots/bot-basics (the handler signature, `event.secrets`, the bot layer)
- https://www.medplum.com/docs/access/access-policies (`readonlyFields`)
- The Medplum behaviour above (a read-only field dropped from a create answered 201) is what
  `make medplum-example` observed on `medplum/medplum-server:5.2.6`.
