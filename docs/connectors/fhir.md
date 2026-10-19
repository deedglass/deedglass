# The FHIR connector

The `fhir` connector puts a FHIR R4 server's REST API behind Deedglass: one tool per interaction
(`read`, `vread`, `search`, `history`, `create`, `update`, `patch`, `delete`) on the resource types
you list, decided by your policy, and every write read back **at the version the server reported**
before the deed is sealed (Checked, L1). It is an adapter (ADR-105): the Gate, the policy engine,
the Witness and the deed format know nothing about FHIR.

Status: beta. Built and tested against HAPI FHIR JPA `v8.12.0-2` and a stand-in server
(`internal/connector/fhir/fhirtest`); see [what it doesn't do](#what-it-doesnt-do-yet). Use
synthetic data while you try it. The [guide](../guides/fhir-server.md) walks through a run.

## The boundary

- `allowed_resource_types` (1 to 64 types) is the hard boundary. Each tool's `resource_type` is an
  enum of that list; a call naming any other type is refused before a request is built, and no
  `request.fhir` facts are added, so a rule that allows on them doesn't match (attack case I10).
- `allowed_interactions` lists the tools that exist. The default is `read`, `vread`, `search` and
  `history`; add `create`, `update`, `patch` and `delete` explicitly. `delete` has side effect
  `destructive`.
- A path is built only from the type, an id matching FHIR's `[A-Za-z0-9\-.]{1,64}`, and
  `_history/<version id>`. Nothing else the agent sends enters the path. Search parameters go to
  the query string; `history` takes only `_since`, `_at` and `_count`.
- Egress rules apply as for every connector: `base_url` (and the token endpoint) must pass the
  network rules, `https` outside dev.

## Config

| Key | Default | What it does |
|---|---|---|
| `base_url` | required | The FHIR base, like `https://fhir.example.org/r4`. `/metadata` hangs off it. |
| `fhir_version` | `R4` | Only `R4` (4.0.1) is accepted. |
| `auth.kind` | required | `smart_backend`, `bearer`, or `none` (dev only). |
| `auth.token_url` | discovered | `smart_backend`: the token endpoint. Left out, it is read from `<base_url>/.well-known/smart-configuration`. |
| `auth.scopes` | derived | `smart_backend`: `system/<Type>.<letters>` per allowed type, with SMART v2 letters for the interactions (`c` create, `r` read and vread, `u` update and patch, `d` delete, `s` search and history). |
| `allowed_resource_types` | required | 1 to 64 FHIR type names. |
| `allowed_interactions` | `[read, vread, search, history]` | Which tools exist. `transaction` and `batch` are refused for now. |
| `if_match` | `when_known` | `required`: every `update` and `patch` must carry `if_match` (the version id the agent read). `when_known`: sent when the agent gives one. |
| `request_id_header` | `X-Request-ID` | The header that carries the action's binding token; `none` turns it off. |
| `server_tag_systems` | `[]` | `meta.tag` and `meta.security` systems your server adds on its own; the read-back ignores them. |
| `timeout_ms`, `max_response_bytes`, `redirects` | 10 s, 4 MiB, `none` | As for the REST connector. Writes never follow a redirect. |

`examples/connectors/fhir.yaml` is a working config for a local HAPI FHIR JPA server.

## What each request carries

- `Accept: application/fhir+json`. Writes send `Content-Type: application/fhir+json` (a JSON Patch
  sends `application/json-patch+json`) and `Prefer: return=representation`.
- `Deedglass-Action-Id: act_…` on every write.
- **The binding token** in `request_id_header`: the last 16 characters of the action id (the random
  part of its ULID). HAPI FHIR keeps only 16 characters of `X-Request-ID` (HC-P05), so the full
  `act_…` id would be cut to its timestamp and collide.
- `If-Match: W/"<if_match>"` when the agent sends `if_match`, and `If-None-Exist: <if_none_exist>`
  for a conditional create.

## Answers

| Server answer | Execution | Error code |
|---|---|---|
| 2xx | succeeded | |
| 400, 422 | failed | `upstream_error`, with the OperationOutcome's issue severities and codes (never its diagnostics, which can echo clinical values) |
| 401, 403 | failed | `upstream_auth_failed` (a cached SMART token is dropped) |
| 404, 410 | failed | `upstream_not_found` |
| 409, 412 | failed, never retried | [`fhir_version_conflict`](../api/errors.md#fhir_version_conflict): a stale `If-Match` (HAPI answers 409, the FHIR spec 412) |
| 5xx, timeout | unknown | `upstream_error`, `timeout` |

Reads are retried once on a connect error, 502, 503, 504 or a short 429; writes are never sent
twice.

## The check (Checked, L1)

After a write the connector reads back exactly the version the server reported (the `Location`
header's `_history/<vid>`, then `ETag: W/"<vid>"`, then the body's `meta.versionId`) with a
`vread`, and compares it with the resource the agent sent:

1. **Normalised first.** Dropped on both sides: `meta.versionId`, `meta.lastUpdated`,
   `meta.source`, a narrative whose `text.status` is `generated`, and (create) the server-assigned
   `id`. Dropped from what the server shows: `meta.tag` and `meta.security` codings of the
   `server_tag_systems`, unless the agent sent them.
2. **Compared field by field.** Fields only the server has are allowed (server-added). A field the
   agent set with another value, or missing, is **Doesn't match** with
   `fhir_field_changed` and the field's path (attack case I03). Numbers compare by value (`72`
   equals `72.0`).
3. **The known traps**, each with its own reason:
   - a conditional create answered 200 with an existing resource: this action created nothing, so
     **Doesn't match** with `fhir_conditional_matched` (I01);
   - an update whose version didn't move (HAPI keeps the version for an update that changes
     nothing): the note `fhir_no_new_version` naming the version, and the comparison decides
     (I02). Without `if_match`, the connector reads the current version just before the update to
     know it;
   - a delete: a later `read` must answer 410 (or 404);
   - a `patch`: the version is read back and the paths a JSON Patch adds, replaces or removes are
     checked; a FHIRPath Patch only for the version;
   - **a 5xx or a timeout:** the resource's history (the type's, for a create) since one second
     before the action started is searched for exactly one version whose `meta.source` ends in
     `#<binding token>`. HAPI writes that only when it stores request ids
     (`hapi.fhir.store_meta_source_information=SOURCE_URI_AND_REQUEST_ID`; the JPA starter's
     default is `NONE`). One match is compared as above; none or several is **Can't check** with
     `fhir_history_no_match` (I04).
