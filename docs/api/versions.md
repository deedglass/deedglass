# Versions

The Deedglass API is versioned by date. A version is a `YYYY-MM-DD` string, such as `2026-10-01`.
You choose one per request with the `Deedglass-Version` header, or let your org's pinned version
apply.

```sh
curl -s -H 'Deedglass-Version: 2026-10-01' http://127.0.0.1:8080/v1/version
```

## How the version is chosen

For a request to an endpoint that exists, Deedglass resolves the version after it checks your
credentials:

1. **You send `Deedglass-Version` once, and it is valid.** That version applies. Valid means it
   matches `YYYY-MM-DD` and this server knows it.
2. **You leave the header out.** For an authenticated request, your org's pinned version applies.
   For a public endpoint, such as `GET /v1/health`, the latest version applies (today
   `2026-10-01`).
3. **The header is malformed or names an unknown version.** The answer is 400
   `api_version_invalid`, with `param: "Deedglass-Version"`. A well-formed date the server doesn't
   serve, such as `2026-09-01`, gets the same answer as `2026-13-01`.
4. **You send the header twice.** The same 400 `api_version_invalid`, with "Send
   Deedglass-Version once."

Every response, errors included, carries `Deedglass-Version` with the version that applied.
Handlers read the same value, so the body and the header always agree.

A few answers come before the version check, and echo the version Deedglass can know at that
point: the header's value when it is a known version, else the latest. They are 404 `not_found`
and 405 `method_not_allowed` for paths with no endpoint, and refusals from the earlier checks
(body size, content type, credentials). A malformed header on those requests gets that earlier
error, not `api_version_invalid`.

`GET /v1/version` lists the versions a server serves, and the one it uses by default:

```json
{
  "object": "version",
  "version": "0.2.1",
  "commit": "5495f7e",
  "built_at": "2026-10-01T10:00:00Z",
  "go": "go1.27.1",
  "api_versions": ["2026-10-01"],
  "default_api_version": "2026-10-01"
}
```

## The rule

**A pinned version never changes behaviour.** Code you wrote against a version keeps getting the
same shapes, codes and semantics for as long as the server serves that version. Breaking changes
ship only under a new date.

Additive changes can reach every version, because well-written clients ignore what they don't
know.

**Breaking** (only under a new version):

- removing or renaming an endpoint, a field, a query parameter or a header
- changing a field's type, format or meaning, or making it nullable
- making an optional request field required, or adding a required one
- changing the error `code` or `type`, or the status, a given request gets
- changing the default of a parameter, such as the default `limit`
- changing an enum value, or what an existing value means
- changing idempotency, pagination or authentication semantics

**Additive** (can arrive in any version):

- a new endpoint
- a new optional request field or query parameter
- a new response field, or a new member in an envelope
- a new error code, on a path that could already fail
- a new enum value in a response field documented as open-ended, such as event types
- a new response header
- a more exact error `message` (messages are for people; branch on `code`)

## Changelog

### 2026-10-01

The first version. It sets the conventions every later endpoint follows:

- The error envelope, with 33 codes in six types ([Errors](errors.md)).
- `Deedglass-Request-Id` on every response, and `Deedglass-Version` resolved as above.
- `Idempotency-Key` on POST requests: 24 h, scoped to your org and principal, with every completed
  response stored and replayed ([API conventions](../API.md#idempotent-requests)).
- Strict JSON bodies, lists with opaque cursors, RFC 3339 UTC timestamps with milliseconds, and
  money in integer minor units.
- `GET /v1/health`, `GET /v1/version` and `GET /api/openapi.yaml`.

Each operation in `api/openapi.yaml` names the version that introduced it in
`x-deedglass-since`.

## What this doesn't do

- There is one version today, so there is nothing to upgrade to yet. How an org moves its pinned
  version will be documented with the second version.
- The version doesn't come from a query parameter or the URL. The `/v1` prefix stays the same
  across dated versions; only the header selects one.
- There is no per-endpoint version. One header applies to the whole request.
- The server doesn't guess. An unknown version is refused; it never falls back to a nearby date.
