# Security backlog

Low findings from the security reviews that are open, each with the session that owns it. Fixed
items leave this page; their record stays in the review that found them
([SECURITY_REVIEW_2026-10.md](SECURITY_REVIEW_2026-10.md)). Open high or medium findings: none.

| Id | Finding | Owner | Next step |
|---|---|---|---|
| SR-019 | Three high npm advisories stay in the docs site's build-time tree: `braces` (every version; no fixed release exists) under `micromatch` under `starlight-llms-txt`. Nothing ships from it: `npm audit --omit=dev` is 0. | S37 | Recheck at the release's dependency review; drop the plugin if a fix doesn't appear. |

Fixed in Pass 2 (S34): SR-005, SR-006 and SR-008 from Pass 1, and SR-009 to SR-015 and SR-017.
Fixed in S35 (ADR-070): SR-007 (`golang.org/x/sys` v0.48.0), SR-016 (the Console shows bidi overrides and isolates in every string it renders as `\uXXXX`, as the CLI does; `web/test/dom.test.ts`), SR-018 (docs-site packages are build-only `devDependencies`, `form-data` overridden to 4.0.6; the `braces` remainder is SR-019).
