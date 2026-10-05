# Good first issues (drafts, S37)

Ten small, well-bounded issues for the first contributors. The founder files them after the repo
goes public (`docs/PUBLIC_REPO_SWITCH.md`), each with the labels shown. Every one has a test to write.

## 1. Cap open connections in `deedglass serve`
- **Labels:** `good first issue`, `area: server`
- **Context:** KI-004. With no cap, a client that opens a connection per request can exhaust the open-file limit.
- **Files:** `cmd/deedglass/serve.go` (`net.Listen`), a new listener wrapper with a semaphore (standard library only).
- **Acceptance:** a setting `DEEDGLASS_MAX_CONNECTIONS` (default 1,024) in `internal/config/keys.go`; a test opens cap + 10 connections and the extra ones wait instead of failing; `docs/CONFIG.md` regenerated.

## 2. Replace `acme.com` addresses in tests with `acme.test`
- **Labels:** `good first issue`, `area: tests`
- **Context:** a few tests and Console copy use `@acme.com`, a real domain. Reserved names (`.test`, `example.com`) are safer.
- **Files:** `git grep -n '@acme.com'`.
- **Acceptance:** no `@acme.com` left; goldens regenerated with their `-update` flags; `make ci` green.

## 3. `deedglass verify --summary-json`
- **Labels:** `good first issue`, `area: cli`
- **Context:** scripts parse the verify line today. A small JSON summary (deeds, chain, keys, first failure) is easier.
- **Files:** `cmd/deedglass/verify.go`, goldens in `cmd/deedglass/testdata/verify/`.
- **Acceptance:** new golden cases for an intact chain and a changed deed; `docs/VERIFYING_DEEDS.md` updated.

## 4. A `make demo` scene timing line
- **Labels:** `good first issue`, `area: demo`
- **Context:** `deedglass dev agent-script --json` records each scene's elapsed time; the text output doesn't show it.
- **Files:** `internal/agentscript/runner.go`, `examples/demo/testdata/launch.golden` (durations are masked there).
- **Acceptance:** a dim `· 1.2 s` at the end of each scene in `--step` mode only; unit test in `agentscript_test.go`.

## 5. Shell completion for `deedglass dev agent-script --only`
- **Labels:** `good first issue`, `area: cli`
- **Context:** completions come from the command table (`cmd/deedglass/completion.go`). Scene ids could be offered from the script file.
- **Files:** `cmd/deedglass/completion.go`, `cmd/deedglass/testdata/completion/`.
- **Acceptance:** static completion stays the rule (no runtime callbacks); document the limit if it can't be static.

## 6. The scan report on US Letter
- **Labels:** `good first issue`, `area: report`
- **Context:** `scripts/report/report.html` fits 190 × 250 mm, so it prints on Letter; `render.mjs` only writes A4.
- **Files:** `scripts/report/render.mjs`, `render.test.mjs`.
- **Acceptance:** `--letter` switches the page size; a test checks one page and the size.

## 7. `deedglass doctor` checks the open-file limit
- **Labels:** `good first issue`, `area: doctor`
- **Context:** KI-004. A low `ulimit -n` is easy to spot before a load test.
- **Files:** `internal/doctor/`, `docs/INIT_AND_DOCTOR.md`.
- **Acceptance:** a check that warns under 4,096 with the fix; a table test on the threshold.

## 8. Explain `UNVERIFIABLE` reasons in `deedglass actions show`
- **Labels:** `good first issue`, `area: cli`, `area: witness`
- **Context:** the reason codes link to docs, but the CLI prints only the code for some gaps.
- **Files:** `cmd/deedglass/witness.go`, `docs/witness/REASONS.md` (generated).
- **Acceptance:** every gap prints its catalog sentence; goldens updated.

## 9. A `postgres-safe` case for `TRUNCATE ... CASCADE`
- **Labels:** `good first issue`, `area: policies`
- **Context:** KI-006. The starter blocks TRUNCATE; a case that pins CASCADE helps keep it that way.
- **Files:** `examples/policies/postgres-safe.cases.yaml`, then `go test ./examples/policies -update`.
- **Acceptance:** the case passes; `make policy-test` green.

## 10. Link `KNOWN_ISSUES.md` from `deedglass version --check`
- **Labels:** `good first issue`, `area: cli`
- **Context:** when a newer release exists, the line could point to what it fixes.
- **Files:** `cmd/deedglass/versioncheck.go`, its golden table.
- **Acceptance:** one extra line naming the release notes URL; no new network call.
