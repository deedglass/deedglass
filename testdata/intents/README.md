# Signed-intent vectors (`deedglass.intent/1`)

These files are the conformance suite for every signer and checker of signed intents: the Go
reference (`pkg/deedglass`, `internal/identity`), and the TypeScript and Python SDKs (S31, S32).
The normative spec is `docs/INTENTS.md`; if an implementation disagrees with a vector, the
implementation is wrong.

They are generated, never edited by hand:

```sh
go test ./internal/identity -run TestIntentVectors -update-intents
```

`testdata/SHA256SUMS` pins every file (`make testdata-check`).

## Keys

`keys/test1.pem` and `keys/test2.pem` are the RFC 8032 §7.1 TEST 1 and TEST 2 Ed25519 keys as
PKCS#8 PEM. **They are public. Use them for tests only, never for anything else.**

`keys.json` describes the agent every vector is checked against: org
`org_01k6h7kp6msmzrhdzwtcr3jnkp`, agent `agt_01k633rrmh3bcz2p39gg4vp46q` ("support-bot"),
`intent_mode` optional, and exactly one registered key, test1
(kid `21fe31dfa154a261626bf854046fd227`). test2 is deliberately not registered (vector i15).

## A vector

| Field | Meaning |
|---|---|
| `header` | The `Deedglass-Intent` value sent. |
| `tool` | The call's tool (the Gate's `connector.tool` name). |
| `args` | The exact JSON bytes of the call's arguments, as a string. Hash these bytes' canonical form, not a re-serialization. |
| `headers` | `Deedglass-On-Behalf-Of` and `Deedglass-Session`, when the call sends them. |
| `now` | The checker's clock (RFC 3339). |
| `sends` | How many times the same call is sent; the expected result is the last send's (i26 sends twice). |
| `sign` | Valid vectors only: the signer's inputs (key, org, agent, tool, args, OBO, `now`, `ttl_ms`, `nonce`). A signer given these must produce `header` byte for byte. |
| `expected` | `ok: true` with `intent_hash`, `args_digest`, `args_canonical`, `kid`, the effective `on_behalf_of` and `session`, `expires_at` and `intent_bytes`; or `ok: false` with `status` (always 403), `code` (`intent_invalid`, `intent_replay`, `intent_required`), the stable `reason`, and `param`. |

Each vector starts with an empty nonce ledger.

## How to run them

For each file:

1. **Signer** (valid files): sign `sign.tool` and `sign.args` with `keys/<sign.key>.pem` at
   `sign.now`, with `sign.ttl_ms` and `sign.nonce`. Compare with `header` and
   `expected.intent_hash`.
2. **Checker** (every file): check `header` against `tool`, `args` and `headers` at `now`, for the
   agent in `keys.json`, `sends` times with one nonce ledger. Compare the last result with
   `expected`: the reason and param for a refusal, every listed value for an accepted intent.

## The set

| File | What it covers |
|---|---|
| v01 | The S09 §3.2 cross-check vector: a refund on behalf of `usr_ext_4821` in session `conv_9f2c`. |
| v02 | Minimal: no OBO. |
| v03 | Nested arguments with non-ASCII text. |
| v04 | Fractions: `12.5`, `1e21` → `1e+21`, `0.0000001` → `1e-7`, `0.1`, `-0` → `0`. |
| v05 | Empty arguments `{}`. |
| v06 | A large number sent as a string. |
| v07 | The longest lifetime, 300000 ms. |
| v08 | `now = exp + 59 s`, inside the skew. |
| i01 to i03 | `syntax`: bad base64url, three parts, a 63-byte signature. |
| i04 | `too_large`: 2,049 bytes. |
| i05 to i06 | `strict`: a duplicate key, the integer 2^53. |
| i07 to i08 | `not_canonical`: unsorted keys, a space. |
| i09 to i12 | `schema` and `version`: an unknown field, a missing nonce, a bad nonce, version 2. |
| i13 to i14 | `org_mismatch`, `agent_mismatch`. |
| i15 to i17 | `unknown_kid`; `bad_signature` (a flipped bit, and a signature without the `"deedglass/intent/v1\n"` domain tag). |
| i18 to i21 | `expired`, `not_yet_valid`, `ttl` (300001 ms, and `exp = iat`). |
| i22 to i23 | `tool_mismatch`, `args_mismatch` (amount 120000 instead of 1200). |
| i24 to i25 | `obo_mismatch`, `unsigned_obo`. |
| i26 | `replay`: v01 sent twice. |
