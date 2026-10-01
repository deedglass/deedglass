# Signed intents

A signed intent is the agent's own statement of what it is about to do, signed with a key that only its runtime holds. Deedglass checks the statement against the call, refuses anything that doesn't match, and records the statement's hash in the deed.

This page is normative. It is the source of truth for every signer and checker of
`deedglass.intent/1`: the Go reference (`pkg/deedglass`, `internal/identity`) and the TypeScript and
Python SDKs (S31, S32). Where this page and the golden vectors in `testdata/intents/` disagree with
an implementation, the implementation is wrong. The words MUST, MUST NOT, SHOULD and MAY are used as
in RFC 2119.

Code: `internal/intent/agent.go` (the wire format, the args profile and the reasons, shared by the
signer and the checker), `internal/identity/intent.go` (the checks, the modes and the nonce ledger),
`internal/identity/intentkeys.go` (key routes and `POST /v1/intents/verify`), `pkg/deedglass/intent.go`
(the reference signer), `cmd/deedglass/intentcmd.go` (the CLI) (S09).

## 1. Purpose and non-goals

An API key says which agent is calling. It is a bearer secret: anyone holding the string can use
it, and it travels through env files, CI logs, proxies and MCP config files. A signed intent adds a
narrower signal. The agent's runtime signs exactly what it means to do with an Ed25519 key that
never leaves that runtime. The signature covers:

- the tool
- a digest of the exact arguments
- a one-time nonce
- a short time window (at most 5 minutes)
- optionally, the person and the session the agent serves

For an agent whose mode is `required`, that means a leaked API key alone can't act for the agent,
nothing in the middle can change an argument without the call being refused, and a captured request
can't be replayed. From S13 the deed records `intent_signed: true` and the `intent_hash`, so an
auditor can match each deed to the agent's own signed statement.

**Non-goals.** A signed intent doesn't decide whether an action is allowed (policies do, S10 to S12),
doesn't say what happened at the tool (the Witness does), and doesn't help when the agent's runtime
itself is compromised. Section 13 says this in full.

## 2. The format

`deedglass.intent/1` is a closed JSON object. Every field is required unless marked optional, and
no other field is allowed.

| Field | Type and pattern | Meaning |
|---|---|---|
| `v` | the string `"deedglass.intent/1"` | the version |
| `org` | `^org_[0-7][0-9a-hjkmnp-tv-z]{25}$` | the org id; must equal the API key's org |
| `agent` | `^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$` | the agent id; must equal the API key's agent |
| `kid` | `^[0-9a-f]{32}$` | the signing key's id: lowercase hex of the first 16 bytes of SHA-256(public key) |
| `tool` | `^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`, at most 200 bytes | the Gate's canonical `connector.tool` name, like `stripe.refund` |
| `args_digest` | `^sha256:[0-9a-f]{64}$` | the digest of the exact arguments (section 3) |
| `nonce` | `^[0-9a-f]{32}$` | 128 random bits, never reused by the agent |
| `iat` | integer, Unix milliseconds, positive | when the intent was issued |
| `exp` | integer, Unix milliseconds, positive | when it stops being valid; `0 < exp - iat <= 300000` |
| `on_behalf_of` | optional; `^[A-Za-z0-9._~:@/+=-]{1,256}$` | the signed person reference ([IDENTITY.md](IDENTITY.md), On-behalf-of) |
| `session` | optional; `^[A-Za-z0-9._~:@/+=-]{1,256}$` | the signed conversation or run reference |

- **The intent is its exact bytes.** What is signed and sent is the RFC 8785 (JCS) serialization of
  that object, and the checker requires the bytes it receives to equal their own JCS form. There is
  no second encoding of the same intent.
- **At most 2,048 bytes.** A typical intent is about 300 to 400 bytes; the cross-check vector below
  is 393.
- **Times are milliseconds**, not seconds. `1791019800000` is 2026-10-03T09:30:00Z.
- **Optional fields are omitted, not empty.** An empty string fails the pattern.

The cross-check vector (`testdata/intents/valid/v01-refund-obo.json`), signed with the public RFC
8032 §7.1 TEST 1 key:

