# Repo map

Every directory and package in `~/txv/product`, what state it is in, and which session owns it. Contract §2 is the source for the package list; this page adds status.

**Status words:** **built** (code and tests) · **kept from V0** (tested V0 code that stays) · **stub** (a `doc.go` with the responsibility, invariants, a planned-API sketch and the owner session; no code yet) · **planned** (does not exist yet; the named session creates it).

Last checked: S08, 2026-10-01 (S01 wrote it). Each session that changes the layout updates this page.

## Go packages (`internal/…` unless noted)

| Package | Status | Owner | Responsibility |
|---|---|---|---|
| `config` | built | S01 | Env and file config, validation, redacted printing |
| `ids` | built | S01 | Prefixed ULIDs and typed ids (`docs/IDS.md`) |
| `buildinfo` | built | S01 | Version, commit, supported API versions |
| `cli/mark` | built | S05X | The terminal mark: the Split Orb banner, the inline `◖│◗` mark and the Sealed moment; `go generate` builds its pixel grid |
| `archtest` | built, test-only | S01 | The import-layering test |
| `intent` | kept from V0, tested | S01 (kept) | Strict JSON parser, JCS, the V0 `TRANSFER` intent |
| `keys` | kept from V0, tested | S01 (kept) | Ed25519 key checks (small-order refusal, `KID`) |
| `bundle` | kept from V0, tested | S01 (kept); absorbed in S17 | The V0 Operation Bundle manifest test |
| `store` | built (S02–S08) | S02+ | pgx pool, migrations, org-scoped transactions (RLS), repositories; `DeedTx` (the Postgres `deed.ChainStore`) and seal-key rows (S04); S08: `notify.go` (`NotifyIdentity`, the one door to `deedglass_identity`, and the listener's dedicated connection settings), `preauth_api_key` in `PreAuth`, and `migrations/0013_identity_agents.sql` (`docs/DATA_MODEL.md`) |
| `store/policystore` | planned | S11–S12 | Postgres adapters for `policy` |
| `httpx` | built (S03–S04) | S03 | Server, middleware, route table, error envelope, versioning; `keys.json` (S04) |
| `deed` | built (S04–S05) | S04–S05 | Deed body and closed schema, builders, JCS, seal, Sealer, chain, verify, key sets and key files, COSE (`docs/DEED_SPEC.md`); S05: the streaming `Verifier` (`stream.go`), the `verification_report` (`report.go`), the message catalog and manifest seals |
| `deed/deedtest` | built, test helper (S04) | S04 | Fixed test-vector keys, deterministic ids, in-memory `ChainStore`, chain generator, tamper helpers |
| `deed/archive` | built (S05) | S05 | Export bundle v1: deterministic writer, streaming reader with strict limits (`docs/BUNDLE_FORMAT.md`) |
| `deed/cmd/gencopy` | built, generator (S05) | S05 | Writes `testdata/deeds/messages.json` and `web/verify/src/*.gen.ts` from Go |
| `deed/deedtest/cmd/genchain` | built, bench helper (S05) | S05 | Writes a test chain (and bundle) of n deeds for benchmarks |
| `ledger` | built (S06) | S06 | The one write path for actions, action events, admin events and deeds (`Transition`, `SealAction`, `SealAmendment`, `RecordAdmin`, `Publish`); queries with cursors; the chain check; exports; retention; the six Ledger routes (`docs/LEDGER.md`) |
| `ledger/fake` | built, dev tool (S06) | S06 | `deedglass dev fake-action`: deterministic plans from a seed, run through the real write path |
| `identity` | people built (S07); agents, keys and Stop built (S08); intents S09 | S07–S09 | Sign-in (link and code), sessions and cookies, CSRF, roles, orgs, members, invites, the last-owner rule, admin deeds; the `/login`, `/auth/confirm` and `/app/` pages (`docs/IDENTITY.md`). S08: `agents.go` (agents, the state machine, Stop all), `agents_api.go` (the routes and `whoami`), `apikey.go` (the key format, parser, generator, hash), `keys.go` (create, list, roll, revoke, admin keys), `keyauth.go` (bearer authentication, the failed-auth limiter, batched `last_used_at`), `cache.go` (the principal cache and epoch guard), `listener.go` (`LISTEN deedglass_identity`, self-ping, bypass), `guard.go` (`CheckActive`, `Watch`), `notify.go` (payloads) (`docs/AGENTS.md`, `docs/STOP.md`) |
| `mailer` | built (S07) | S07 | `Mailer` (log in dev, Resend, none), the bounded send queue, login and invite templates with goldens (`docs/EMAIL.md`) |
| `policy` | stub | S10–S12 | YAML → CEL compiler, evaluator, budgets, activation, replay input |
| `sqlguard` | stub | S11 | Pure-Go SQL tokenizer and classifier |
| `gate` | `state.go` built (S02, S06 `Sealable`); the rest stub | S13 | Action pipeline, state machine, idempotency, worker |
| `mcp` | stub | S14–S15 | MCP stdio wrap, Streamable HTTP gateway, `mcp serve` |
| `vault` | stub | S16 | Envelope encryption of connector credentials, rotation |
| `connector` | stub | S16–S17, S29–S30 | Connector interface, registry and implementations |
| `connector/postgres` | stub | S17 | The Postgres connector. Its `doc.go` holds the V0 executor and installer notes |
| `connector/postgres/setup` | planned | S17 | Setup planner, `check`, SCRAM |
| `connector/egress` | planned | S16 | The only outbound HTTP and dial path (SSRF-safe) |
| `connector/schema` | planned | S16 | JSON Schema subset validator |
| `connector/rest` | planned | S16 | Generic REST connector |
| `connector/fake` | planned | S13 | Dev-only connector for tests and demos |
| `connector/github`, `connector/slack` | planned | S29 | |
| `connector/stripe`, `connector/gmail`, `connector/notion` | planned | S30 | |
| `witness` | stub | S19, S21, S23 | Verdicts, L0/L1, L2 expected-versus-observed, diffs, undo planner. Its `doc.go` holds the V0 verifier and boundary notes |
| `decoder` | stub | S20 | pgoutput decoding, slots, correlation |
| `approve` | stub | S22 | Approvals, tokens, expiry, four-eyes, resume |
| `notify` | stub | S22, S29 | Email, Slack and webhook notices |
| `learn` | stub | S27–S28 | Replay, stats, anomalies, suggestions (the Insights engine) |
| `console` | stub | S24–S26 | The embedded Console (`web/` build output) and SSE stream |
| `lint` | planned | S16, S34 | Static guards G1–G5 (S16) and G6–G10 (S34) |
| `cmd/deedglass` | built in part | all | The CLI. S01 builds `help`, `help <command>`, `version` and `config print`; every other command says it isn't built yet (`docs/CLI.md`). S08: `agentcmd.go` (`agent`, `agent keys`, `org stop-all`, and the operator commands `org start-all` and `org keys`), goldens in `testdata/agent/` |
| `pkg/deedglass` | built in part (S08) | S08, S09, S31 | A small public Go client. S08: `client.go` (`Client{BaseURL, APIKey, HTTP}`, `Do`, a typed `*Error`, `Deedglass-Version`, an automatic `Idempotency-Key` on POSTs), used by the CLI; S09 adds intents, S31 verify helpers |

**V0 packages removed in S01** (doc-only, no code): `statement` moved into `deed/doc.go`; `verify` and `boundary` into `witness/doc.go`; `executor` and `pgsql` into `connector/postgres/doc.go`. `decoder` and `store` stayed and were rewritten.

## Top-level directories

| Path | Exists today | Status | Owner | Contents |
|---|---|---|---|---|
| `attack/` | yes | kept from V0 | S18 | V0 attack-suite metadata (`attack_meta.json`) and the digest-pinning test |
| `bench/` | yes | stub | S35 | `results/` for raw measurements (`docs/BENCHMARKS.md`). S08: `2026-10-01-darwin-arm64-stop.json` and `-keyauth.json`, written by `make bench-stop` (`docs/STOP.md`) |
| `bundles/transfer_v1/` | yes | kept from V0 | S17 | The V0 Operation Bundle; the Postgres connector absorbs it |
| `cmd/deedglass/` | yes | built in part | all | The CLI, its `internal/ui` style helpers and golden files |
| `deploy/` | yes | kept from V0 | S03, S35 | Docker Compose for local Postgres; S03 adds the dev stack, S35 the release images |
| `docs/` | yes | stubs | per page | Living docs; `docs/adr/` mirrors the ADR index; `docs/v0/` holds V0 references |
| `internal/` | yes | see above | | Go packages |
| `scripts/` | yes | built | S01 | `banned_words.sh`, `rename_guard.sh`, integration scripts; `dev-pg.sh` (a Docker-free dev Postgres, S06); `demo/flip-byte.sh` and `demo/tamper-clip.sh` (S06's recording) |
| `sql/` | yes | kept from V0 | S02, S17 | V0 customer-side SQL `001`–`004`. S02 moves control-plane migrations to `internal/store/migrations` |
| `testdata/` | yes | kept from V0; `deeds/` built (S04) | S04 | Offline vectors: JCS (RFC 8785), Ed25519 (RFC 8032, Wycheproof), Deed v1 (`deeds/`, generated), with `SHA256SUMS` |
| `.github/workflows/` | yes | kept from V0 | S03 | `ci.yml` and `pg-matrix.yml` (Postgres 16, 17, 18) |
| `api/openapi.yaml` | no | planned | S03 | The OpenAPI document, served at `GET /api/openapi.yaml`; each API session extends it |
| `web/` | partly | S05 built `web/verify` | S05, S24 | `web/verify`: the browser verifier (TypeScript, zero runtime dependencies, ADR-027). S24 builds the Console |
| `examples/` | no | planned | S12, S32, S37 | S12 adds `examples/policies/`; S32 and S37 add the quickstart examples |
| `sdk/typescript`, `sdk/python` | no | planned | S31 | The `@deedglass/sdk` and `deedglass` client libraries |
| `docs-site/` | no | planned | S33 | The docs site for `docs.deedglass.com` |

Root files: `CLAUDE.md` (builder rules), `README.md`, `Makefile` (`make help` lists targets), `LICENSE` (Apache-2.0), `go.mod`, `deedglass.example.yaml` (every config key, commented), `.rename-allow`, `.banned-words-allow`, `.editorconfig`.

Git-ignored: `bin/` (build output), `.deedglass/` (local keys and dev secrets), `.tmp/` (throwaway clusters), `.env`.

## What this page does not do

It does not describe how the packages talk to each other (`docs/ARCHITECTURE.md` does), and it does not promise dates. Owner sessions come from contract §2 and can move by ADR.
