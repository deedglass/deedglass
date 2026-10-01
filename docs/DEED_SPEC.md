# Deed v1 specification

**Status:** normative for Deed v1 · **Owner:** `internal/deed` · **Vectors:** `testdata/deeds/` ·
**Replaces:** `docs/v0/STATEMENT_SPEC.md` (the V0 verification statement) · **Checked:** 2026-10-01

A deed is the signed record of one thing that happened in Deedglass: an agent action, an
administrative change, or a later witness verdict about an earlier action. This document defines
the deed exactly enough that you can write a verifier in any language from it alone and match the
test vectors byte for byte. Section 16 walks one deed through every step with real bytes.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119 and RFC 8174, and only in the
normative sections (4 to 13).

## 1. Purpose and scope

Deedglass sits between AI agents and the tools they use. For every action it decides, runs or
refuses, and checks, it seals a deed. A customer hands deeds to an auditor; a security lead reads
them after an incident; anyone with the issuer's public keys can check them offline.

This document covers the deed body, its canonical form, the hash and the seal, the per-org chain,
the envelope, seal keys and `keys.json`, the verification algorithm and its error codes, and an
optional COSE_Sign1 encoding. It does not cover the export bundle (S05), the Ledger API (S06) or
how the Gate decides what goes into a deed (S13).

## 2. What a deed shows, and what it does not

A deed records:

- **who**: the agent, and the opaque person and session references the agent passed along;
- **what**: the tool, the connector, and a digest of the arguments;
- **under which policy**: the policy version, its hash, the rule and the decision;
- **who approved it**, when a person had to;
- **what happened when it ran**: status, times, latency and a digest of the result;
- **what the Witness saw**: the verification level, the verdict and a digest of the detail;
- **when**, and **where it sits** in the org's chain.

A deed does not:

- judge whether an action was wise, only what was decided and what was observed;
- contain the arguments, the result or any payload: only SHA-256 digests of them;
- show that a tool did anything the Witness didn't check: `UNVERIFIABLE` means exactly that;
- stop a party holding the seal key from sealing a false deed (section 15);
- show, on its own, that nothing was removed from the end of a chain (section 8.4).

## 3. Terminology

| Term | Meaning |
|---|---|
| **Deed** | One sealed record: an envelope holding a body, its hash and its seal. |
| **Body** | The JSON object that is hashed and sealed. |
| **Seal** | An Ed25519 signature over the body hash with a domain tag. "Sealed" means signed this way. |
| **Seal key** | The Ed25519 key pair an issuer seals with. Its public half is published in `keys.json`. |
| **Deed chain** | The sequence of an org's deeds, numbered by `seq` and linked by `prev_hash`. |
| **Issuer** | The Deedglass deployment that sealed the deed, named by its base URL. |
| **Verifier** | Anyone checking deeds: `deedglass verify`, the browser verifier, or your own code. |
| **Org** | The customer account a deed belongs to. Each org has its own chain. |

## 4. Body fields

Every body has exactly these 19 keys, always present, with `null` where a section doesn't apply
(section 5). Unknown keys at any level MUST be refused (`schema_invalid`). Free text is limited to
the fields marked *text*; everything else is an id, a digest, a time, an enum or an integer.

Patterns used below:

| Name | Pattern |
|---|---|
| id with prefix `p` | `^p_[0-7][0-9a-hjkmnp-tv-z]{25}$` (a lowercase ULID; contract §3) |
| digest | `^sha256:[0-9a-f]{64}$` |
| time | `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`, a valid UTC instant |
| header ref | `^[A-Za-z0-9._~:@/+=-]{1,256}$` |
| text | valid UTF-8, no control characters (U+0000–U+001F, U+007F–U+009F), length in characters |

**Top level**