4. A missing version, a failed read or an unreadable answer is a gap (Can't check), never Doesn't
   match. The method is `readback.fhir.<tool>`.

The reason catalog is [docs/witness/REASONS.md](../witness/REASONS.md).

## Credentials

**SMART Backend Services** (`auth.kind: smart_backend`) is generic OAuth 2.0 client credentials
with a signed JWT client assertion (RFC 6749 §4.4, RFC 7523, `private_key_jwt`); SMART discovery is
a thin layer on top. Store a `fields` credential with `client_id` and `private_jwk`:

```sh
$ export FHIR_CLIENT_ID=…            # from the authorization server's registration
$ deedglass connector credentials set fhir --kind fields \
    --field client_id=env:FHIR_CLIENT_ID --field private_jwk=file:client.jwk
```

- `private_jwk` is a private JWK on one line: RSA (RS384, at least 2048 bits) or EC P-384 (ES384),
  or a JWK Set of one or two keys while you rotate; the first key signs. The JWS is built with the
  standard library.
- The assertion carries `iss` and `sub` (the client id), `aud` (the token endpoint), `exp` 300 s
  ahead and a random `jti`. The token request asks for `auth.scopes`.
- The access token is kept in memory for at most min(300 s, `expires_in`), never written anywhere,
  and there is no refresh token. The key is opened from the vault for each token and never leaves
  the process.

`bearer` takes a static access token (a `bearer` credential with `token`).

## Policy facts: `request.fhir`

| Fact | What it holds |
|---|---|
| `request.fhir.interaction` | `read`, `vread`, `search`, `history`, `create`, `update`, `patch` or `delete` |
| `request.fhir.resource_type` | one of the allowed types |
| `request.fhir.id` | the resource id; `""` for create, search and a type's history |
| `request.fhir.conditional` | a create with `if_none_exist` |
| `request.fhir.if_match` | an update, patch or delete with `if_match` |
| `request.fhir.codes` | `system|code` pairs from the resource's `code` and `category` (never free text) |
| `request.fhir.profiles` | the resource's `meta.profile` URLs |
| `request.fhir.enriched` | true when the call built a request |

```yaml
rules:
  - id: fhir-reads
    match: { connector_type: fhir, side_effect: read }
    when: request.fhir.enriched
    decision: allow
  - id: no-delete-clinical
    match: { tool: ["fhir.delete"] }
    decision: deny
    reason: Clinical records are corrected, not deleted. Ask a clinician to mark it entered-in-error.
  - id: vitals-writes
    match: { tool: ["fhir.create", "fhir.update"] }
    when: request.fhir.resource_type == "Observation" && "http://terminology.hl7.org/CodeSystem/observation-category|vital-signs" in request.fhir.codes
    decision: allow
    verify: L1
```

## PHI mode

With [PHI mode](../PHI_MODE.md) on, a `fhir` action keeps the arguments `resource_type`, `id` and
`version_id`, and the result's `status`, `headers.etag`, `headers.location`,
`headers.last-modified`, `body.resourceType`, `body.id` and `body.meta.versionId`. Resources,
search parameters, patches and codes become digests once the action is sealed and checked.

## What it doesn't do yet

- **Proven (L2)** for Postgres-backed FHIR servers (HAPI, Medplum) and the `out_of_band`,
  `request_id_reused` and `fhir_extra_versions` findings: ticket HC-P05 (attack cases I05 to I08).
- **AuditEvent and Provenance export** (IHE BALP): ticket HC-P10.
- **Transaction and batch Bundles**: one resource per call.
- **A Medplum test matrix**: HAPI FHIR JPA only so far.
- **Patient references as salted digests** in `request.fhir`: needs the per-org salt (HC-P09's
  second step).
- FHIR R5 is never a separate code path; R6 will be a mapping when HL7 publishes it.

## Durability (DL-109)

| Part | Governed by | If it disappears | Replacement path |
|---|---|---|---|
| R4 REST interactions | HL7 (normative since 2018) | nothing in the core | `fhir_version` is config; R6 is a mapping |
| SMART Backend Services | HL7, on IETF OAuth 2.0 and RFC 7523 | the credential's discovery layer | generic client credentials with a JWT assertion stays |

## Sources (checked 2026-10-19)

- https://hl7.org/fhir/R4/http.html (interactions, `If-Match`, `If-None-Exist`, `_history`, 409 and 412)
- https://hl7.org/fhir/smart-app-launch/backend-services.html (client credentials, the assertion's claims, 5-minute `exp`)
- https://www.rfc-editor.org/rfc/rfc7523 (JWT client assertions)
- HAPI's behaviour above (409 for a stale `If-Match`, 200 for a matching conditional create, the
  same version for a no-change update, 16 characters of `X-Request-ID`) is what HC-P05's probe
  (`make fhir-spike`) and `make fhir-it` observed on `v8.12.0-2`.

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**FHIR** · beta · levels L0 L1

