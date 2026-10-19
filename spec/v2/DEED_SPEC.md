# Deed specification (v1 and v2)

> Licence: [CC-BY-4.0](../LICENSE) (the code that implements it is Apache-2.0). Part of the record
> spec, edition 2: see [spec/README.md](../README.md) for editions, the change log and how to cite it.
> Edition 2 describes both `v: 1` and `v: 2` records; edition 1 ([`../v1/DEED_SPEC.md`](../v1/DEED_SPEC.md))
> stays as written.

**Status:** normative for Deed v1 and Deed v2 · **Owner:** `internal/deed` · **Vectors:** `testdata/deeds/`
(v1) and `testdata/deeds/v2/` (v2) · **Replaces:** `docs/v0/STATEMENT_SPEC.md` (the V0 verification
statement) · **Checked:** 2026-10-19 · **Changes:** section 18

Deed v2 (ADR-109) differs from v1 in one thing: the body names every seal it carries
(`seal_keys`), the envelope carries one seal per key (`seals`), and every seal names its algorithm.
Everything else (the body's other fields, the canonical form, the hash, the chain) is the same, and
v1 and v2 deeds sit in one chain. Sections 4, 7, 9, 10 and 11 say where v2 differs.

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
| **Seal** | A signature over the body hash with a domain tag, by one algorithm of section 7.4 (v1: Ed25519 only). "Sealed" means signed this way. |
| **Seal key** | A key pair an issuer seals with, of one algorithm. Its public half is published in `keys.json`. |
| **Primary seal key** | v1: the deed's only seal key. v2: the first entry of `seal_keys`. |
| **Deed chain** | The sequence of an org's deeds, numbered by `seq` and linked by `prev_hash`. |
| **Issuer** | The Deedglass deployment that sealed the deed, named by its base URL. |
| **Verifier** | Anyone checking deeds: `deedglass verify`, the browser verifier, or your own code. |
| **Org** | The customer account a deed belongs to. Each org has its own chain. |

## 4. Body fields

Every body has exactly these 19 keys, always present, with `null` where a section doesn't apply
(section 5). A v2 body has the same 19 keys except that `seal_key_id` is replaced by `seal_keys`. Unknown keys at any level MUST be refused (`schema_invalid`). Free text is limited to
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
| `v` | integer | no | `1` or `2` | Format version. Anything else is `unsupported_version`. |
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
| `seal_key_id` | string | no | id `sk` | v1 only. The seal key. MUST equal the envelope's `seal_key_id`. |
| `seal_keys` | array | no | 1 to 4 objects | v2 only. Every key the deed carries a seal of, the primary first (below). |

**`seal_keys`** (v2): each entry is `{"alg", "id"}`: `alg` matches `^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`
(section 7.4 lists the algorithms; a verifier MUST accept other names matching the pattern at this
step), `id` is an id `sk`. No two entries share an `alg` or an `id`. Because the list is inside the
body, every seal covers it: a seal removed from the envelope is detected (`seal_missing`), never
silently ignored.

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
| `body_hash` | `"sha256:" + lowercase_hex(SHA-256(JCS(body)))` (v1 and v2) |
| seal input (v1) | the ASCII bytes `"deedglass/deed/v1\n" + body_hash`: 18 + 71 = 89 bytes |
| `seal` (v1) | `"ed25519:" + base64url_nopad(Ed25519_Sign(seal_key, seal input))`: 8 + 86 = 94 characters |
| seal input (v2) | the algorithm's tag (section 7.4) + body_hash, for example `"deedglass/deed/v2/ed25519\n" + body_hash` |
| seal (v2) | the algorithm's prefix + base64url_nopad(signature over the v2 seal input) |
| thumbprint | `"sha256:" + lowercase_hex(SHA-256(raw 32-byte public key))` |

`\n` is the single byte 0x0A. Ed25519 is pure Ed25519 as in RFC 8032 section 5.1 (no pre-hash, no
context), with 64-byte signatures. base64url is RFC 4648 section 5 without padding; a verifier MUST
refuse a value with non-zero trailing bits.

### 7.2 Domain tags

The seal signs a tagged string, never bare bytes, so a signature made for one purpose can never be
presented as another:

| Tag | Used for |
|---|---|
| `deedglass/deed/v1\n` | v1 deed seals (this document) |
| `deedglass/deed/v2/<alg>\n` | v2 deed seals, one tag per algorithm (section 7.4) |
| `deedglass/bundle/v1\n` | sealed export-bundle manifests (S05) |
| `deedglass/intent/v1\n` | signed intents from agents (S09), with separate keys |
| `deedglass/genesis\n` | the first link of a chain (section 8); hashed, never signed |

### 7.3 Payload digests

`request_digest`, `result_digest`, `detail_digest` and `data_digest` are
`"sha256:" + lowercase_hex(SHA-256(JCS(value)))` with plain RFC 8785 and **no** strict profile:
tool arguments may hold fractions and negative numbers. Two payloads that differ only in
whitespace or member order have the same digest.

### 7.4 Seal algorithms (v2)

| `alg` | Seal prefix | v2 domain tag | Public key | Signature | Accepted alone for `issued_at` |
|---|---|---|---|---|---|
| `Ed25519` | `ed25519:` (then 86 characters) | `deedglass/deed/v2/ed25519\n` | 32 bytes | 64 bytes, pure Ed25519 (RFC 8032 §5.1) | before `2035-01-01T00:00:00.000Z` |
| `ML-DSA-65` | `ml-dsa-65:` (then 4,412 characters) | `deedglass/deed/v2/ml-dsa-65\n` | 1,952 bytes (FIPS 204 encoding) | 3,309 bytes, ML-DSA.Sign of FIPS 204 (pure, not HashML-DSA) with the empty context string | always |

The ML-DSA-65 seal signs the same kind of tagged string as Ed25519 with the empty context, as RFC 9964
requires for COSE and JOSE, so any FIPS 204 library checks it. The issuer signs hedged (FIPS 204's
default); the test vectors use the deterministic variant so they are reproducible. Both verify the
same way. An issuer that holds an Ed25519 key and an ML-DSA-65 key writes v2 deeds with
`seal_keys` = `[{Ed25519}, {ML-DSA-65}]`: the **dual seal**.

A tag per algorithm and per version means a signature made for one algorithm, one version or one
purpose is never accepted as another. The date in the last column is the accept policy of section
11.4; NIST IR 8547 (initial public draft, 12 Nov 2024) lists EdDSA as disallowed after 2035. A
verifier MUST treat an `alg` that is not in this table as one it cannot check
(`unsupported_algorithm`), and MUST NOT check a seal with a key of another algorithm.

A seal of an algorithm not in the table still has a fixed shape: the lower-case `alg`, a colon, and
base64url (`^[a-z0-9][a-z0-9-]{0,31}:[A-Za-z0-9_-]+$`).

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

**v2 envelope.** `seal` and `seal_key_id` are replaced by `seals`, one entry per `body.seal_keys`
entry, in the same order:

```json
{ "object": "deed", "id": "deed_…", "body": { … }, "body_hash": "sha256:…",
  "seals": [ { "alg": "Ed25519", "seal": "ed25519:…", "seal_key_id": "sk_…" } ] }
```

| Field | Rule |
|---|---|
| `seals` | 1 to 4 objects `{"alg", "seal", "seal_key_id"}`; `alg` as in `seal_keys`; `seal` of the shape its algorithm needs (section 7.4); `seal_key_id` an id `sk` |

The stored, served and exported form of a deed is `JCS(envelope)`, UTF-8, at most 16,384 bytes for
v1 and 24,576 bytes for v2, one deed per line in `.jsonl` files. The Ledger serves these bytes verbatim.

A verifier recanonicalizes: it parses the envelope strictly, recomputes `JCS(body)` from the parsed
value and compares the hash. Whitespace and member order in transit therefore don't matter (a deed
pretty-printed with `jq` still verifies), while every value does. A verifier MAY additionally
require the input bytes to equal `JCS(envelope)` exactly (`deedglass verify --strict`, code
`non_canonical`). Verifiers SHOULD accept inputs up to 64 KiB so pretty-printed deeds parse; the
canonical form MUST still fit in 16 KiB (v2: 24 KiB).

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

