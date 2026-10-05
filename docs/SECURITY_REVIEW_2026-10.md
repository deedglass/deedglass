# Security review, October 2026

Each pass records what was reviewed, how, what was found, how bad it is and what was done, with the
file and the test that show it. **S18 wrote Pass 1; S34 appends Pass 2.** Low findings that stay
open move to [SECURITY_BACKLOG.md](SECURITY_BACKLOG.md) with an owner session.

Severity: **High** means an attacker can run, read or change something they shouldn't, or a
credential can leave Deedglass. **Medium** means a control is weaker than documented, or a whole
class of mistake is possible without a test catching it. **Low** means the record or a message is
less exact than it should be, or a guard covers less than it could; nothing runs or leaks.

## Pass 1 · 2026-10-06 (run 2026-10-02, S18)

### Scope

- The paths an agent can reach today: `POST /v1/actions`, `/mcp/native`, `/mcp/{connector}`, the
  Gate, the policy evaluator, the Postgres connector, the REST connector, the upstream MCP pool,
  the vault and the egress door.
- Every route in the route table, for authentication (no credential, the wrong kind) and for org
  isolation (another org's ids).
- The Go code base, for five classes of mistake (static guards G1 to G5) and known vulnerabilities
  (`govulncheck`).

### Method

1. **The attack suite** (`make attack`, [ATTACK_SUITE.md](ATTACK_SUITE.md)): 19 cases declared in
   `attack/attack_meta.json` and pinned (ADR-040) before their code existed, run against a real
   `deedglass serve` process, a real PostgreSQL 18.6 and loopback upstreams. After every case:
   both orgs' deed chains intact, the target unchanged where nothing should have run, every action
   sealed, a planted credential (`dgSENTINEL…`) found 0 times across the server's logs, every HTTP
   response, the recorded actions, events, deeds and admin events, connector configs, credential
   hints and the results file, and 0 hits on forbidden listeners. `TestInvariantsCatch` shows the
   invariants can fail (a planted secret, a changed target).
2. **Route auth coverage** (`internal/httpx/routes_auth_test.go` `TestRouteAuthCoverage`,
   `cmd/deedglass/routes_auth_test.go` `TestServeRoutesUnauthenticated`): every route declares its
   auth kind; the public set is an exact, reasoned list; every other route answers 401
   `authentication_error` with `WWW-Authenticate` without a credential.
3. **Org isolation** (`attack/isolation_test.go` `TestOrgIsolation`): Globex calls every
   id-bearing route with Acme's ids, using each credential kind the route accepts.
4. **Static guards** (`internal/lint`, `TestRepoClean` plus a positive and a near-miss test per
   guard): G1 SQL built from strings reaching pgx, G2 the single egress door, G3 secret-named
   `slog` keys, G4 `InsecureSkipVerify`, G5 `math/rand`.
5. **`make vuln`**: `govulncheck` v1.8.0 (the current release, re-checked on 2026-10-02; ADR-024).

### Results

| Check | Result |
|---|---|
| Attack suite | 15 matched · 0 differ · 4 waiting on S21 · chains intact · 0 secrets seen |
| Route auth, contract table | 118 routes: 17 public (each with a reason), 101 swept, all 401 without a credential |
| Route auth, mounted by `serve` | 80 routes: 13 public, 67 swept; 14 session-only routes refuse agent and admin keys, 8 agent-key routes refuse a session |
| Org isolation | 33 id-bearing routes × their credential kinds: 52 answers 404 `not_found`, 10 refused by validation before the lookup (the same answer as for an id that exists nowhere); 0 Acme ids in any answer, list or export; Acme's rows in 10 tables unchanged |
| Static guards | G1 to G5 clean after the fixes below; 4 files carry `//deedglass:allow-sql-build` with a reason, 1 carries `//deedglass:allow-math-rand` |
| `govulncheck` | No vulnerabilities in code Deedglass calls. 1 in a required module that isn't reachable (SR-007) |

### Findings

| Id | Area | Finding | Severity | Status | Evidence |
|---|---|---|---|---|---|
| SR-001 | MCP upstreams | A remote MCP server's credential came only from the server's environment (`mcp.upstreams.*.headers_from_env`): not encrypted at rest by the vault, the same for every org on the instance, no rotation, no admin deed when it changed. | Medium | **Fixed.** A new connector type, `mcp_http`, keeps the credential in the vault per org (opened only when a session is made, redacted from every output); prod refuses `headers_from_env` at config load. | `internal/mcp/upstream/connector.go`; `TestRowCredentialFromVault`, `TestRowValidate`; `internal/config/config.go` (prod refusal), `TestMCPSettingsProblems`; attack K04 runs through an `mcp_http` row with the credential in the vault |
| SR-002 | REST connector, egress | A read tool's redirect to another host was refused (nothing was sent there) but recorded as `upstream_error` with an unknown outcome ("may have run"), and a redirect to cloud metadata wasn't named as a blocked destination. The record was less exact than it should be. | Low | **Fixed.** The same-host redirect check names a blocked target as `destination_blocked`; a refused redirect on a read tool is `failed` with `destination_blocked` (writes never follow redirects). | `internal/connector/egress/client.go`, `internal/connector/rest/execute.go`; `TestRedirects`, `TestRedirectRefusals`; attack K02 |
| SR-003 | MCP upstreams | An upstream tool whose name isn't in the MCP charset (a Cyrillic look-alike, a trailing space) was dropped silently; an admin had no record of the attempt. | Low | **Fixed.** Each listing logs `Hid tool "…" from <upstream>: the name isn't plain ASCII (letters, digits, _ . -).` | `internal/mcp/upstream/source.go` `HiddenToolMessage`; attack K04 asserts the log lines |
| SR-004 | Randomness | `math/rand/v2` drove the REST retry jitter and the Gate's sweep jitter (G5). Not a security value, but the rule is crypto/rand only. | Low | **Fixed.** Both use `crypto/rand`. The seeded demo-data generator (`internal/ledger/fake`) keeps PCG under a reasoned `//deedglass:allow-math-rand` marker: it must be reproducible and never makes a secret, an id or a token. | `internal/gate/worker.go`, `internal/connector/rest/execute.go`; `internal/lint` `TestG5`, `TestRepoClean` |
| SR-005 | REST connector | With `redirects: none`, a 3xx answer is recorded as "The service failed (HTTP 302). The call may have run." The outcome (unknown) is right for writes; the wording isn't. | Low | Backlog (S34) | `internal/connector/errors.go` `FromStatus` |
| SR-006 | Route table | `httpx.Mount` doesn't check an extension route's auth against the contract table, so a future extension could add a public route the httpx test never sees. Today the cmd sweep's list of public extensions (one route, `GET /auth/assets/{file}`) catches it. | Low | Backlog (S34) | `internal/httpx/routes.go:189-192`; `cmd/deedglass/routes_auth_test.go` |
| SR-007 | Dependencies | `govulncheck`: GO-2026-5024 in `golang.org/x/sys` v0.41.0 (`NewNTUnicodeString`, Windows only). Deedglass doesn't call it and doesn't ship for Windows. | Low | Backlog (S35): raise `golang.org/x/sys` to v0.44.0 or later at the next dependency review, with its ADR. | `make vuln` |
| SR-008 | Identity | `identity.KeyAuth` with no database dereferences a nil pre-auth store on a well-formed key. `serve` always has a database, so only route tests can reach it; it should fail closed with `service_unavailable`. | Low | Backlog (S34) | `internal/identity/keyauth.go:192` |

**Open high or medium findings: 0.**

### Also changed in this pass

- `/mcp/native` and `GET /v1/tools` now list the caller's org's own native connectors (`postgres.*`,
  REST connectors) next to the built-ins, so H01's MCP path reaches the Postgres connector through
  the Gate (`internal/connector/registry.go` `NativeRows`, `TestNativeSourceRows`).
- The attack harness's DNS answers come from a file only in a `-tags attackhooks` build, refused
  outside dev; `TestNoAttackHooks` checks that a default build has no hook.

### Deferred to S34 (Pass 2)

The full STRIDE model, the remaining V0 cases, MCP tool-description poisoning, approval-link
replay, forged Slack interactions, YAML and CEL bombs, 30-minute fuzz runs, guards G6 to G10 and
`SECURITY.md`.

## Pass 2 · 2026-10-11 (run 2026-10-05, S34)

### Scope

Every feature built through S33, attacked as the threat model ([THREAT_MODEL.md](THREAT_MODEL.md)
v2) describes them: the Gate and policies, the MCP wrapper and gateway, connectors, the vault and
the egress door, the Witness and the decoder, deeds and keys, people and sessions, approvals in
email and Slack, the Console and its event stream, Insights and its optional AI wording, the CLI,
the SDKs and `deedglass init`. Pass 1's open lows owned by S34 (SR-005, SR-006, SR-008).

### Method

1. **The attack suite**, extended by 23 cases in groups P (people, sessions and the stream), Q
   (hostile MCP servers and upstreams) and R (resources, records and output), declared in
   `attack/attack_meta.json` and pinned under `expectedDigestS34` (ADR-065) at
   2026-10-04T23:25:34Z, before any of their code existed. P01 and P09 of the session plan are
   S22's L01 and S23's U01, already pinned, so they are referenced, not declared again. Every V0
   case is accounted for: 19 run (the deed and verify suites, the decoder suite), 18 point to the
   V7 case that runs their attack (`superseded_by`), and 29 no longer apply to the V7 architecture
   and name the test that checks the V7 control instead (`retired`). Their expected outcomes are
   unchanged.
2. **The 20-row checklist below**, each row with its evidence (file:line) and test.
3. **Static guards G6 to G10** (`internal/lint`), each with a planted violation, its fix and its
   marker, run over the repo by `TestRepoClean`.
4. **Fuzzing**: `make fuzz-all`, every parser that reads untrusted bytes for 5 to 30 minutes
   (results below); `make fuzz-regress` replays every corpus in CI.
5. **Supply chain**: `make audit`.

Severity (unchanged; S34's wording for the same scale): **High** means an attacker in A1 to A7 can
cause an unauthorized write, a cross-org read, a forged or silently dropped deed, a secret's
disclosure or a false `VERIFIED`. **Medium** means the same with a realistic precondition (a
viewer role, a revoked key inside the propagation window), a control weaker than documented, or a
denial of service of the Gate under 100 requests a second. **Low** means defence in depth, an
information leak without secrets, or hardening.

### Results

| Check | Result |
|---|---|
| Attack suite | 68 matched · 0 differ · 0 waiting · both chains intact · 0 secrets seen in 7,394 log lines (3 min 20 s on PostgreSQL 18.6) |
| New cases P, Q, R | 23 of 23 matched; 5 differed on their first run and were fixed in the code or the case's observation, never in the expectation (below) |
| Static guards | G1 to G10 clean; 9 files marked `allow-exec`, 1 `allow-unsafe`, 1 `allow-text-template`, 27 comparisons of public digests marked `allow-compare` |
| Fuzzing | 21 targets, 6.5 hours, 300,617,223 inputs, 0 crashers |
| Supply chain | Nothing reachable in Go; 0 advisories in what ships from npm and PyPI; build-time docs packages recorded (SR-018) |

**First runs that differed.** Q04's 10,000-deep answer waited out the call's timeout (the SDK
dropped it), and R07 found `actions list` printing a bidi override raw: both fixed in the code
(SR-012, SR-014). P04 looked for a log line the self rule never writes (the
refusal is the person's ephemeral Slack reply), R02's run time stop came from the evaluation
deadline rather than the cost counter, R03's export was a tar.gz bundle where the case expected
JSON lines, and R07 also counted a bidi character inside JSON answers, where it is data, as a raw control
byte: each case's
observation was corrected to what the control really does, with the expectation unchanged.

### The checklist

| # | Control | Evidence | Test | Result |
|---|---|---|---|---|
| 1 | Route auth: every route declares it; the public set is exact; `/v1/slack/interact` is signed | `internal/httpx/contract_routes.go`; `internal/httpx/routes.go:197` (`Mount`, now with `PublicReason` for public extensions) | `TestRouteAuthCoverage`, `TestServeRoutesUnauthenticated`, `TestPublicExtensionNeedsReason` | Pass; SR-006 fixed |
| 2 | Authorization matrix: viewers never mutate outside a reasoned list; admins never reach owner routes; agent keys scoped per route | `internal/httpx/authz_s34_test.go` (`viewerMutations`) | `TestAuthzMatrix`, `TestAuthzMatrixServe`, `TestRoleMatrix`, `TestKeyRoutesDeclareScopes`, P06 | Pass |
| 3 | Org isolation, every id-bearing route including the stream, exports, replays, insights, suggestions, webhooks, approvals and undo | `attack/isolation_test.go` | `TestOrgIsolation`, P08 | Pass |
| 4 | RLS: on every org table; only the five global tables without it; the runtime role isn't superuser, has no BYPASSRLS and owns nothing (RLS is enabled, not forced, on purpose: ADR-066) | `internal/store/store_integration_test.go:88` | `TestSchemaShape`, `TestRLSMatrix`, `TestRuntimeRoleCannotBypassRLS` | Pass |
| 5 | Cookies: `__Host-`, Secure, Path=/, no Domain; session HttpOnly and SameSite=Lax (links from email and Slack open the Console signed in; CSRF is closed by the token and Sec-Fetch-Site), CSRF cookie SameSite=Strict; double-submit compared in constant time | `internal/identity/session.go:91`, `:203` | `TestCookieFlags`, `TestProdCookies`, P07 | Pass |
| 6 | CORS: no `Access-Control-*` on any `/v1` answer; `/mcp/*` refuses a foreign Origin | `internal/httpx/server.go:543` (no CORS headers); `identity.MCPAuth` | `TestCORSDenied`, the S15 Origin tests | Pass |
| 7 | Security headers on the API and the Console: CSP with `frame-ancestors 'none'`, nosniff, no-referrer, COOP, Permissions-Policy; HSTS in prod over https only | `internal/httpx/server.go:546`; `internal/console/console.go:226` | `TestSecurityHeadersRoutes` (6 routes, dev and prod), `TestSecurityHeaders`, `TestPages` | Pass; SR-015 fixed |
| 8 | Server limits: ReadHeaderTimeout 5 s, ReadTimeout 60 s, WriteTimeout per route, IdleTimeout 120 s, MaxHeaderBytes 64 KiB; 1 MiB bodies; args depth 32; route timeouts; stream limits | `cmd/deedglass/serve.go:425` | `TestLimits` (httpx), G10, R06, `TestEventsPerUserLimit` | Pass |
| 9 | SSRF: every outbound path uses the egress door | `internal/connector/egress`; `internal/archtest/egress_test.go:69` | `TestSingleEgressDoor`, G2, K01, K02, Q08, L07 | Pass |
| 10 | SQL: no string-built SQL; one statement per connector call; READ ONLY query transactions; undo parameterized | `internal/lint/guards.go` (G1); `internal/connector/postgres` | `TestRepoClean`, `TestAdmission`, `TestStatementsUseParameters`, H01–H07 | Pass |
| 11 | Secrets: never logged, never returned, redacted, write-only in the Console; `.env` and `.deedglass/` ignored | `internal/vault/redact.go`; G3 | the sentinel sweep in every attack case (0 hits), K03, Q07, `TestDescriptionRefusesCredentials` | Pass; SR-013 fixed |
| 12 | Crypto: stdlib Ed25519, small-order refusal, seal key files refused above 0600, `crypto/rand` only, constant-time compares for key hashes, CSRF, approval tokens, webhook and Slack signatures | `internal/deed/keyfile.go:163`; `internal/identity/token.go:112`; `internal/connector/slack/verify.go:83`; `pkg/deedglass/webhook/webhook.go:109` | A13, `TestKeyFiles`, G5, G8 | Pass |
| 13 | Tokens: approval tokens and magic links hashed at rest, single use, bound to person and approval, expiring; logout ends the session server-side | `internal/approve/links.go:46` | L01, L02, P02, `FuzzApprovalToken`, `TestLinks` | Pass |
| 14 | MCP: description scanning, catalog pinning, qualified names, size and depth limits, fail closed, server-to-client request policy | `internal/mcp/toolscan`; `internal/mcp/upstream/pool.go` (`screen`, `capped`); `internal/mcp/wrap/wrap.go` (`serverRequestKind`) | Q01–Q06, `TestScreenHidesAndPins`, `TestFlaggedDescription`, `TestSamplingRefused`, `TestDeepStreamFailsFast`, `TestHangIsTimeout` | Pass after SR-009 to SR-012 |
| 15 | LLM channel: an allow-list of keys, output treated as untrusted (length, numbers, placeholders, words), never used to build the diff, a person accepts | `internal/learn/phrase_validate.go:48`; `internal/learn/privacy.go:29` | R05, `TestPrivacySentinels`, `TestNeverEnforceMethodSet` | Pass |
| 16 | Denial of service: policy and CEL limits, request limits, login and link rate limits, slot lag alert, replay concurrency | `internal/policy/limits.go` | R01, R02, R06, `TestCostLimitFailsClosed`, `TestHugeArgsNeverAllow` | Pass |
| 17 | Output encoding: CLI escaping, slog escaping, Console text nodes, html/template email | `cmd/deedglass/verify.go:722` (`clean`), `cmd/deedglass/ledgercmd.go` (`clip`) | R07, `TestClipCleans`, G9, the web sink grep | Pass; SR-014 fixed; SR-016 open (Low) |
| 18 | Supply chain: `make vuln`, npm audit ×3, pip-audit, Actions pinned by SHA, base images by digest, `go.sum` committed, no `replace` | `.github/workflows/ci.yml` (the pin check), `Dockerfile:11`, `Dockerfile:27` | `make audit` (below) | Pass for what ships; SR-007 and SR-018 open (Low) |
| 19 | Container: distroless nonroot, read-only root, `cap_drop: ALL`, `no-new-privileges`, no shell | `Dockerfile:27`; `deploy/docker-compose.yml:24` | the CI `docker-smoke` job | Pass |
| 20 | CLI, SDKs and `init`: file modes, no secret echo, `--keys` https only, offline verify trusts only given keys, adapters fail closed | `internal/setup`; `cmd/deedglass/verify_keys.go` | `TestVerifyGolden`, `TestInitLive`, the SDK suites | Pass |

### Findings

| Id | Area | Finding | Severity | Status | Evidence |
|---|---|---|---|---|---|
| SR-005 | REST connector | A 3xx under `redirects: none` was worded as "The service failed". | Low | **Fixed.** "The service redirected the call (HTTP 302), and Deedglass doesn't follow redirects for this connector. The call may have run." | `internal/connector/errors.go` `FromStatus`; `TestFromStatusRedirect` |
| SR-006 | Route table | A public extension route needed no reason. | Low | **Fixed.** `Mount` refuses a public extension route without `PublicReason`; the four existing ones carry theirs. | `internal/httpx/routes.go`; `TestPublicExtensionNeedsReason` |
| SR-008 | Identity | `KeyAuth` without a database dereferenced nil on a well-formed key. | Low | **Fixed.** It answers `service_unavailable`. | `internal/identity/keyauth.go`; `TestKeyAuthWithoutDatabase` |
| SR-009 | MCP | Tool descriptions carrying instructions for the model (plain phrases, Unicode tag characters, `<IMPORTANT>` blocks) reached MCP clients through the gateway and the wrapper. | Medium | **Fixed.** `internal/mcp/toolscan` flags them; the gateway and the wrapper hide them, refuse calls as an unknown tool and log the admin's sentence; the gateway seals `connector.tool_hidden`. A heuristic (LIMITATIONS). | Q01; `TestScan`, `TestScreenHidesAndPins`, `TestFlaggedDescription` |
| SR-010 | MCP gateway | The HTTP gateway didn't pin tool definitions, though `docs/MCP.md` said it followed the wrapper's lists: a tool could change its description or schema after it was listed. | Medium | **Fixed.** The first definition's digest is pinned per connector; a changed tool is hidden and refused until Deedglass restarts, and sealed as `connector.tool_hidden` (`definition_changed`). | Q02; `TestScreenHidesAndPins` |
| SR-011 | MCP wrapper | `mcp wrap` relayed `sampling/createMessage` and `elicitation/create` from the server to the client ungoverned (documented as a v1 limit). | Low | **Fixed.** Refused with a JSON-RPC error and logged unless `--allow-server-requests` names them; the gateway logs the requests its client refuses. | Q06; `TestSamplingRefused`, `TestSampling` |
| SR-012 | MCP gateway | An upstream answer nested 10,000 deep was dropped by the SDK's decoder, so the call waited out its whole timeout; a hanging upstream was recorded as a lost answer, not a timeout. | Low | **Fixed.** Nesting is checked while reading (512) and after decoding (128): `upstream_error`, unknown, at once. A transport timeout is `timeout`. | Q04, Q05; `TestDeepStreamFailsFast`, `TestHangIsTimeout` |
| SR-013 | Connectors | A connector description holding a credential (a live Stripe key) was accepted and stored, shown to every member and sealed in the admin deed's data. | Low | **Fixed.** A description the vault's scrubber recognizes as a credential is 400 `validation_failed`, and the message never echoes it. | Q07; `TestDescriptionRefusesCredentials` |
| SR-014 | CLI | `actions list` and `actions show` printed names an agent supplied without escaping; a recorded unknown tool name with a bidi override reordered the terminal line. | Low | **Fixed.** The Ledger tables and headers pass names through `clean()`. | R07; `TestClipCleans` |
| SR-015 | HTTP | API answers lacked `Cross-Origin-Opener-Policy` and `Permissions-Policy` (the Console's pages had them). | Low | **Fixed.** Every answer carries both; one constant (`httpx.PermissionsPolicy`) for both. | `TestSecurityHeadersRoutes` |
| SR-016 | Console | Bidirectional characters in an agent-supplied name can reorder text in the Console's tables. | Low | Backlog (S35) | R07 evidence (`events` and API carry the character as data, as JSON allows) |
| SR-017 | Egress | The metadata refusal told every caller to "point base_url at the service's public address", including webhook admins. | Low | **Fixed.** "Use the service's public address." | Q08 evidence; `internal/connector/egress/policy.go` |
| SR-018 | docs-site | 6 high npm advisories in build-time packages (`braces`, `micromatch` under `starlight-llms-txt`; `form-data` under `httpsnippet` under `starlight-openapi`). The site ships static files; the packages run on our own sources at build time. | Low | Backlog (S35): move build packages to `devDependencies`, take upstream fixes. | `make audit` |

**Open high or medium findings: 0.** Lows open with an owner: SR-007, SR-016, SR-018
([SECURITY_BACKLOG.md](SECURITY_BACKLOG.md)).

### Fuzzing

`make fuzz-all` (`scripts/fuzz_all.sh`), 2026-10-05, Apple M1, Go 1.27.1, 2 workers per target and
3 targets at a time (the laptop also ran the session's builds), the times S34 set: 6.5 hours of
fuzzing over 21 targets, 300,617,223 inputs, **0 crashers**. Three targets are new in S34:
`FuzzSlackVerify` over the whole `/v1/slack/interact` path (nothing is decided without a valid,
fresh signature), `FuzzWebhookSignature` (a signature verifies only its own body and secret) and
`FuzzApprovalToken` (the link token's shape check and hash). `make fuzz-regress` (CI job
`fuzz-regress`) runs every target for 10 seconds, replaying any committed corpus under
`testdata/fuzz/`. Raw results: `bench/results/2026-10-05-darwin-arm64-fuzz.json`.

| Target | Package | Time | Inputs | New interesting | Crashers |
|---|---|---|---|---|---|
| `FuzzReader` | `internal/mcp/jsonrpc` | 30 min | 59,528,906 | 299 | 0 |
| `FuzzFrontEnvelope` | `internal/mcp/front` | 30 min | 10,391,088 | 297 | 0 |
| `FuzzCompile` | `internal/policy` | 30 min | 2,785,214 | 441 | 0 |
| `FuzzYAMLReader` | `internal/policy` | 30 min | 4,707,692 | 173 | 0 |
| `FuzzParseEnvelope` | `internal/deed` | 30 min | 34,687,715 | 121 | 0 |
| `FuzzVerifyEnvelope` | `internal/deed` | 30 min | 30,687,640 | 110 | 0 |
| `FuzzIntentHeader` | `internal/identity` | 30 min | 6,376,807 | 210 | 0 |
| `FuzzTokenize` | `internal/sqlguard` | 30 min | 44,333,553 | 176 | 0 |
| `FuzzAnalyze` | `internal/sqlguard` | 30 min | 42,848,683 | 114 | 0 |
| `FuzzSafeParse` | `internal/decoder` | 15 min | 6,671,464 | 12 | 0 |
| `FuzzArchiveOpen` | `internal/deed/archive` | 10 min | 3,372,068 | 132 | 0 |
| `FuzzVerifyStream` | `internal/deed` | 10 min | 4,831 | 56 | 0 |
| `FuzzCheckURL` | `internal/connector/egress` | 10 min | 6,709,539 | 306 | 0 |
| `FuzzRedactor` | `internal/vault` | 10 min | 3,448,535 | 97 | 0 |
| `FuzzTemplatePath` | `internal/connector/rest` | 10 min | 3,399,984 | 8 | 0 |
| `FuzzSlackVerify` | `internal/notify/slack` | 10 min | 2,589,714 | 147 | 0 |
| `FuzzWebhookSignature` | `pkg/deedglass/webhook` | 10 min | 30,742,996 | 5 | 0 |
| `FuzzApprovalToken` | `internal/approve` | 10 min | 3,418,540 | 16 | 0 |
| `FuzzParseAPIKey` | `internal/identity` | 5 min | 1,210,496 | 0 | 0 |
| `FuzzNormalize` | `internal/gate` | 5 min | 1,117,143 | 287 | 0 |
| `FuzzHeaderDecode` | `internal/mcp/httpgw` | 5 min | 1,584,615 | 8 | 0 |

`FuzzVerifyStream` runs a whole stream verification per input, so it covers fewer inputs; its
seeds hold the chain's failure cases. **Python:** Hypothesis 6.168.3, 50,000 examples each on the
strict parser (bytes and text: it only ever raises `VerifyError`) and on JCS (canonical output
parses back to the same value and canonicalizes to itself): 3 properties passed (6 minutes).

### Supply chain (`make audit`, 2026-10-05)

| Check | Result |
|---|---|
| `govulncheck` v1.8.0 | No vulnerabilities in code Deedglass calls. One in a required module, not reachable: GO-2026-5024 in `golang.org/x/sys` v0.41.0 (SR-007, S35) |
| `npm audit --omit=dev`, `web/` | 0 (the Console ships no npm runtime code) |
| `npm audit --omit=dev`, `sdk/typescript` | 0 (`@deedglass/sdk` has no runtime dependency) |
| `npm audit --omit=dev`, `docs-site/` | 6 high in build-time packages (SR-018, S35); the site ships static files |
| `pip-audit` 2.10.1, the Python SDK's runtime closure (`httpx` and 6 packages it pulls in) | 0 |
| `pip-audit` 2.10.1, the SDK's dev venv (91 packages, not shipped) | 0 |
| GitHub Actions | Every `uses:` pinned by a 40-character SHA (the CI pin check) |
| Base images | `golang:1.27.1-trixie@sha256:…` and `distroless/static-debian13:nonroot@sha256:…` |
| `go.mod` | No `replace` directives; `go.sum` committed and verified in CI |

### Deferred

- S35: the SBOM, container image scanning, signing, SR-007, SR-016, SR-018.
- S36: `security.txt` on deedglass.com (the docs site serves it today) and the site's security page.
- S37: GitHub private vulnerability reporting, secret scanning and Dependabot when the repository
  goes public.
- Post-launch: an external penetration test, a bug bounty, KMS- or HSM-backed seal keys.
