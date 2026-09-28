# CLAUDE.md: builder rules for the txv product repo

This is the private V0 product repo (Go module `github.com/testamur/testamur`, provisional).
Working brand **Testamur** is not cleared until the founder's name gate passes (fallback Deltamark).
Code and database identifiers use the brand-neutral token `txv` and never change. The brand appears
only in the module path, README and docs.

**Source of truth** (read the sections a task needs; do not re-derive them):
- `~/txv/ops/plan/00_MASTER_OPERATING_PLAN_V4.md`: §3 (product V0), §3.0 (spike amendments),
  §3.12 (these rules), §1.4 (banned words). Where the plan and an appendix disagree, the plan wins.
- `~/txv/ops/plan/20_ARCHITECTURE_SPEC_V0.md`: the build spec, as amended by plan §3.0.
- `~/txv/ops/plan/50_EXPERIMENT_LOG.md` and `~/txv/ops/spikes/`: Day-1 spike results (S1-S5 done;
  do not redo them). ADRs: `~/txv/ops/adr/` (see `docs/adr/README.md`).

## Commands

```sh
make ci             # fmt-check, tidy-check, vet, test (-race), testdata-check, banned-words
make test           # go test -race -count=1 ./...     (make test-short: without -race)
make vet | make fmt | make fmt-check | make tidy-check
make integration    # throwaway PG (TXV_PG=18|17|16, TXV_IT_MODE=brew|docker): sql/001-004 + smoke
make matrix         # integration on 16, 17 and 18 (local gate evidence)
make fuzz           # 30 s fuzz of the strict intent parser
make banned-words   # plan §1.4 list over files and all commit messages
```

- Go 1.27.1 at `/opt/homebrew/bin/go`. PostgreSQL 17.11/18.6 at `/opt/homebrew/opt/postgresql@{17,18}/bin`
  (keg-only; never `brew services`). PG16 is not installed yet: use Docker `postgres:16.15`.
- Integration scripts refuse to run unless `TXV_IT_THROWAWAY=1` (set by `make integration`).
- CI: `.github/workflows/ci.yml` (push to main, PRs: lint + unit + PG 18.6 integration);
  `pg-matrix.yml` (16.15/17.11/18.6) runs only on `workflow_dispatch` and nightly (private-minutes rule).

## Rules (plan §3.12)

1. Follow `20_ARCHITECTURE_SPEC_V0.md` exactly, as amended by plan §3.0. Any deviation needs an ADR the
   founder approves.
2. Every attack test declares its expected outcome in `attack/attack_meta.json` **before** it is
   implemented. Never change an expected outcome (or its detail or priority) to make a test pass
   without a founder-approved ADR. `attack/meta_test.go` pins a digest of them; updating
   `expectedDigest` is itself the visible act that needs the ADR.
3. No code path turns an evidence gap into INVALID, or anything other than a clean pass into VALID.
   The precedence function is unit-tested exhaustively over all reason combinations.
4. SQL: no dynamic SQL; no EXCEPTION blocks in the function; every object schema-qualified; REVOKE and
   GRANT in the same transaction as CREATE; `search_path` pinned to `pg_catalog, pg_temp`.
5. Identifiers: nothing named `alloy` (internal codename only, never external-facing). Permanent
   identifiers use `txv`.
6. Pinned dependencies only (spec §3.2: pgx/v5 v5.11.0, pglogrepl pseudo-version
   `v0.0.0-20260824121319-4ae5c490f7ce`, gowebpki/jcs v1.0.2, veraison/go-cose v1.3.0,
   modernc.org/sqlite, testcontainers-go). No new dependency without an ADR. `go.sum` is committed.
7. CI matches the banned words (below) in docs, strings and commit messages as whole words/phrases,
   case-insensitive, and fails on any hit not in `.banned-words-allow` (`path::phrase::reason`; exact
   quoted prior-art titles and explanatory negations only). "open source" stays banned until the repo
   is public.
8. Benchmarks write raw output to `bench/results/<date>-<host>.json`; docs cite only those files.
9. Every factual sentence in public docs or site copy needs a row in `CLAIMS_REGISTER.md` (ops repo)
   with a primary URL and date.
10. Never touch production or customer credentials, databases or environments. Throwaway databases
    only (local Docker/Homebrew clusters, the RDS spike/P0 instance, one Supabase and one Neon smoke
    project). The founder creates, resizes and deletes anything that can bill, and keeps all cloud
    account credentials; Claude never holds AWS IAM keys. Supabase `create_project`/`pause_project`
    only after the founder's explicit go in chat.
11. Nothing becomes public (repo visibility, release, deploy, post) without the founder's explicit
    approval in chat. Never push, tag, publish or create remote repos unasked.
12. Demo footage comes only from real engine runs; `.tape` files and the commit hash go with every video.
13. Usage hygiene: one primary session plus at most 2 subagents; `/compact` at phase boundaries; long
    test matrices and renders run in the shell.