`alg` names the key's algorithm (section 7.4). An ML-DSA-65 key's `public_key` is base64url of its
1,952-byte FIPS 204 encoding, its `thumbprint` the SHA-256 of those bytes, and its `jwk` an RFC 9964
AKP key, in which `alg` is required: `{"kty": "AKP", "alg": "ML-DSA-65", "pub": <= public_key>,
"kid": <= id, "use": "sig"}`. Keys are listed oldest activation first and are never removed. `jwk` is the same key as an RFC 8037
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
  `alg` that doesn't match `^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`, or a `status` that contradicts its
  times (`active`: both null; `retired`: `retired_at` set; `revoked`: `revoked_at` set);
- shares its `id` with another key.

A key whose `alg` matches the pattern but is not one the verifier implements is **kept and never
used** (since ADR-109; before it, any `alg` other than `Ed25519` refused the whole set). The verifier
checks what it can without the algorithm (`id`, `status` and times, `thumbprint`, `jwk.kid`,
`jwk.use`, a non-empty `jwk.kty`, a `jwk` holding only `kty`, `crv`, `x`, `alg`, `pub`, `kid`, `use`);
a deed that names the key is `unsupported_algorithm`, and the set's other keys verify their deeds.
Without this, a verifier would refuse every deed the day an issuer adds a key of a newer algorithm.

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

