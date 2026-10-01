# MCP authorization

How MCP clients authenticate to the Gate, what Deedglass publishes so they can discover how, and
what it plans for your identity provider. Deedglass v1 accepts its own pre-issued API keys as bearer
tokens. It publishes RFC 9728 protected-resource metadata and answers with RFC 6750 challenges that
point to it, so OAuth-aware clients and gateways see a well-behaved resource server. It has no
authorization server of its own yet, and says so.

Code: `internal/identity/mcp.go` (`MCPAuth`, the metadata, the stub routes,
`StripInboundCredentials`), the bearer pipeline in `internal/httpx/server.go` (S09). The gateway
itself is S15; until then every authenticated MCP request answers "not enabled" (below).

## 1. v1: API keys as bearer tokens

The MCP 2026-07-28 revision makes authorization optional. Deedglass v1 uses the simplest form that
is still exact about who is calling: each agent's own API key (`dg_live_…` or `dg_test_…`,
[AGENTS.md](AGENTS.md)), sent as `Authorization: Bearer …` on every request. The key needs the
`actions:execute` scope, which agent keys have by default.

Configure your client with a static header. These snippets are **examples**: field names differ
between clients and versions, so check your client's own documentation.

Claude Code style (`.mcp.json`):

```json
{
  "mcpServers": {
    "deedglass": {
      "type": "http",
      "url": "https://api.deedglass.com/mcp/con_01k67c04kxevyex3pjyqe0t45d",
      "headers": { "Authorization": "Bearer ${DEEDGLASS_API_KEY}" }
    }
  }
}
```

Cursor style (`mcp.json`):

```json
{
  "mcpServers": {
    "deedglass": {
      "url": "https://api.deedglass.com/mcp/native",
      "headers": { "Authorization": "Bearer dg_live_…" }
    }
  }
}
```

- Prefer an environment variable over pasting the key into the file, where your client supports
  one. MCP config files get copied, synced and committed; a key in one is a key in all of them.
- One key per agent. Don't share a key between two agents: the deed would name the wrong one.
- `deedglass mcp snippet` (S15) will print the exact block for each client.
- **stdio (S14)** takes the key from the environment (`DEEDGLASS_API_KEY`), as the MCP spec says
  stdio transports SHOULD. stdio has no HTTP challenges and no metadata.

## 2. Protected-resource metadata

Three public documents, no key needed:

| URL | Describes |
|---|---|
| `GET /.well-known/oauth-protected-resource` | the API as a whole |
| `GET /.well-known/oauth-protected-resource/mcp/{connector}` | one connector's MCP endpoint, `/mcp/con_…` |
| `GET /.well-known/oauth-protected-resource/mcp/native` | the Gate's native MCP endpoint, `/mcp/native` |

The two MCP forms are path-inserted, as RFC 9728 §3.1 prescribes: the well-known prefix goes between
the host and the endpoint's path. With `DEEDGLASS_BASE_URL=https://api.deedglass.com`:

```http
GET /.well-known/oauth-protected-resource

200 {"resource":"https://api.deedglass.com","resource_name":"Deedglass",
     "bearer_methods_supported":["header"],
     "scopes_supported":["actions:execute","actions:read","deeds:read"],
     "resource_documentation":"https://docs.deedglass.com/mcp/authorization"}
```

```http
GET /.well-known/oauth-protected-resource/mcp/con_01k67c04kxevyex3pjyqe0t45d

200 {"resource":"https://api.deedglass.com/mcp/con_01k67c04kxevyex3pjyqe0t45d","resource_name":"Deedglass Gate",
     "bearer_methods_supported":["header"],
     "scopes_supported":["actions:execute"],
     "resource_documentation":"https://docs.deedglass.com/mcp/authorization"}
```

`/mcp/native` gets the same shape with `"resource":"https://api.deedglass.com/mcp/native"`.

- **`resource`** is the canonical URI of `DEEDGLASS_BASE_URL`: lower-case scheme and host, no
  default port, no trailing slash, then the endpoint's path. `HTTPS://API.Deedglass.com:443/` becomes
  `https://api.deedglass.com`.
- **Cacheable for an hour** (`Cache-Control: public, max-age=3600`), with
  `Access-Control-Allow-Origin: *` and `Cross-Origin-Resource-Policy: cross-origin`. These documents
  and the seal keys' `keys.json` are the only routes with a CORS header: public metadata is meant to
  be fetched from anywhere.
- **The same document for every well-formed connector id, with no database read.** The endpoint
  can't be used to learn which connectors exist, and connector names (which may be sensitive) never
  appear. A malformed id is `404 not_found`: `There's no MCP endpoint with that id. Connector ids look
  like con_01k67c04kxevyex3pjyqe0t45d.`
- **`authorization_servers` is absent, on purpose** (section 6).

## 3. The challenges

Every MCP endpoint (`POST /mcp/{connector}`, `POST /mcp/native`) runs `MCPAuth` and then S08's
bearer pipeline ([AGENTS.md](AGENTS.md) §5). Each bearer challenge names the endpoint's metadata and
the scope it needs, so a client can discover both from one failed request.