FHIR R4 reads and writes on the resource types you list, each write checked at the version the server reported.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `fhir.read` | read | L0 | Not read back |
| `fhir.vread` | read | L0 | Not read back |
| `fhir.search` | read | L0 | Not read back |
| `fhir.history` | read | L0 | Not read back |
| `fhir.create` | write | L0 L1 | The version the server reported, read back and compared field by field with the resource sent; an existing resource returned for a conditional create doesn't count as created. |
| `fhir.update` | write | L0 L1 | The version the server reported, read back and compared field by field with the resource sent; an update the server kept at the same version is noted. |
| `fhir.patch` | write | L0 L1 | The version the server reported, read back; the paths a JSON Patch adds, replaces or removes are checked. |
| `fhir.delete` | destructive | L0 L1 | The resource read again: it must answer 410 Gone (or 404). |

| Tool | What it does |
|---|---|
| `fhir.read` | Read the current version of one resource. |
| `fhir.vread` | Read one version of a resource. |
| `fhir.search` | Search resources of one type with FHIR search parameters. |
| `fhir.history` | The version history of one resource, or of a type when id is left out. |
| `fhir.create` | Create a resource. if_none_exist makes it a conditional create: the server creates nothing when a match exists. |
| `fhir.update` | Replace a resource with a new version (or create it at that id when the server allows it). |
| `fhir.patch` | Change parts of a resource with a JSON Patch (json_patch) or a FHIRPath Patch Parameters resource (fhirpath_patch). Send exactly one. |
| `fhir.delete` | Delete a resource. The server keeps its history; a later read answers 410 Gone. |

### Credentials

Kind `fields`:

- `client_id` (not secret): The client id the FHIR server's authorization server registered for this backend service.
- `private_jwk` (secret): The client's private key as a JWK (RS384 or ES384) or a JWK Set of one or two keys, on one line; the first key signs.

Kind `bearer`:

- `token` (secret): A static access token (auth.kind bearer).

### Setup

1. **Register a backend service.** On the FHIR server's authorization server, register a SMART Backend Services client with your public key and system scopes for the types you list. https://docs.deedglass.com/connectors/fhir
2. **Create the connector.**
   `deedglass connector create --config examples/connectors/fhir.yaml`
3. **Store the key.**
   `deedglass connector credentials set fhir --kind fields --field client_id=env:FHIR_CLIENT_ID --field private_jwk=file:client.jwk`
4. **Test the connection.**
   `deedglass connector test fhir`

### What it doesn't do

- FHIR R4 (4.0.1) only, JSON only.
- No transaction or batch Bundles yet; one resource per call.
- Checked (L1) only: Proven (L2) for Postgres-backed FHIR servers and AuditEvent or Provenance export aren't built yet.
- A patch is read back for the version and the JSON Patch add, replace and remove paths; a FHIRPath Patch only for the version.
- After a 5xx or a timeout the write is found only when the server stores request ids (meta.source); otherwise the action stays UNVERIFIABLE.

<!-- deedglass:generated end -->
