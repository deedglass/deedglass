# Architecture decision records

The canonical ADRs are written and approved in the private ops repository at `~/txv/ops/adr/`. While this product repo is private, this directory holds only a mirror of the index below; accepted, publishable ADRs are copied here before the repo goes public.

Process: Claude Code drafts, the founder approves. Status moves `Proposed` → `Accepted (date)`, or `Superseded by ADR-NNN (date)`. Any deviation from the architecture contract, any new dependency, and any change to an expected outcome in `attack/attack_meta.json` needs an approved ADR.

ADR-000 to ADR-008 were written for the V0 verifier and keep their original text; ADR-012 renamed the permanent identifiers they mention. ADR-009 to ADR-011 were withdrawn when V0 was parked (DL-012) and never written; their numbers are not reused.

| ADR | Title | Status (2026-10-01) | Summary |
|---|---|---|---|
| 000 | Why we proceed despite the recorded dissents | Accepted; write-up pending review | Proceed with a narrowed V0 under pre-committed tripwires T1–T6 |
| 001 | Language: Go, one static binary | Proposed | Go with the spike-verified pins (pgx v5.11.0, pglogrepl pseudo-version, jcs v1.0.2, go-cose v1.3.0); binary renamed by ADR-012 |
| 002 | CDC: pgoutput proto 1 via SQL peek-then-advance | Proposed | `safeParse` wrapper, pinned session settings, frame-position classification, `marker_lsn` as evidence only |
| 003 | Marker emitted last; authenticated only by the same-transaction ledger row | Proposed | One ledger row and one marker per transaction; the entry guard is "first write" |
| 004 | Signature verified outside Postgres, twice | Proposed | Before commit (prevention) and after commit (detection); the database does every non-crypto check |
| 005 | Intent: closed-schema JCS + Ed25519, amounts as strings | Proposed | Strict integer pre-parser before JCS; small-order key rejection (A13) |
| 006 | Operation Bundle authority | Proposed | Vendor authors, admin activates, principal signs the hash; exact expected-delta equality |
| 007 | Observation boundary | Proposed | Explicit publication before the slot, hashed boundary manifest, bracketing snapshots, gap taxonomy |
| 008 | Statement format: COSE_Sign1, alg −8 | Superseded in part by ADR-025 (2026-10-01) | Signed statement shape; −8 because go-cose 1.3.0 lacks −19; hash-chained; offline check |
| 009 | Payee allowlist | Withdrawn (V0 parked, DL-012), never written | — |
| 010 | Demo MCP tool | Withdrawn (V0 parked, DL-012), never written | — |
| 011 | Site hero copy | Withdrawn (V0 parked, DL-012), never written | — |
| 012 | Rename every permanent identifier to `deedglass` | Accepted (2026-10-01) | Module, binary, schema, roles, env prefix and type strings renamed; bundle re-hashed; attack digest unchanged; `scripts/rename_guard.sh` in `make ci` |
| 013 | Go package layout follows the contract | Accepted (2026-10-01) | `doc.go` per package with invariants; `intent`, `keys`, `bundle` kept; `buildinfo` and `archtest` added; `internal/canon` decided in S04 |
| 014 | Ids are prefixed, lowercase ULIDs | Accepted (2026-10-01) | `^<prefix>_[0-7][0-9a-hjkmnp-tv-z]{25}$`; monotonic, `crypto/rand`, strict parsing; additive prefixes; no dependency |
| 015 | One typed config; `deedglass.yaml` via go.yaml.in/yaml/v3 | Accepted (2026-10-01) | Pin v3.0.5 (MIT + Apache-2.0); secrets redact themselves; no YAML anchors, aliases or merge keys; 64 KiB cap |
| 016 | CLI on the standard `flag` package plus a command registry | Accepted (2026-10-01) | No CLI framework; exit codes 0/1/2/3; colour only on a TTY and without `NO_COLOR`; `--json` everywhere |
| 017 | Policies: YAML outside, CEL inside | Accepted (2026-10-01) | Pin `cel.dev/cel-go` v0.32.0 (Apache-2.0); one declared environment; cost limits; enters `go.mod` in S10 |
| 018 | MCP: official Go SDK for clients and tests, own front | Accepted (2026-10-01) | Pin go-sdk v1.8.0; serve the 2026-07-28 and 2025 protocol revisions; enters `go.mod` in S14 |
| 019 | Embedded, forward-only, checksummed SQL migrations | Accepted (2026-10-01) | `internal/store/migrations/NNNN_name.sql`; small runner in S02; no dependency |
| 020 | Console: static Astro 7 + Tailwind 4, embedded in the binary | Accepted (2026-10-01) | Vanilla TypeScript islands; brand tokens shared with the site |
| 021 | Attack-suite outcomes move to VERIFIED / MISMATCH / UNVERIFIABLE | Accepted (2026-10-01) | Not applied yet; the attack digest stays until S18 applies it |
| 022 | Control-plane isolation: RLS, two roles, five global tables | Accepted (2026-10-01) | Migrator owns, app runs under RLS with no rights on history; cross-org reads through SECURITY DEFINER functions; S02 |
| 023 | HTTP stack: stdlib router, json/v2 strict, hand-written OpenAPI 3.1.1, error catalog, dated versions | Accepted (2026-10-01) | Every route declares its auth and idempotency rule, checked against contract §9 at mount; 33 error codes; the spec is tested against the routes; S03 |
| 024 | Container and CI supply chain: distroless, digest-pinned images, SHA-pinned actions, govulncheck | Accepted (2026-10-01) | Distroless non-root image with a read-only root; every image pinned by digest and every CI action by commit SHA, checked in CI; nothing published from CI; S03 |
| 025 | Deed format v1: native JCS + Ed25519 envelope, hash-chained per org | Accepted (2026-10-01) | Unsigned envelope `id`; `prev_hash` = previous `body_hash`; deployment-wide seal keys; COSE optional |
| 026 | Pin go-cose v1.3.0 for the optional COSE deed encoding | Accepted (2026-10-01) | MPL-2.0 (go-cose) and MIT (fxamacker/cbor v2.9.4); alg −8 until a tagged release has −19 |
| 027 | TypeScript and Vitest as dev-only tools for the browser verifier | Accepted (2026-10-01) | TypeScript 7.0.2, Vitest 5.0.3 (Apache-2.0, MIT); no runtime dependencies; exact pins and a committed lock file |
| 028 | Human sign-in: an emailed link and code, no passwords | Accepted (2026-10-01) | Fragment token, preview, single use; pre-auth functions; `__Host-` cookies; three CSRF layers; no new dependency |
| 029 | Stop propagation: LISTEN/NOTIFY, an epoch guard, self-ping liveness and fail-closed bypass | Accepted (2026-10-01) | `deedglass_identity` NOTIFY in the change's transaction; epoch-guarded cache; self-ping 500 ms, bypass after 1.5 s; no new dependency |
| 030 | Signed intent format: deedglass.intent/1 (JCS + Ed25519, not JWS) | Accepted (2026-10-01, DL-023) | Closed schema as exact JCS bytes, at most 2,048; Ed25519 over `"deedglass/intent/v1\n" + intent_hash`; header of two base64url parts, no `alg`; 13 ordered checks, all 403; nonce primary key; `off`/`optional`/`required` modes; no new dependency |
| 031 | MCP authorization v1: API keys as bearer tokens, RFC 9728 metadata, no authorization server | Accepted (2026-10-01, DL-023) | Pre-issued API keys as bearer tokens (MCP authorization is optional); root and path-inserted protected-resource metadata without `authorization_servers`; challenges with `resource_metadata` and `scope`; Origin check; no token passthrough; v2 adds the org's issuer |
| 032 | Raise the binary size budget from 20 MB to 30 MB for the CEL runtime | Proposed | Stripped binary 17.7 MB → 24.8 MB with cel-go (S10); new budget < 30 MB, re-measured in S24 and S35; startup budget unchanged; no new dependency |