| Situation | Status and code | `WWW-Authenticate` |
|---|---|---|
| No key | 401 `authentication_required` | `Bearer realm="deedglass", resource_metadata="https://api.deedglass.com/.well-known/oauth-protected-resource/mcp/con_…", scope="actions:execute"` |
| A bad key (invalid, revoked, expired) | 401 `invalid_api_key`, `api_key_revoked` or `api_key_expired` | `Bearer realm="deedglass", error="invalid_token", error_description="The API key has been revoked. Create a new key in Console > Agents.", resource_metadata="…", scope="actions:execute"` |
| An org admin key, or an agent key without `actions:execute` | 403 `insufficient_scope` | `Bearer realm="deedglass", error="insufficient_scope", scope="actions:execute", resource_metadata="…"` |
| The agent is stopped or suspended, or the org is stopped | 403 `agent_stopped`, `agent_suspended` or `org_stopped` | none: it isn't a token problem. S15 turns it into an MCP tool error |

- `error_description` is the API message in printable ASCII with no `"` or `\` (RFC 6750 §3): `›`
  becomes `>`, a quote or backslash becomes `'`, `…` becomes `...`, and anything else outside
  printable ASCII is dropped.
- The bodies are the standard error envelope ([api/errors.md](api/errors.md)).
- Signed-intent errors are 403, never 401 ([INTENTS.md](INTENTS.md) §6): a 401 would make an
  OAuth-capable client start an authorization flow for a key that is fine.

## 4. Origin, methods and the stub

**Origin.** A request with an `Origin` header other than the origin of `DEEDGLASS_BASE_URL` is
refused before anything else runs, with 403 and a JSON-RPC error without an `id`:

```json
{"jsonrpc":"2.0","error":{"code":-32600,"message":"This origin is not allowed to call Deedglass MCP endpoints. Add it to mcp.allowed_origins if it should be."}}
```

This is the Streamable HTTP transport's protection against DNS rebinding: a web page can't use a
browser to reach a Gate on your network. A request without `Origin` (every non-browser MCP client)
passes. An `Origin` that is sent twice, or isn't a bare origin, is refused.

**Methods.** `GET` and `DELETE` on an MCP endpoint answer `405 method_not_allowed` with
`Allow: POST`, as 2026-07-28 prescribes for servers that don't serve older clients.