| Field | Type | Null | Rule | Meaning |
|---|---|---|---|---|
| `v` | integer | no | `1` | Format version. Anything else is `unsupported_version`. |
| `kind` | string | no | `action`, `admin`, `amendment` | What the deed records. |
| `deed_id` | string | no | id `deed` | This deed's id. |
| `org` | string | no | id `org` | The org whose chain holds it. |
| `seq` | integer | no | 1 to 2^53−1 | Position in the org's chain. |
| `prev_hash` | string | no | digest | Link to the previous deed (section 8). |
| `issued_at` | string | no | time | When it was sealed, from the issuer's database clock. |
| `agent` | object | by kind | section below | The agent. |
| `on_behalf_of` | string | yes | header ref | Opaque reference to the person the agent acted for. |
| `session` | string | yes | header ref | Opaque reference to the agent's session. |
| `action` | object | by kind | | The tool call. |
| `policy` | object | by kind | | The decision. |
| `approval` | object | by kind | | The human decision, if one was needed. |
| `execution` | object | by kind | | What happened when the tool ran. |
| `witness` | object | by kind | | The Witness's verdict. |
| `cost` | object | by kind | | Money spent, if known. |
| `admin` | object | by kind | | The administrative change. |
| `amends` | string | by kind | id `deed` | The deed an amendment is about. |
| `seal_key_id` | string | no | id `sk` | The seal key. MUST equal the envelope's `seal_key_id`. |

**`agent`**: `id` (id `agt`), `name` (text, 1–64 characters; informational only).

**`action`**: `id` (id `act`); `tool` (text, 1–200 bytes, for example `postgres.execute`);
`connector` (id `con` or null); `mode` (`gateway` when Deedglass ran the tool, `proxy` when it ran
at the edge); `request_digest` (digest of the arguments, section 7.3); `intent_hash` (digest or
null); `intent_signed` (boolean; when true, `intent_hash` MUST NOT be null).

**`policy`**: `id` (id `pol` or null, null when the org default decided); `version_id` (id `pv` or
null); `version_hash` (digest or null; MUST be set when `version_id` is); `mode` (`enforce` or
`shadow`); `decision` (`allow`, `deny` or `require_approval`); `rule_id` (null or
`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`).

**`approval`**: `id` (id `apr`); `outcome` (`approved`, `denied`, `expired`, `cancelled`);
`decided_by` (array of id `usr`, sorted ascending, no duplicates, never null; non-empty when
`approved` or `denied`, empty when `expired`); `decided_at` (time or null); `deed` (id `deed` or
null: the admin deed of the decision).

**`execution`**: `status` (`succeeded`, `failed`, or `unknown` when Deedglass cannot know whether
the tool ran); `started_at`, `finished_at` (time; finished not before started); `latency_ms`
(integer ≥ 0); `result_digest` (digest or null); `error_code` (null or `^[a-z][a-z0-9_]{0,63}$`,
an API error code such as `upstream_error`).

**`witness`**: `level` (`L0` Reported, `L1` Confirmed, `L2` Witnessed); `verdict` (`VERIFIED`,
`MISMATCH`, `UNVERIFIABLE`); `method` (`^[a-z0-9][a-z0-9._-]{0,63}$`, for example
`postgres.pgoutput`); `detail_digest` (digest or null).

**`cost`**: `amount` (integer minor units, 0 to 2^53−1); `currency` (`^[a-z]{3}$`, ISO 4217
lowercase).

**`admin`**: `event` (id `adm`); `action` (`^[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}$`, an object
and a past-tense verb: `agent.stopped`, `member.invited`, `ledger.pruned`); `actor` (a ref);
`target` (a ref or null); `reason` (text, 1–280 characters, newlines allowed, or null);
`data_digest` (digest or null).

**ref**: `type` (`^[a-z][a-z_]{0,31}$`: `user`, `agent`, `api_key`, `system`, `org`, `membership`,
`policy`, `policy_version`, `connector`, `approval`, `webhook`, `suggestion`, `anomaly`, `action`;
verifiers MUST accept other values matching the pattern) and `id` (`^[a-z][a-z0-9_-]{0,63}$`: an id,
or `deedglass` for jobs and `operator` for database-direct CLI commands when `type` is `system`).

## 5. Kinds and the presence table

`—` means the value MUST be `null`.

| Key | `action` | `admin` | `amendment` |
|---|---|---|---|
| `agent` | object | object or null | object |
| `on_behalf_of`, `session` | string or null | — | string or null |
| `action` | object | — | object (copied from the amended deed) |
| `policy` | object | — | — |
| `approval` | object or null | — | — |
| `execution` | object, or null when never executed (denied, expired, cancelled) | — | — |
| `witness` | object, or null when no witness ran | — | object (the new verdict) |
| `cost` | object or null | — | — |
| `admin` | — | object | — |
| `amends` | — | — | id `deed` |

