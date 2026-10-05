# Contributing to Deedglass

Thank you for looking. Deedglass is maintained by one person today, so a small, well-tested change
with a clear reason is the fastest to land.

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
