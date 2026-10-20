# Medplum example

An agent-style client and a Medplum Bot write to a self-hosted Medplum server through the
Deedglass gate. Each write is decided by a policy, read back at the version Medplum reported,
checked against Medplum's own change log (Proven), and sealed as a deed. Synthetic data only.

| Scenario | What happens | What Deedglass says |
|---|---|---|
| (a) a normal create | Medplum stores the Observation the agent sent | Proven · Matches |
| (b) a create with a `note` | Medplum answers 201, but the project's AccessPolicy marks `Observation.note` read-only, so the stored Observation has no note | Proven · Doesn't match, naming `note` |
| (c) an update | a rule says a person countersigns first; nothing reaches Medplum until then | pending approval, then Proven · Matches |
| (bot) a Medplum Bot handler | the bot calls the gate instead of `medplum.createResource` | Matches |

Scenario (b) isn't staged: it is Medplum 5.2.6's own behaviour with an AccessPolicy that has
`readonlyFields`. The agent's result looks like success; only reading the stored version shows the
difference. Deedglass doesn't look at or change clinical content; it records what was sent and
compares it with what Medplum stored.

## Run it

You need Docker, Node.js 24, Go, and the TypeScript SDK built once (`make sdk-ts`). The images
(Medplum 5.2.6, PostgreSQL 18.6, Redis 7.4.6, pinned by digest) take about 1.6 GB on disk.

```sh
make medplum-example                  # or: examples/medplum/run.sh
```

It starts Medplum and its database in Docker, starts Deedglass from this tree on
`127.0.0.1:8094`, sets everything up, runs the agent and the bot, and removes it all afterwards.
Every secret is made up for the run and kept in `examples/medplum/.env` and `.env.keys` (0600,
git-ignored).

```sh
KEEP=1 examples/medplum/run.sh        # leave Medplum and Deedglass running (prints how to stop them)
APPROVE=manual examples/medplum/run.sh  # countersign (c) yourself: deedglass approve <apr_…> --as owner@clinic.test
examples/medplum/run.sh down          # remove the containers, volumes and state
make medplum-example-it               # the same run as a Go test that checks every outcome below
```

Ports: Medplum `MEDPLUM_PORT=8103`, its database `MEDPLUM_DB_PORT=55441`, Deedglass's database
`LEDGER_DB_PORT=55442`, Deedglass `DEEDGLASS_PORT=8094`. Set any of them to move it.
`DEEDGLASS_BIN` uses a deedglass binary you already have instead of building one.

## What a run looks like

From `make medplum-example` on 2026-10-20 (ids shortened):

```text
✓ Medplum · project 1041105b-… · AccessPolicy/283d123d-… (Observation.note read-only) · ClientApplication/8ebf1819-… · Patient/2f7f420a-…
✓ Deedglass · connector medplum (con_01m5c9z5…) · Proven on · agent charting-agent (agt_01m5c9z5…)

$ node examples/medplum/agent.ts
✓ (a) · medplum.create · executed · Proven · Matches
  The server's database committed Observation/5d827e44-… version eeb81943-… in this action's transaction, as medplum.create sent it.
✕ (b) · medplum.create · executed · Proven · Doesn't match
  The server's database committed Observation/2aca30b2-… version 109fbce1-… with note different from what medplum.create sent.
  L1 fhir_field_changed: medplum.create reported success, but Observation/2aca30b2-… version 10… shows note as null, not [{"text":"Synthetic note: measured after a short walk."}].
  L2 version_field_changed: The server's database committed Observation/2aca30b2-… version 109fbce1-… with note different from what medplum.create sent.
◐ (c) · medplum.update · pending approval · apr_01m5c9z6… · a person decides in Console › Approvals
✓ (c) · medplum.update · executed · Proven · Matches
  The server's database committed Observation/5d827e44-… version 7f85c639-… in this action's transaction, as medplum.update sent it.

$ node examples/medplum/bot/run-local.ts
✓ (bot) · medplum.create · executed · Matches

$ deedglass actions list --connector medplum
TIME     AGENT         TOOL                    DECISION   WITNESS             LATENCY
0s ago   charting-age… medplum.create          allow      ✓ L2 · VERIFIED     11 ms
1s ago   charting-age… medplum.update          approval   ✓ L2 · VERIFIED     22 ms
1s ago   charting-age… medplum.create          allow      ✕ L2 · MISMATCH     19 ms
2s ago   charting-age… medplum.create          allow      ✓ L2 · VERIFIED     28 ms
```

