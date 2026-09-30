# Naming: Deed, Seal, Witness, Gate, Ledger, Console, Stop, Insights

One name, one set of words. This page lists the product vocabulary, the S01 rename from the V0 identifiers to `deedglass`, what stayed on purpose, and what each retired V0 command became.

Sources: `~/txv/ops/brand/DEEDGLASS_BRAND.md` §1 and §3, `~/txv/ops/plan/prompts-v7/00_CONTRACT.md` §1 and §3–§4, and ADR-012 (proposed).

## Spelling

- **Deedglass**: one word, capital D only. Never "DeedGlass", "Deed Glass", "DEEDGLASS" or "DG".
- `deedglass`, all lowercase, in the CLI, the wordmark, the Go module, env var prefixes (`DEEDGLASS_` is the one uppercase form), SQL names and type strings.
- Ids are lowercase everywhere, including examples: `agt_01j9…`, `deed_01j9…`.

## Product vocabulary

| Concept | Deedglass word | Notes |
|---|---|---|
| Signed record of one action | **Deed** | Plural: deeds. Id prefix `deed_`. |
| Signing a deed | **Seal**, **sealed** | "Deed sealed." The signing key is the **seal key** (`sk_`). |
| The hash-linked sequence of deeds per org | **Deed chain** | "Chain intact." |
| Verification engine | **Witness** | "Witnessed by Postgres." |
| Verification result | **Verdict** | `VERIFIED` · `MISMATCH` · `UNVERIFIABLE` |
| Verification levels | **L0 Reported** · **L1 Confirmed** · **L2 Witnessed** | L0 is the tool's own response. L1 is a read-back from the target. L2 is the system of record (the Postgres change stream). |
| Execution proxy and MCP gateway | **Gate** | "Every tool call passes through the Gate." |
| Rules | **Policies** | A plain word, on purpose. |
| Human approval | **Approval** (approve, countersign) | "Countersigned by Priya." |
| History of actions and events | **Ledger** | Searchable and append-only. |
| Suggestions a human approves | **Insights** | Never "self-learning" or "autonomous". |
| Web app | **Console** | `app.deedglass.com` |
| Registered AI agent | **Agent** | |
| Tool integration | **Connector** | |
| Kill switch | **Stop** | "Stop agent", "Stop all agents". |
| Company or team account | **Org** | The one word for it, in the UI and in the docs. |

The attack suite keeps its V0 outcome words (VALID, INVALID, INCONCLUSIVE and the rest) until S18 applies ADR-021 (approved 2026-10-01).

## The rename map (S01, ADR-012)

| Old | New | Notes |
|---|---|---|
| Go module `github.com/testamur/testamur` | `github.com/deedglass/deedglass` | Imports rewritten |
| `cmd/txv` | `cmd/deedglass` | Moved with `git mv`, so history follows |
| `bin/txv` | `bin/deedglass` | `make build` passes version ldflags |
| `TXV_PG` | `DEEDGLASS_IT_PG` | Integration tooling |
| `TXV_IT_{MODE,KEEP,THROWAWAY,DB}` | `DEEDGLASS_IT_{MODE,KEEP,THROWAWAY,DB}` | Integration tooling |
| `TXV_PG_PASSWORD` | `DEEDGLASS_DEV_PG_PASSWORD` | Tooling; the binary ignores it |
| `TXV_REPO_PUBLIC` | `DEEDGLASS_REPO_PUBLIC` | Read by `scripts/banned_words.sh` |
| Schema `txv` | `deedglass` | Customer-side schema (contract §4) |
| Role `txv_owner` | `deedglass_owner` | Owns the controlled functions; NOLOGIN |
| Role `txv_executor` | `deedglass_agent` | Least-privilege role |
| Role `txv_verifier` | `deedglass_witness` | REPLICATION role; keeps its pinned session settings |
| `txv_it_app`, schema and database `txv_it` | `deedglass_it_app`, `deedglass_it` | Test fixtures |
| Publication `txv_pub` | `deedglass_pub` | Created before any slot |
| Slot `txv_slot` | `deedglass_v0` | Connector slots are `deedglass_<connector id>`; the V0 demo has no connector |
| Logical message prefix `txv/v1` | `deedglass` | The V0 payload tag `txv1\|` became `v1\|`; the version moves fully into the payload in S17 |
| `txv.intent/1`, `txv.bundle/1`, `txv.attack_meta/1` | `deedglass.intent/1`, `deedglass.bundle/1`, `deedglass.attack_meta/1` | Type strings |
| `urn:txv:schema:intent:1` | `urn:deedglass:schema:intent:1` | Intent JSON Schema id |
| SQL messages `txv: …` | `deedglass: …` | Message text only |
| SQLSTATEs `TX001`–`TX031` | Unchanged | TX means transfer, not the old name |
| Compose `name: txv`, volume `txv-pgdata`, containers `txv-it-pg*` | `deedglass`, `deedglass-pgdata`, `deedglass-it-pg*` | `deploy/docker-compose.yml` and the integration scripts |
| "Testamur", "Deltamark" and working-name banners | Removed | |

