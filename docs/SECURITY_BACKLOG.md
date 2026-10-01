# Security backlog

Low findings from the security reviews that are open, each with the session that owns it. Fixed
items leave this page; their record stays in the review that found them
([SECURITY_REVIEW_2026-10.md](SECURITY_REVIEW_2026-10.md)).

| Id | Finding | Owner | Next step |
|---|---|---|---|
| SR-005 | With `redirects: none`, a REST 3xx is worded as "The service failed (HTTP 302)". | S34 | Say the service redirected and Deedglass didn't follow; keep the unknown outcome for writes. |
| SR-006 | `httpx.Mount` doesn't check extension routes' auth against the contract table. | S34 | Make `Mount` require a reason for every public extension route, like the contract table. |
| SR-007 | GO-2026-5024 in `golang.org/x/sys` v0.41.0 (Windows only, not reachable). | S35 | Raise `golang.org/x/sys` to v0.44.0 or later at the dependency review, with its ADR. |
| SR-008 | `identity.KeyAuth` without a database dereferences a nil pre-auth store. | S34 | Answer `service_unavailable` instead. |