**The stub.** Until S15 mounts the gateway, every authenticated `POST` answers 404 with JSON-RPC
`-32601`, echoing the request's `id` (or `null`):

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"The MCP gateway isn't enabled on this server."}}
```

Try it:

```
$ curl -si -X POST localhost:8080/mcp/con_01k67c04kxevyex3pjyqe0t45d
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="deedglass", resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp/con_01k67c04kxevyex3pjyqe0t45d", scope="actions:execute"
…
```

(The `resource_metadata` host is `DEEDGLASS_BASE_URL`'s, not the request's.)

## 5. No token passthrough

`identity.StripInboundCredentials` deletes everything a client sent to authenticate to Deedglass
before a request is forwarded upstream:

- `Authorization`
- `Proxy-Authorization`
- `Cookie`
- every `Deedglass-*` header (`Deedglass-Intent`, `Deedglass-On-Behalf-Of`, `Deedglass-Session`, …)

Other headers (`Content-Type`, `Mcp-Method`, …) are kept. MCP forbids passing a client's token
through to another resource; S15 must call this on every forwarded request. Upstream servers get
the connector's own credentials from the vault (S16), never the client's.

## 6. Why `authorization_servers` is absent

The MCP spec says an HTTP server that implements MCP authorization MUST list at least one
authorization server in its metadata. Deedglass v1 authenticates with pre-issued API keys, which
MCP allows because authorization is optional, and it has no authorization server to list. Listing
one anyway (a placeholder, or Deedglass itself) would send OAuth clients into a flow that can't
succeed. So the field is omitted, a test asserts its absence, and this page says why.

v2 lists the org's own issuer there (section 8).

## 7. Notes for gateway builders (2026-07-28)

What the revision changes for anyone building in front of or behind the Gate. S15 implements these;
S09 only reserves the endpoints.

- **A stateless core.** No `initialize` handshake and no `Mcp-Session-Id`. Every request stands
  alone.
- **Mirrored request headers.** Every `POST` carries `MCP-Protocol-Version` and `Mcp-Method`;
  `tools/call`, `resources/read` and `prompts/get` also carry `Mcp-Name`. They must match the body:
  a mismatch is 400 with JSON-RPC `-32020` (`HeaderMismatch`). A value that isn't ASCII is sent in a
  `=?base64?…?=` form.
- **`server/discover`** is a new RPC for learning what a server offers.
- **Unknown methods** are 404 with JSON-RPC `-32601`; **unsupported protocol versions** are 400.
- **405 for `GET` and `DELETE`** is a SHOULD for servers that don't serve older clients. S15 decides
  whether the Gate also serves 2025-11-25 clients, which use those methods.
- **Invalid `Origin` is 403**, as above.

## 8. The v2 plan: your identity provider

Design only; nothing here is built. The full list is in [IDENTITY.md](IDENTITY.md), "OAuth and your
identity provider (planned)".

1. **The org registers an issuer** (`PUT /v1/org/oauth {issuer, audience}`). The metadata then lists
   it in `authorization_servers`, and MCP clients get tokens from the org's identity provider with
   `resource` set to the Gate's URL (RFC 8707).
2. **Deedglass validates JWT access tokens** (the RFC 9068 profile): `iss`, `aud` (which must be the
   Gate's resource URI), `exp` and scopes, with signatures checked against the issuer's JWKS,
   fetched through the SSRF-safe egress client and cached.
3. **Claims map to identities.** `client_id` or `azp` maps to a registered agent; `sub` and `iss`
   become the verified user for policies, replacing the unsigned on-behalf-of claim.
4. **Enterprise-Managed Authorization** (the MCP extension built on ID-JAG): a Deedglass token
   endpoint accepts ID-JAG assertions from allow-listed identity providers and mints short-lived
   agent tokens bound to both the agent and the user.
5. **Downstream calls** keep using vault credentials. Per-user downstream access through RFC 8693
   token exchange is v3.
6. **Never** token passthrough, and never a token minted for another audience.

## 9. Spec versions checked

| Spec | Version | Checked |
|---|---|---|
| Model Context Protocol | 2026-07-28, the current revision (modelcontextprotocol.io/specification/versioning) | 2026-10-01 |
| RFC 9728, OAuth 2.0 Protected Resource Metadata | April 2025 | 2026-10-01 |
| RFC 6750, Bearer Token Usage (§3, the challenge) | October 2012 | 2026-10-01 |
| RFC 8707, Resource Indicators for OAuth 2.0 | February 2020 | 2026-10-01 |
| RFC 8785, JSON Canonicalization Scheme | June 2020 | 2026-10-01 |
| RFC 8032, Ed25519 | January 2017 | 2026-10-01 |
| RFC 9207, Authorization Server Issuer Identification | March 2022: authorization servers SHOULD send `iss`; clients MUST validate it | 2026-10-01 |
| Client ID Metadata Documents | in MCP 2026-07-28: SHOULD; Dynamic Client Registration deprecated, MAY | 2026-10-01 |
| Enterprise-Managed Authorization | a stable MCP extension since 2026-06-18, `io.modelcontextprotocol/enterprise-managed-authorization` (github.com/modelcontextprotocol/ext-auth) | 2026-10-01 |
| draft-ietf-oauth-identity-assertion-authz-grant (ID-JAG) | -04, 2026-05-21 | 2026-10-01 |
| RFC 9421, HTTP Message Signatures | February 2024; Web Bot Auth's draft-ietf-webbotauth-httpsig-protocol-00 (2026-09-01) builds on it. Considered for signed intents and not used ([INTENTS.md](INTENTS.md)) | 2026-10-01 |

### Requirements adopted, and not adopted

| Requirement | Level | In v1 | Why |
|---|---|---|---|
| Serve RFC 9728 protected-resource metadata | MUST (when implementing authorization) | adopted | root and path-inserted, for every MCP endpoint |
| Point clients to the metadata: `resource_metadata` in the 401 challenge, or the well-known URI | MUST (one of the two) | adopted, both | `resource_metadata` on every bearer challenge from an MCP endpoint, and the path-inserted and root well-known documents |
| `scope` in challenges | SHOULD | adopted | `actions:execute` |
| Scope failures are 403 `insufficient_scope` with `scope` and `resource_metadata` | SHOULD | adopted | an org admin key on `/mcp/*` gets exactly this |
| No token passthrough | MUST | adopted | `StripInboundCredentials`, required in S15 |
| Validate `Origin`; 403 when invalid | MUST | adopted | DNS-rebinding protection; no `Origin` passes |
| 405 for `GET` and `DELETE` | SHOULD | adopted in the stub | S15 decides on 2025-11-25 clients |
| Validate the token's audience (RFC 8707) | MUST | n/a in v1 | Deedglass accepts only its own API keys and no OAuth tokens, so there is no audience to check. v2 checks `aud` against the Gate's resource URI |
| List at least one `authorization_servers` entry | MUST (when implementing authorization) | **not adopted** | v1 has no authorization server; inventing one would mislead clients (section 6). v2 lists the org's issuer |
| stdio takes credentials from the environment | SHOULD | adopted (S14) | `DEEDGLASS_API_KEY` |

## 10. What it doesn't do

- **No OAuth in v1.** No authorization server, no token endpoint, no JWT validation, no dynamic
  client registration and no Client ID Metadata Documents. Clients that can only do OAuth can't
  connect until v2.
- **An API key is a bearer secret.** Whoever holds it can call the Gate as the agent. Signed
  intents narrow this for SDK and REST callers ([INTENTS.md](INTENTS.md)); MCP clients can't sign
  intents in v1.
- **No per-user identity from MCP.** `Deedglass-On-Behalf-Of` is the agent's claim until v2 maps
  verified token claims to people.
- **The gateway isn't built.** Until S15, `/mcp/*` authenticates and then answers `-32601`.
- **The metadata doesn't say which connectors exist,** by design, so a client can't use it to
  check an id either.
