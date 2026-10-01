# web/verify: the browser verifier

Check a deed in a browser tab: strict JSON, RFC 8785 canonical JSON, SHA-256, the Ed25519 key
rules and seal, and the deed chain, all on the reader's machine. It gives the same answer as
`deedglass verify` on every published test vector and on a 500-case tamper corpus. **The CLI is the
reference verifier**; this page is the same check where people already are.

- **No runtime dependencies.** `src/` imports nothing from npm. TypeScript and Vitest are dev tools
  (ADR-027).
- **Nothing leaves the page.** No analytics, no storage. The only network call is the key fetch the
  person starts with **Fetch keys**, after the page has said which host it contacts
  (`credentials: 'omit'`, `redirect: 'error'`, no referrer, 1 MiB, 10 s).
- **Strict CSP**, no inline scripts or styles, and text enters the page with `textContent` only (a
  test greps for anything else).

## Layout

| Path | What it is |
|---|---|
| `src/strict.ts` | the strict JSON profile (port of `internal/intent`): duplicate keys, 2^53, lone surrogates, depth 16, invalid UTF-8 → `malformed` |
| `src/jcs.ts` | RFC 8785 serializer |
| `src/b64url.ts`, `src/hex.ts`, `src/sha256.ts` | strict base64url, hex, SHA-256 (WebCrypto) |
| `src/keys.ts` | `parseKeySet` and the Ed25519 public key checks with BigInt (y < p, on the curve, 8·A ≠ identity) |
| `src/schema.ts`, `src/deed.ts` | the closed schema by kind and `verifyEnvelope`, in exactly Go's check order |
| `src/chain.ts`, `src/report.ts` | the chain checker, the `Verifier` and the `verification_report` (Go's JSON field names) |
| `src/*.gen.ts` | **generated** from Go by `go run ./internal/deed/cmd/gencopy`: message templates, small-order keys, test-key thumbprints, the sample. Do not edit. |
| `src/ui/` | the page: `main.ts`, the Mirror mark (`mirror.ts`), the key fetch (`fetchkeys.ts`), `styles.css` (brand tokens) |
| `index.html`, `scripts/serve.mjs` | the page and a 30-line static server for local preview |

Public API (`src/index.ts`, what S25 and S36 import): `parseStrict`, `canonicalize`,
`parseKeySet`, `verifyDeed`, `verifyDeeds`, `Verifier`, `message`, `VerifyError`,
`UnsupportedError`.

## Run it

```sh
make web-verify-test     # npm ci (once), typecheck, Vitest
make web-verify-serve    # build to dist/ and serve http://127.0.0.1:5178
```

## Parity rules that are easy to get wrong

- `JSON.parse` accepts duplicate keys and rounds 2^53+1: deed input never goes through it.
- Unknown keys are reported in code point order (Go's byte order), not UTF-16 order; JCS sorts by
  UTF-16. The corpus has keys that tell the two apart.
- Integers from the strict parser are at most 2^53−1, so `String(n)` is exact.
- Browsers disagree on edge-case Ed25519 inputs. Key safety and `s < L` are checked here before
  WebCrypto. A browser without WebCrypto Ed25519 gets the "use the CLI" state, never a verdict.
- Bundles (`.tar.gz`) are CLI-only for now.

## Tests

`test/vectors.test.ts` (every `testdata/deeds` vector except COSE), `parity.test.ts` (500/500 on
`testdata/deeds/parity`), `strict.test.ts`, `jcs.test.ts` (the RFC 8785 pairs in `testdata/jcs`),
`keys.test.ts` (A13 small-order keys, RFC 8032 keys, Wycheproof Ed25519), `safety.test.ts` (CSP, no
HTML from strings, no storage, one fetch).