```
public key (b64url)  11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo
kid                  21fe31dfa154a261626bf854046fd227
args                 {"amount":1200,"charge":"ch_3QxZ8f2L"}
args_digest          sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36
intent (393 bytes)   {"agent":"agt_01k633rrmh3bcz2p39gg4vp46q","args_digest":"sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36","exp":1791019860000,"iat":1791019800000,"kid":"21fe31dfa154a261626bf854046fd227","nonce":"05c18be68a24c056cd6c7c8910dc0729","on_behalf_of":"usr_ext_4821","org":"org_01k6h7kp6msmzrhdzwtcr3jnkp","session":"conv_9f2c","tool":"stripe.refund","v":"deedglass.intent/1"}
intent_hash          sha256:87c73e450a53f0269e33f7adb8430a6f89714606f9aae38bc732536fef7e3089
signature (b64url)   TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA
```

The V0 transfer demo intent (`op: TRANSFER`, `internal/intent/intent.go`) is a different closed
schema with its own tag, `deedglass.transfer/1` (renamed in S17 under DL-023), so the version string
alone tells the two apart.

## 3. Canonicalization

Two JSON documents are canonicalized with RFC 8785: the intent itself, and the call's arguments.
Each passes a strict parser first, so nothing ambiguous reaches JCS and nothing is repaired.

**The intent** uses the strict profile:

- valid UTF-8, no lone UTF-16 surrogates
- no duplicate keys
- **integers only**, with magnitude at most 2^53−1 (9007199254740991); no fractions, no exponents
- shallow nesting (an intent is flat)
- the bytes must equal JCS(bytes): keys sorted by UTF-16 code units, no whitespace

**The arguments** use the args profile. `args` is the JSON value the call sends: the `args` object
of `POST /v1/actions`, or MCP `params.arguments`.

- valid UTF-8, no lone UTF-16 surrogates
- no duplicate keys
- depth at most 32
- at most 256 KiB
- integers with magnitude at most 2^53−1. A larger integer is refused with `big_int`: JCS would
  silently round it, so the digest would cover a different number than the one sent. **Send large
  integers as strings.**
- **fractions and exponents are allowed**, as IEEE-754 doubles, serialized as JCS prescribes

```
args_digest = "sha256:" + lowercase hex of SHA-256(JCS(args))
```

The digest doesn't depend on key order or whitespace in what you send: `{"charge":"ch_3QxZ8f2L","amount":1200}`
and `{ "amount": 1200, "charge": "ch_3QxZ8f2L" }` have the same digest.

Exact double outputs, verified with `github.com/gowebpki/jcs` v1.0.2 on 2026-10-01 and pinned by
vector v04:

| Sent | Canonical |
|---|---|
| `12.5` | `12.5` |
| `1e21` | `1e+21` |
| `0.0000001` | `1e-7` |
| `0.1` | `0.1` |
| `-0` | `0` |

