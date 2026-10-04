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
make mock-orders    # S16: the mock orders API on 127.0.0.1:8090 (MOCK_ORDERS_TOKEN; MOCK_ORDERS_MODE=lying for S19)
make bench-connectors # S16: vault open, redaction, egress classification, REST build and loopback call; JSON in bench/results/
go test ./internal/connector/egress -run TestCorpus -v   # the egress corpus (testdata/corpus.json, 59 destinations × prod and dev)
go test ./internal/vault -run TestGoldenEnvelope -update   # testdata/vault/envelope_v1.json (then update SHA256SUMS)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestConnectorsLive" make integration   # S16 §9 on a real server, with the sentinel sweep
go test ./internal/connector/postgres/setup -run TestPlannerGoldens -update   # S17: the setup planner goldens in setup/testdata/plans (review the diff)
go test ./internal/connector/postgres -run TestValuesGolden -update   # S17: testdata/values.golden (values to JSON)
DEEDGLASS_IT_PKGS=./internal/connector/postgres/ make integration   # S17: the Postgres connector on a throwaway cluster (DEEDGLASS_IT_GOFLAGS="-v -run TestOneTransaction")
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestPostgresLive" make integration   # S17 §9 on a real server
make bench-postgres # S17: the connector's extra latency over raw pgx and 100 writes/s for 30 s; JSON in bench/results/
make bench-witness  # S19: Decide, L0 with a 64 KiB schema check, expect over 32 fields, a CEL postcondition; JSON in bench/results/
go generate ./internal/witness   # S19: regenerate docs/witness/REASONS.md from the reason catalog
DEEDGLASS_IT_PKGS="./cmd/deedglass/ -update" DEEDGLASS_IT_GOFLAGS="-run ^TestWitnessLive$" make integration   # S19 §9 on a real server; refresh testdata/gate/cli/witness-* (then SHA256SUMS)
make bench-decoder  # S20: SafeParse vs Parse, then 100 writes/s for 60 s (commit → frame persisted, poll round); JSON in bench/results/
DEEDGLASS_IT_PKGS=./internal/decoder/ make integration   # S20: the decoder on a throwaway cluster (crash, prepared and E08 tests start their own Homebrew cluster; skipped without one)
DEEDGLASS_IT_PKGS="./internal/decoder/ -update-corpus" DEEDGLASS_IT_GOFLAGS="-run TestCaptureCorpus" make integration   # S20: re-capture testdata/decoder/pgoutput (per DEEDGLASS_IT_PG; then SHA256SUMS)
go test ./internal/decoder -run TestSafeParsePrefixes -v   # S20: every strict prefix of the corpus refused (0 panics, 0 accepted)
make bench-l2       # S21: Derive, Compare, the render, then 100 writes/s for 60 s through a real serve (commit → verdict p50 < 1 s); JSON in bench/results/
go test ./internal/witness/l2 -run TestRenderGoldens -update   # S21: the L2 diff goldens in internal/witness/l2/testdata/golden (review the diff)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestWitnessL2Live" make integration   # S21 on a real server: VERIFIED, the hero MISMATCH, MCP, an amendment
make demo-hero      # S21: the L2 hero in one command (make demo-up first; HERO_PACE=2 for recording); demo-hero-check runs it twice
make demo-trigger-on | demo-trigger-off | demo-order-reset [ORDER=42] | demo-diff [ID=act_…]   # S21: the hero's pieces
DEEDGLASS_IT_PKGS="./internal/approve/ ./internal/notify/..." make integration   # S22: approvals (decision table, 200-run decide race, links, ?wait) and webhooks (outbox, signing, retries, egress)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestApprovalsLive" make integration   # S22 §9 on a real server: approve-then-run through mcp wrap, the deny reason, the emailed link (dev mail log), a signed webhook
go test ./internal/mailer -run 'TestGoldens|TestApprovalGoldens' -update   # S22: the approval email goldens (approval_requested, approval_foureyes)
deedglass org dev mail-preview approval|approval-foureyes   # S22: render the approval email to the terminal (no send)
make demo-approvals-on | demo-approvals-off | demo-approver EMAIL=… | demo-lan | demo-lan-off   # S22: the approval beat, real email (repo .env), phone links on the Wi-Fi
go test ./internal/witness/undo -cover   # S23: the undo planner (net effect, order, digest, the twelve checks, SQL, exact footprint, copy, fuzz seeds); pure, no database
go test ./internal/witness/undo -run TestSQLGoldens -update   # S23: the generated-statement goldens in internal/witness/undo/testdata/sql (review the diff)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run TestUndoLive" make integration   # S23 §9 on a real server: the hero MISMATCH, refused and clean previews, countersign by another admin, L2 VERIFIED, redo, the race
make undo-matrix    # S23: the undo round trips and the property test, 2,000 seeds on PG 16, 17 and 18 (logs in .tmp/undo-matrix-<v>.log)
make bench-undo     # S23 §3.10: plan 1,000 rows, preview 301, RunUndo 301 and 1,000; JSON in bench/results/
make bench-overview # S25 §3.8: GET /v1/overview's read (cache bypassed) at 100,000 actions in 24 h, both windows (p99 < 80 ms) and the EXPLAIN plans; JSON in bench/results/
DEEDGLASS_IT_PKGS="./internal/console/ ./internal/ledger/ ./internal/identity/" DEEDGLASS_IT_GOFLAGS="-v -run TestOverview|TestAgentsExpandUsage" make integration   # S25: the Overview (golden, windows, isolation, attention, live chain check) and ?expand[]=usage
DEEDGLASS_IT_PKGS="./internal/ledger/ -args -update-overview" DEEDGLASS_IT_GOFLAGS="-run TestOverviewGolden" make integration   # S25: regenerate internal/ledger/testdata/overview_golden.json (review the diff)
go test ./internal/identity -run TestAgentObjectGolden -args -update-agent-golden   # S25: the plain agent object's bytes (must not change)
make bench-events   # S24 §3.12: the event hub, 1,000 events/s to 200 SSE streams (p99 enqueue→write < 50 ms), memory per stream (< 64 KiB); JSON in bench/results/
go test ./internal/console -run 'TestIsolationProperty|TestResume|TestBackpressure|TestEvents' -v   # S24: the live stream (isolation, resume, backpressure, timers in synctest, limits, hydration)
DEEDGLASS_IT_PKGS=./cmd/deedglass/ DEEDGLASS_IT_GOFLAGS="-v -run ^TestEventsLive$" make integration   # S24 on a real server: fake-action over /v1/events, two orgs, agent key, rollback, clean stop
go test ./cmd/deedglass -run TestFollowSSEGolden -update-follow   # testdata/cli/actions-follow-sse.txt, --follow over SSE (then update SHA256SUMS)
make attack         # S18: the V7 attack suite on a throwaway PG 18 (real serve, real Postgres, loopback upstreams), then the report
make attack-report  # S18: print the last run and regenerate docs/ATTACK_SUITE.md (never edit that page by hand; -check in CI)
DEEDGLASS_IT_ATTACK_RUN='TestAttack/H03' make attack   # one case; 'TestOrgIsolation' for the isolation sweep
make vuln           # S18: govulncheck v1.8.0 (ADR-024); also the CI govulncheck job
go test ./internal/lint/...   # S18: static guards G1-G5 (allow markers: //deedglass:allow-sql-build <reason>, //deedglass:allow-math-rand <reason>)
make demo-check     # S17: the Postgres connector's readiness report on the demo (make demo-up first; it runs init --apply)
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
make web-build      # S24: the Console (web/): tokens → icons → astro build → .br/.gz → check-dist; writes internal/console/dist (make build runs it when Node is present; make build-only skips it)
make web-test       # (in ci) S24: tsc + Vitest (api, stream, format, palette, dom, tokens and contrast, copy, icons, sinks, types)
make web-e2e        # S24/S25: Playwright in the installed Chrome against a throwaway PG 18 and a real serve, seeded with 600 fake actions and the real L2 hero (shell, keyboard, palette, Stop all, live, the five S25 screens, the seal check, keys shown once, viewer, 200 events/s, axe, CSP)
make web-lighthouse # S24/S25: Lighthouse 13 on /login, /app/_kit and the seven S25 pages (the MISMATCH detail, a deed, an agent), desktop and mobile (≥ 95 accessibility and best practices); JSON in ~/txv/ops/plan/prompts-v7/reports/lighthouse/S25
DEEDGLASS_IT_THROWAWAY=1 scripts/web_e2e.sh serve|shots   # keep the seeded Acme stack up on :18241 (PG 18 on :55468, the Acme Store on :55469; ids in .tmp/web-e2e/fixtures.json; WEB_E2E_SEED=0 skips the seed), or write the screenshot pass to ~/txv/ops/plan/prompts-v7/reports/screens/S25
DEEDGLASS_DEV_PROXY=http://127.0.0.1:18241 make web-dev   # S25: astro dev proxied to that throwaway stack instead of :8080
node web/scripts/contrast.mjs   # S24: WCAG ratios for every token pair in both themes (exit 1 on a failure); make web-tokens regenerates the token files
go test ./internal/console -run 'TestPages|TestTokensMatchDesignMD' -v   # S24: the page handler (routes, redirect, CSP, assets, placeholder) and the tokens.json ⇄ DESIGN.md lockstep
make rename-guard   # retired identifiers (rule 5)
make banned-words   # the banned list over files and every commit message
make bench | make cover
make dev            # Docker: Postgres 18.6 + migrate + serve on 127.0.0.1:8080 (creates .env); dev-down, dev-reset, dev-logs
make run            # serve --migrate from source against the compose Postgres (127.0.0.1:55432)
make docker         # the deedglass:dev image (distroless, 65532); make bench-http: S03 HTTP budget
scripts/ci-local.sh # the CI jobs locally (docker-smoke and govulncheck skip when Docker or network is missing)
```

- Go 1.27.1 at `/opt/homebrew/bin/go`. Node 24 (`/opt/homebrew/bin/node`) for `web/verify` and the Console (`web/`); their dev
  tools are exact pins in `web/verify/package-lock.json` (ADR-027) and `web/package-lock.json` (ADR-053), installed with `npm ci --ignore-scripts`. PostgreSQL 16, 17 and 18 are installed at
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
   ADR-027, accepted): `typescript` 7.0.2, `vitest` 5.0.3, `vite` 8.3.1, `@types/node` 24.19.0; no runtime dependency. The Console (`web/`, ADR-053, accepted under DL-038), dev and build only: `astro` 7.3.5, `tailwindcss` and `@tailwindcss/vite` 4.3.3, `@fontsource-variable/inter` and `@fontsource/geist-mono` 5.3.0, `lucide-static` 1.49.0, `typescript` 7.0.2, `vitest` 5.0.3, `happy-dom` 20.14.5, `@playwright/test` 1.63.0, `@axe-core/playwright` 4.13.0, `lighthouse` 13.5.0; nothing from npm runs in the shipped Console.
   `cel.dev/cel-go` v0.32.0 (ADR-017, in go.mod since S10; brings protobuf, antlr4-go, cel.dev/expr,
   genproto and x/exp; the binary-size budget is ADR-032, accepted under DL-024 and amended under DL-031: < 40 MB). The MCP Go SDK
   `github.com/modelcontextprotocol/go-sdk` v1.8.0 (ADR-018, in go.mod since S14; since S15 the
   binary links it for the gateway's upstream client, ADR-037 accepted under DL-029; stripped binary 27.1 MB then, 29.6 MB at S17; budget 40 MB). S16 (vault,
   egress, connectors) added no dependency: AES-256-GCM, the dialer and the JSON Schema subset are standard library (ADR-038, accepted under DL-030). S17 (the Postgres connector) added none either: `pgxpool` is part of pgx, SCRAM is `crypto/pbkdf2` (ADR-039, accepted under DL-031). S20 (the decoder) added `github.com/jackc/pglogrepl`
   v0.0.0-20260824121319-4ae5c490f7ce with `jackc/pgio` v1.0.0 (MIT; ADR-043, accepted under DL-034): only `Parse`
   and the message types, always behind `decoder.SafeParse`. No cgo in the main binary.
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
- The attack suite uses the verdict words since S18 applied ADR-021: VERIFIED, MISMATCH,
  UNVERIFIABLE, plus BLOCKED, DETECTED, NO EFFECT, OUTSIDE SCOPE and S18's DENIED, REJECTED,
  REDACTED. `TestADR021WordsOnly` maps the V0 words back to the frozen digest.
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
3. No custom pgoutput parser: `pglogrepl.Parse` runs only inside `decoder.SafeParse`, which first
   checks the message's whole layout (fixed sizes, strings inside the message, declared lengths
   exact, no trailing bytes, tuple kinds n/u/t only), then calls Parse in `recover`. Any failure
   halts that connector's runner (`malformed_evidence`) before anything is persisted.
   `FuzzSafeParse` and `TestSafeParsePrefixes` run on the captured corpus `testdata/decoder/pgoutput`.
4. COSE encodings use alg -8 (EdDSA, crv Ed25519), ADR-008: go-cose 1.3.0 has no built-in -19.
5. Decode sessions pin `TimeZone='UTC'`, `DateStyle='ISO, YMD'`, `IntervalStyle='postgres'`,
   `bytea_output='hex'`, `extra_float_digits=3`: sent in the decoder's startup packet (a client
   setting outranks `ALTER ROLE … SET`), also set by S17's init on the role, and asserted before
   every peek (`session_settings_changed` halts).
6. The strict pre-parser runs before JCS: gowebpki/jcs rounds 9007199254740993 and accepts 1.5, so
   integers only, magnitude at most 2^53-1, then `bytes == JCS(bytes)` (`internal/intent`).
7. Small-order Ed25519 public keys are refused at registration (`internal/keys`; attack case A13).
8. A controlled function's entry guard shows "first write", not "only write": extra markers are
   caught by counting markers per transaction. Callers retry in a NEW transaction.
9. Classification uses frame position and the message's transactional flag, never the peek `xid`
   or an assumed monotonic peek `lsn`. The checkpoint is the largest `COMMIT.end_lsn` or message LSN
   returned, and, when the peek drained, the flush LSN read before it (ADR-045, amending ADR-002 point
   2). Frames are persisted before the slot advances.
10. A frame is bound only with exactly one `deedglass.ledger` insert and one transactional
   `deedglass` marker naming the same action, of the connector's org, with low32(xid8) = BEGIN.xid
   (ADR-003). The decoder never decides a verdict.

## Guards in place (keep them green; extend, never weaken)

- `internal/ids`: strict parser, spec vector, monotonic and concurrency tests, clock-back and
  overflow tests, pattern-vs-parser agreement, `FuzzParse`.
- `internal/config`: the key table, every prod refusal, `.deedglass/` safety, secret canaries across
  fmt, JSON, slog and panics, and the `docs/CONFIG.md` drift test.
- `internal/store`: catalog shape (34 tables since S23, RLS on every org table, `system_scope` list pinned,
  composite FKs, grants), RLS matrix on all 29 org tables, pooled-connection leak, privilege
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
- S16 (vault, egress, connectors): `internal/archtest` `TestSingleEgressDoor` (only `internal/connector/egress` builds an
  `http.Client`/`Transport`, dials, or uses `http.DefaultClient`/`Get`/`Post`/`Head`/`PostForm`) and `TestOpenerCallSites`
  (only `internal/connector` and the vault open credentials; nobody else names `vault.Opener`); `TestLayering` lets
  `internal/config` import only the leaves `vault/keyring` and `connector/egress` (itself stdlib plus `buildinfo`).
  `internal/vault`: round trip per kind, integrity (flipped bytes, org swap, version and kind edits), unknown key, every
  formatter on `Secret`, redaction forms, `FuzzRedactor`, `Scrub` (12 patterns, 6 near misses), the golden envelope
  (`testdata/vault/envelope_v1.json`, pinned); integration on PG 16/17/18: lifecycle with RLS and a sweep, a moved row,
  rotation (2 orgs × 50, interrupted, two rotators, unknown key). `internal/connector/egress`: `TestCorpus` (59 rows ×
  prod/dev, ≥ 47 required), rebinding on a real socket, redirects, body caps and gzip bombs, proxies ignored, the TLS
  floor, `FuzzCheckURL`. `internal/connector/rest`: S16 §6's table, config refusals and normalization, the checks,
  `FuzzTemplatePath`. `internal/connectorapi`: the route × role matrix. `cmd/deedglass`: `TestConnectorsLive`
  (integration: create, credentials, test, every sealed code, metadata refusal, idempotency, 409, rotation, the dev
  override removed → `destination_blocked`, export → verify, the sentinel sweep: 0 hits). The migration count is 17.
- S17 (the Postgres connector): `internal/connector/postgres` `TestAdmission` (S17 §6's table: stacked, transaction control,
  session, DO/COPY/CALL/TRUNCATE/GRANT, comments, denylisted functions, schema deedglass, CTE writes, EXPLAIN ANALYZE, sizes),
  `TestValuesGolden` (every §3.8 mapping, `testdata/values.golden`), config/DSN/params/digest/call tables; `setup`:
  `TestBeginActionHash` (the pinned body sha256 recomputed from `sql/20_schema.sql`), `TestPlannerGoldens` (fresh, RDS,
  partial, complete → `Nothing to change.`, table added/removed, function tampered, unsafe agent, control plane, missing
  table; no plan holds `PASSWORD` or `SCRAM-SHA-256`), `TestChecksMatrix` (every check id's states), `TestSCRAMVerifier`
  (recomputed from RFC 5802), `TestDetect`. Integration (throwaway cluster, under `pgtest.Lock`: the deedglass_* roles are
  cluster-wide): the decode test (one xid for ledger insert, marker and change), the rows cap rollback, statement and
  lock timeouts, the session reset, stacked statements refused by Postgres with admission bypassed, calls and RETURNING,
  describe, the unsafe role (prod refuses), the live check matrix, a stale plan refused, missing setup, no password in
  the server log (log_statement=all). `cmd/deedglass` `TestPostgresLive` (S17 §9 on a real server, with the output
  sweep). Agent SQL never goes through `Conn.Exec` (pgx uses the simple protocol with no arguments).
- S19 (the Witness): `internal/witness` is pure (archtest). `TestDecideExhaustive` (1,536 cases: every
  {∅,P,G,V,PG,PV,GV,PGV} at L0/L1/L2 × required L0/L1/L2 against an independent oracle), the worked table,
  notes ignored, `TestPrecedenceProperty` (100,000 seeded lists), `TestVerdictGuard` (only `precedence.go`
  produces VERIFIED; it parses internal/ and cmd/, allows comparisons and case clauses, and keeps a reasoned
  allowlist), the catalog rules (one class, ≤ 200 runes, a period, no banned word) and `TestReasonsDoc`
  (`docs/witness/REASONS.md` is generated: `go generate ./internal/witness`), L0/L1/expect/CEL/detail tables,
  the postcondition compile goldens (`testdata/postcondition`, `-update`), ≥ 85% coverage, `make bench-witness`.
  `internal/sqlguard` `ShapeOf` (70 goldens in `testdata/shape`, `-update`; `FuzzShape`). `internal/connector/rest`
  `TestReadBack*` (honest, lying, a 500, not found, redaction, path rules again, config refusals);
  `internal/connector/postgres` `TestPostgresReadBack` (integration: row_written, a trigger's value_mismatch,
  row_superseded, row_missing, typed values, not the PK, READ ONLY as deedglass_agent). `internal/gate`:
  `TestReadBackInline` (no pooled connection held during the read-back; the detail digest), `TestCrashBetweenB1AndB2`
  (one deed), `TestWorkerDeferredWitness` (inline budget → the worker; give-up decides without L1).
  `cmd/deedglass` `TestWitnessLive` (honest, lying, read-back down, the API object and expand[]=witness.detail,
  actions list --verdict, export → verify, the sentinel sweep incl. witness_jobs; CLI goldens `testdata/gate/cli/witness-*`).
  The migration count is 18.
- S20 (the decoder): `internal/decoder` unit tests: `TestSafeParsePrefixes` (≥ 50,000 prefixes: 0 panics, 0 accepted),
  `TestSafeParseMatchesParse`, `TestSafeParseRefusals`, `FuzzSafeParse`; the assembler (old images, TOAST `u`, the
  1,000-image cap with the same digest, sensitive digests, `relation_changed`, malformed streams) and `TestCanonMatchesJCS`;
  `TestClassifyTable` (every class); heartbeat, marker and ledger-row parsing; the guards `TestMarkerLSNNeverComparedWithCommitLSN`
  (AST over internal/, cmd/, pkg/ plus SQL), `TestRawHasNoXid`, `TestEmitMessageThreeArguments`, `TestNoVerdictWords`;
  `TestConsoleCopy`. Integration on PG 16/17/18: the bound update, exact images (2^53+1), D01–D04, D17, sensitive columns,
  E01, the 500-action burst across a restart, E10, E11, the 12,000-row transaction, the publication drop (16/17 halt,
  18 gap), idle advance, two supervisors and the lease move, the advisory-lock wait, relation_changed, E04 drop and
  re-slot with missing frames, ledger_function_changed; on a private Homebrew cluster: E02 rewind, E05, E07, E03,
  E08 (skipped in Docker mode). `TestCaptureCorpus` (`-update-corpus`). `cmd/deedglass` `TestDecoderCommandGoldens`
  (`testdata/decoder/`), `TestDecoderWordsCopy`. `internal/gate` `TestEvidenceJSON` (ADR-044). The migration count is 19.
- S21 (L2): `internal/witness/l2` is pure (archtest). `TestFootprintTable` (30 rows in
  `testdata/footprint/cases.json`), `TestCompareTable` (S21 §6), `TestCompareProperty` (a fitting
  frame never gives a violation; one extra row always does), `TestNoGapBecomesViolation` (every L2
  catalog code through `Decide`), `TestHero`, `TestRenderGoldens` (`testdata/golden`, 80/120 columns,
  TTY and plain, redacted, many columns, `detail_hero.json`), `TestCrossCheck`, `TestDetailCap`.
  `internal/gate` (integration): `TestL2FrameArrives`, `TestL2FrameBeforeB1` (the race),
  `TestL2UnknownExecution` (E06), `TestL2SlotLostAndMissing` (and halted/dropped decoders),
  `TestL2Amendments` (late VERIFIED, multiple_transactions, l2_late_confirmed, the 3-amendment
  limit, the chain). `cmd/deedglass` `TestWitnessL2Live` (real decoder, CLI, API, `/mcp/native` with
  the Go SDK, an amendment, export → verify), `TestL2Budget` (`make bench-l2`). `attack/`: group W
  (W01–W13, `expectedDigestW`, ADR-046) and H08–H11 run; every case points the decoder at its clone
  (`H.L2Ready`). `examples/demo` `TestHeroScript` (`make demo-hero-check`). The migration count is 20.
- S22 (approvals and webhooks): `internal/approve` unit tests (agent and API copy, summaries,
  clocks, `CheckDecideInput`, eligibility and the self rule, link tokens, `redactArgs`,
  `fitContext`, sections, `FuzzApprovalLinkBody`); integration on PG 16/17/18: `TestApproveRunsOnce`,
  `TestDecisionTable` (four-eyes, veto, self, not eligible, keys never vote, stale context, expiry,
  cancel), `TestApprovalGuard` (DG006: context, request, decided rows, append-only countersigns;
  a vote can't be edited; `internal/store`'s append-only test covers `approval_votes`), `TestDecideRace` (200 runs: one decision),
  `TestApproveRacesExpiryAndCancel`, `TestLinks` (preview decides nothing, one use, void, review
  mode, 401 for every unusable link), `TestWaitWakes`. `internal/notify`: the email notifier over a
  fake queue (one link per person, idempotency key, reserved addresses skipped under Resend).
  `internal/notify/webhook` (integration): `TestWebhookDelivery` (outbox in the change's
  transaction, signature, retries, auto-disable with a deed),
  `TestWebhookDestinations` (egress refusals). `pkg/deedglass/webhook`: sign/verify table,
  rotation, `FuzzSignatureHeader`, 100% coverage. `internal/connector/postgres` `TestPlanRows` and
  (integration) `TestEstimate` (EXPLAIN only, READ ONLY as `deedglass_agent`, every missing-estimate
  reason). `internal/mailer` `TestApprovalGoldens`. `cmd/deedglass` `TestApprovalsLive`.
  `attack/`: group L (L01–L08, `expectedDigestL`, ADR-049). The migration count is 21.
- S23 (undo): `internal/witness/undo` (pure) unit tests: `TestNetEffect` (the net-effect table: inserts,
  updates, deletes, TOAST `'u'` datums, key moves, identity ALWAYS), `TestNetEffectCascadeOrder`,
  `TestNetEffectUnavailable` (TRUNCATE, DDL, missing old images, sensitive values a step must write),
  `TestSensitiveOnlyInGuard`, `TestCollapse`, `TestCollapseContradictions`, `TestDigestDeterministic`,
  `TestKeySwapCycle`, `TestKeyReusedByAnotherRow`, `TestIdentityMovedThenReplaced`, `TestFromFrames`,
  `TestFromFramesProblems`, `TestChecksAllPass`, `TestChecksOneEach` (a failing case per check),
  `TestChecksWithoutPlan`, `TestHolds` (sensitive values by digest), `TestCompare` (byte-exact
  guards), `TestSQLGoldens` (`testdata/sql`), `TestStatementsUseParameters`, `TestSQLRefusals`,
  `TestValidTypeName`, `TestExactFootprint`, `TestExactFootprintCompare`, `TestExactFootprintMoved`,
  `TestCopyRules` and the copy tests; fuzz `FuzzNetEffect` (a plan or a typed refusal, never a panic,
  and a consistent sequence's plan puts the table back exactly) and `FuzzTypeName`.
  `internal/witness/l2`: the ledger cross-check compares `sql_digest` with the plan digest. `internal/connector/postgres`:
  `undo` is an internal tool; `internal/mcp/front`: internal tools never listed or resolved.
  `internal/identity`: `console`, `system`, `deedglass` reserved. `internal/httpx` the route table
  and OpenAPI cover `previewUndo` and `createUndo` (admin). `internal/store`: `undo_plans` grants
  (`SIu`) and the 34-table catalog. `cmd/deedglass` usage tests for `undo preview|run` and
  `TestUndoLive` (integration: the hero MISMATCH, a refused preview with the trigger's fix SQL, agent
  keys 403, an agent refused `postgres.undo`, a stale plan, `already_undone` while pending, the self
  rule, L2 VERIFIED with the table back byte for byte, a redo, the race ending `undo_conflict` with
  nothing changed, the chain verifying with the original deed unchanged). `internal/gate`
  `TestConsolePrincipal` (Stop all skips the console agent, only the console principal names
  `undoes_action_id`, one live undo per action with `ErrUndoTaken`, a failed undo frees its slot, a
  stopped console agent is denied). `internal/identity` `TestSystemAgent` (created once with one
  deed, no keys, name reserved, Stop all doesn't count it). `internal/connector/postgres`
  `TestUndoRoundTrip` (real pgoutput through the decoder's own code: the named type and column
  cases, the race and lock conflicts, cascades, and the property test, 200 seeds by default and
  2,000 per version in `make undo-matrix`) and `TestUndoBudget` (`make bench-undo`). `attack/`: group
  U (U01–U05, `expectedDigestU`, ADR-052). The migration count is 22.
- S24 (the Console): `internal/console` `TestPages` (every route, the exact CSP, ETag and 304, HEAD),
  `TestNotFoundNeverEchoes`, `TestKitIsDevOnly`, `TestSignedOutRedirects`, `TestSafeNext`, `TestAssets`
  (immutable, br/gzip, Vary), `TestPlaceholder` (prod refuses), `TestEmbeddedBuild`, `TestTokensMatchDesignMD`
  (tokens.json ⇄ DESIGN.md), plus the hub and events suites. `web/` Vitest: the HTML-sink and storage greps, no
  hex outside tokens.json, aurora only in the Mirror, the copy lift and banned words, icons used/missing, token
  staleness, contrast (0 failing pairs, the S24 §3.2 ratios), `types.ts` against the Go goldens and structs.
  `scripts/check-dist.mjs` (no inline script or style, no sinks, budgets). Playwright (`make web-e2e`): 41 cases
  with a CSP-violation and console-error listener on every page, axe 0 serious or critical in both themes.
  `internal/connector/postgres` `TestGeneratedColumnsPG18` (DL-037: `publish_generated_columns = stored`, the
  S17-era publication upgraded, virtual generated columns refused).
- S25 (Console screens I): `GET /v1/overview` (`internal/console/overview.go`, `internal/ledger/overview.go`; one
  read-only org transaction, 2 s statement timeout, a forced custom plan; 10 s cache with single flight; a background
  24 h chain check at most every 5 min per org), migration 0023 (`actions_overview`, a covering index); `?expand[]=usage`
  on the agents routes (`identity/agents_usage.go`, `policystore.Usage.AgentsTx`; without it the agent object is
  byte-identical, `TestAgentObjectGolden`). Tests: ledger `TestOverviewGolden`, `TestOverviewWindowBoundaries`,
  `TestOverviewIsolation`, `TestOverviewAttention`, `TestOverviewNoSystemAgent`, `TestOverviewBudget` (`make
  bench-overview`); console `TestOverviewRoleMatrix` (agent keys 403), `TestOverviewLive`; identity
  `TestAgentsExpandUsage`; `TestOrgIsolation` sweeps the Overview. `web/` Vitest: `filters`, `feed`, `diff` (S21's
  fixtures and the real hero), `sentences`, `snippets` (equal to the CLI goldens), `overview`, `deedview`. Playwright
  (`make web-e2e`, 65 cases on a seeded stack with the real L2 hero): the five screens, the in-browser seal check and
  the byte-exact download, keys shown once, Stop across tabs < 2 s, viewer refusals, the state matrix, 360 px, reduced
  motion, 200 events/s with 0 long tasks. `internal/policy`: a folded `when` with a more-indented line is written
  literal (FuzzCompile seed `8dc64553839b3266`). The migration count is 23.
- `cmd/deedglass verify` (S05): 80 golden cases in `testdata/verify/<case>/{args,stdout,stderr,exit}`
  (`go test ./cmd/deedglass -run TestVerifyGolden -update`), every vector's code and exit, `--keys`
  URL rules (same-host redirects, 1 MiB, timeout, loopback-only http), key-set merge conflicts, no
  network without a `--keys` URL, terminal sanitizing.
- `web/verify` (S05): vectors, parity 500/500, strict, JCS, keys (A13, RFC 8032, Wycheproof), page
  safety (CSP, no HTML from strings, no storage, one fetch).
- `attack/` (S18): 106 case ids (66 V0 + 19 V7: H01–H11, J01–J04, K01–K04 + S21's W01–W13 + S22's L01–L08 + S23's U01–U05), the outcome enum, the
  demo sets, one pinned digest per group set (`expectedDigest` A–G after ADR-021,
  `expectedDigestV7` H/J/K, ADR-040, `expectedDigestW` W, ADR-046, `expectedDigestL` L, ADR-049, `expectedDigestU` U, ADR-052; a group without a pin fails), `TestADR021WordsOnly`, `needs`
  names S19–S37 and stays outside the digest, V7 OWASP tags only ASI01/02/03/10. `make attack`
  (tag `attack`): a real `serve` built with `-tags attackhooks` (hosts-file DNS, dev only;
  `TestNoAttackHooks` keeps it out of default builds), per-case target clones, the five
  invariants after every case (`TestInvariantsCatch` shows they can fail), `TestOrgIsolation`
  (every id-bearing route answers another org's ids exactly as unknown ids; lists and exports leak
  nothing; no Acme row changes; a route without a fixture fails), and `attack-report -check` (the
  generated page matches the committed results). `internal/lint` (S18): G1–G5 with `TestRepoClean`
  and a positive test per guard. `internal/httpx` `TestRouteAuthCoverage` and `cmd/deedglass`
  `TestServeRoutesUnauthenticated`: the public route set is exact and every other route is 401
  without a credential.
- `scripts/integration_smoke.sql`: live prosrc hash, roles, grants, RLS, replica identity, one real
  transfer, refusals with exact SQLSTATEs, decode checks through pgoutput and test_decoding.
- `scripts/rename_guard.sh` and `scripts/banned_words.sh`; `testdata/SHA256SUMS`. Test strings with
  backslash-u escapes are built at run time (`u("0061")` in intent tests).

## Schedule

The session calendar, founder reminders and budget live in
`~/txv/ops/plan/prompts-v7/00_SCHEDULE.md`. A missed date moves the schedule; expected outcomes are
never weakened to meet one.