**Adding an ML-DSA-65 key** (ADR-109): `deedglass keys rotate --alg ML-DSA-65 --overlap 5m` writes
an ML-DSA-65 key file and registers the key **beside** the Ed25519 key, which it never touches. The
first one activates at now + overlap, so every instance can be restarted with
`DEEDGLASS_SEAL_PQ_KEY_FILE` before the key-set rule (section 11.4) wants its seal; an instance that
holds it earlier keeps sealing v1 until then. A later one activates at once and retires the previous
ML-DSA-65 key after the overlap, as an Ed25519 rotation does. Once the key is active, the issuer
refuses to seal (an error, never a weaker deed) on an instance that doesn't hold it.

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
| 4 | When `body.v` is an integer, it is `1` or `2` | `unsupported_version` |
| 5 | The closed schema of sections 4 and 5 for that version: keys, types, nullability, patterns, enums, lengths; then the envelope fields of section 9 | `schema_invalid` (field: dotted path such as `body.execution.status` or `seals[1].seal`) |
| 6 | `JCS(envelope)` is at most 16,384 bytes (v2: 24,576) | `malformed` |
| 7 | `"sha256:" + hex(SHA-256(JCS(body)))` equals `body_hash` | `body_hash_mismatch` |
| 8 | v2: every `body.seal_keys` entry has a seal naming its key id | `seal_missing` |
| 9 | v1: `envelope.seal_key_id` equals `body.seal_key_id`. v2: `seals` are exactly `seal_keys`, same order, same `alg` and key id, nothing extra | `seal_key_mismatch` |
| 10 | Then for each seal, in order (v1: the one Ed25519 seal): its `alg` is one this verifier implements | `unsupported_algorithm` |
| 11 | The key set holds that key | `seal_key_unknown` |
| 12 | The key's own `alg` is the seal's `alg` | `seal_invalid` |
| 13 | The key is not revoked | `seal_key_revoked` |
| 14 | `activated_at ≤ issued_at < retired_at` | `seal_key_not_valid_at` |
| 15 | The signature verifies over the seal input: v1 `"deedglass/deed/v1\n" + body_hash`, v2 the algorithm's tag + body_hash (Ed25519 refusing `S ≥ L` and non-canonical `R`, as RFC 8032 requires) | `seal_invalid` |
| 16 | The accept policy (section 11.4) | `seal_missing` |
| 17 | Optional, `--strict`: the input bytes equal `JCS(envelope)` | `non_canonical` |

