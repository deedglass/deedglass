# attack/

The attack suite: what Deedglass does with the calls a manipulated model, a stolen key, a hostile
upstream or a curious org neighbour makes. The results page is [docs/ATTACK_SUITE.md](../docs/ATTACK_SUITE.md),
generated from this directory.

```sh
make attack                                          # a throwaway PostgreSQL 18 cluster, every V7 case, then the report
make attack-report                                   # print the last run and regenerate docs/ATTACK_SUITE.md
make attack-one CASE=Q01                             # one case (S34)
DEEDGLASS_IT_ATTACK_RUN='TestAttack/H03' make attack    # one case (any go test -run pattern)
DEEDGLASS_IT_ATTACK_RUN='TestOrgIsolation' make attack  # the org isolation sweep
DEEDGLASS_IT_PG=17 make attack                       # another major version
```

`make attack` refuses to run outside a throwaway cluster (`DEEDGLASS_IT_THROWAWAY=1` and
`DEEDGLASS_IT_ADMIN_URL`, which `scripts/attack_local.sh` sets for the cluster it starts on
127.0.0.1:5544x). It never touches your dev database or a running server.

## Layout

| Path | What |
|---|---|
| `attack_meta.json` | Every case's expected outcome, declared before its code. `meta_test.go` pins a digest per group set. |
| `harness/` | Boot (a real `deedglass serve` process, built with `-tags attackhooks`), clients, upstreams, the per-case target clone, observers and invariants. |
| `cases_gate_test.go` | H01 to H07: SQL through the Gate and the Postgres connector. |
| `cases_identity_test.go` | J01 to J04: suspension, revocation, intent replay, the budget burst. |
| `cases_connectors_test.go` | K01 to K04: SSRF, redirects, credential echo, tool-name confusion. |
| `cases_witness_test.go` | H08 to H11 and W01 to W13: the L2 witness (S21). |
| `cases_approvals_test.go`, `cases_undo_test.go` | L01 to L08 (S22) and U01 to U05 (S23). |
| `cases_people_test.go` | P02 to P08 and P10 (S34): approval links, Slack callbacks, the self rule in Slack, double votes, viewers, CSRF, the event stream across orgs, Stop mid-burst. |
| `cases_upstreams_test.go` | Q01 to Q08 (S34): poisoned tool descriptions, the rug pull, shadowed names, huge and deep results, down and hanging servers, sampling through `mcp wrap`, live Stripe keys, webhook rebinding and redirects. |
| `cases_resources_test.go` | R01 to R07 (S34): YAML and CEL bombs, a forked chain, skewed intents, injection through the AI wording (in process, against an LLM stub), oversized JSON, control characters in every sink. |
| `agility_test.go` | Y01 to Y12 (TY-01, TY-02): seal algorithm versioning and the ML-DSA-65 dual seal, library and CLI level, plain `go test`. |
| `surfaces_test.go` | T01 to T11 (ADR-115): the A2A proxy against browser-shaped requests, DNS rebinding, missing keys, CORS and replays; the Debezium witness's cut and oversized feeds; the shell guard failing open; edited and unpinned timestamps. Plain `go test`. T12 and T13 are declared and wait on their code. |
| `isolation_test.go` | `TestOrgIsolation`: every id-bearing route, across two orgs. |
| `fixtures/` | The target database (`sql/target.sql`), the declared L2 fixtures, the policy (`policies/attack-baseline.yaml`). |
| `results/latest.json` | The last run. `cmd/attack-report` turns it into the page. |

## How a run works

1. `harness.Boot` builds the binary, creates a migrated control-plane database (S02's
   `storetest`), starts the loopback upstreams (an orders API, a redirector, the host it redirects
   to, a server that echoes credentials, an MCP server that renames its tools), and starts `serve`
   in dev with the private-network override and a hosts file the `attackhooks` build reads
   (`metadata.attack.test` → 169.254.169.254, `rebind.attack.test` → public, then metadata).
