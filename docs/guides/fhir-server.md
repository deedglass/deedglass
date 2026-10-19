# Use Deedglass with a FHIR server today

You can put a FHIR R4 server behind Deedglass's REST connector now, with no FHIR-specific code. You
write the tools by hand (one per resource type and interaction), your policy decides each call, and
after each write a read-back reads the resource again and compares the fields you name (Checked).
This page shows the setup, real output from a run against HAPI FHIR JPA, and what this setup can't
check. The native [FHIR connector](../connectors/fhir.md) (beta) removes most of the limits below:
generated tools, conditional headers, version-aware checks and SMART Backend Services. Nothing on
this page depends on it.

Use synthetic data while you try this. The limits below say why.

## The connector

`examples/connectors/fhir-hapi.yaml` has three tools on
`/fhir/Observation`: `get_observation`, `create_observation` and `update_observation`. Two things
make FHIR's JSON fit the REST connector:

- **`field`** sends a body arg under a different JSON key. Arg names are lowercase
  (`resource_type`, `value_quantity`); FHIR's keys aren't (`resourceType`, `valueQuantity`):

  ```yaml
  resource_type:  { in: body, field: resourceType, type: string, enum: [Observation], required: true }
  value_quantity: { in: body, field: valueQuantity, type: object, required: true }
  ```

- **Read-back paths** may use FHIR's mixed-case keys after the root:

  ```yaml
  readback:
    tool: get_observation
    args: { id: "{{result.body.id}}" }          # the id the server assigned
    expect:
      body.status: "{{args.status}}"
      body.valueQuantity.value: "{{args.value_quantity.value}}"
  ```

`allowed_methods` is `[GET, POST, PUT]` and `allowed_path_prefixes` is `[/fhir/Observation]`, so
no tool can reach another resource type or send a DELETE.

## A policy

```yaml
rules:
  - id: observation-reads
    match: { tool: ["fhir.get_observation"] }
    decision: allow
  - id: no-entered-in-error
    match: { tool: ["fhir.update_observation"] }
    when: args.status == "entered-in-error"
    decision: deny
    reason: Agents don't mark an Observation entered-in-error. Ask a clinician to correct it in the record system.
  - id: observation-writes
    match: { tool: ["fhir.create_observation", "fhir.update_observation"] }
    decision: allow
    verify: L1
```

`verify: L1` means a write counts as checked only when the read-back agrees.

## What a run looks like

From `make fhir-it` (HAPI FHIR JPA `v8.12.0-2` in Docker, a synthetic patient, 2026-10-19):

```text
$ deedglass act fhir.update_observation --args '{… "status":"final" …}'
✓ executed · fhir.update_observation · 44 ms
  action   act_01m58nq2jthtfkkv8qdejqts5b
  check    Checked · Matches · Read back fhir.get_observation: status is "final" and valueQuantity.value is 74.
  record   deed_01m58nq2… · seq 9 · sealed

$ deedglass act fhir.update_observation --args '{… "status":"entered-in-error" …}'
✕ denied · fhir.update_observation
  Blocked by policy "no-entered-in-error": Agents don't mark an Observation entered-in-error. Ask a clinician to correct it in the record system.
  action   act_01m58nq2n3w3wc3h325fmd0a7n
  record   deed_01m58nq2… · seq 10 · sealed
  Next: change the rule in Console › Policies, or ask an admin to run it by hand.

$ deedglass act fhir.update_observation --args '{"id":"1001","resource_id":"not-1001", …}'
✕ failed · fhir.update_observation · 11 ms
  upstream_error · The service refused the request (HTTP 400). Check the arguments.
  action   act_01m58nq2nepfspqnx9e0rjvhzh
  record   deed_01m58nq2… · seq 11 · sealed
  Next: fix the cause in the error, then send the action again with a new Idempotency-Key.
```