**Every seal must pass.** One seal that verifies never covers another that doesn't.

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
| `unsupported_version` | Deed {seq} uses deed format v{v}, which this verifier can't read. | Update deedglass. |
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
| `unsupported_algorithm` | Deed {seq} is sealed with an algorithm this verifier can't check ({detail}). | Update deedglass, or check it with a verifier that supports that algorithm. |
| `seal_missing` | Deed {seq} is missing a seal it must carry ({detail}). | Treat this deed as altered, or ask the issuer for the deed as it was sealed. |

When `seq` is unknown (an unreadable deed), "Deed {seq}" reads "The deed". `keys_missing`,
`bundle_invalid` and `bundle_seal_invalid` were added in S05; the templates are written once in
`internal/deed/messages.go` and generated into `testdata/deeds/messages.json` and the browser
verifier. `unsupported_algorithm` and `seal_missing` were added by ADR-109. `deedglass verify` exits 0 when
everything verifies, 1 on any failure (including a deed whose key is not in the key set it was
given, `seal_key_unknown`), and 2 when it cannot decide (`keys_missing`: no key set at all;
`unsupported_version`; `unsupported_algorithm`).

### 11.4 The accept policy

Applied to v1 and v2 deeds whose seals all verified, by `issued_at` only, never by the verifier's
clock (so a deed gets the same answer in 2026 and in 2046):

1. **Alone.** At least one seal's algorithm is accepted alone at `issued_at` (section 7.4: Ed25519
   only before `2035-01-01T00:00:00.000Z`).
2. **The key-set rule.** For every algorithm the verifier implements of which the key set holds a key
   that is not revoked and was active at `issued_at` (`activated_at ≤ issued_at < retired_at`), the
   deed carries a seal of that algorithm. An issuer that publishes an active key of a second algorithm
   seals every deed with it; a deed issued inside that key's window without such a seal is refused.

Either failure is `seal_missing`. A verifier MAY apply a stricter policy; it MUST NOT apply a looser
one and report the deed as verified.

**What the policy does not show.** Someone who can sign with a seal key (a stolen key today, a broken
algorithm later) can also write any `issued_at`. So the policy only stops forged deeds dated after
the issuer's second key became active. Earlier deeds are protected by the chain: a later deed with
a second seal covers every earlier deed through `prev_hash`, so a reader who keeps that head detects a
forged earlier deed as a broken link (section 8.4).

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

**v2 deeds** (ADR-109): an issuer with an ML-DSA-65 key signs the COSE export of a v2 deed with it:
protected alg **−49** (ML-DSA-65, RFC 9964), content type `"application/vnd.deedglass.deed.v2+json"`,
the same kid and CWT claims, and the signature is ML-DSA-65 (pure, empty context) over the
`Sig_structure`. A COSE verifier also checks that the key's `alg` is the COSE alg's algorithm and that
the content type names the payload's version. An issuer without an ML-DSA-65 key signs v2 exports with
Ed25519 (−8) and the v2 content type.

It follows the shape of a Signed Statement in RFC 9943: CWT Claims with `iss` and `sub` in the
protected header. It is not registered with any transparency service, and it is not one of the
objects defined in RFC 9942, "CBOR Object Signing and Encryption (COSE) Receipts": no transparency
service has seen it. RFC 9864 registers `-19` for Ed25519; v1 uses `-8` because the pinned
go-cose v1.3.0 does not support `-19`. The signature bytes are identical.

## 13. Versioning

- A new optional field, or a new enum value, is **v1.x**: the closed schema widens only by a revision
  of this document, published together with new vectors. A v1 verifier that hasn't been updated
  refuses such deeds as `schema_invalid` rather than ignoring what it can't read.
