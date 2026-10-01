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
