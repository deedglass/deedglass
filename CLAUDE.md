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

## Commands

```sh
make help           # every target
make ci             # fmt-check, tidy-check, vet, test (-race), testdata-check, rename-guard, banned-words, nocgo
make build          # bin/deedglass with version ldflags; bin/deedglass help
make test           # go test -race -count=1 ./...     (make test-short: no -race, short mode)
make integration    # throwaway PG (DEEDGLASS_IT_PG=18|17|16, DEEDGLASS_IT_MODE=brew|docker): sql/ smoke,
                    # control-plane roles + deedglass migrate + go test -tags integration
make bench-store    # S02 store budget; raw output in bench/results/
make matrix         # integration on 16, 17 and 18
make fuzz           # 30 s fuzz of the intent parser and the id parser
make rename-guard   # retired identifiers (rule 5)
make banned-words   # the banned list over files and every commit message
make bench | make cover
make dev            # Docker: Postgres 18.6 + migrate + serve on 127.0.0.1:8080 (creates .env); dev-down, dev-reset, dev-logs
make run            # serve --migrate from source against the compose Postgres (127.0.0.1:55432)
make docker         # the deedglass:dev image (distroless, 65532); make bench-http: S03 HTTP budget
scripts/ci-local.sh # the CI jobs locally (docker-smoke and govulncheck skip when Docker or network is missing)
```

- Go 1.27.1 at `/opt/homebrew/bin/go`. PostgreSQL 16, 17 and 18 are installed at
  `/opt/homebrew/opt/postgresql@{16,17,18}/bin` (keg-only; never `brew services`). Docker images
  `postgres:16.15`, `17.11` and `18.6` work too (`DEEDGLASS_IT_MODE=docker`).
- Integration scripts refuse to run unless `DEEDGLASS_IT_THROWAWAY=1` (set by `make integration`).
- CI: `.github/workflows/ci.yml` (push to main and PRs: lint, unit, rename guard, banned words,
  no-cgo build, PG 18.6 integration); `pg-matrix.yml` (16.15/17.11/18.6) runs on demand and nightly.

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
   `github.com/jackc/pgx/v5` v5.11.0 (ADR-001, in go.mod since S02). Planned, and added only by the
   session that uses them: `cel.dev/cel-go` v0.32.0 (S10, ADR-017), the MCP Go SDK v1.8.0 (S14,
   ADR-018), `pglogrepl` at the spike-verified pin (S20), `github.com/veraison/go-cose` v1.3.0
   (S05, ADR-008). No cgo in the main binary.
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
  33-code error catalog (copy rules, banned words, `docs/api/errors.md` headings); the middleware and
  idempotency tables; log canaries; `CheckSpec`/`CheckRequired` keep `api/openapi.yaml` equal to the
  mounted routes (also run on everything `serve` mounts in `cmd/deedglass`). A new route goes into
  `serveRoutes`, `contract_routes.go` (if new) and `api/openapi.yaml` in the same change.
- `cmd/deedglass`: goldens for help, version, config print and errors, with exit codes.
- `internal/intent`, `internal/keys`, `internal/bundle`: strict JSON and JCS vectors, key checks,
  manifest hashes bound to the SQL and schema files.
- `attack/`: all 66 case ids present, outcome enum, demo set, digest pin.
- `scripts/integration_smoke.sql`: live prosrc hash, roles, grants, RLS, replica identity, one real
  transfer, refusals with exact SQLSTATEs, decode checks through pgoutput and test_decoding.
- `scripts/rename_guard.sh` and `scripts/banned_words.sh`; `testdata/SHA256SUMS`. Test strings with
  backslash-u escapes are built at run time (`u("0061")` in intent tests).

## Schedule

The session calendar, founder reminders and budget live in
`~/txv/ops/plan/prompts-v7/00_SCHEDULE.md`. A missed date moves the schedule; expected outcomes are
never weakened to meet one.
