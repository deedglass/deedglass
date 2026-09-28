# Testamur

> **Working name.** Testamur is not final until the founder's name gate passes (fallback: Deltamark).
> Code and database identifiers use the brand-neutral token `txv` (binary `txv`, schema `txv`,
> roles `txv_*`, message prefix `txv/v1`, type strings `txv.intent/1`) and never change.

**Status: pre-release, private.** Nothing in this repository is published. It becomes public only
after release gate G4 and the founder's approval.

## What it is

Effect verification for agent writes to PostgreSQL. Gateways and identity layers decide what an AI
agent may do before it acts; this checks what it actually wrote, by comparing the row changes decoded
from the database's own logical-replication stream with the changes a signed, typed authorization
should have caused. The scoped claim:

> For each committed PostgreSQL transaction that carries a txv ledger entry, the verifier checks
> whether the row changes decoded from the database's own logical-replication stream, within the
> declared observation boundary, exactly equal the changes that the pinned Operation Bundle computes
> from the signed intent and the decoded before-images. It then issues a signed **VALID**,
> **INVALID** or **INCONCLUSIVE** statement. It also raises a signed alert for any change to a
> protected table that no valid intent accounts for. This holds under explicit assumptions: an
> honest PostgreSQL server, its superusers and the owners of verified objects; an honest verifier
> host and verifier key; and an uncompromised intent-signing key. It does not cover reads, effects
> outside the boundary or outside the WAL, or whether the authorized intent itself was a good idea.

In one line: signed intent in; independently computed expected delta versus decoded observed delta
out; a signed three-valued verdict.

## Quickstart

TODO(G3): `docker compose -f deploy/docker-compose.yml up` will run PostgreSQL 18.6, the executor
and the verifier, and `demo.sh` will run the demo cases (A01, B01, C01, A03, D05, D06). Today:

```sh
make ci            # gofmt, go mod tidy check, go vet, go test -race, test-vector checksums, banned words
make integration   # throwaway PostgreSQL (Homebrew, else Docker): apply sql/001-004 and run the smoke checks
make build && bin/txv version
```

## Layout

| Path | What |
|---|---|
| `cmd/txv` | the single CLI (subcommands per spec 20_ §3.3; only `version` works today) |
| `internal/intent` | txv.intent/1: strict pre-parser, schema, canonical (JCS) check |
| `internal/keys` | kid derivation; small-order and non-canonical key rejection |
| `internal/{bundle,pgsql,decoder,boundary,verify,statement,store,executor}` | package docs only so far |
| `sql/` | `001_roles`, `002_schema`, `003_transfer_v1` (the controlled function), `004_publication` |
| `bundles/transfer_v1` | Operation Bundle manifest and intent JSON Schema |
| `attack/attack_meta.json` | every attack case with its expected outcome, written before implementation |
| `testdata/` | RFC 8785, RFC 8032 and Wycheproof vectors with `SHA256SUMS` |
| `deploy/docker-compose.yml` | local PostgreSQL 18.6 (executor and verifier to follow) |
| `docs/` | specification documents (stubs until G4); `docs/adr/` points to the ADRs |
| `CLAUDE.md` | builder rules |

## Renaming the module

The module path `github.com/testamur/testamur` is provisional. If the name gate picks another
brand (for Deltamark the plan names `github.com/deltamark-dev/deltamark`, because the GitHub user
`deltamark` is taken):

```sh
go mod edit -module github.com/<org>/<name>
grep -rl 'github.com/testamur/testamur' --include='*.go' . \
  | xargs sed -i '' 's#github.com/testamur/testamur#github.com/<org>/<name>#g'   # GNU sed: -i without ''
go mod tidy && go vet ./... && go test ./...
```

Then replace `Testamur` / `testamur` in `README.md`, `CLAUDE.md` and `docs/`. Nothing else carries
the brand: the binary, schema, roles, message prefix and type strings all use `txv`.

## License

Apache-2.0 (see `LICENSE`). The repository stays private until release gate G4.