Any `TXV_*` environment variable is an error, in dev and in prod, with a hint that names its `DEEDGLASS_*` replacement.

The rename changed the three hashes in `bundles/transfer_v1/manifest.json`. ADR-012 records the old and new values. The attack-suite digest did not change.

## What stays on purpose

- The directory `~/txv/product` and every `~/txv/ops/…` path. They are places on disk, not identifiers (contract §1).
- The SQLSTATE codes `TX001`–`TX031` of the V0 transfer demo.
- The text of ADR-000 to ADR-008. ADR-001 and ADR-008 gain one first line, "Identifiers renamed by ADR-012", and nothing else.

## Retired V0 commands

The V0 binary listed these commands. None of them was built. Each has a successor in the contract §10 command tree; the session in brackets builds it.

| V0 command | Successor |
|---|---|
| `sign` | `deedglass dev sign-intent` (S09), and signed intents in the `Deedglass-Intent` header |
| `submit` | `deedglass act` and `POST /v1/actions` (S13) |
| `executor serve` | `deedglass serve`, which runs the Gate (S03, S13) |
| `verifier run` | the Witness, inside `deedglass serve` (S19–S21) |
| `statement verify` | `deedglass verify` (S05) |
| `evidence export`, `evidence verify`, `evidence report` | `deedglass deeds export` (S05, S06) and `deedglass verify` |
| `install` | `deedglass connector postgres init` (S17) |
| `audit` | `deedglass connector postgres check` (S17) and `deedglass doctor` (S32) |
| `boundary` | `deedglass connector postgres check` (S17) |
| `status` | `deedglass actions show` (S13) |
| `chain` | `deedglass ledger check` (S06) |
| `keygen` | `deedglass keys gen` for seal keys (S04) and `deedglass agent intent keygen` for intent keys (S09) |

`version` survives unchanged in name.

## How the rename is enforced

- `scripts/rename_guard.sh` runs in `make ci` (target `rename-guard`) and in the CI lint job.
- It scans every file git would track and matches, case-insensitively, `testamur`, `deltamark`, and `txv` as a token: not preceded by a letter or digit, and followed by `_ . / -`, a word boundary or the end of the line.
- The on-disk paths `~/txv/`, `$HOME/txv/` and `/Users/<name>/txv/` are allowed without an entry.
- Anything else needs a line in `.rename-allow`, in the form `path::token::reason`. The reason is required.
- A hit prints `path:line: retired identifier "txv". Rename it to deedglass (docs/NAMING.md).` and the script exits 1.
- The old internal codename is caught separately, by `scripts/banned_words.sh`.

## What this page does not do

It does not rename anything by itself, and it does not cover the brand's visual identity (see the brand doc). It does not list every id prefix; `docs/IDS.md` does.