## The files

| File | What it is |
|---|---|
| `compose.yaml` | Medplum 5.2.6 on PostgreSQL 18.6 (`wal_level=logical`) with Redis, and Deedglass's own database |
| `run.sh` | the whole run: containers, the witness role and publication, Deedglass, setup, agent, bot, cleanup |
| `setup.ts` | on Medplum: the AccessPolicy, a ClientApplication, a synthetic Patient; on Deedglass: the `medplum` fhir connector, an agent and its key |
| `policy.yaml` | reads and Observation creates run and are checked; updates wait for a person; everything else is refused |
| `agent.ts` | the agent-style client: scenarios (a), (b) and (c) with `@deedglass/sdk` |
| `bot/deedglass-gate-bot.ts` | a Medplum Bot handler that writes through the gate |
| `bot/run-local.ts` | calls that handler on your machine, as Medplum would, without deploying it |
| `example_test.go` | `TestMedplumExample` (`make medplum-example-it`); skips without Docker, not in CI |

## With a Medplum you already run

Use synthetic data, or a project with no real patients, while you try this.

1. **A client for the connector.** In your Medplum project, create a ClientApplication for
   Deedglass (Admin › Project › Clients) with an AccessPolicy limited to the types the agent
   needs. Its access token is the connector's bearer token.
2. **The connector.** Save as `medplum.yaml` and run `deedglass connector create --config medplum.yaml`:

   ```yaml
   type: fhir
   name: medplum
   description: Our Medplum project. Observations only; no deletes.
   config:
     base_url: https://medplum.example.org/fhir/R4
     auth: { kind: bearer }
     allowed_resource_types: [Observation, Patient]
     allowed_interactions: [read, vread, search, history, create, update, patch]
   ```

   Then store the token: `deedglass connector credentials set medplum --kind bearer --field token=env:MEDPLUM_TOKEN`.
   An access token expires; renew the credential when it does.
3. **Proven (optional).** On Medplum's database: `wal_level = logical`, a role with `LOGIN
   REPLICATION`, and a publication on the history tables of the allowed types
   (`"Observation_History"`, `"Patient_History"`). Add
   `witness: { enabled: true, server: medplum, author: ClientApplication/<id> }` to the config and
   `--field witness_dsn=env:MEDPLUM_WITNESS_DSN` to the credential.
   [The connector reference](../../docs/connectors/fhir.md#proven-l2-on-hapi-fhir-jpa-and-medplum)
   has every step. Without it, writes are Checked (the read-back); scenario (b) is caught either way.
4. **A policy.** Start from `policy.yaml` here or the healthcare-fhir pack
   ([guide](../../docs/guides/healthcare-fhir-pack.md)), then point your agent at Deedglass with
   an agent key.

The page [Medplum and Deedglass](../../docs/integrations/medplum.md) explains the setup and
what is and isn't checked.

## Medplum Bots

`bot/deedglass-gate-bot.ts` is a bot handler that sends its write through the gate:

```ts
const dg = new Deedglass({
  baseUrl: event.secrets['DEEDGLASS_SERVER']?.valueString,
  apiKey: event.secrets['DEEDGLASS_API_KEY']?.valueString,
});
const a = await dg.act('medplum.create', { resource_type: resource.resourceType, resource });
```

Put `DEEDGLASS_SERVER` (an https URL your bot runtime can reach) and `DEEDGLASS_API_KEY` (an agent
key, one agent per bot) in the project's secrets, and bundle `@deedglass/sdk` into the bot's dist
file. A rule that needs a person returns `pending_approval`; the write runs when someone
countersigns. The bot's own Medplum client isn't used for the write, so the connector's
ClientApplication is the author Medplum records.