- A changed meaning, hash or seal input is a new version, with a new `v` and a new domain tag. A
  verifier that doesn't know the version reports `unsupported_version` (exit 2: it cannot decide).
  **v2** (ADR-109) is such a version: deedglass 0.4.0 and older report v2 deeds as
  `unsupported_version`.
- An issuer with only an Ed25519 seal key keeps writing v1, so older verifiers keep reading its deeds;
  it writes v2 only when it holds a key of a second algorithm.
- A new algorithm is a new row in section 7.4 with its own tag, published with new vectors. Older
  verifiers report deeds that need it as `unsupported_algorithm`.
- Test vectors for every version are kept forever.
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
- **Algorithm downgrade and stripping** (v2): the body names every seal (`seal_keys`), every seal must
  verify, a seal is never checked under another algorithm, and the accept policy (section 11.4)
  refuses Ed25519 alone after 2035 or inside the window of an issuer's key of a second algorithm.
  Attack cases Y01–Y12 (`attack/attack_meta.json`).

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
- NIST IR 8547 (initial public draft), Transition to Post-Quantum Cryptography Standards, 12 November
  2024: EdDSA disallowed after 2035 (section 7.4's date).
- RFC 8037, CFRG Elliptic Curve Diffie-Hellman (ECDH) and Signatures in JOSE (OKP JWKs), January 2017.
- RFC 4648, base64url, October 2006.
- RFC 9052, CBOR Object Signing and Encryption (COSE): Structures and Process, August 2022.
- RFC 9597, CBOR Web Token (CWT) Claims in COSE Headers, June 2024.
- RFC 9864, Fully-Specified Algorithms for JOSE and COSE, 2025 (Ed25519 = −19).
- FIPS 204, Module-Lattice-Based Digital Signature Standard (ML-DSA), August 2024.
- RFC 9964, ML-DSA for JOSE and COSE, May 2026 (ML-DSA-65 = −49; key type AKP; empty context).
- Go 1.27 `crypto/mldsa` (the product's Go is 1.27.2).
- RFC 9943, An Architecture for Trustworthy and Transparent Digital Supply Chains, June 2026
  (Proposed Standard): Signed Statements carry CWT Claims with `iss` and `sub`.
- RFC 9942, "CBOR Object Signing and Encryption (COSE) Receipts", June 2026: not produced by Deedglass.
- `github.com/gowebpki/jcs` v1.0.2 (ADR-005); `github.com/veraison/go-cose` v1.3.0, released
  2024-07-19 and still the latest tag on 2026-10-01 (ADR-026).
- ADR-025 (Deed format v1), ADR-008 (superseded in part).

## 18. Changes

| Date | Change | Record |
|---|---|---|
| 2026-10-01 | Deed v1. | ADR-025 |
| 2026-10-19 | The dual seal (TY-02): ML-DSA-65 in section 7.4, AKP keys in `keys.json` (section 10.1), `keys rotate --alg ML-DSA-65` (section 10.4), COSE −49 for v2 deeds (section 12). Vectors: the dual-sealed deeds, the chain across the rotation and every ML-DSA-65 failure in `testdata/deeds/v2/`. | ADR-109, TY-02 |
| 2026-10-19 | Deed v2: `seal_keys` in the body, `seals` in the envelope, one domain tag per algorithm (section 7.4), the v2 size limit (24 KiB). Verification order for v2 and the accept policy (section 11.4), applied to v1 deeds too: Ed25519 alone only before 2035-01-01, and the key-set rule. New codes `unsupported_algorithm` (exit 2) and `seal_missing`; the `unsupported_version` sentence no longer says "reads v1". Key sets keep keys of algorithms the verifier doesn't implement instead of refusing the whole set. Every v1 deed ever written verifies as before. Vectors: `testdata/deeds/v2/`; the v1 vector `invalid/version-2` is now `invalid/version-3`. | ADR-109, TY-01 |
