# Identity: people, sign-in, sessions and roles

This page covers the human half of identity: the people who invite teammates, stop agents, change
policies and countersign risky actions. Agents and API keys (S08, [Agents and keys](#agents-and-keys))
and signed intents and on-behalf-of (S09) extend it. The model below shows how the pieces fit; the
details live on their own pages:

- [AGENTS.md](AGENTS.md): agents, API keys, scopes, rolling, revoking, admin keys
- [STOP.md](STOP.md): suspend, Stop and Stop all
- [INTENTS.md](INTENTS.md): signed intents, the normative spec
- [MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md): how MCP clients authenticate to the Gate

## The whole model

```
org_  (Acme)                                        every row below carries org_id; RLS keeps orgs apart
├── people
│   ├── usr_   a person: one email, global, grants nothing by itself
│   │   └── mem_   (org, user, role, status)         what the person may do in this org
│   └── ses_   one person, one active org            cookie + CSRF token; 12 h idle, 7 days at most
├── org admin keys   key_  dg_live_…, scopes {admin}  scripts and the CLI; act as Admin, never Owner
└── agents   agt_  name, labels, budgets, owner, status, intent_mode
    ├── API keys      key_  dg_live_… / dg_test_…   which agent is calling; at most 10 active
    └── intent keys   kid   ed25519:…               what the agent's runtime meant; at most 2

each call from an agent key
    Authorization: Bearer dg_live_…                 the agent (and its org)
    Deedglass-Intent                                the signed tool, args digest, nonce and window
    Deedglass-On-Behalf-Of, Deedglass-Session       the person and the session it serves
```

- **People act through sessions; agents act through keys.** A Console session is checked against
  the person's live role on every request. An API key is checked against its scopes; an org admin
  key counts as Admin.
- **An API key says which agent; a signed intent says what it meant.** An intent key never leaves
  the agent's runtime, so a leaked API key alone can't act for an agent that requires intents.
- **On-behalf-of says whom the agent serves.** It is an opaque reference the agent sends; signed
  in the intent, it is bound to the agent's key. Deedglass doesn't verify the person until v2.

## 1. People, orgs and memberships

```
            ┌──────────── org_ (Acme) ─────────────┐
 usr_ ──────┤ mem_  role=owner   status=active     │──── ses_ (active org = Acme)
 (a person, │ mem_  role=viewer  status=invited    │
 one email) └──────────────────────────────────────┘
```

- **A user is a person**: one row per email address, global across orgs. A user row grants nothing.
- **A membership is access**: `(org, user, role, status)`. What someone may do is read from their
  live membership in the session's org on every request, so a role change or a removal applies to
  the next request.
- **A session** belongs to one person and one active org. Switching org issues a new session.
- **Signup is closed.** Orgs are created with `deedglass org create`. People join by invite
  (`DEEDGLASS_SIGNUP=invite`, the default); `closed` refuses new and pending invites too.

| Capability | Viewer | Admin | Owner |
|---|---|---|---|
| Read everything in the org | ✓ | ✓ | ✓ |
| Create or change agents, keys, policies, connectors (S08+) | — | ✓ | ✓ |
| Stop an agent · Stop all agents (S08) | — | ✓ | ✓ |
| Start all agents again (S08) | — | — | ✓ |
| Invite or remove viewers and admins | — | ✓ | ✓ |
| Invite owners · change roles · remove owners | — | — | ✓ |
| Change the org's name and time zone | — | ✓ | ✓ |

Anyone trusted to pull the brake may pull it; only an owner releases it.

Every route declares who may call it (`Auth`) and the lowest role (`MinRole`); the server refuses to
start with a route that doesn't. A role below `MinRole` gets 403 `insufficient_role`:

```json
{ "error": { "type": "permission_error", "code": "insufficient_role",
  "message": "Your role in Acme is Viewer, and inviting people needs Admin. Ask an owner to change your role.",
  "param": null, "request_id": "req_…", "doc_url": "https://docs.deedglass.com/errors/insufficient_role" } }
```

## 2. Signing in

There are no passwords. You type your work email; Deedglass emails one link and one 6-digit code.

```mermaid
sequenceDiagram
    participant B as Browser
    participant D as Deedglass
    participant W as Mail worker
    B->>D: POST /v1/auth/login {email}
    Note over D: validate · IP and email limits · queue · no database read
    D-->>B: 202 login_request (the same for every address)
    W->>D: find the person · pick their org · store the token and code hashes
    W-->>B: email: /auth/confirm#t=… and a code
    B->>D: GET /auth/confirm (the page; the token stays in the browser)
    B->>D: POST /v1/auth/preview {token}
    D-->>B: whose account, which org (nothing used)
    B->>D: POST /v1/auth/callback {token} (after the person clicks Sign in)
    D-->>B: 200 me + Set-Cookie (session, CSRF)
```

- **The answer never says whether an account exists.** `POST /v1/auth/login` returns 202 with the
  same body for every well-formed address, and the handler doesn't touch the database (a test fails
  if it does). A worker looks the person up afterwards and sends nothing for an unknown address.
- **The token lives in the URL fragment** (`/auth/confirm#t=…`). Browsers never send the fragment to
  servers, so it never reaches access logs, proxies or `Referer` headers. Email security scanners
  that open links get a page and nothing else: opening the page uses nothing.
- **The preview stops login CSRF.** The page first shows "Sign in to Acme · You're signing in as
  priya@acme.com." Someone who mails you their own link can't sign you into their org without you
  seeing whose account it is.
- **A token works once**, only while it is unused, not replaced by a newer email and not expired
  (sign-in links after 15 minutes, invites after 7 days). Using it is one atomic `UPDATE … WHERE
  used_at IS NULL …`; of two clicks racing, exactly one wins.
- **The code** is for signing in on another device. It belongs to the newest email only, allows 5
  attempts (counted in the same statement that reads it), and is stored as
  `sha256(pepper ‖ "deedglass/login-code/v1\n" ‖ token id ‖ "\n" ‖ code)`, so a leaked hash can't be
  brute-forced without the pepper. Every miss gets the same 400 `login_code_invalid`.
- **The landing org** is the membership you used most recently (ties: the one you joined first).
  `POST /v1/auth/switch-org` moves to another.

Tokens are 32 random bytes (base64url, 43 characters); only their sha256 is stored.

## 3. Sessions and cookies

| | Session cookie | CSRF cookie |
|---|---|---|
| Name | `__Host-deedglass_session` | `__Host-deedglass_csrf` |
| Value / stored as | 32 random bytes / `sessions.token_hash` (sha256) | 32 random bytes / `sessions.csrf_hash` (sha256) |
| Attributes | `Secure; HttpOnly; SameSite=Lax; Path=/`, no `Domain`, `Max-Age` to absolute expiry | `Secure; SameSite=Strict; Path=/`, readable by the page, same `Max-Age` |

- **Lifetimes:** a session ends after 12 hours without a request, and 7 days after sign-in in any
  case. `last_seen_at` and the idle expiry are written at most every 5 minutes.
- **A fresh session id and fresh secrets at every sign-in and org switch**, so nobody can fix a
  session id for you.
- **Logout:** `POST /v1/auth/logout` ends this session; `{"everywhere": true}` ends every session of
  yours in every org. Removing a member ends their sessions in that org at once.
- **Dev over http:** `__Host-` cookies need `Secure`, and browsers differ on Secure cookies over
  `http://localhost`. With `DEEDGLASS_ENV=dev` and an `http` base URL on `localhost` or `127.0.0.1`,
  the cookies are `deedglass_session` and `deedglass_csrf` without `Secure` (serve logs this once at
  startup). Prod already refuses a non-https base URL.

## 4. CSRF and cross-origin protection

Three independent layers protect every session request that changes something (POST, PUT, PATCH,
DELETE):

1. **Cross-origin protection** (Go's `http.CrossOriginProtection`): a browser request marked
   `Sec-Fetch-Site: cross-site`, or with an `Origin` other than this server's, is refused.
2. **A double-submit token bound to the session:** the `Deedglass-CSRF` header must equal the CSRF
   cookie, and its sha256 must equal the session's stored hash (both compared in constant time).
   `GET /v1/me` returns the token for scripts.
3. **SameSite:** `Lax` on the session cookie, `Strict` on the CSRF cookie.

A refusal is 403 `csrf_failed`. API keys (`Authorization: Bearer`, S08) never use cookies, so they
never need the header. No route sends `Access-Control-Allow-Origin` except the public key and
metadata documents.

## 5. Invites, members and the last owner

```http
POST /v1/org/members      {"email":"sam@acme.com","role":"viewer"}   → 201 membership (status invited) + deed
PATCH /v1/org/members/mem_…  {"role":"admin"}                        → 200 (owners only)
DELETE /v1/org/members/mem_…                                         → 200 (status removed)
```

- Admins invite and remove viewers and admins; only owners invite owners, change roles or remove
  owners. Removing yourself is leaving.
- Inviting someone still pending sends a fresh link and voids the old one (200, same membership).
  Inviting an active member is 409 `conflict`.
- **An org always keeps at least one active owner.** Every membership change takes the org row lock
  first, then counts owners, so two owners demoting each other at the same moment can't both
  succeed: one gets 409 `last_owner`. A database trigger (`DG005`, checked at commit) is the second
  layer. The concurrent case runs 100 times in the integration tests.

## 6. Admin deeds and admin events

**A deed is sealed for every change to who can act, or what they may do.** Signing in and out are
security events, not changes: they go to `admin_events` without a deed.

| Action | Actor → target | Sealed | Data (only its digest enters the deed) |
|---|---|---|---|
| `org.created` | operator → org | yes | `{name_digest, owner}` |
| `org.updated` | user → org | yes | `{fields, timezone}`, plus `retention_days` (null for the default) and `approvals` (all three settings) when sent (S26) |
| `member.invited` | user → membership | yes | `{user, role}` |
| `member.joined` | invitee → membership | yes | `{role}` |
| `member.role_changed` | user → membership | yes | `{from, to}` |
| `member.removed` | user → membership | yes | `{role, self}` |
| `login_link.issued` | operator → user | yes | `{purpose, via: "cli"}` |
| `user.signed_in` | user → session | no | `{via: link\|code\|invite\|org_switch}` |
| `user.signed_out` | user → session | no | `{everywhere}` |

No email address ever appears in a deed: deeds name ids. `deedglass deeds list --kind admin` shows
them, and an export checks offline with `deedglass verify`.

## 7. Self-hosting without email

Without `DEEDGLASS_RESEND_API_KEY`, dev prints each email to the server's log and prod sends none
(serve warns once). Operators issue links from the database directly:

```
$ deedglass org create --name "Acme" --owner-email priya@acme.com
✓ Org created · Acme · org_01k6h7kp6msmzrhdzwtcr3jnkp
✓ Owner · priya@acme.com · usr_01k6c9kb2zcjsyn7b8g2k1vqvp
✓ Deed sealed · org.created · deed_01k6vrddq2atgb2023qvhhct3g

  Sign-in link for priya@acme.com (works once, expires in 15 minutes):
  http://127.0.0.1:8080/auth/confirm#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc

$ deedglass org login-link --email sam@acme.com
✓ Sign-in link issued · sam@acme.com · Acme
✓ Deed sealed · login_link.issued · deed_01k6…
  https://app.deedglass.com/auth/confirm#t=…
  Anyone with this link can sign in as sam@acme.com for 15 minutes. Send it privately.
```

A pending invitee gets an invite link (7 days) from `org login-link`. In prod, `org create` emails
the owner unless you pass `--print-link`.

## Rate limits

| Limit | Key | Budget | Result |
|---|---|---|---|
| Sign-in requests | client IP | 20 an hour | 429 `rate_limited` with `Retry-After` |
| Sign-in requests | sha256(pepper ‖ email) | 5 an hour | 429, the same for known and unknown addresses |
| Tokens created | user, counted in the database | 5 an hour | the worker sends nothing more |
| Failed previews, callbacks and codes | client IP | 30 an hour | 429 |
| Code attempts | token | 5 in all | the token stops working |

The client IP is the one S03's trusted-proxy rules resolve (`DEEDGLASS_TRUSTED_PROXIES`). The IP,
email and failure limits live in each server process: behind N instances a client can get up to N
times the budget. The per-user token cap is counted in the database and holds across instances.

## Agents and keys

Agents are the other half of identity (S08). Each has an immutable name, labels, budgets, an
owner and its own API keys (`dg_live_…` / `dg_test_…`, shown once, rolled with an overlap, revoked
at once). Org admin keys let scripts manage agents; they act as Admin, never as Owner.

- [AGENTS.md](AGENTS.md): agents, the key format, scopes, rolling, revoking, `whoami`, admin keys,
  the order of the bearer checks and the failed-auth limiter.
- [STOP.md](STOP.md): suspend and stop, Stop all and who can start agents again, how a stop reaches
  every instance within 2 seconds, and what it doesn't do.
- [INTENTS.md](INTENTS.md): signed intents (S09), the format, the checks in order, modes, intent
  keys, nonces, and what they don't do.
- [MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md): API keys as bearer tokens for MCP clients, the
  protected-resource metadata, the challenges and the v2 plan.

People and keys meet in one place: a Console session is checked against the role table above; an
API key is checked against its scopes, and an org admin key counts as Admin.

## Headers

Every header that carries identity or shapes how a request is checked:

| Header | Who sends it | What checks it |
|---|---|---|
| `Authorization: Bearer dg_live_…` | agents, scripts and the CLI | the bearer pipeline ([AGENTS.md](AGENTS.md) §5): strict parse, constant-time hash, revoked, expired, Stop, scopes. On `/mcp/*`, the challenges name the metadata ([MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md)) |
| `Cookie: __Host-deedglass_session=…; __Host-deedglass_csrf=…` | the Console, in a browser | the session middleware: hash lookup, idle and absolute expiry, the live membership and role (section 3) |
| `Deedglass-CSRF` | the Console, on every request that changes something | equal to the CSRF cookie and to the session's stored hash, in constant time (section 4); API keys never need it |
| `Deedglass-Version` | any client, optional | a known dated version, sent once (`400 api_version_invalid` otherwise); without it, the org's pinned version ([API.md](API.md)) |
| `Idempotency-Key` | clients, on `POST` | 1 to 255 printable ASCII characters; a replay returns the stored answer; a different body is `422 idempotency_conflict` ([API.md](API.md)) |
| `Deedglass-Intent` | the agent's runtime, through an SDK | the intent checker, by the agent's mode ([INTENTS.md](INTENTS.md) §6 and §7) |
| `Deedglass-On-Behalf-Of` | the agent, optional | the OBO grammar on every key-authenticated request, then the signed intent when there is one (below) |
| `Deedglass-Session` | the agent, optional | the same as `Deedglass-On-Behalf-Of` |
| `X-Request-Id` | any client, optional | logged next to Deedglass's own `req_…` as `client_request_id` when it is at most 64 printable ASCII characters; ignored otherwise |

Every response carries `Deedglass-Request-Id` (`req_…`) and the resolved `Deedglass-Version`.
Before anything is forwarded upstream, the Gate removes `Authorization`, `Proxy-Authorization`,
`Cookie` and every `Deedglass-*` header (`StripInboundCredentials`, S15).

## Keys and rotation

| Key | Format | Stored as | Rotation |
|---|---|---|---|
| Session | 32 random bytes, base64url, in the `__Host-` cookie | `sha256` of the token | a fresh session at every sign-in and org switch; ends after 12 hours idle or 7 days; logout ends one or all |
| API key (agent) | `dg_live_<8-char prefix>_<32-char secret>`, 49 characters | the prefix and `sha256(pepper ‖ secret)` | **roll with an overlap** (0 to 7 days, 24 hours by default): the old key works until the overlap ends, then answers `api_key_expired`; revoke ends it on every instance within 2 seconds ([AGENTS.md](AGENTS.md)) |
| Org admin key | the same format, scopes exactly `{admin}` | the same | created by a signed-in admin or the operator, never by another admin key; revoke and create a new one |
| Intent key | public `ed25519:<43 base64url>`; private PKCS#8 PEM, mode 0600, on the agent's machine | the public key and its `kid` in `agents.intent_keys` | **add, deploy, remove**: register the new key (at most two at once), deploy it to the runtime, then remove the old one ([INTENTS.md](INTENTS.md) §8) |
| Seal key | an Ed25519 key file, mode 0600, on the server | the public key in `seal_keys`, published in `keys.json` | **`deedglass keys rotate`**: the new key seals at once, the old one retires after the overlap and keeps verifying old deeds ([SEAL_KEYS.md](SEAL_KEYS.md)) |

Seal keys and intent keys never mix: each signs under its own domain tag
(`deedglass/deed/v1\n`, `deedglass/intent/v1\n`), so a signature made for one never verifies as the
other.

## On-behalf-of

An agent says which person it is serving with `Deedglass-On-Behalf-Of`, and which conversation or
run with `Deedglass-Session`. Both are optional and opaque to Deedglass.

- **The grammar:** `^[A-Za-z0-9._~:@/+=-]{1,256}$`. Each header may appear at most once.
- **Malformed or repeated is `422 validation_failed`,** with `param` set to the header name:
  - `Deedglass-On-Behalf-Of must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -. Use an opaque user id, not a name.`
  - `Deedglass-Session must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -. Use an opaque conversation or run id.`
  - sent twice: `Send Deedglass-On-Behalf-Of once. It names one person or session per call.` (or
    `Send Deedglass-Session once. …`)
- **Use stable opaque ids,** like `usr_ext_4821` and `conv_9f2c`, never names or email addresses.
  Deeds may be shared with auditors, and whatever you send is what gets sealed. Keep the mapping
  from your ids to people in your own system.
- **The headers are parsed on every request an API key authenticates,** right after the key
  checks, so a malformed value is refused even on routes that don't use it.

**If you sign, sign everything.** When a call carries a valid signed intent:

| Header | Intent | Result |
|---|---|---|
| absent | `on_behalf_of` set | the intent's value is used |
| equal to the intent's value | set | fine; the intent's value is used |
| different | set | `obo_mismatch` (`session_mismatch` for `Deedglass-Session`) |
| present | not set | `unsigned_obo` |

Without a signed intent, the headers are **recorded as the agent's claim**: Deedglass can't know
the person is real, or that the agent is telling the truth about whom it serves. A signed
`on_behalf_of` binds the reference to the agent's intent key, which is narrower, but it is still the
agent's statement. v2 replaces both with identity verified by your identity provider (below).

Where the values go:

- **S10** exposes them to policies as `user.ref` and `session.ref`.
- **S13** records them on the action and seals them in the deed's `on_behalf_of` and `session`
  fields.
- In Go, `identity.ParseOBO` reads them and `identity.OBOFrom(ctx)` returns them; when a signed
  intent covers them, the checker's result holds the effective values.

## Threat notes

Condensed from S09's design; [INTENTS.md](INTENTS.md) §13 and [THREAT_MODEL.md](THREAT_MODEL.md) have
the detail.

| Attacker | What they hold | What happens |
|---|---|---|
| **A leaked key** | an agent's API key, from `.env`, CI logs or an MCP config | Without signed intents, they act as the agent until the key is revoked (within 2 seconds of revoking). With `required` intents, every call is refused: they have no intent key. |
| **A malicious middlebox** | a proxy or wrapper on the path, seeing and changing requests | With intents, a changed argument is `args_mismatch`, a changed tool `tool_mismatch`, a changed OBO header `obo_mismatch`, and a replayed request `intent_replay`. Without them, changes go through. It never gets the client's token upstream: the Gate strips it. |
| **A prompt-injected agent** | the agent's own keys, steered by hostile input | Intents don't help: the agent signs the harmful call faithfully. Policies, budgets and approvals decide; the deed records what was meant and what happened. An agent key can never change agents, keys or modes, including its own. |
| **A compromised runtime** | both the API key and the intent key | **It acts as the agent.** Intents can't help here. Stop, revoking the keys, policies and approvals still apply. |
| **A malicious viewer** | a Console session with the Viewer role | Reads everything in the org, including OBO references in deeds and intent public keys; that is why references should be opaque ids. Can't see API key secrets or private intent keys (Deedglass stores neither), and can't change keys, modes or agents (Admin and up). |

The design choices behind these:

- **No algorithm confusion.** The intent has no `alg` field; Ed25519 only; the signed input is
  domain-separated.
- **No weak keys.** Small-order, non-canonical and off-curve public keys are refused at
  registration (422).
- **No JSON ambiguity.** The strict parser runs before JCS; the intent's bytes must equal their JCS
  form; integers above 2^53−1 are refused.
- **Bounded clocks.** 60 seconds of skew, a lifetime of at most 5 minutes.
- **No header smuggling.** One value per OBO header, a strict grammar, and Go refuses CR and LF.
- **No token passthrough, no DNS rebinding, no connector enumeration** on the MCP endpoints
  ([MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md)).
- **Intent errors are 403, never 401,** so a refused intent never starts an OAuth flow in a client.

Must never happen: an intent accepted twice; an accepted intent whose arguments differ from the
call's; a registered small-order key; a pruned nonce accepted again; an inbound bearer token
forwarded upstream; a 401 for an intent problem.

## OAuth and your identity provider (planned)

Design only, for v2; nothing here is built. The MCP side is in
[MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md).

1. **The org registers an issuer** with `PUT /v1/org/oauth {issuer, audience}`. The protected-resource
   metadata then lists it in `authorization_servers`, and MCP clients get tokens from the org's
   identity provider with `resource` set to the Gate's URL (RFC 8707).
2. **Deedglass validates JWT access tokens** (the RFC 9068 profile): `iss`, `aud` (which must be the
   Gate's resource URI), `exp` and the scopes. Signatures are checked against the issuer's JWKS,
   fetched through the SSRF-safe egress client and cached.
3. **Token claims map to Deedglass identities.** `client_id` or `azp` maps to a registered agent
   (`agents.oauth_client_ids`); `sub` and `iss` become the verified `user.sub` and `user.iss` that
   policies see. Verified identity replaces the unsigned on-behalf-of claim.
4. **Enterprise-Managed Authorization** (the MCP extension, using ID-JAG): a Deedglass token endpoint
   accepts ID-JAG assertions from allow-listed identity providers and mints 10-minute agent tokens,
   bound to both the agent and the user.
5. **Downstream calls keep using vault credentials** (S16). Per-user downstream access through RFC
   8693 token exchange is v3.
6. **Never:** passing a client's token through to an upstream, or accepting a token minted for
   another audience.

## 8. What it doesn't do

- **No passwords**, and no password reset. Sign-in needs access to the email inbox (or an operator's
  link).
- **No SSO or SCIM yet** (v2), and no GitHub sign-in yet (planned after launch, linked only to people
  who already have an account).
- **No self-serve signup.** Orgs are created by an operator; people join by invite. The waitlist is
  separate (S36).
- **Per-instance limits drift** across instances, as above.
- **Sign-in email depends on your mail provider.** If it is down, the API still answers 202 and no
  email arrives; operators can issue a link with `deedglass org login-link`.
- **Voiding older links is per org.** A newer sign-in email voids your unused links in the org it
  signs into; a link from an older email into a different org still works until it expires (15
  minutes). The code always belongs to the newest email only.
- **Timing is close, not identical.** An unknown address and a wrong code take a similar path (a dummy
  hash is computed when there is nothing to compare), but the number of database reads differs.
- **On-behalf-of is a claim.** Deedglass records the reference the agent sends (or signs). It can't
  know the person is real until v2 checks tokens from your identity provider.
- **Signed intents don't help against a compromised runtime,** which holds both keys, and they
  don't judge whether an action is wise (policies do). MCP clients can't sign intents in v1.
- **No OAuth yet.** v1 has no authorization server; MCP clients use API keys as bearer tokens. The
  identity-provider design above is v2.
- **The Gate enforces intent modes from S13.** In S09 the checks answer only
  `POST /v1/intents/verify`.