2. It creates two orgs with `deedglass org create --admin-key`: **acme** runs the cases, **globex**
   is the neighbour for the isolation sweep. Acme gets six agents, the `attack-baseline` policy,
   a REST connector, an `mcp_http` connector and a Postgres connector prepared with
   `deedglass connector postgres init --apply` on `attack_target_tpl`. Group L (S22) adds two
   admins to Acme on first use (invited by the owner's session, signed in with
   `deedglass org login-link`) and three agents the policy's approval rules name; approval links
   come from the dev mail log on the server's stderr (no mail provider is configured).
3. Every case gets its own clone, `CREATE DATABASE case_<id> TEMPLATE attack_target_tpl`, and the
   Postgres connector's credential is pointed at it.
4. After every case the invariants run: both deed chains intact; the target's fingerprint and
   `deedglass.ledger` unchanged for DENIED, REJECTED, BLOCKED and REDACTED; every action sealed
   within 5 s; the planted credential (`dgSENTINEL…`) seen 0 times in the server's output, every
   HTTP response and the control-plane tables; 0 hits on the listeners the case marked forbidden.
   A broken invariant fails the case even if its headline matched.

## S34's harness helpers (`harness/s34.go`, `harness/sse.go`)

| Helper | What it does |
|---|---|
| `SlackSign`, `SlackForm`, `SlackClick`, `FakeSlack` | Slack's v0 signature (with any timestamp, for stale and replayed bodies), interaction bodies, and a loopback Web API the Slack connector is pointed at (`api_base_url`, dev only) |
| `ParseSSE`, `Client.Peek` with headers | Read an event stream for a fixed window with a chosen `Last-Event-ID` |
| `NewHostile("poison"/"rug"/"shadow"/"results")`, `RugPull`, `ClosedURL` | Hostile MCP servers: instruction-bearing descriptions, a tool that changes after listing, a second `read_file`, a 50 MiB result, a 10,000-deep result answered by hand, a tool that hangs, and a port nothing listens on |
| `hostileStdioMain` (`ATTACK_STDIO_MCP=1`) | The test binary as a stdio MCP server that sends sampling, elicitation and roots requests through `mcp wrap` |
| `NewLLMStub` | An httptest Messages API with attacker-chosen replies; records every request body |
| `Env.Probe`, `Env.ServerRSS` | Wall time and the server's RSS growth around a request |
| `Env.NewOrg`, `Env.Run`, `Env.Command`, `Env.CPExec`, `Client.CSRF` | Another org, the binary without panicking on exit codes, the binary over pipes, multi-statement superuser SQL, a session's CSRF token |

The pure helpers have smoke tests in `harness/sse_test.go`, which run in `make ci`.

## Add a case

1. **Declare it.** Add the case to `attack_meta.json` with `status: "not implemented"`, in a group
   your session owns (the group-letter table is in `docs/ATTACK_SUITE.md`). New groups get their
   own digest constant and `groupSets` row in `meta_test.go`.
2. **Pin it under an ADR.** Run `go test ./attack -run TestExpectedOutcomesPinned`, put the printed
   digest in the constant, and write the ADR (PROPOSED) with the declared table. No case code yet.
3. **Implement it.** Add a `harness.Case{ID, Run}` with `register(...)` in a `cases_*_test.go` file.
   `Run` returns what it observed (`Outcome`, a `Detail` head, the action ids, evidence, forbidden
   listeners); the runner compares it with the meta. A case has no expected value of its own.
   A case runs for at most 30 s; set `Cap` when it must wait longer (L05 waits out a real
   one-minute approval expiry).
4. Flip `status` to `"implemented"` (outside the digest) and run `make attack && make attack-report`.

If a case differs, fix the code or the fixture, never the expectation. A case that was declared
wrongly goes to the founder as an ADR.