The denied call never reached HAPI: the test reads the Observation's `_history` afterwards and finds
only the two versions Deedglass let through. If the server answers a write with success but stores
something else, the check says **Checked · Doesn't match** and names each field
(`internal/connector/rest/fhir_test.go` shows this against a stand-in server).

To run it yourself: `make fhir-up` starts HAPI on a free localhost port and prints its base URL; put
that URL in the example's `base_url`, then `deedglass connector create --config …`. `make fhir-it`
runs the whole test above and removes the container.

## What this setup can't do

- **The tools are hand-written.** One tool per resource type and interaction, with the fields you
  list. Nothing is generated from the server's CapabilityStatement.
- **No conditional headers.** The REST connector can't send `If-None-Exist` (conditional create) or
  `If-Match` (version-aware update). Header args must start with `X-`, and the connector sets
  `Content-Type: application/json` itself (HAPI accepts it; some servers want
  `application/fhir+json`).
- **The read-back compares fields, not versions.** It doesn't know FHIR's rules: a conditional
  create answered 200 (nothing created), an update the server kept as a no-change version, or a
  write that landed on a different version can still read back as Matches when the fields agree.
- **Updates are whole-resource PUTs.** The agent sends every field it wants kept, and the id twice
  (in the path and in the body); the server refuses a mismatch.
- **No transaction or batch Bundles**, no `_history` lookup after a 5xx (a 5xx leaves the action
  unknown, Can't check), and no Proven checks: the FHIR server writes its own database, not
  Deedglass.
- **Static credentials only.** `bearer`, `header`, `basic` or `query`; no SMART Backend Services
  token exchange.
- **The action history stores arguments and results as sent**, including clinical values, until
  you prune them (`deedglass ledger prune`). Sealed records hold digests only.

## With the native connector

The [`fhir` connector](../connectors/fhir.md) replaces the hand-written tools with one tool per
interaction on the types you list (`examples/connectors/fhir.yaml`), sends `If-None-Exist` and
`If-Match`, and reads each write back at the version the server reported. From `make fhir-it`
(`TestFHIRConnectorLive`, the same HAPI image, 2026-10-19), after a create of the same Observation:

```text
$ deedglass act fhir.create --args '{… "if_none_exist":"identifier=urn:deedglass:test|…" …}'
✕ executed · fhir.create · 6 ms
  check    Checked · Doesn't match · fhir.create reported success, but the conditional create matched an existing resource ("Observation/1006 version 3"), so this action created nothing.
           field      expected             observed
           created    a new Observation    Observation/1006 version 3
  record   deed_01m58wpp… · seq 16 · sealed
  Next: deedglass actions show act_01m58wppma44zg9541a9n65v97 · https://docs.deedglass.com/witness/mismatch

$ deedglass act fhir.update --args '{… "if_match":"1" …}'
✕ failed · fhir.update · 4 ms
  fhir_version_conflict · The server refused the write because the resource changed since the version you sent (HTTP 409). Nothing was written. Read it again and send the new version id.
  action   act_01m58wppmy8p3097pq3m5x46rp
  record   deed_01m58wpp… · seq 17 · sealed
  Next: fix the cause in the error, then send the action again with a new Idempotency-Key.
```

HAPI answered the conditional create with 200 and the existing resource, and wrote nothing; the
REST connector's field-only read-back would have called that Matches. The same run shows an update
that changed nothing kept at HAPI's version with the note `fhir_no_new_version`, a create of an
Encounter (not in `allowed_resource_types`) denied before any request left, and HAPI's history
holding exactly the versions Deedglass let through.

## Sources (checked 2026-10-19)

- https://hl7.org/fhir/R4/http.html (create, update, `If-None-Exist`, `If-Match`, `_history`)
- https://hub.docker.com/r/hapiproject/hapi (the image `scripts/fhir.sh` pins by digest)
- The HAPI behaviour above (references checked on write, a body id that differs from the URL refused
  with 400, `application/json` accepted) is what `make fhir-it` observed on `v8.12.0-2`.
