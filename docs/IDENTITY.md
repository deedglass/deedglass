# Identity: people, sign-in, sessions and roles

This page covers the human half of identity: the people who invite teammates, stop agents, change
policies and countersign risky actions. Agents and API keys (S08, [Agents and keys](#agents-and-keys))
and signed intents (S09) extend it.

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
| `org.updated` | user → org | yes | `{fields, timezone}` |
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

People and keys meet in one place: a Console session is checked against the role table above; an
API key is checked against its scopes, and an org admin key counts as Admin.

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