**A fraction token is a double, whatever it looks like.** `9007199254740993.0` passes the args
profile (it has a fraction part, so it isn't an integer) and canonicalizes to `9007199254740992`:
the signer and the checker agree, but on a number you didn't send. So:

- send money as integer minor units (`{"amount":1200,"currency":"usd"}` is $12.00)
- send large ids and anything that must survive exactly as strings (vector v06)

## 4. `intent_hash` and the signature input

```
intent_hash   = "sha256:" + lowercase hex of SHA-256(intent bytes)
signed bytes  = "deedglass/intent/v1\n" + intent_hash          (ASCII, one line feed)
signature     = Ed25519(private key, signed bytes)              (RFC 8032, pure Ed25519, 64 bytes)
```

The signed bytes are 20 bytes of domain tag plus the 71-byte `intent_hash`. This is the same
domain-separation shape as deeds (`"deedglass/deed/v1\n" + body_hash`): a signature made for one
purpose never verifies as another. Seal keys are never intent keys. Signing the hash without the
domain tag is refused as `bad_signature` (vector i17).

## 5. The header

```
Deedglass-Intent: <base64url(intent bytes)>.<base64url(64-byte signature)>
```

- Two parts, unpadded base64url (RFC 4648 §5), joined by one dot. Decoders MUST be strict: no
  padding, no other alphabet, no non-zero trailing bits, so the same bytes have exactly one encoding.
- **It is not a JWS.** There is no header object and no `alg` field, so there is nothing to
  negotiate and nothing to confuse. Ed25519 is the only algorithm.
- Send it once per request. The header for the cross-check vector is about 700 characters; any
  intent fits well under common 8 KiB header limits.

## 6. The checks, in order

Deedglass runs these checks cheapest first and stops at the first failure. Steps 1 to 12 run in
memory; step 13 consumes the nonce, last, so a refused intent never burns its nonce.

**All three codes are 403 `permission_error`, never 401.** The API key itself is valid, and a 401
would make OAuth-capable MCP clients start an authorization flow.

| # | Check | Reason | Error code | Param |
|---|---|---|---|---|
| 1 | two unpadded base64url parts joined by one dot; the signature is exactly 64 bytes | `syntax` | `intent_invalid` | `Deedglass-Intent` |
| 2 | the intent is at most 2,048 bytes | `too_large` | `intent_invalid` | `Deedglass-Intent` |
| 3 | the strict profile: UTF-8, no duplicate keys, integers only and at most 2^53−1, nesting | `strict` | `intent_invalid` | `Deedglass-Intent` |
| 4 | the bytes equal JCS(bytes) | `not_canonical` | `intent_invalid` | `Deedglass-Intent` |
| 5 | the closed schema: no unknown field, no missing field, each field's type and pattern; then `v` | `schema` / `version` | `intent_invalid` | the field (`v` for `version`) |
| 6 | `org` and `agent` equal the API key's | `org_mismatch` / `agent_mismatch` | `intent_invalid` | `org` / `agent` |
| 7 | `kid` is registered for the agent | `unknown_kid` | `intent_invalid` | `kid` |
| 8 | the Ed25519 signature over `"deedglass/intent/v1\n" + intent_hash` verifies | `bad_signature` | `intent_invalid` | `kid` |
| 9 | `iat <= now + 60 s`; `now <= exp + 60 s`; `0 < exp - iat <= 300000` | `not_yet_valid` / `expired` / `ttl` | `intent_invalid` | `iat` / `exp` / `exp` |
| 10 | `tool` equals the call's tool | `tool_mismatch` | `intent_invalid` | `tool` |
| 11 | the call's arguments pass the args profile, and `args_digest` equals their digest | `args_mismatch` / `big_int` / `args_not_canonicalizable` | `intent_invalid` | `args_digest` / `args` / `args` |
| 12 | OBO consistency: if you sign, sign everything | `obo_mismatch` / `session_mismatch` / `unsigned_obo` | `intent_invalid` | `Deedglass-On-Behalf-Of` / `Deedglass-Session` |
| 13 | insert the nonce; a conflict means it was used | `replay` | `intent_replay` | `nonce` |

Before step 1, the mode decides whether the header is checked at all (section 7). A required agent
with no header gets `intent_required` (param `Deedglass-Intent`).

Notes on the order:

- **Step 5** reports unknown fields first (in sorted order), then missing fields, then the first
  field whose type or pattern is wrong (`v`, `org`, `agent`, `kid`, `tool`, `args_digest`, `nonce`,
  `on_behalf_of`, `session`, the tool's length, then `iat` and `exp`), and the version last. So an
  intent from a future version with new fields reports `schema`, not `version`.
- **Step 9** checks `not_yet_valid`, then `expired`, then `ttl`.
- **Step 11** canonicalizes the arguments before comparing: `big_int` and `args_not_canonicalizable`
  come before `args_mismatch`.
- **Step 12** checks `on_behalf_of` before `session`, and for each, a header the intent doesn't
  cover (`unsigned_obo`) before a header that differs (`obo_mismatch`, `session_mismatch`).

### The messages

These are the exact messages (`internal/httpx/errors.go`, `IntentCopy`). `{placeholders}` are
filled at the call site; values are clipped to 120 characters. Kids are shown as their first and
last four hex characters (`21fe…d227`), and times in UTC as `15:04:05`.

| Reason | Message |
|---|---|
| `required` (code `intent_required`) | `Agent "{agent}" requires a signed intent for every call. Send a Deedglass-Intent header; see docs.deedglass.com/intents.` |
| `syntax` | `The Deedglass-Intent header must be two base64url parts joined by a dot: the intent, then its signature.` |
| `too_large` | `The signed intent is larger than 2,048 bytes. Keep only the fields listed at docs.deedglass.com/intents.` |
| `strict` | `The signed intent isn't strict JSON: {detail}. Use the SDK signer, or follow the profile at docs.deedglass.com/intents.` |
| `not_canonical` | `The signed intent isn't canonical JSON (RFC 8785): keys must be sorted and there must be no whitespace. Use the SDK signer, or canonicalize before signing.` |
| `schema` (an unknown field) | `The signed intent has an unknown field "{field}". Put arguments in the call, and their digest in args_digest.` |
| `schema` (a missing field) | `The signed intent is missing the field "{field}". Sign every required field listed at docs.deedglass.com/intents.` |
| `schema` (a wrong type or pattern) | `The signed intent's {field} isn't valid: it must be {want}. See the field table at docs.deedglass.com/intents.` |
| `version` | `The signed intent's version is "{got}", and Deedglass reads deedglass.intent/1. Sign with deedglass.intent/1.` |
| `org_mismatch` | `The signed intent names {intent_org}, but this API key belongs to {org}. Sign with the agent's own org id.` |
| `agent_mismatch` | `The signed intent names {intent_agent}, but this API key belongs to {agent} ({agent_id}). Sign with the agent's own identity.` |
| `unknown_kid` | `The signed intent uses key {kid}, which isn't registered for {agent}. Register it with deedglass agent intent add {agent}, or in Console › Agents › Security.` |
| `bad_signature` | `The signature doesn't verify with key {kid}. Sign the bytes "deedglass/intent/v1\n" + intent_hash with the matching private key.` |
| `expired` | `The signed intent expired at {time} UTC, {ago} ago. Sign a fresh intent for each call; an intent lasts at most 5 minutes.` |
| `not_yet_valid` | `The signed intent was issued {ahead} in the future. Check the agent's clock; Deedglass allows 60 seconds of skew.` |
| `ttl` | `The signed intent is valid for {ms} ms, and it must be more than 0 and at most 300000 (5 minutes). Sign it again with exp after iat, at most 5 minutes later.` |
| `tool_mismatch` | `The signed intent is for {intent_tool}, but this call is for {tool}. Sign one intent per call.` |
| `args_mismatch` | `The signed intent's args_digest doesn't match the arguments sent. Sign the exact arguments you send, canonicalized with RFC 8785.` |
| `big_int` | `The arguments contain an integer larger than 9007199254740991, which JSON can't carry exactly. Send it as a string.` |
| `args_not_canonicalizable` | `The arguments can't be canonicalized with RFC 8785: {detail}. Send JSON with unique keys, at most 32 levels deep and at most 256 KiB.` |
| `obo_mismatch` | `Deedglass-On-Behalf-Of says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.` |
| `session_mismatch` | `Deedglass-Session says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.` |
| `unsigned_obo` | `{name} was sent, but the signed intent doesn't include {field}. Sign it too, or leave the header out.` (`{name}` is `Deedglass-On-Behalf-Of` or `Deedglass-Session`; `{field}` is `on_behalf_of` or `session`) |
| `replay` (code `intent_replay`) | `This signed intent was already used at {time} UTC. Sign a new intent, with a fresh nonce, for every call.` |
| `replay`, the first use already pruned | `This signed intent was already used. Sign a new intent, with a fresh nonce, for every call.` |

`{detail}` for `strict` is one of: `the key "x" appears twice`, `it contains an integer larger than
9007199254740991`, `it contains a number with a fraction or exponent, and intents carry integers
only`, `it isn't valid UTF-8`, `it contains a lone UTF-16 surrogate`, `it is nested too deeply`, `it
isn't valid JSON`.

`{detail}` for `args_not_canonicalizable` is one of: `a key appears twice`, `they are nested more
than 32 levels deep`, `a number is outside the range of a double`, `they aren't valid UTF-8`, `they
contain a lone UTF-16 surrogate`, `they are larger than 256 KiB`, `they aren't valid JSON`.

`{want}` for `schema` names the field's form: `the org's id (org_…)`, `the agent's id (agt_…)`, `32
lowercase hex characters`, `32 lowercase hex characters (128 random bits)`, `a connector.tool name
such as stripe.refund`, `sha256: followed by 64 lowercase hex characters`, `positive Unix
milliseconds`, `1 to 256 characters: letters, digits and . _ ~ : @ / + = -`.

The logs and the vectors use the reason column; the copy keys `schema_unknown`, `schema_missing`,
`unsigned_session` and `replay_unknown` in `IntentCopy` log as `schema`, `schema`, `unsigned_obo`
and `replay`. Nothing from a failed check is cached.

An error, as the API renders it:

```json
HTTP/1.1 403 Forbidden
{ "error": { "type": "permission_error", "code": "intent_replay",
  "message": "This signed intent was already used at 09:30:02 UTC. Sign a new intent, with a fresh nonce, for every call.",
  "param": "nonce", "request_id": "req_01k63z884jzfmyy0mxnbfmpp0t", "doc_url": "https://docs.deedglass.com/errors/intent_replay" } }
```

## 7. Modes

Each agent has an intent mode, changed with `PATCH /v1/agents/{id} {"intent_mode": "…"}` or
`deedglass agent intent mode`.

| Mode | No header | Valid header | Invalid header |
|---|---|---|---|
| `off` (the default) | accepted, `intent_signed: false` | ignored, not checked, `intent_signed: false` | ignored, not checked |
| `optional` | accepted, `intent_signed: false` | accepted, `intent_signed: true` | refused (`intent_invalid` or `intent_replay`) |
| `required` | refused, `intent_required` | accepted, `intent_signed: true` | refused |

- **Switching to `required` with no keys registered is 409 `conflict`:** `support-bot has no intent
  keys yet, so requiring signed intents would refuse every call. Register a key first: deedglass
  agent intent add support-bot.`
- **Removing the last key while the mode is `required` is 409:** `support-bot requires signed
  intents, so removing its last key would refuse every call. Add the new key first, or set
  intent_mode to optional.`
- **Setting the mode it already has seals nothing** and returns `"deed": null`.
- **MCP clients can't sign intents in v1.** A `required` agent can act only through the SDKs or the
  REST API. From S13 the Gate refuses MCP-origin calls for `required` agents with `intent_required`.
- **The Gate enforces modes from S13.** In S09 the checker, the keys, the modes and
  `POST /v1/intents/verify` exist; `POST /v1/actions` and the MCP gateway call the checker once
  S13 and S15 build them. Until then a mode changes nothing about what an agent can do.

## 8. Keys and rotation

**Public keys** are registered as `"ed25519:"` followed by 43 base64url characters (32 bytes):

```http
POST /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/intent-keys
{"public_key":"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}

201 {"object":"intent_key","kid":"21fe31dfa154a261626bf854046fd227","agent":"agt_01k633rrmh3bcz2p39gg4vp46q",
     "public_key":"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo","created_at":"2026-10-03T11:40:02Z",
     "created_by":"usr_01k6c9kb2zcjsyn7b8g2k1vqvp","deed":"deed_01k6…"}
```

| Route | Who |
|---|---|
| `GET /v1/agents/{id}/intent-keys` | viewer and up, or an org admin key |
| `POST /v1/agents/{id}/intent-keys` | admin and up, or an org admin key |
| `DELETE /v1/agents/{id}/intent-keys/{kid}` | admin and up, or an org admin key; answers `{"object":"intent_key","kid":"…","agent":"agt_…","deleted":true,"deed":"deed_…"}` |

- **Registration refuses unsafe keys with 422 `validation_failed`** (param `public_key`), through
  `internal/keys` (attack case A13):
  - a small-order point: `That public key is a small-order point, so a single signature would
    verify for every message.`
  - a non-canonical encoding: `That public key isn't a canonical Ed25519 encoding, so two different
    strings would name the same key.`
  - a point off the curve: `That public key isn't a point on the Ed25519 curve.`
  - any other shape: `The public key must be "ed25519:" followed by 43 base64url characters (32
    bytes), like ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo.`

  Each ends with `Generate a fresh key with deedglass agent intent keygen <agent>.`
- **At most two keys per agent.** A third is 409: `support-bot already has two intent keys. Remove
  one first; two lets you rotate without downtime.` The same key twice is 409 too.
- **`kid`** is lowercase hex of the first 16 bytes of SHA-256(the 32 public-key bytes):
  `21fe31dfa154a261626bf854046fd227` for the TEST 1 key.
- **Private keys** are PKCS#8 PEM (`-----BEGIN PRIVATE KEY-----`), which Go's `crypto/x509`, Node's
  `crypto` and Python's `cryptography` all load. `deedglass agent intent keygen <agent>` writes one
  with mode 0600 to `.deedglass/intent/<agent>.pem` (the directory 0700) and prints the public key.
  The private key never goes to Deedglass.
- **Rotation:** add the new key, deploy it to the agent's runtime, then remove the old one. Two
  keys at once is what makes this possible without refused calls.
- **Every change is sealed as an admin deed** in the same transaction, and every instance's cache
  drops the agent's old keys and mode within 2 seconds:

| Action | Data |
|---|---|
| `agent.intent_key_added` | `{kid}` |
| `agent.intent_key_removed` | `{kid}` |
| `agent.intent_mode_changed` | `{from, to}` |

Stored keys are parsed again on every read, so a row edited outside the API can't bring in a
small-order key; the database also checks each element's shape (`deedglass.intent_keys_valid`,
[DATA_MODEL.md](DATA_MODEL.md)).

## 9. Nonces and replay

- **The nonce** is 128 bits from a CSPRNG, as 32 lowercase hex characters. An agent MUST NOT reuse
  one.
- **The ledger** is `deedglass.intent_nonces`, with primary key `(agent_id, nonce)` and row-level
  security on `org_id`. Consuming a nonce is one
  `INSERT … ON CONFLICT (agent_id, nonce) DO NOTHING`: of any number of concurrent uses of one
  header, exactly one succeeds and the rest get `intent_replay` (the integration test sends 100 at
  once).
- **The same nonce for two agents is fine.** Each agent has its own nonce space.
- **Pruning:** `serve` runs `deedglass.maint_prune_intent_nonces` every minute, in batches of
  10,000, deleting rows whose `expires_at` (the intent's `exp`) is more than 2 minutes in the past.
- **The invariant:** an intent is accepted until `exp + 60 s`, and its nonce row is kept until at
  least `exp + 2 minutes`. So by the time a nonce is pruned, its intent is always refused as
  `expired` at step 9, before the nonce is looked at. A pruned nonce can never be replayed.
- **`Check` and `CheckTx`.** `Check` consumes the nonce in its own short transaction. `CheckTx`
  (S13) consumes it inside the action-creation transaction, so a failed action insert doesn't burn a
  nonce the agent can't retry.

## 10. Clocks

- **Skew:** Deedglass allows 60 seconds either way. `iat` may be up to 60 s in Deedglass's future,
  and an intent is accepted up to 60 s after `exp`.
- **Lifetime:** `exp - iat` must be more than 0 and at most 300000 ms (5 minutes).
- **The SDK default is 60 seconds.** Sign each intent just before the call it covers. A longer
  lifetime only widens the window in which a captured, unused intent could be sent.

## 11. Golden vectors

`testdata/intents/` holds 8 valid and 26 invalid vectors, all built from the public RFC 8032 TEST 1
key (`keys/test1.pem`; TEST 2 is there as an unregistered key). `testdata/intents/README.md` explains
each field and how to run them. In short, for every file:

1. **Signer** (valid files): sign `sign.tool` and `sign.args` with `sign.key` at `sign.now`, with
   `sign.ttl_ms` and `sign.nonce`. The header must equal `header` byte for byte.
2. **Checker** (every file): check `header` against `tool`, `args` and `headers` at `now`, for the
   agent in `keys.json` (mode `optional`, key test1 registered), `sends` times with one empty nonce
   ledger. Compare the last result with `expected`: the code, reason and param for a refusal, every
   listed value for an accepted intent.

| Valid | | Invalid | Reason |
|---|---|---|---|
| v01 | the cross-check vector (OBO and session) | i01 to i03 | `syntax` |
| v02 | minimal, no OBO | i04 | `too_large` |
| v03 | nested arguments with non-ASCII text | i05, i06 | `strict` |
| v04 | doubles: `12.5`, `1e21`, `0.0000001`, `0.1`, `-0` | i07, i08 | `not_canonical` |
| v05 | empty arguments `{}` | i09 to i11, i12 | `schema`, `version` |
| v06 | a large number sent as a string | i13, i14 | `org_mismatch`, `agent_mismatch` |
| v07 | the longest lifetime, 300000 ms | i15, i16 and i17 | `unknown_kid`, `bad_signature` |
| v08 | `now = exp + 59 s`, inside the skew | i18, i19, i20 and i21 | `expired`, `not_yet_valid`, `ttl` |
| | | i22, i23 | `tool_mismatch`, `args_mismatch` |
| | | i24, i25 | `obo_mismatch`, `unsigned_obo` |
| | | i26 | `replay` (v01 sent twice) |

The vectors are generated, never edited by hand. To regenerate them after a deliberate change:

```sh
go test ./internal/identity -run TestIntentVectors -update-intents
```

then update `testdata/SHA256SUMS` for the changed files and run `make testdata-check`. One table
test runs every file through both the Go signer and the checker.

## 12. Notes for SDK authors

- **Sign the exact bytes you send.** Build the intent object, serialize it with JCS, hash those
  bytes, sign, and base64url the same bytes into the header. Never re-serialize between steps.
- **Canonicalize the arguments with RFC 8785**, after enforcing the args profile (section 3). Hash
  the arguments you put in the request body, not a copy that went through another serializer.
  Refuse integers above 2^53−1 locally, with the same advice: send them as strings.
- **Never reuse a nonce.** Generate 16 bytes with the platform CSPRNG (`crypto.getRandomValues`,
  `secrets.token_bytes`, Go's `crypto/rand`) for every intent.
- **Keep keys out of logs**, crash reports and telemetry. Load the PEM once at start-up; the signer
  holds the key in memory. Treat an unused header like the API key until it expires: together they
  can be sent once.
- **Default the lifetime to 60 seconds**, and never exceed 5 minutes.
- **Send OBO in the intent.** If the caller sets `on_behalf_of` or `session`, sign them; sending
  the header as well is fine only with the same value.
- **`POST /v1/intents/verify` is the conformance check.** It takes an agent key with
  `actions:execute` and a body `{"tool": "…", "args": {…}}`, checks the header **in every mode**
  (`off` included), consumes the nonce exactly as the Gate will, and never acts. It answers 200 with
  object `intent_check`, or the same 403s the Gate will send. Without a header it is
  `422 validation_failed`: `Send a Deedglass-Intent header to check.` (for a `required` agent,
  `intent_required`). A header sent twice is `syntax`.

```json
{"object":"intent_check","signed":true,"intent_hash":"sha256:87c73e450a53f0269e33f7adb8430a6f89714606f9aae38bc732536fef7e3089",
 "kid":"21fe31dfa154a261626bf854046fd227","agent":"agt_01k633rrmh3bcz2p39gg4vp46q","tool":"stripe.refund",
 "args_digest":"sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36",
 "on_behalf_of":"usr_ext_4821","session":"conv_9f2c","expires_at":"2026-10-03T09:31:00Z",
 "nonce_consumed":true,"intent_mode":"optional"}
```

- **The Go reference** is `pkg/deedglass`:

| Name | What it does |
|---|---|
| `GenerateIntentKey() (privPEM []byte, publicKey, kid string, err error)` | a new key: PKCS#8 PEM, `"ed25519:<b64url>"`, kid |
| `LoadIntentSigner(pemBytes []byte, org, agent string) (*IntentSigner, error)` | a signer for one agent; safe for concurrent use |
| `(*IntentSigner).Sign(tool string, args json.RawMessage, o SignOptions) (header, intentHash string, err error)` | one header; `SignOptions{OnBehalfOf, Session, TTL, Now, Nonce}` (TTL defaults to 60 s; `Nonce` is for tests and vectors only) |
| `ArgsDigest(args json.RawMessage) (string, error)` | the args digest under the args profile |
| `(*Client).VerifyIntent(ctx, tool, args, header, opts...) (*IntentCheck, error)` | calls `POST /v1/intents/verify`; `WithOnBehalfOf` and `WithSession` add the OBO headers |

The signer and the checker share `internal/intent/agent.go`, so the Go pair can't drift. A port
in another language is conformant only when it passes every vector.

**Try it** (a dev server, an agent key in `$AGENT_KEY`, and the agent's key registered with
`deedglass agent intent add`):

```
$ H=$(deedglass dev sign-intent --key .deedglass/intent/support-bot.pem --tool stripe.refund --args '{"charge":"ch_3QxZ8f2L","amount":1200}')
$ curl -s localhost:8080/v1/intents/verify -H "Authorization: Bearer $AGENT_KEY" -H "Deedglass-Intent: $H" \
       -H 'Content-Type: application/json' \
       -d '{"tool":"stripe.refund","args":{"charge":"ch_3QxZ8f2L","amount":1200}}' | jq -r .object
intent_check
$ (the same request again)                        → 403 intent_replay
$ (a fresh intent, but the body says 120000)      → "The signed intent's args_digest doesn't match the arguments sent. …"
```

`dev sign-intent` reads the org and agent from `whoami` when `DEEDGLASS_API_KEY` is the agent's key;
otherwise pass `--org` and `--agent`. `--explain` prints every intermediate value
([CLI.md](CLI.md)).

## 13. What it doesn't do

This section is part of the spec, not a footnote.

| Attack | API key only | Key and `required` intents |
|---|---|---|
| The key leaks from `.env`, CI logs or an MCP config | the attacker acts as the agent | refused: the attacker has no intent key (`intent_required`, or `bad_signature` for a forged one) |
| A proxy, wrapper or middlebox changes an argument | goes through | refused: `args_mismatch` |
| A captured request is replayed | executes twice | refused: `intent_replay` (or `expired`, once the window has passed) |
| The agent's runtime is compromised, so the attacker holds both keys | the attacker acts | **the attacker still acts.** Intents can't help here. |
| A prompt-injected agent asks for a harmful but well-formed action | policy's job (S10 to S12) | the same: the intent records what was meant; policy decides whether it's allowed |

- **A compromised runtime holds both keys.** Whoever controls the process that signs can sign
  anything. Policies, budgets, approvals and Stop still apply; intents add nothing there.
- **Prompt injection is policy's job.** An injected agent signs the harmful action as faithfully as
  a useful one. The signature says the runtime meant it, not that it was a good idea.
- **Unsigned OBO is the agent's claim.** `Deedglass-On-Behalf-Of` without a signed intent is
  recorded as the agent sent it. A signed `on_behalf_of` binds the reference to the agent's key, but
  Deedglass still can't know the person is real until an identity provider vouches for them (v2,
  [IDENTITY.md](IDENTITY.md)).
- **Intents say what was meant, not what happened.** What the tool actually did is the Witness's
  job (S19 to S21), and the deed records both.
- **MCP clients can't sign in v1**, so `required` agents can't use MCP.
- **The Gate doesn't enforce modes until S13.** Today the checker answers only
  `POST /v1/intents/verify`.
- **A valid intent can be sent once, by anyone who holds it and the API key**, within its window.
  The nonce stops the second use, not the first. Keep the lifetime short.
- **No key expiry and no revocation list.** A removed key stops verifying within 2 seconds; a key
  that is still registered works until it is removed.
