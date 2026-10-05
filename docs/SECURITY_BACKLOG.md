# Security backlog

Low findings from the security reviews that are open, each with the session that owns it. Fixed
items leave this page; their record stays in the review that found them
([SECURITY_REVIEW_2026-10.md](SECURITY_REVIEW_2026-10.md)). Open high or medium findings: none.

| Id | Finding | Owner | Next step |
|---|---|---|---|
| SR-007 | GO-2026-5024 in `golang.org/x/sys` v0.41.0 (Windows only, not reachable). | S35 | Raise `golang.org/x/sys` to v0.44.0 or later at the dependency review, with its ADR. |
| SR-016 | Bidirectional characters in names an agent supplies (a recorded unknown tool name) can reorder text in the Console; the CLI already escapes them. | S35 | Isolate agent-supplied names in the Console (`unicode-bidi: isolate` on name cells, or the CLI's escaping), with a Playwright case. |
| SR-018 | `docs-site` build dependencies carry 6 high npm advisories (`braces` and `micromatch` under `starlight-llms-txt`; `form-data` under `httpsnippet` under `starlight-openapi`). They run only at build time, on our own files; npm's suggested fix is a downgrade. | S35 | Move the build packages to `devDependencies`, take upstream releases that drop the vulnerable versions, and re-run `make audit`. |

Fixed in Pass 2 (S34): SR-005, SR-006 and SR-008 from Pass 1, and SR-009 to SR-015 and SR-017.
