# Deedglass

Deedglass is the execution and accountability layer for AI agents. It authorizes every action, runs
it under control, checks the result against the system of record, and seals a deed.

**Status: private, pre-release.** Nothing in this repository is published yet.

![deedglass in a terminal](docs/images/cli-banner-dark.jpg)

## Quickstart

Docker running, then in a new directory:

```sh
deedglass init      # Postgres in Docker, serve, an org, an agent, postgres-safe, then three smoke actions
deedglass doctor    # sixteen read-only checks, each with a fix
deedglass scan      # what your own agent setup can change, graded A to F (local, read-only)
```

`deedglass init` ends with a blocked `DROP TABLE` and an `UPDATE` witnessed by Postgres's change log
(L2); a second run says `Nothing to change.` Then govern a LangChain agent in three lines with the
Python SDK (`pip install "deedglass[langchain]"`, `docs/SDK_PYTHON.md`) or a TypeScript one with
`@deedglass/sdk`. The Quickstart (`docs-site/src/content/docs/quickstart.mdx`, published at docs.deedglass.com/quickstart) walks through it.

## What works today

This is the foundation. Most of the product is still ahead, and `deedglass help` says exactly what
this build can and can't do.

| Works | Where |
|---|---|
| `deedglass help`, `help <command>`, `help --json` | `cmd/deedglass`: every contract command is listed; unbuilt ones say so |
| `deedglass version [--json]` | `internal/buildinfo`: version, commit, supported API versions |
| `deedglass config print [--json]` | `internal/config`: the effective config with every secret hidden |
| Prefixed ids (`agt_01j9…`) | `internal/ids`: strict, monotonic, sortable |
| Strict JSON and JCS, Ed25519 key checks | `internal/intent`, `internal/keys` (kept from V0) |
| The V0 Postgres transfer demo | `sql/001–004`, `bundles/transfer_v1`, checked by `make integration` |
| The V0 attack-case list, outcomes pinned | `attack/attack_meta.json` |

What it doesn't do yet: serve an API, run the Gate, seal deeds, verify deeds, or connect to any
tool. `docs/REPO_MAP.md` shows which session builds each part.

## Build and test

Go 1.27.1. PostgreSQL 16, 17 or 18 (Homebrew or Docker) for the integration run.

```sh
make build && bin/deedglass help    # the CLI
bin/deedglass config print          # dev generates local secrets in .deedglass/ (never used in prod)
make ci                             # gofmt, tidy, vet, race tests, test vectors, rename guard, banned words, no-cgo build
make integration                    # throwaway PostgreSQL 18: sql/001-004 and the smoke checks
make matrix                         # the same on 16, 17 and 18
make help                           # every target
```

## Docs

| Doc | What |
|---|---|
| `docs/ARCHITECTURE.md` | how the parts fit, the action lifecycle, what each part doesn't do |
| `docs/REPO_MAP.md` | every directory and package, its status and owner session |
| `docs/NAMING.md` | the product vocabulary and the rename from the V0 names |
| `docs-site/` | docs.deedglass.com: the Quickstart, concepts, guides, the generated reference (`make docs`) |
| `docs/INIT_AND_DOCTOR.md` | every `deedglass init` step and `deedglass doctor` check |
| `docs/SCAN.md` | `deedglass scan`: what it reads, the score, the read-only database report |
| `docs/SDK_PYTHON.md` | the Python SDK and its LangChain and Claude Agent SDK adapters |
| `docs/CLI.md` | the `deedglass` CLI: principles, flags, exit codes, JSON mode |
| `docs/CONFIG.md` | every setting, precedence, dev and prod |
| `docs/IDS.md` | id format, prefixes, ordering |
| `docs/LIMITATIONS.md` | what Deedglass doesn't do |
| `docs/adr/README.md` | architecture decision records |
| `docs/v0/` | V0 references kept for the L2 design |
| `CLAUDE.md` | builder rules |

## License

Apache-2.0 (see `LICENSE`). The repository stays private until the founder decides otherwise.
