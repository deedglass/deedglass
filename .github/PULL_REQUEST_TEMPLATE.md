<!-- Thank you for the pull request. First one? CONTRIBUTING.md has a five-minute path, and a
     maintainer will help with anything the checklist below trips on: ask in the PR. -->

## What changed and why

<!-- One topic. Link the issue or ADR ("Closes #14"). -->

## How you tested it

<!-- Commands you ran (go test ./internal/scan/, make ci, make integration, …) and what you checked by hand. -->

## Checklist

- [ ] `make ci` is green (or, for a data-file or docs change, the package's own `go test`)
- [ ] Tests cover the change (unit, golden, fuzz or integration, as it needs)
- [ ] Docs updated, including what it doesn't do
- [ ] A new fact about Deedglass or anyone else links its source
- [ ] A new dependency has an ADR in this pull request
- [ ] Every commit is signed off (`git commit -s`)