An amendment never changes the deed it amends. It is a new deed in the same chain carrying a later
verdict; a reader takes the latest amendment as the current verdict and keeps the history.

## 6. Canonical form

Bodies MUST pass the **strict profile** before canonicalization, and a verifier MUST refuse
(`malformed`), never repair, any input that fails it:

1. the bytes are valid UTF-8;
2. no object has two members with the same name (compared after unescaping);
3. every number is an integer written `0` or `[1-9][0-9]*`: no fraction, no exponent, no minus sign
   (so no negatives and no `-0`), at most 2^53−1 = 9007199254740991;
4. no `\u` escape forms a lone UTF-16 surrogate;
5. nesting is at most 16 levels;
6. nothing follows the top-level value except whitespace.

The canonical form is then **JCS**, the JSON Canonicalization Scheme of RFC 8785: members sorted
by the UTF-16 code units of their names, no whitespace, strings escaped minimally (only `"`, `\`
and control characters, the latter as `\b \f \n \r \t` or `\u00xx` in lowercase hex), integers in
plain decimal. Because a body holds only strings, integers, booleans, null, arrays and objects, a
verifier needs none of RFC 8785's floating-point rules.

The strict profile exists because JSON libraries disagree. Some round 9007199254740993 to
9007199254740992, some accept `1.0`, some keep the last of two duplicate keys and some the first.
Each of those would let two parties read different deeds from the same bytes.

The strict profile applies to the whole envelope as a verifier reads it. It does **not** apply to
payloads (section 7.3).

## 7. Hashes, domain tags and the seal

### 7.1 Definitions

| Name | Exact definition |
|---|---|
| `JCS(x)` | RFC 8785 serialization of `x`, after the strict profile for bodies and envelopes. |
| `body_hash` | `"sha256:" + lowercase_hex(SHA-256(JCS(body)))` |
| seal input | the ASCII bytes `"deedglass/deed/v1\n" + body_hash`: 18 + 71 = 89 bytes |
| `seal` | `"ed25519:" + base64url_nopad(Ed25519_Sign(seal_key, seal input))`: 8 + 86 = 94 characters |
| thumbprint | `"sha256:" + lowercase_hex(SHA-256(raw 32-byte public key))` |

`\n` is the single byte 0x0A. Ed25519 is pure Ed25519 as in RFC 8032 section 5.1 (no pre-hash, no
context), with 64-byte signatures. base64url is RFC 4648 section 5 without padding; a verifier MUST
refuse a value with non-zero trailing bits.

### 7.2 Domain tags

The seal signs a tagged string, never bare bytes, so a signature made for one purpose can never be
presented as another:

| Tag | Used for |
|---|---|
| `deedglass/deed/v1\n` | deed seals (this document) |
| `deedglass/bundle/v1\n` | sealed export-bundle manifests (S05) |
| `deedglass/intent/v1\n` | signed intents from agents (S09), with separate keys |
| `deedglass/genesis\n` | the first link of a chain (section 8); hashed, never signed |

### 7.3 Payload digests

`request_digest`, `result_digest`, `detail_digest` and `data_digest` are
`"sha256:" + lowercase_hex(SHA-256(JCS(value)))` with plain RFC 8785 and **no** strict profile:
tool arguments may hold fractions and negative numbers. Two payloads that differ only in
whitespace or member order have the same digest.

## 8. The chain

### 8.1 Links

Each org has one chain. Its deeds are numbered `seq` = 1, 2, 3… with no gaps or duplicates.

- `prev_hash` of seq 1 is `"sha256:" + lowercase_hex(SHA-256("deedglass/genesis\n" + org))`, the
  **genesis value** of the org (the org id as ASCII after the tag).
- `prev_hash` of seq n > 1 is the `body_hash` of the org's deed with seq n−1.

Because `prev_hash` is inside the body, each seal covers its link: changing any earlier deed breaks
every later link.

### 8.2 Time order

`issued_at` comes from the issuer's database clock, read while the issuer holds the org's chain
lock, truncated to the millisecond. It MUST NOT decrease along `seq`. When the clock steps back,
the issuer reuses the previous deed's `issued_at`.

### 8.3 How the issuer keeps the chain whole

The issuer seals a deed inside the same database transaction as the state change it records,
after taking a transaction-scoped advisory lock on the org's chain. The database refuses a second
deed with the same `(org, seq)` or the same `(org, prev_hash)` (no forks), checks every link in a
trigger, and refuses updates and deletes. A rolled-back transaction leaves no deed and consumes no
`seq`.

### 8.4 What the chain does not show

A verifier detects a changed, reordered, inserted or removed deed anywhere before the last one it
holds. It cannot detect that deeds were removed from the **end**: a chain cut at seq 800 looks like
a complete chain of 800. Keep the head (`seq` and `body_hash` of the last deed) of every export you
receive; S05's sealed bundle manifest records it.

## 9. Envelope and storage form

```json
{ "object": "deed", "id": "deed_…", "body": { … }, "body_hash": "sha256:…",
  "seal": "ed25519:…", "seal_key_id": "sk_…" }
```

| Field | Rule |
|---|---|
| `object` | the string `deed` |
| `id` | equal to `body.deed_id`. It is a convenience and is **not** signed; verifiers MUST check it (`id_mismatch`). |
| `body` | section 4 |
| `body_hash` | digest; MUST equal the recomputed value (`body_hash_mismatch`) |
| `seal` | `^ed25519:[A-Za-z0-9_-]{86}$` |
| `seal_key_id` | id `sk`; MUST equal `body.seal_key_id` (`seal_key_mismatch`) |

The stored, served and exported form of a deed is `JCS(envelope)`, UTF-8, at most 16,384 bytes,
one deed per line in `.jsonl` files. The Ledger serves these bytes verbatim.

A verifier recanonicalizes: it parses the envelope strictly, recomputes `JCS(body)` from the parsed
value and compares the hash. Whitespace and member order in transit therefore don't matter (a deed
pretty-printed with `jq` still verifies), while every value does. A verifier MAY additionally
require the input bytes to equal `JCS(envelope)` exactly (`deedglass verify --strict`, code
`non_canonical`). Verifiers SHOULD accept inputs up to 64 KiB so pretty-printed deeds parse; the
canonical form MUST still fit in 16 KiB.

## 10. Seal keys and keys.json

### 10.1 Publication

An issuer publishes every seal key it ever registered at
`GET <issuer>/.well-known/deedglass/keys.json`:

```json
{
  "object": "seal_key_set",
  "issuer": "https://api.deedglass.com",
  "updated_at": "2026-10-02T09:17:14.000Z",
  "keys": [
    {
      "object": "seal_key",
      "id": "sk_…",
      "alg": "Ed25519",
      "public_key": "<base64url, 32 bytes>",
      "thumbprint": "sha256:…",
      "status": "active",
      "activated_at": "2026-10-02T09:17:14.000Z",
      "retired_at": null,
      "revoked_at": null,
      "jwk": { "kty": "OKP", "crv": "Ed25519", "x": "<= public_key>", "kid": "<= id>", "use": "sig" }
    }
  ]
}
```

Keys are listed oldest activation first and are never removed. `jwk` is the same key as an RFC 8037
JWK; it omits `alg` because RFC 9864 deprecates `EdDSA` in favour of `Ed25519`, and many JOSE
libraries don't know the new name yet. The response is public, cacheable for 300 s, carries a strong
`ETag`, and may be fetched cross-origin.

### 10.2 Checking a key set

A verifier MUST refuse the **whole** key set (`seal_key_rejected`) when any key:

- has a public key that is not 32 bytes, is a non-canonical encoding (y ≥ p, or x = 0 with the
  sign bit set), is not on the curve, or is of small order (8·A is the identity). With such a key a
  single signature can verify many messages; Go's `crypto/ed25519` and others accept them, so the
  check is the verifier's job (attack case A13; `testdata/deeds/keys/small-order-keys.json` lists
  the 12 encodings to refuse);
- has a `thumbprint` that is not the SHA-256 of its public key, a `jwk` that differs from it, an
  `alg` other than `Ed25519`, or a `status` that contradicts its times (`active`: both null;
  `retired`: `retired_at` set; `revoked`: `revoked_at` set);
- shares its `id` with another key.

### 10.3 Validity

A deed is valid under seal key K when K is in the set, K is **not revoked**, and
`K.activated_at ≤ issued_at < K.retired_at` (open-ended when `retired_at` is null).

A revoked key fails **every** deed it sealed, whatever its `issued_at`: someone holding a stolen key
can write any `issued_at` they like. The issuer re-seals what it still trusts with a new key.

### 10.4 Rotation

`deedglass keys rotate --overlap 5m` registers a new key as `active` and sets the old key's
`retired_at` to now + overlap, in one transaction. Both stay published. The issuer refuses to seal
with a key that retires within 30 seconds of `issued_at`, so an instance that missed the rotation
errors instead of sealing at the edge of the window. docs/SEAL_KEYS.md has the procedure.

Seal keys are deployment-wide in v1: one set per Deedglass instance, shared by all orgs. Per-org
keys are planned for v2.

## 11. Verification algorithm

### 11.1 One deed

Given the deed's bytes and a checked key set (section 10.2), a verifier runs these checks in this
order. The **first** failure is the result.

| # | Check | Code |
|---|---|---|
| 1 | At most 64 KiB, the strict profile (section 6), and the top level is an object | `malformed` |
| 2 | `object` is `"deed"` | `schema_invalid` (field `object`) |
| 3 | When `id` and `body.deed_id` are both strings, they are equal | `id_mismatch` |
| 4 | When `body.v` is an integer, it is `1` | `unsupported_version` |
| 5 | The closed schema of sections 4 and 5: keys, types, nullability, patterns, enums, lengths; then the envelope fields of section 9 | `schema_invalid` (field: dotted path such as `body.execution.status`) |
| 6 | `JCS(envelope)` is at most 16,384 bytes | `malformed` |
| 7 | `"sha256:" + hex(SHA-256(JCS(body)))` equals `body_hash` | `body_hash_mismatch` |
| 8 | `envelope.seal_key_id` equals `body.seal_key_id` | `seal_key_mismatch` |
| 9 | The key set holds that key | `seal_key_unknown` |
| 10 | The key is not revoked | `seal_key_revoked` |
| 11 | `activated_at ≤ issued_at < retired_at` | `seal_key_not_valid_at` |
| 12 | Ed25519 verifies `seal` over `"deedglass/deed/v1\n" + body_hash` (refusing `S ≥ L` and non-canonical `R`, as RFC 8032 requires) | `seal_invalid` |
| 13 | Optional, `--strict`: the input bytes equal `JCS(envelope)` | `non_canonical` |

For schema checks within one object, unknown members are reported first (the alphabetically first
one), then missing members in the order of section 4.

### 11.2 A chain

Feed each deed that passed 11.1 to a chain check, in input order:

| # | Check | Code |
|---|---|---|
| 1 | The deed's `org` equals the chain's org (the first deed's, or the one the reader expects) | `chain_org_mismatch` |
| 2 | First deed: `seq` is 1 (or the anchor's `seq` + 1 when the reader holds an earlier head) | `chain_seq_gap` |
| 3 | First deed: `prev_hash` is the org's genesis value (or the anchor's `body_hash`) | `chain_genesis_mismatch` |
| 4 | Next deeds: `seq` is greater than the previous one | `chain_seq_out_of_order` |
| 5 | Next deeds: `seq` is exactly the previous one + 1 | `chain_seq_gap` |
| 6 | `prev_hash` equals the previous deed's `body_hash` | `chain_prev_hash_mismatch` |
| 7 | `issued_at` is not earlier than the previous deed's | `chain_time_regression` |

### 11.3 Codes and messages

The sentences a verifier shows (`internal/deed/messages.go`; S05's CLI and browser verifier show
exactly these):

| Code | Sentence | Next step |
|---|---|---|
| `malformed` | Deed {seq} is not valid strict JSON ({detail}). | Export it again from the issuer; do not edit deeds by hand. |
| `unsupported_version` | Deed {seq} uses deed format v{v}. This verifier reads v1. | Update deedglass. |
| `schema_invalid` | Deed {seq} has an invalid field: {field}. | Export it again from the issuer. |
| `id_mismatch` | Deed {seq}'s id does not match its body. | Export it again from the issuer. |
| `body_hash_mismatch` | Deed {seq} was changed after it was sealed. | Get a fresh export from the issuer and compare deed {seq}. |
| `seal_key_mismatch` | Deed {seq} names two different seal keys. | Treat this deed as altered. |
| `seal_key_unknown` | Deed {seq} names seal key {key}, which is not in the key set. | Fetch the issuer's keys: --keys <issuer>/.well-known/deedglass/keys.json |
| `seal_key_rejected` | The key set contains an unsafe key ({key}). | Do not use this key set. Fetch it again from the issuer. |
| `seal_key_not_valid_at` | Deed {seq} was sealed at {issued_at}, outside seal key {key}'s active window. | Treat this deed as altered, or check you have the issuer's full key set. |
| `seal_key_revoked` | Deed {seq} was sealed by revoked key {key}. | Ask the issuer which deeds they re-sealed after the revocation. |
| `seal_invalid` | Deed {seq}'s seal does not match its contents. | Treat this deed as altered. |
| `non_canonical` | Deed {seq} is not in canonical form (--strict). | Re-export it; canonical bytes come straight from the Ledger. |
| `keys_missing` | Cannot check deed {seq}: no seal keys were given. | Add --keys <file or https URL>. Issuers publish them at /.well-known/deedglass/keys.json |
| `bundle_invalid` | {detail} Nothing was extracted. | Do not use this file. Ask the issuer for a new export. |
| `bundle_seal_invalid` | The bundle manifest's seal does not match the manifest. | Treat this bundle as altered. Ask the issuer for a new export. |
| `chain_genesis_mismatch` | Deed 1 does not start this org's chain. | Check the org id; the export may belong to another org. |
| `chain_seq_gap` | Deed {want} is missing. The chain jumps from {prev} to {seq}. (Without an anchor, a first deed after seq 1: "The deeds start at deed {seq}, not deed 1.") | A deed was removed or the export is incomplete. Ask the issuer for seq {prev}–{seq}. |
| `chain_seq_out_of_order` | Deed {seq} appears out of order (after {prev}). | Sort by seq, or export again. |
| `chain_prev_hash_mismatch` | Deed {seq} does not link to deed {prev}. | A deed before it was changed or replaced. Get a fresh export. |
| `chain_org_mismatch` | Deed {seq} belongs to {org}, not {want_org}. | Verify each org's deeds separately. |
| `chain_time_regression` | Deed {seq} was sealed before deed {prev}. | Treat the chain as altered from deed {seq}. |

When `seq` is unknown (an unreadable deed), "Deed {seq}" reads "The deed". `keys_missing`,
`bundle_invalid` and `bundle_seal_invalid` were added in S05; the templates are written once in
`internal/deed/messages.go` and generated into `testdata/deeds/messages.json` and the browser
verifier. `deedglass verify` exits 0 when everything verifies, 1 on any failure (including a deed
whose key is not in the key set it was given, `seal_key_unknown`), and 2 when it cannot decide
(`keys_missing`: no key set at all; `unsupported_version`).

## 12. COSE_Sign1 encoding

A deed MAY also be exported as a tagged COSE_Sign1 (RFC 9052, CBOR tag 18):

| Part | Value |
|---|---|
| protected `1` (alg) | `-8` (EdDSA, with crv Ed25519) |
| protected `3` (content_type) | `"application/vnd.deedglass.deed.v1+json"` |
| protected `4` (kid) | the seal key id as a byte string |
| protected `15` (CWT Claims, RFC 9597) | `{1 iss: <issuer>, 2 sub: "<org>/<deed_id>", 6 iat: <Unix seconds>}` |
| payload | `JCS(body)`, attached |
| signature | Ed25519 over the COSE `Sig_structure` (`"Signature1"`, protected, empty external AAD, payload) |

It is a second, independent signed statement about the same body, made at export time with the
issuer's **active** seal key; the native `seal` stays the primary record. A COSE verifier checks the
tag, `alg`, `content_type`, that `kid` is in the key set and valid at `iat`, the signature, that `iss`
and `sub` are present and `sub` names the payload's org and deed id, and that the payload is
canonical and a valid Deed v1 body.

It follows the shape of a Signed Statement in RFC 9943: CWT Claims with `iss` and `sub` in the
protected header. It is not registered with any transparency service, and it is not one of the
objects defined in RFC 9942, "CBOR Object Signing and Encryption (COSE) Receipts": no transparency
service has seen it. RFC 9864 registers `-19` for Ed25519; v1 uses `-8` because the pinned
go-cose v1.3.0 does not support `-19`. The signature bytes are identical.

## 13. Versioning

- A new optional field, or a new enum value, is **v1.x**: the closed schema widens only by a revision
  of this document, published together with new vectors. A v1 verifier that hasn't been updated
  refuses such deeds as `schema_invalid` rather than ignoring what it can't read.
- A changed meaning, hash or seal input is **v2**, with `v: 2` and a new domain tag. A v1 verifier
  reports `unsupported_version` (exit 2: it cannot decide).
- Codes in section 11 are never renamed. A new failure gets a new code.

## 14. Privacy

A deed carries digests, never payloads: no SQL text, no email addresses, no amounts beyond `cost`,
no tool results. The schema is closed, so a payload cannot be smuggled in through an extra field.
`on_behalf_of` and `session` are copied from the agent's headers; issuers and integrators SHOULD
pass opaque references (`usr_ref:4821`), never email addresses or names. Digests of small payloads
can be guessed by trying values; treat digests of low-entropy data (a yes/no answer, a short
amount) as revealing that data.

## 15. Security considerations

- **Seal key theft.** Whoever holds a seal key can seal any deed, with any `issued_at`. Keep key
  files at mode 0600 (the issuer refuses wider modes), rotate, and revoke on suspicion: revocation
  makes every deed of that key fail. KMS and HSM signing are planned (docs/SEAL_KEYS.md).
- **One issuer.** A deed is signed by the deployment that issued it. It shows what that deployment
  recorded; it does not add an independent witness. Registration with a transparency service
  (sections 12 and 13) is future work.
- **Tail deletion** is not visible in the chain alone (section 8.4).
- **Test keys.** The keys in `testdata/deeds/keys/` have public seeds. Deedglass refuses to load them
  in prod; a verifier SHOULD warn when a key set contains their thumbprints.
- **Small-order keys** (section 10.2) must be refused by the verifier, not only by the issuer.
- **Canonicalization ambiguity** is refused, never repaired (section 6).
- **Cross-protocol signatures** are prevented by the domain tags (section 7.2) and by using separate
  keys for seals and signed intents.

## 16. Worked example

Every byte below comes from `testdata/deeds/worked-example.txt`, which the vector generator writes
(`go test ./internal/deed -run TestGoldenVectors -update`). The deed is
`testdata/deeds/valid/01-action-verified-l2.json`. **Test key a: never trust it.**

Seal key:

```text
seal key id   sk_01m3xxf0m1crvjtd24mgqnmgah
seed (hex)    c3e4d1ffac933410b6290312fcde687e364ecddc837940e708ef89232bca4dac   = SHA-256("deedglass/test-vector-key/a")
public key    pEzUz7QOOPD171uzsw5ASsNYKkd5Uq4K9zc4PQUk2e8   (base64url)
thumbprint    sha256:f6b1d24a4efdc39230905e0c217093f4d1d5e301ac52c9e763a773e864d93f33
```

Genesis (seq 1):

```text
org           org_01m3xxf0m1j6c7jawy99f93bhb
input         "deedglass/genesis\norg_01m3xxf0m1j6c7jawy99f93bhb"
prev_hash     sha256:0a83f030e30b1313dd2168b5362ab5d30930a252a2ce8df0226160f5e7da350d
```

`JCS(body)`, 1,417 bytes:

```json
{"action":{"connector":"con_01m3xxf0m1brgcvtgpf81k7skv","id":"act_01m3xxf0m1s0xcra979g0jfa5y","intent_hash":null,"intent_signed":false,"mode":"gateway","request_digest":"sha256:f9a265a627598090eda73f296064b1db59c7295a4367f7c9780808af2137b1d7","tool":"postgres.execute"},"admin":null,"agent":{"id":"agt_01m3xxf0m1v4emq98a0jp8qj9z","name":"billing-agent"},"amends":null,"approval":null,"cost":{"amount":120,"currency":"usd"},"deed_id":"deed_01m3xxj291vb6nxg25pympae2g","execution":{"error_code":null,"finished_at":"2026-10-02T08:59:59.841Z","latency_ms":41,"result_digest":"sha256:8b8808243de0b66ff885ef7902186fc4b37bfe2dbb62907e0301c5f7753434ba","started_at":"2026-10-02T08:59:59.800Z","status":"succeeded"},"issued_at":"2026-10-02T09:00:00.000Z","kind":"action","on_behalf_of":"usr_ref:priya","org":"org_01m3xxf0m1j6c7jawy99f93bhb","policy":{"decision":"allow","id":"pol_01m3xxf0m13yc5js9w392rsx8r","mode":"enforce","rule_id":"allow-billing-updates","version_hash":"sha256:18b4687da8d05494dcc0adc5a8f18efe6900f6a5f8faa4b519accdd01e15b3b5","version_id":"pv_01m3xxf0m1e81mgsvsfcj3shx3"},"prev_hash":"sha256:0a83f030e30b1313dd2168b5362ab5d30930a252a2ce8df0226160f5e7da350d","seal_key_id":"sk_01m3xxf0m1crvjtd24mgqnmgah","seq":1,"session":"ses-7f3a","v":1,"witness":{"detail_digest":"sha256:7254304143e36a4e106c525e35b74e30961135caf01c192641c9e6322a741d37","level":"L2","method":"postgres.pgoutput","verdict":"VERIFIED"}}
```

Hash, seal input and seal:

```text
body_hash     sha256:63c2f195853d6251fa7c69e8f485002c4421c412951ebbae0757dfd20745e4d9

seal input (89 bytes, hex):
64656564676c6173732f646565642f76310a7368613235363a36336332663139353835336436323531
666137633639653866343835303032633434323163343132393531656262616530373537646664323037
343565346439

seal          ed25519:A0ziAW6MG6UrFN1eWF_MJ5oRCjttYFaiLP7346QQ3McEv5Nglh6oPOtnAww117hD7kGG7yuoFGtM0jOu7jLTDg
```

`JCS(envelope)` is the 1,717 bytes of the vector file (SHA-256
`7a23c1a3a221ca170f6c1c9c5fcaea71cd86c344ad35b2dfc1b4ebb64eec5230`).

To check your own implementation: canonicalize the body, hash it, compare with `body_hash`, build
the 89-byte seal input and verify the seal with the public key above. On 2026-10-01 this was done
independently of Go with Python's standard library (JCS and SHA-256) and OpenSSL 3.6.4
(`openssl pkeyutl -verify -rawin`) for the vectors `01`, `05` and `06`, which all verified, and for
`invalid/seal-bit-flip`, which failed. Then run every vector in `testdata/deeds/` and compare your
`code`, `seq` and `field` with its `expected.json`.

## 17. References

Checked 2026-10-01 at rfc-editor.org and proxy.golang.org.

- RFC 8785, JSON Canonicalization Scheme (JCS), June 2020.
- RFC 8032, Edwards-Curve Digital Signature Algorithm (EdDSA), January 2017.
- RFC 8037, CFRG Elliptic Curve Diffie-Hellman (ECDH) and Signatures in JOSE (OKP JWKs), January 2017.
- RFC 4648, base64url, October 2006.
- RFC 9052, CBOR Object Signing and Encryption (COSE): Structures and Process, August 2022.
- RFC 9597, CBOR Web Token (CWT) Claims in COSE Headers, June 2024.
- RFC 9864, Fully-Specified Algorithms for JOSE and COSE, 2025 (Ed25519 = −19).
- RFC 9943, An Architecture for Trustworthy and Transparent Digital Supply Chains, June 2026
  (Proposed Standard): Signed Statements carry CWT Claims with `iss` and `sub`.
- RFC 9942, "CBOR Object Signing and Encryption (COSE) Receipts", June 2026: not produced by Deedglass.
- `github.com/gowebpki/jcs` v1.0.2 (ADR-005); `github.com/veraison/go-cose` v1.3.0, released
  2024-07-19 and still the latest tag on 2026-10-01 (ADR-026).
- ADR-025 (Deed format v1), ADR-008 (superseded in part).