14. Commits: author is the configured git user; every message ends with a blank line and
    `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit messages are scanned for banned words.
15. Spike-derived invariants (plan §3.0): verifier decode sessions pin their settings; `marker_lsn` is
    never compared with `commit_lsn`; every pgoutput message goes through `safeParse`; the strict
    pre-parser runs before JCS; small-order keys are refused at registration; classification uses
    frame position and the transactional flag, never peek `xid`/`lsn` order.
16. When rate-limited, drop work in this order: message drafts, then ops files, then content; never
    ADRs, gate tests or anything on the T1/T2 path.

## Spec amendments in force (plan §3.0; they override `20_` until the spec is edited)

1. §7 marker LSN: `pg_logical_emit_message` returns the END LSN of the message record; it equals
   `commit_lsn` only by coincidence. `marker_lsn` is evidence only; a unit test asserts the verifier
   never compares it with `commit_lsn`.
2. §17 S1 fallback: `pg_control_system()` is PUBLIC-executable on PG 17/18; no `pg_monitor` needed.
3. §17 S3 / §8.3: no custom parser. `pglogrepl.Parse` runs inside `safeParse` (recover, per-type
   minimum lengths, declared-length checks); any failure is `MALFORMED_EVIDENCE` plus a fail-secure
   halt of statement issuance. A Go fuzz test is seeded from the spike truncation corpus.
4. §11 statement `alg`: **-8** (EdDSA, crv Ed25519), ADR-008; go-cose 1.3.0 has no built-in -19.
5. §8.3/§10.1 pinned verifier session: `TimeZone='UTC'`, `DateStyle='ISO, YMD'`,
   `IntervalStyle='postgres'`, `bytea_output='hex'`, `extra_float_digits=3`; set per connection,
   asserted every poll, set with `ALTER ROLE txv_verifier SET` (done in `sql/001_roles.sql`), recorded
   in attested metadata. Attack case E11.
6. §5 strict pre-parser before JCS: gowebpki/jcs rounds 9007199254740993 and accepts 1.5, so integers
   only, magnitude <= 2^53-1, enforced before canonicalizing; then `bytes == JCS(bytes)`
   (`internal/intent`).
7. §12 small-order public keys are refused at registration (`internal/keys`); attack case A13.
8. §4.4 the entry guard shows "first write", not "only write". Extra markers are caught by C4
   `MARKER_COUNT` (D04), a non-transactional marker by C3 (D02), an app-role marker by C2 (D01). The
   executor calls the function once per transaction and retries in a NEW transaction (TX001 otherwise).
9. §8.3 classification by frame position and `M.Transactional`, never by peek `xid` or assumed
   monotonic peek `lsn`; the checkpoint stays `max(COMMIT.end_lsn)`.

## Vocabulary and banned words (plan §1.3, §1.4; canonical list: `scripts/banned_words.txt`)

Use: effect verification, write verification, (signed) verification statement, controlled write
path, Operation Bundle, observation boundary; verdicts VALID / INVALID / INCONCLUSIVE, plus OUTSIDE
SCOPE (docs) and BLOCKED (prevention). Never a binary "VERIFIED".

Banned in every file, string and commit message: "trustless", "irrefutable", "unbreakable",
"tamper-proof", "mathematically proves", "cryptographic proof of execution", "proof of execution",
"proves the transaction happened", "proves" or "prove/proof" as a guarantee, "guaranteed",
"SCITT-compliant", "RFC 9942 receipt", "receipt" for our statement, "EU AI Act compliant",
"Article 12 infrastructure/solution", "compliance" as positioning, "prevents all tampering",
"no hidden side effects", "liability transfer", "verify any agent action", "intent verification",
"synchronous", "patented", "certified", "certification", "certificate", "attests"/"attestation" as a
product claim, "we testify", any gloss of the name as a certificate, "alloy", and "open source" until
the repo is public. Prefer: checks, shows, compares, detects, reports.

## Guards already in place (keep them green; extend, never weaken)

- `internal/intent`: strict parser, closed schema, canonical check, JCS reference vectors, a pin on
  the JCS rounding behaviour, schema-file sync test, `FuzzParse`.
- `internal/keys`: kid derivation, small-order points derived from first principles and cross-checked
  against the stdlib forgery probe and the published list, non-canonical and off-curve encodings,
  RFC 8032 and Wycheproof keys accepted.
- `internal/bundle`: manifest hashes bound to `intent.schema.json`, the function body (prosrc) and
  `002_schema.sql` (provisional raw-bytes hash; normalization lands with the installer).
- `attack/`: all 66 case ids present, outcome enum, demo set, digest pin.
- `scripts/integration_smoke.sql`: live prosrc hash, roles/grants/RLS/replica identity, one real
  transfer, refusals with exact SQLSTATEs, and decode checks via pgoutput and test_decoding.
- Test data: `testdata/SHA256SUMS`. Test strings containing backslash-u escapes are built at run time
  (`u("0061")` in intent tests) because some tools unescape them in source.

## Gates (plan §3.10)

G0 Mon 09-28 spikes + ADR-000..008 · G1 Wed 09-30 A01 frame, B01/B02/C01/C02 BLOCKED · G2 Thu 10-01
VALID/INVALID/INCONCLUSIVE from real runs, offline statement check · G3 Fri 10-02 P0 A/B/C on 3 PG
versions + D01/D05/D06, compose demo < 10 min · G4 Mon 10-05 all P0 green, docs complete, banned-word
grep clean · G5 Tue 10-06 runbook, `v0.1.0`. A missed gate moves the schedule; expected outcomes are
never weakened to meet a date.
