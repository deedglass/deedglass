# CLAUDE.md: builder rules for the Deedglass product repo

This is the Deedglass product repo: Go module `github.com/deedglass/deedglass`, binary `deedglass`.
**Status: private, pre-release.** Deedglass is the execution and accountability layer for AI agents.

**Sources of truth** (read what a task needs; don't re-derive it):
1. `~/txv/ops/plan/prompts-v7/00_CONTRACT.md`: names, packages, ids, schema, lifecycle, deed format,
   API and CLI conventions. Binding.
2. `~/txv/ops/brand/DEEDGLASS_BRAND.md`: voice, vocabulary, colour, type and surfaces. Binding for
   every user-visible string and pixel.
3. `~/txv/ops/plan/prompts-v7/00_COMMON.md`: how every build session works (rule 19).
4. `~/txv/ops/plan/85_ALLOY_MASTER_PLAN_V7.md` §3 and §3.3: architecture intent and limits (cite it by
   this filename only).
5. `~/txv/ops/plan/20_ARCHITECTURE_SPEC_V0.md`: the L2 design reference only (the V0 verifier it
   specifies is superseded). `~/txv/ops/spikes/` holds proven logical-decoding code on PG
   16.15/17.11/18.6: port it, don't redo it.
6. ADRs: `~/txv/ops/adr/` (index mirrored in `docs/adr/README.md`).
7. `DESIGN.md` (repo root): the design tokens and components agents read for every human-facing
   surface, a copy of `~/txv/ops/brand/DESIGN.md` (DESIGN.md format). Build from its tokens; never
   hard-code a value. The design-skill loop is `~/txv/ops/plan/prompts-v7/00_DESIGN_SKILLS.md`.

## Commands

```sh
make help           # every target
make ci             # fmt-check, tidy-check, vet, test (-race), fuzz-policy-short, policy-test, testdata-check, rename-guard, banned-words, nocgo, web-verify-test
make build          # bin/deedglass with version ldflags; bin/deedglass help
make test           # go test -race -count=1 ./...     (make test-short: no -race, short mode)
make integration    # throwaway PG (DEEDGLASS_IT_PG=18|17|16, DEEDGLASS_IT_MODE=brew|docker): sql/ smoke,
                    # control-plane roles + deedglass migrate + go test -tags integration
make bench-store    # S02 store budget; raw output in bench/results/
make bench-deed     # S04 seal/verify budget and the Postgres Sealer p50/p99; raw output in bench/results/
make matrix         # integration on 16, 17 and 18
make fuzz           # 30 s each: intent and id parsers, deed envelope, verifier, key-set parsers, verify stream, bundle reader
make bench-verify   # S05 budget: verify 100,000 deeds (.jsonl and bundle) and one deed cold; raw JSON in bench/results/
make bench-ledger   # S06 budget at 100,000 actions: lists, get, chain check, export memory, seal overhead, EXPLAIN plans
make bench-identity # S07 budget: session middleware, login handler, invite with its deed
make bench-stop     # S08: Stop propagation across two instances (p50/p99) and the API-key budget; JSON in bench/results/
make bench-intents  # S09: signed-intent checks without the DB (4 KiB args) and with the nonce insert; JSON in bench/results/
go test ./internal/identity -run TestIntentVectors -update-intents   # regenerate testdata/intents/ (then update SHA256SUMS)
make fuzz-policy    # S10-S11: FuzzCompile, FuzzYAMLReader, FuzzEvaluate, FuzzAnalyze, FuzzTokenize, 60 s each (fuzz-policy-short: 10 s, in ci)
make bench-policy   # S10: compile 50 and 500 rules (< 20 ms, < 250 ms); JSON in bench/results/
make bench-eval     # S11: Evaluate p50/p90/p99 at 50 and 500 rules, sqlguard.Analyze at 200 B / 4 KiB / 256 KiB; JSON in bench/results/
go test ./internal/sqlguard -run TestCorpus -v           # the sqlguard corpus (testdata/corpus.yaml, 206 cases; differential run in make integration)
go test ./cmd/deedglass -run TestPolicyEvaluateGoldens -update  # regenerate the policy evaluate CLI goldens (µs normalised)
go test ./internal/policy -run TestLintGolden -update   # regenerate the lint goldens, hashes.json and costs.json (review the diff)
go test ./cmd/deedglass -run TestPolicyGoldens -update  # regenerate the policy lint/fmt CLI goldens
make policy-test    # S12: deedglass policy test --dir examples/policies --require-coverage --voice (in ci)
make bench-gate     # S13: Gate overhead (allow, fake.echo, concurrency 8, 1 org, 50 rules): p50 < 5 ms, p99 < 25 ms; JSON in bench/results/
go test ./internal/gate -run TestDigests -update   # re-pin testdata/gate/digests (request_digest and fingerprint; review the diff)
go test -tags integration ./cmd/deedglass -run TestGateLive -update   # the act CLI goldens in testdata/gate/cli (throwaway cluster)
go test ./examples/policies -update   # re-pin the starters' version_hash in examples/policies/hashes.json (review the diff)
go test ./cmd/deedglass -run 'TestPolicyTestGoldens|TestPolicyServerGoldens' -update  # the policy test/push/activate CLI goldens
make mcp-it         # S14: the real @modelcontextprotocol/server-filesystem (npx) behind deedglass mcp wrap, on a throwaway PG 18
make bench-mcp      # S14 §3.10: wrapper relay, tools/call and tools/list overhead, peak RSS; JSON in bench/results/
go generate ./internal/mcp/snippet   # regenerate examples/clients, examples/demo/clients and testdata/mcp/snippets (then update SHA256SUMS)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestMCPGatewayLive" make integration   # S15: the HTTP gateway, /mcp/native and mcp serve on a real server
make bench-gateway  # S15: /mcp/native fake.echo and the gateway's added latency; JSON in bench/results/
make demo-up        # S15: the Acme demo stack (examples/demo; HTTP=1 adds the everything upstream, DEMO_PORT=8091 if 8080 is taken)
make demo-status | demo-snippet CLIENT=… | demo-follow | demo-verify | demo-psql | demo-reset [HARD=1] | demo-down
make demo-smoke     # TestDemoScript: SCRIPT.md through the real wrapper and postgres-mcp, against the running stack
go test ./cmd/deedglass -run TestFollowGolden -update-follow   # testdata/cli/actions-follow.txt (then update SHA256SUMS)
go test ./internal/mcp/present -update   # the MCP result goldens in testdata/mcp/present (then update SHA256SUMS)
go test ./internal/mcp/wrap -run TestStderrGolden -update   # testdata/mcp/stderr/session.txt (then update SHA256SUMS)
DEEDGLASS_IT_PKGS=./internal/identity/ make integration   # one suite (DEEDGLASS_IT_GOFLAGS="-v -run X" adds flags)
scripts/dev-pg.sh up  # a Docker-free dev Postgres on 127.0.0.1:55432 (writes .tmp/dev-pg.env); down | reset
scripts/demo/tamper-clip.sh  # S06's recording: 1,000 real deeds, export, verify ✓, flip one digit, verify ✕
make gencopy        # regenerate testdata/deeds/messages.json and web/verify/src/*.gen.ts from Go
make web-verify-test  # (in ci) the browser verifier: npm ci once, tsc --noEmit, vitest (vectors, parity 500/500)
make web-verify-serve # build and serve the verify page on http://127.0.0.1:5178
make rename-guard   # retired identifiers (rule 5)
make banned-words   # the banned list over files and every commit message
make bench | make cover
make dev            # Docker: Postgres 18.6 + migrate + serve on 127.0.0.1:8080 (creates .env); dev-down, dev-reset, dev-logs
make run            # serve --migrate from source against the compose Postgres (127.0.0.1:55432)
make docker         # the deedglass:dev image (distroless, 65532); make bench-http: S03 HTTP budget
scripts/ci-local.sh # the CI jobs locally (docker-smoke and govulncheck skip when Docker or network is missing)
```

- Go 1.27.1 at `/opt/homebrew/bin/go`. Node 24 (`/opt/homebrew/bin/node`) for `web/verify` only; its dev
  tools are exact pins in `web/verify/package-lock.json` (ADR-027), installed with `npm ci --ignore-scripts`. PostgreSQL 16, 17 and 18 are installed at
  `/opt/homebrew/opt/postgresql@{16,17,18}/bin` (keg-only; never `brew services`). Docker images
  `postgres:16.15`, `17.11` and `18.6` work too (`DEEDGLASS_IT_MODE=docker`).
- Integration scripts refuse to run unless `DEEDGLASS_IT_THROWAWAY=1` (set by `make integration`).
- CI: `.github/workflows/ci.yml` (push to main and PRs: lint, unit, rename guard, banned words,
  no-cgo build, PG 18.6 integration, the `web-verify` Vitest job on Node 24); `pg-matrix.yml` (16.15/17.11/18.6) runs on demand and nightly.

## Rules

1. Follow `00_CONTRACT.md`. A design that turns out wrong gets an ADR (Proposed), a "CONTRACT
   CHANGE" line in the session report, and a note to the founder. Never drift silently.
2. Every attack test declares its expected outcome in `attack/attack_meta.json` **before** it is
   implemented. Never change an expected outcome (or its detail or priority) to make a test pass
   without a founder-approved ADR. `attack/meta_test.go` pins a digest of them; updating
   `expectedDigest` is itself the visible act that needs the ADR.
3. No code path turns an evidence gap into MISMATCH, or anything short of a clean pass into
   VERIFIED. The precedence function is unit-tested over every combination of results.
4. SQL: no dynamic SQL built from user input; no EXCEPTION blocks in controlled functions; every
   object schema-qualified; REVOKE and GRANT in the same transaction as CREATE; `search_path`
   pinned to `pg_catalog, pg_temp`. Migrations are forward-only files
   `internal/store/migrations/NNNN_name.sql`; an applied file is never edited.
5. Permanent identifiers use `deedglass` (module, binary, `DEEDGLASS_`, schema, `deedglass_*` roles,
   message prefix). `txv` and `testamur` are retired; `scripts/rename_guard.sh` enforces this. The
   old codename stays banned.
6. Pinned dependencies only, each with an ADR (name, version, licence, why, alternatives):
   `github.com/gowebpki/jcs` v1.0.2 (ADR-005); `go.yaml.in/yaml/v3` v3.0.5 (ADR-015);
   `github.com/jackc/pgx/v5` v5.11.0 (ADR-001, in go.mod since S02); `github.com/veraison/go-cose`
   v1.3.0 with `fxamacker/cbor/v2` v2.9.4 (ADR-026, in go.mod since S04). npm, dev only (`web/verify`,
   ADR-027, accepted): `typescript` 7.0.2, `vitest` 5.0.3, `vite` 8.3.1, `@types/node` 24.19.0; no runtime dependency.
   `cel.dev/cel-go` v0.32.0 (ADR-017, in go.mod since S10; brings protobuf, antlr4-go, cel.dev/expr,
   genproto and x/exp; the binary-size budget is ADR-032, accepted under DL-024: < 30 MB). The MCP Go SDK
   `github.com/modelcontextprotocol/go-sdk` v1.8.0 (ADR-018, in go.mod since S14; since S15 the
   binary links it for the gateway's upstream client, ADR-037 Proposed; stripped binary 27.1 MB, budget 30 MB). Planned, and added only by the session that uses it: `pglogrepl` at the
   spike-verified pin (S20). No cgo in the main binary.
   `go.sum` is committed and CI runs `go mod verify`.
7. CI matches the banned words (below) in files, strings and commit messages as whole words or
   phrases, case-insensitive, and fails on any hit not in `.banned-words-allow`
   (`path::phrase::reason`; exact quoted prior-art titles and explanatory negations only).
   "open source" stays banned until the founder makes the repo public.
8. Benchmarks write raw output to `bench/results/<date>-<host>.json`; docs cite only those files.
9. Every factual sentence in public docs or site copy needs a row in `~/txv/ops/CLAIMS_REGISTER.md`
   with a primary source and date.
10. Never touch production or customer credentials, databases or environments. Throwaway databases
    only. The founder creates, resizes and deletes anything that can bill, keeps every cloud
    credential, and puts secrets in `.env` himself; never ask for them in chat.
11. Nothing becomes public (repo visibility, release, deploy, post) without the founder's explicit
    approval in chat. Never push, tag, publish or create remote repos unasked.
12. Demo footage comes only from real engine runs; `.tape` files and the commit hash go with every video.
13. Usage hygiene: one primary session plus a few subagents that never edit the same file;
    compact at phase boundaries; long test matrices run in the shell.
14. Commits only when the founder says so in chat. The author is the configured git user; every
    message ends with a blank line and `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
    Commit messages are scanned for banned words.
15. The L2 invariants below hold in every session that touches decoding or the Witness.
16. When rate-limited, stop at a clean point with the repo green and write the report with an exact
    "resume from step N" line. Never drop ADRs, tests on the verdict path, or the report.
17. Every user-visible string follows the brand doc: calm, exact, sentence case, a next step on
    every error, no exclamation marks, no emoji in the product. Errors come from catalogs
    (`internal/httpx/errors.go`, the CLI's messages, `web/src/lib/copy.ts`), not scattered inline.
18. Ids and errors follow contract §3 and §8: prefixed lowercase ULIDs from `internal/ids` (never
    hand-built), and the error envelope with a `doc_url` for every code.
19. The session protocol is `~/txv/ops/plan/prompts-v7/00_COMMON.md`; the calendar and founder
    reminders are in `00_SCHEDULE.md` there.

## Vocabulary

- Product words: **Deed** (the signed record of one action), **Seal** / sealed (signing a deed;
  the key is the seal key), **Witness** (the verification engine), **Gate** (the execution proxy
  and MCP gateway), **Ledger** (the history of actions and events), **Console** (the web app),
  **Stop** (the kill switch), **Insights** (suggestions a person approves), **Org** (never
  "tenant" in anything a user sees).
- Verdicts `VERIFIED` · `MISMATCH` · `UNVERIFIABLE`; levels `L0` Reported · `L1` Confirmed ·
  `L2` Witnessed; policy decisions `allow` · `deny` · `require_approval`.
- The attack suite keeps its V0 outcome words (VALID, INVALID, INCONCLUSIVE, BLOCKED, DETECTED,
  NO EFFECT, OUTSIDE SCOPE) until S18 applies ADR-021 (approved 2026-10-01).
- Prefer: see, show, seal, witness, verify, record, check, compare, detect, report.

**Banned** in every file, string and commit message (canonical list: `scripts/banned_words.txt`):
"trustless", "irrefutable", "unbreakable", "tamper-proof", "mathematically proves", "cryptographic
proof of execution", "proof of execution", "proves the transaction happened", "proves" or
"prove/proof" as a guarantee, "guaranteed", "SCITT-compliant", "RFC 9942 receipt", "receipt" (the
record is a Deed), "EU AI Act compliant", "Article 12 infrastructure/solution", "compliance" as
positioning, "prevents all tampering", "no hidden side effects", "liability transfer", "verify any
agent action", "intent verification", "synchronous" (say "inline"), "patented", "certified",
"certification", "certificate", "attests"/"attestation" as a product claim, "we testify", "alloy",
and "open source" until the repo is public. Style bans: "revolutionary", "world-class",
"cutting-edge", "seamless", "next-gen", "military-grade", "bank-grade", "unhackable".

## L2 invariants (from the spikes)

1. `pg_logical_emit_message` returns the END LSN of the message record; it equals `commit_lsn` only
   by coincidence. The marker LSN is evidence only and is never compared with `commit_lsn`.
2. `pg_control_system()` is PUBLIC-executable on PG 17/18; no `pg_monitor` grant is needed.
3. No custom pgoutput parser: `pglogrepl.Parse` runs inside `safeParse` (recover, per-type minimum
   lengths, declared-length checks). Any failure halts that connector's verdicts rather than
   guessing. A fuzz test is seeded from the spike truncation corpus.
4. COSE encodings use alg -8 (EdDSA, crv Ed25519), ADR-008: go-cose 1.3.0 has no built-in -19.
5. Decode sessions pin `TimeZone='UTC'`, `DateStyle='ISO, YMD'`, `IntervalStyle='postgres'`,
   `bytea_output='hex'`, `extra_float_digits=3`, set with `ALTER ROLE deedglass_witness SET`
   (`sql/001_roles.sql`), asserted on every poll.
6. The strict pre-parser runs before JCS: gowebpki/jcs rounds 9007199254740993 and accepts 1.5, so
   integers only, magnitude at most 2^53-1, then `bytes == JCS(bytes)` (`internal/intent`).
7. Small-order Ed25519 public keys are refused at registration (`internal/keys`; attack case A13).
8. A controlled function's entry guard shows "first write", not "only write": extra markers are
   caught by counting markers per transaction. Callers retry in a NEW transaction.
9. Classification uses frame position and the message's transactional flag, never the peek `xid`
   or an assumed monotonic peek `lsn`; the checkpoint is `max(COMMIT.end_lsn)`.

## Guards in place (keep them green; extend, never weaken)

- `internal/ids`: strict parser, spec vector, monotonic and concurrency tests, clock-back and
  overflow tests, pattern-vs-parser agreement, `FuzzParse`.
- `internal/config`: the key table, every prod refusal, `.deedglass/` safety, secret canaries across
  fmt, JSON, slog and panics, and the `docs/CONFIG.md` drift test.
- `internal/store`: catalog shape (32 tables, RLS on every org table, `system_scope` list pinned,
  composite FKs, grants), RLS matrix on all 27 org tables, pooled-connection leak, privilege
  matrix, deed chain, domains = `ids.Registry()`, `set_config(…, true)` grep, `SystemTx` call
  sites; `internal/gate`: 225-pair trigger test; migrate runner refusals. Integration tag,
  PG 16/17/18.
- `internal/archtest`: import layering (pure `deed`, `policy`, `sqlguard`, `witness`; `httpx` and
  `store` never import each other).
- `internal/httpx`: `Mount` panics on a route with no auth, a POST with no idempotency rule, or a
  route missing from `contract_routes.go` (contract §9 as data; `testdata/routes.golden`); the
  41-code error catalog (copy rules, banned words, `docs/api/errors.md` headings; S11 added
  `policy_unavailable`, S12 `policy_invalid` with the additive `diagnostics` envelope field); the middleware and
  idempotency tables; log canaries; `CheckSpec`/`CheckRequired` keep `api/openapi.yaml` equal to the
  mounted routes (also run on everything `serve` mounts in `cmd/deedglass`). A new route goes into
  `serveRoutes`, `contract_routes.go` (if new) and `api/openapi.yaml` in the same change.
- `cmd/deedglass`: goldens for help, version, config print and errors, with exit codes.
- `internal/intent`, `internal/keys`, `internal/bundle`: strict JSON and JCS vectors, key checks,
  manifest hashes bound to the SQL and schema files.
- `internal/deed` (S04): the §6.1 verify table (every code), first-failure order, chain checks,
  kind × presence, a 2,000-iteration round-trip property, the closed-schema privacy test, A13 over
  12 encodings, key-file modes, the Sealer under 50-way concurrency (in memory and on Postgres),
  rollback, forced conflict, key rotation; `FuzzParseEnvelope`, `FuzzVerifyEnvelope`,
  `FuzzParseKeySet`. `testdata/deeds/` is generated (`go test ./internal/deed -run TestGoldenVectors
  -update`) and pinned in `SHA256SUMS`; never edit a vector by hand. `deed` must not import
  `internal/ids` (it pulls in `database/sql/driver`); `IDPattern` is tested equal to `ids.Pattern`.
- `attack/`: A13, F01 and F02 at the library level (S04); F01–F04 at CLI level on the real binary (S05).
- `internal/deed` (S05): `VerifyStream` (valid, first failure, `--all` in seq order, bounded at 100,
  cancellation with no goroutine leak, blank and 64 KiB lines, anchors, per-org directories),
  `FuzzVerifyStream`; the message templates (every placeholder known, every code covered);
  `testdata/deeds/messages.json` and `parity/corpus-500.*` are generated with the vectors.
  `docs/VERIFYING_DEEDS.md`'s code table and `docs/BUNDLE_FORMAT.md`'s worked example are generated
  (`-run 'TestVerifyingDocs|TestBundleFormatDoc' -update`).
- `internal/deed/archive` (S05): round trip, byte-identical writes (golden SHA-256), every reader rule,
  gzip bomb, seals, `FuzzArchiveOpen`; the reader never writes to disk.
- `internal/deed/cmd/gencopy`: the generated TypeScript and JSON are never stale.
- `internal/ledger` (S06): the one write path for `action_events`, `admin_events` and `deeds`
  (nothing else writes them; `ledger.Publish` is the only `pg_notify('deedglass_events', …)`);
  filter and cursor units, closed `EventData`, event payload shape ≤ 1 KiB, the admin catalogue;
  integration: every seal path, illegal and concurrent transitions, NOTIFY only after COMMIT, the
  filter matrix against an in-memory reference, pagination under concurrent inserts, RLS (foreign
  ids are not found), a superuser rewrite (`body_hash_mismatch`) and delete (`chain_seq_gap`),
  incomplete checks, export → verify byte-identical twice, export abort, prune → verify, and the six
  HTTP routes against `api/openapi.yaml`. `TestLedgerBudget` (`make bench-ledger`) is the 100k
  budget. CLI goldens: `cmd/deedglass/testdata/ledger/` (`go test -tags integration ./cmd/deedglass
  -run TestLedgerCommandsLive -args -update`, against a throwaway cluster).
- `internal/identity` (S07): every session route × every role (`TestRoleMatrix`); login enumeration
  (identical answers, the handler runs with no database); tokens (10,000 distinct, malformed table,
  `FuzzHashToken`), codes (chi-square over 1e6, 5 attempts), `FuzzEmailNormalize`; integration: the
  demo flow, single use (50 racing runs), CSRF (5 refusals), sessions (idle, absolute, logout
  everywhere across orgs, removal), the last owner (100 concurrent runs), RLS isolation, pre-auth
  grants, prune, prod cookie attributes, no secrets in logs. `internal/store`: `TestPreAuthNamesStayInStore`
  (the `preauth_*`/`maint_*` names and `set_config('deedglass.org_id'` only in `internal/store`).
  `internal/httpx`: a Session or SessionOrKey route without `MinRole` can't mount; every 401 carries
  `WWW-Authenticate`. `internal/mailer`: goldens (`-run TestGoldens -update`), Resend retry table,
  log capture. The S06 dev principal is gone.
- `internal/identity` (S08): key parse table + `FuzzParseAPIKey`, 10,000 unique keys and a chi-square
  on the characters, no `math/rand` and no plain hash comparison (grep tests), the cache's epoch and
  bypass rules (also under 200 concurrent invalidations), the listener's handler, the failed-auth
  limiter's doubling; integration: every edge of the agent state machine, the §3.5 precedence table
  and challenges, agent keys never manage (insufficient_scope), admin keys never act as owner, the
  10-key limit, rolling (old key until the overlap ends, then api_key_expired), the Stop all overlay,
  a failed NOTIFY changes nothing, a replayed key create hides the secret, batched last_used, the
  limiter, RLS, all 11 S08 admin actions sealed with the chain intact, no key in logs; two instances
  (`TestStopPropagation`: stop, revoke, stop-all, a killed listener and a silent stall, each under
  2 s), `TestEpochGuard` (the forced interleaving), `TestWatch`. `internal/httpx`: `TestKeyAllowed`
  (steps 6–9, admin keys act as admin). `cmd/deedglass`: `TestKeyRoutesDeclareScopes` (every key
  route declares Scopes; only whoami skips the stop checks), the agent CLI goldens in
  `testdata/agent/` (`go test ./cmd/deedglass -run TestAgentCommandGoldens -update`).
  `internal/ledger`: `TestOneWritePath` allows `store.NotifyIdentity` as the one door for
  `deedglass_identity`.
- `internal/intent` + `internal/identity` + `pkg/deedglass` (S09): one wire-format implementation
  (`internal/intent/agent.go`) shared by the checker and the signer; `TestIntentVectors` runs all 34
  vectors in `testdata/intents/` (generated, pinned in `SHA256SUMS`, never edited by hand) through
  both, and `TestCrossCheckVector` pins the brief's v01 literally; the args float table, refusals and
  digest invariance; `FuzzArgsDigest`, `FuzzIntentHeader`; a 1,000-case sign/check property; the
  intent copy (every refusal 403, never 401). Integration: the demo round trip, every mode cell, key
  rules (small-order and non-canonical 422, third key and duplicate kid 409, required without keys
  409, last key while required 409), 100 concurrent uses → exactly 1, the prune invariant, nonce RLS,
  a new key reaching a warm cache, OBO grammar and binding, and the MCP challenges, Origin, stub and
  405s against the real key pipeline. `httpx.RouteGuard` (`identity.MCPAuth`) and
  `httpx.KeyRequestHook` (OBO) are the two pipeline hooks; `StripInboundCredentials` is required
  before anything is forwarded upstream (S15).
- `internal/policy` (S10): `TestLintGolden` over 95 fixtures in `testdata/lint/` (text and JSON
  goldens; `valid/` pins every `version_hash` in `hashes.json` and every rule's estimated cost in
  `costs.json`, so a normalizer or cost-model change shows as a diff); `TestHashStability` (same
  hash for re-indenting, key order, flow/block, comments, quoting, CRLF, BOM; different for rule
  order, reason, numbers, verify); `TestDeterminism` (100 goroutines); `TestFmtProperties`
  (idempotent and hash-preserving over every valid fixture and 500 generated policies);
  `TestDeclaredFields` (the CEL-visible facts S11 fills); `TestSourceMap` (CEL positions in every
  YAML style); `TestCompileCases`; `TestErrorDocs` and `TestErrorDocExamples` (every code has a
  `docs/POLICY_ERRORS.md` section and every printed example matches the compiler);
  `TestBannedWordsCopy` (`internal/policy/bannedwords.txt` equals `scripts/banned_words.txt`);
  `TestAgentIDPattern` (policy doesn't import `internal/ids`, which pulls in `database/sql/driver`);
  `FuzzCompile` (no panic, bounded time, deterministic, fmt idempotent) and `FuzzYAMLReader`.
  `cmd/deedglass`: `TestPolicyGoldens` (`testdata/policy/`, the §5 demo layout), `TestPolicyFmtWrite`.
- `internal/sqlguard` + `internal/policy` evaluation (S11): `TestCorpus` (206 cases in
  `testdata/corpus.yaml`, every expected fact; one readable line per differing field) and
  `TestCorpusDifferential` (integration: every `differential: true` case inside BEGIN … ROLLBACK
  on PG 16/17/18, command tags vs `kinds`; refuses server-code, dangerous-function, PROGRAM and role
  cases); the 42-case tokenizer table and the lossless check; `TestInvariance` (comments, case,
  whitespace), `TestTruncationFailsClosed`, `TestNeverReadWhenDangerous`; `FuzzTokenize`,
  `FuzzAnalyze`. `TestEvaluateMatrix`, `TestCostLimitFailsClosed` (DL-024: over the runtime cost
  limit is a deny for every rule decision and for measures), `TestHugeArgsNeverAllow`,
  `TestEvaluateUnavailable` (nil policy, panics, deadline), `TestEvaluatePair`,
  `TestEvaluateDeterminism` (1,000 goroutines), `TestPostgresSafeDemo` (the exact trace),
  `TestAlwaysDenies` (12 cases), `TestPrepare`, `TestTimeParts`, `FuzzEvaluate`. `usagetest.Run` is
  the one contract suite for `MemUsage` and `policystore.Usage` (integration: two-instance 400 → 60,
  RLS, `TestUsageCountersShape` pins migration 0003's table). `cmd/deedglass`:
  `TestPolicyEvaluateGoldens` (`testdata/policy/evaluate_*`), `TestStopMessagesMatchCatalog` (the
  evaluator's Stop and policy_unavailable strings equal `internal/httpx/errors.go`),
  `TestTimeZoneDatabase` (`time/tzdata` is embedded).
- `internal/policy` + `internal/policyapi` + `internal/store/policystore` (S12): the service over
  an in-memory repo with real sealed deeds (`memrepo_test.go`): every activation edge (shadow,
  promote with two deeds, replace, roll back, deactivate shadow, the 409s), `default_allow` needs
  `acknowledge_warnings` and the deed records it, identical pushes save nothing, 10 concurrent
  activations; the registry: single flight (50 cold loads → 1), every load check fails closed
  (compile, `version_hash`, `source_hash`, deed seal, deed target, deed data, wrong keys, database
  down) and never falls back, a broken shadow never blocks, hot swap on NOTIFY, the safety poll,
  reconnect invalidation, idle eviction, a cache hit allocates nothing (`TestRegistryHitAllocs`);
  the case-input parser and test runner. `TestRouteRoleMatrix` (12 routes × owner/admin/viewer/admin
  key/agent key/none; agent keys refused on reads too). `examples/policies`: `TestStarters` (every
  starter compiles clean under `--voice`, passes its cases, covers every rule and budget, ≥ 84
  cases, `hashes.json` pinned), `TestStartersAlwaysDeny`, `TestEmbedMatchesDisk`. `docs`:
  `TestCookbook` (17 recipes lint clean and pass their cases). Integration: the table guards
  (append-only versions, one live enforce, end-only activations, RLS), onboarding through
  `identity.CreateOrg`, the service on Postgres with the chain intact, 10 parallel activations,
  `TestHotSwapTwoInstances` (20 of 20 under 2 s), `TestListenerKilled`, `TestTamperFailsClosed`.
  `internal/ledger`: `TestOneWritePath` allows `store.NotifyPolicy` as the one door for
  `deedglass_policy`.
- `internal/gate` (S13): the edge table is `internal/gate/lifecycle` (gate aliases it; 225 pairs, 10,000
  seeded walks, `Settled`, the test-time panic on an illegal edge, `docs/GATE.md`'s diagram equal to
  the table); `ledger.Transition`'s `Patch` writes only its edge's columns and `ledger.Advance` writes a
  run of edges as one UPDATE whose `hops` the database guard checks hop by hop (`TestAdvance`,
  `TestPatchSetsAgree`); the normalization table, `FuzzNormalize`, pinned digests
  (`testdata/gate/digests`); the classification table (failed only when certainly not applied, every
  doubt unknown, a panic unknown); the copy (no dashes, no exclamation marks, banned words); the route
  × role matrix (only `POST /v1/actions` records stopped agents: `RecordStopped`, pinned in
  `cmd/deedglass`); integration on PG 16/17/18: every path of S13 §6 (allow, deny with no connector
  call, Stop, suspend, org stop, budget with `Retry-After`, approval approved / stopped / cancelled /
  expired with the reservation released, tool error, timeout and panic as UNVERIFIABLE, proxy round
  trip, edge lost, late and double reports, the Postgres edge's SQL facts, idempotency incl. 5
  in-flight duplicates → one call, intents in every mode incl. MCP + required, shadow recorded, the
  five events in order, no pooled connection held while a tool runs, crash between Tx A and Tx B and
  between the outcome and the seal), the worker with a deferring witness (retry, then
  `witness_gave_up`), two instances sweeping 200 stale actions (each sealed once, chain intact),
  `TestLoad1000`, and `cmd/deedglass`'s `TestGateLive` (S13 §9 on a real server; CLI goldens in
  `testdata/gate/cli`). `TestPreAuthNamesStayInStore` also keeps `claim_*` and
  `maint_release_action_keys` inside `internal/store`; `TestOneWritePath` allows `store.NotifyJobs`
  as the door for `deedglass_jobs`.
- `internal/mcp` (S14): `jsonrpc` framing table (4 MiB + 1, truncation, batch, depth 129, non-UTF-8,
  duplicate members, resync) and `FuzzReader`; `gateclient` maps every row of S13's status table and
  treats every unreadable answer as Unreachable (retries reuse one Idempotency-Key); `wrap` runs the
  fake server (`internal/mcp/mcptest`, the test binary re-executed with `FAKE_MCP_MODE`) in both eras
  through every S14 §6 row, asserting that every stdout line parses as JSON-RPC, that a governed call
  never reaches the server without `authorized`, and that the child never sees `DEEDGLASS_*`;
  `TestSDKInterop` (go-sdk client ⇄ wrap ⇄ go-sdk server, 4 era combinations); goldens in
  `testdata/mcp/{present,snippets,stderr}`. `internal/gate`: `POST /v1/tools/resolve` in the route ×
  role matrix and `TestResolveTools`; `internal/policy`: `TestAlwaysDeniesWhy` (agrees with
  `AlwaysDenies`); `examples/policies`: `TestDemoVisibility`. `cmd/deedglass`: `TestMCPSnippet`,
  `TestMCPWrapRefusals`, and (integration) `TestMCPWrapLive`, `TestMCPWrapNodeLive` (`make mcp-it`),
  `TestMCPBudget` (`make bench-mcp`).
- `internal/mcp` (S15): `httpgw`'s `TestConformance` (S15 §6's HTTP table: headers, eras, 405s,
  Origin, 401 with `resource_metadata`, 413, 415, 406, batches, unknown connector, stopped agents),
  `TestInFlightLimit`, `TestHeaderCodec`, `FuzzHeaderDecode`; `front`'s method, pagination, outcome,
  check_approval and passthrough tables, `TestParamHeaders`, `FuzzFrontEnvelope`; `upstream`'s pool
  in both eras (SDK upstreams, `mcptest.HTTPUpstream`), the dial guard, credential isolation;
  `bridge`'s down server, headers, SSE and cancellation. `cmd/deedglass`: `TestMCPGatewayLive`
  (integration: SDK clients in both eras × stateless and stateful SDK upstreams, deny without a call,
  approval resume runs once, Stop, timeout, client disconnect, upstream down, no inbound credential
  upstream and no upstream credential in logs or actions, `/mcp/native`, `GET /v1/tools`, the
  `mcp serve` bridge), `TestGatewayBudget` (`make bench-gateway`), `TestFollowGolden`;
  `TestKeyRoutesDeclareScopes` pins `RecordStopped` to `POST /v1/actions` and the two MCP routes.
  `examples/demo`: `TestDemoScript` (`make demo-smoke`), the 42 `acme-demo` cases in `make policy-test`.
- `cmd/deedglass verify` (S05): 80 golden cases in `testdata/verify/<case>/{args,stdout,stderr,exit}`
  (`go test ./cmd/deedglass -run TestVerifyGolden -update`), every vector's code and exit, `--keys`
  URL rules (same-host redirects, 1 MiB, timeout, loopback-only http), key-set merge conflicts, no
  network without a `--keys` URL, terminal sanitizing.
- `web/verify` (S05): vectors, parity 500/500, strict, JCS, keys (A13, RFC 8032, Wycheproof), page
  safety (CSP, no HTML from strings, no storage, one fetch).
- `attack/`: all 66 case ids present, outcome enum, demo set, digest pin.
- `scripts/integration_smoke.sql`: live prosrc hash, roles, grants, RLS, replica identity, one real
  transfer, refusals with exact SQLSTATEs, decode checks through pgoutput and test_decoding.
- `scripts/rename_guard.sh` and `scripts/banned_words.sh`; `testdata/SHA256SUMS`. Test strings with
  backslash-u escapes are built at run time (`u("0061")` in intent tests).

## Schedule

The session calendar, founder reminders and budget live in
`~/txv/ops/plan/prompts-v7/00_SCHEDULE.md`. A missed date moves the schedule; expected outcomes are
never weakened to meet one.
