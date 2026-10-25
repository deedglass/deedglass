# Contributing to Deedglass

Thank you for looking. Deedglass is maintained by one person today, so a small, well-tested change
with a clear reason is the fastest to land.

## Ways to help

- **Try it and tell us what happened.** The **I ran it** issue form is the most useful thing you can
  send, good or bad.
- **Pick up a [good first issue](https://github.com/deedglass/deedglass/labels/good%20first%20issue)**
  or a [help wanted](https://github.com/deedglass/deedglass/labels/help%20wanted) one. Comment
  "I'll take this" so nobody duplicates the work; if you go quiet for two weeks, someone else may pick it up.
- **Fix a page.** Typos, unclear steps and missing caveats all count. The **Docs problem** form is
  there if you'd rather report it.
- **Suggest a first issue.** Spotted a small, self-contained change? File it with the **Suggest a
  good first issue** form: the context, where to look, what done means and the test command.
- **Answer a question** in [Discussions](https://github.com/deedglass/deedglass/discussions).

## Your first pull request in five minutes

Most [good first issues](https://github.com/deedglass/deedglass/labels/good%20first%20issue) need
only Go 1.27: no Docker, no Node, no database.

```sh
git clone https://github.com/deedglass/deedglass && cd deedglass
go test ./internal/scan/ ./examples/policies/   # about 10 s; the packages most first issues touch
# make the change the issue describes, then run the package's tests again
git commit -s -m "scan: add the MongoDB MCP server to the catalog"
gh pr create --fill                                # or push to your fork and open the PR on GitHub
```

Three kinds of change are data files, not code, and are the quickest to land:

- **A known MCP server or SDK for `deedglass scan`:** one entry in
  [`internal/scan/catalog.yaml`](internal/scan/catalog.yaml) with its tools, what each can do
  (read, write, destructive, money), a primary source URL and the date you checked it. Run
  `go test ./internal/scan/`.
- **A case for a starter policy:** add it to the policy's `.cases.yaml` in
  [`examples/policies/`](examples/policies/), then `go test ./examples/policies/` and
  `make policy-test`.
- **A docs fix:** pages live in [`docs/`](docs/). Say what a feature doesn't do as plainly as what it does.

Where things live, for orientation:

| Path | What |
|---|---|
| `cmd/deedglass/` | the CLI, one file per command |
| `internal/scan/` | `deedglass scan` and its catalog of MCP servers |
| `internal/policy/`, `examples/policies/` | the policy language and the starter policies |
| `internal/gate/`, `internal/witness/` | running a call, and checking what it changed |
| `internal/connector/` | Postgres, FHIR, REST, GitHub, Slack, Stripe and the others |
| `docs/`, `docs-site/` | the docs pages, and the site that publishes them |
| `attack/` | the attack suite (expected outcomes never change to make a test pass) |

If a test prints a `-update` command, the change moved a golden file: run it, read the diff, and
commit the golden with your change. Stuck? Ask in the issue; that is what it is for.

## Before you start

- **Bugs and ideas:** open an issue with the bug or feature form. For "I tried it and here is what
  happened", use the **I ran it** form: it is the most useful thing you can send.
- **Security issues:** never in a public issue. Follow [`SECURITY.md`](SECURITY.md).
- **Bigger changes** (a new connector, a change to the deed format, the API or the CLI): open an issue
  first, so we can agree on the shape before you write code. Decisions are recorded as ADRs in
  [`docs/adr/`](docs/adr/).

## Set up

You need Go 1.27, Docker, and Node 24 for the Console and the docs site.

```sh
make dev            # Postgres 18.6 + migrate + serve on 127.0.0.1:8080
make build          # bin/deedglass
make ci             # fmt, tidy, vet, race tests, test vectors, banned words, no-cgo build
make integration    # the integration tests on a throwaway Postgres 18
make help           # every target
```

`make ci` must be green before you open a pull request. CI runs the same checks.

## What a change needs

- **Tests.** Table-driven unit tests; golden files for crypto, JCS and CLI output; fuzz tests for
  parsers; an integration test against throwaway Postgres for anything that touches the database.
  Never change an attack case's expected outcome in `attack/attack_meta.json` to make a test pass.
- **Docs.** A user-visible change updates its page in `docs/` and says what it doesn't do.
- **Dependencies.** Prefer the standard library. A new dependency needs an ADR (name, pinned version,
  licence, why, alternatives) in the same pull request. No cgo in the main binary.
- **Words.** Errors and messages are calm and exact and end with a next step. CI rejects the words in
  [`scripts/banned_words.txt`](scripts/banned_words.txt); a deed is always called a deed.
- **Claims.** A sentence that states a fact about Deedglass or anyone else needs a source. If your
  change adds one to the docs, link the source in the pull request.

## Sign your commits (DCO)

Every commit carries a `Signed-off-by` line, which says you wrote the change or have the right to
submit it under the project's licence ([Developer Certificate of Origin 1.1](https://developercertificate.org/)):

```sh
git commit -s -m "Explain the change in one line"
```

## Pull requests

One topic per pull request. Describe what changed, why, and how you tested it; the template asks.
Expect a written review, usually within two business days.
