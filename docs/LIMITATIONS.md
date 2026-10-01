# Limitations

What Deedglass does not do, stated plainly. **Owner: S37**, which finalizes this page before launch. Until then, each session adds the limits it decides. This list already holds the ones the contract fixes.

## Known limits in v1

- **L2 covers Postgres only.** Other connectors reach L0 (Reported) or L1 (Confirmed).
- **L2 needs database settings.** `wal_level=logical`, a replication slot, and `REPLICA IDENTITY FULL` on watched tables. Some managed services need a flag or a restart (`docs/CLOUD_COMPATIBILITY.md`).
- **L2 sees only the WAL.** Effects outside it (pg_net, foreign data wrappers, NOTIFY), unlogged and temp tables, sequences and other databases are not witnessed. DDL can be blocked by policy beforehand, but it is not witnessed or undone.
- **L1 is the target's own view.** "Stripe says the refund exists" is not "the refund was a good idea".
- **Seal keys are per deployment.** One set of seal keys signs every org's deeds. Per-org seal keys are v2.
- **No test or live mode for connectors.** `dg_test_` and `dg_live_` API keys are recorded on each action (`livemode`) but not enforced yet (S08 section below).
- **COSE is an encoding, not a transparency log.** The optional COSE_Sign1 output is a signed statement from your Deedglass instance. It is not an RFC 9942 transparency-log entry, and deeds are not anchored in any external log in v1.
- **Deeds hold digests, not payloads.** A deed shows that a request with a given digest was handled. You need the original request to match it.
- **Unknown stays unknown.** When Deedglass cannot tell whether a tool ran, the verdict is UNVERIFIABLE.
- **Authorized is not the same as wise.** A hijacked agent with a valid key and an in-policy action is allowed. Policies, budgets and approvals narrow this; they do not close it.
- **Trust assumptions.** Deedglass trusts the Postgres server and its superusers, the host it runs on, and its seal key. A REPLICATION role can decode every row change in its database.

## Database limits (S02)

- **A Postgres superuser or the schema owner can change anything.** They can disable triggers, turn off row-level security and rewrite deeds. The database can't stop its own superuser; offline deed verification (S05) detects a rewritten or broken chain, it doesn't prevent one.
- **Row-level security stops a forgotten filter, not an attacker on the server.** Anyone who can run SQL as the runtime role can set the org setting too. Injection and a compromised server are handled by parameterized SQL, no user SQL on the control plane, the two-role split and offline verification.
- **The database checks a seal's shape, not its signature.** Ed25519 seals are checked by `deedglass verify`, not by Postgres.
- **Forward-only migrations.** There is no down migration; a mistake is fixed by the next one. `CREATE INDEX CONCURRENTLY` isn't available through the runner in v1.
- **Direct Postgres only for session settings.** Session settings travel in the connection's startup packet; a transaction-mode pooler may drop them.
- **The control plane and a watched database are separate databases.** Deedglass refuses to put its own schema into a database it watches.

## Deed limits (S04)

- **Removing the newest deeds isn't visible in the chain alone.** A chain cut at seq 800 looks complete. Keep the head of each export you receive; S05's sealed manifest records it.
- **The seal key holder can write any deed.** Including any `issued_at`. A deed shows what this Deedglass deployment recorded, signed by that deployment, not by an independent party. Revoking a key makes every deed it sealed fail.
- **One signer per deed.** A deed is not countersigned by a transparency service or a second party in v1.
- **Small payloads can be guessed from their digests.** A digest of a yes/no answer or a short amount can be found by trying values.
- **New seal keys reach `keys.json` within 60 seconds,** not at once, and clients may cache it for 300 seconds.
- **No `keys revoke` command yet.** Revocation is a manual database change until it ships (`docs/SEAL_KEYS.md`).

## Verifier limits (S05)

- **A bare `.jsonl` can lose its newest deeds unnoticed.** Only a sealed bundle manifest, or a head you kept from an earlier export, shows that deeds were cut from the end. `deedglass verify` warns when a bundle's manifest is not sealed.
- **Keys inside a bundle show integrity, not authenticity.** Whoever made the bundle chose those keys. Pin the issuer's published keys (`--keys`, `--pin`) to know who sealed the deeds; `--strict` refuses to call an unpinned check valid.
- **`verify` checks seals and the chain, not the truth of a verdict.** A `VERIFIED` written in a deed is what the Witness recorded then; `verify` does not re-run it.
- **Browsers verify Ed25519 with their own engines.** The page checks key safety and `s < L` itself first and agrees with Go on every published vector and the 500-case parity corpus, but a browser engine could still differ on an input nobody has tried. The CLI is the reference verifier. Browsers without WebCrypto Ed25519 (before Safari 17, Firefox 129, Chrome 137) get "use the CLI", never a verdict.
- **The browser verifier reads `.json` and `.jsonl`, not bundles, in v1.**

## Ledger limits (S06)

- **The chain check runs inside the database it checks.** It finds a changed, removed or reordered
  deed anywhere before the head, but not deeds deleted from the end of the chain. Keep the head
  (`deedglass ledger check` prints it) somewhere outside Deedglass and compare.
- **Someone with database superuser rights can change history.** The append-only triggers stop
  every Deedglass role; a superuser can disable them. The chain check and every external
  `deedglass verify` then name the first changed deed; they can't undo the change or say who made it.
- **Pruned payloads are gone.** Deeds keep their digests, so a copy you kept elsewhere can still be
  matched, but Deedglass can no longer show the original arguments or results.
- **No search inside payloads.** `q` matches the tool name and the error code only.
- **Exports stop at 1,000,000 deeds and checks at 2,000,000 deeds or 30 s per request**; larger
  ranges are split (`next_from_seq`).

## Identity limits (S07)

- **Sign-in needs the email inbox, or an operator.** There are no passwords, no SSO or SCIM yet, and
  no self-serve signup. Without a mail provider, operators issue links with `deedglass org login-link`.
- **Rate limits live in each server process.** Behind N instances a client gets up to N times the IP
  and email budgets; the per-user cap on new sign-in tokens is counted in the database.
- **A newer sign-in email voids older links in its own org only**; a link into another org stays
  usable until it expires (15 minutes).
- **Unknown addresses and wrong codes take similar, not identical, time** (a dummy hash is computed
  when there is nothing to compare; the number of database reads differs).

## Agents, keys and Stop limits (S08)

- **Test keys aren't enforced against connectors.** A `dg_test_` key carries `livemode: false` and
  is recorded on each action from S13, but connectors have no test mode in v1, so a test key can
  reach a live system. Enforcement waits for connector modes (v0.2).
- **The failed-auth limiter lives in each server process.** Behind N instances, a client gets up to
  N times the per-address and per-prefix budgets.
- **While an org is stopped, its admin keys can only read** (`GET`). Every request from them that
  changes state gets `org_stopped`. Changing agents during a Stop all needs a Console session or the
  operator CLI.
- **Stop can't un-run an action already executing at a tool.** It refuses new calls and, from S13
  and S22, queued and approved work; a call a tool already received happens.
- **The 2-second Stop bound assumes every instance can reach Postgres.** If Postgres is down, no
  agent can act at all: Deedglass fails closed.
- **Agent keys' deed reads filter on `body->'agent'->>'id'` without an index.** Fine at v1 volumes;
  in a large org, an agent key listing its deeds reads more rows than it returns.
- **Key lists show at most 100 keys per agent** (and 100 org admin keys), newest first, revoked ones
  included. There is no pagination yet.

## Connectors, the vault and egress (S16)

- **Go can't reliably zero memory.** The vault zeroes the data keys and decrypted bytes it holds, but the garbage collector may have copied them, and HTTP header values are strings ([VAULT.md](VAULT.md)).
- **A vault key plus a database dump reads every credential.** Rotation can't un-leak a key: rotate *and* replace the upstream credentials.
- **The vault doesn't protect against a compromised Deedglass process** or the upstream service itself, and it has no rollback protection: restoring an older, consistent credential row brings the older credential back.
- **No KMS yet.** The key ring lives in `DEEDGLASS_VAULT_KEY`; a KMS-backed key provider is v2.
- **Egress sees addresses, not intent.** Misuse of an allowed API is policy's job ([NETWORK_EGRESS.md](NETWORK_EGRESS.md)).
- **No HTTP proxy support** for connector traffic, and no per-connector trust roots for private APIs (v2).
- **REST calls are witnessed at L0 only** until S19 adds read-back: a service that answers 200 without saving is VERIFIED today.
- **`sensitive` args are redacted from outputs and logs, not digested** in the action row (S22).
- **Credentials aren't cached.** Every call reads its credential row (one indexed read); a 30 s cache is a stretch item, taken only if the benchmark asks for it.
- **The MCP HTTP gateway's upstreams still take their headers from the environment** (S15's `mcp.upstreams`), not from the vault; their connections already go through egress. Moving them to `mcp_http` connector rows is S18 carry-over.
- **`/mcp/native` serves the built-in tools only.** An org's REST connectors are called through `POST /v1/actions`; listing them as MCP tools comes later.

## The Postgres connector (S17)

- **The marker alone can be forged.** `pg_logical_emit_message` is open to PUBLIC by default, so any role can write a message that looks like Deedglass's; the ledger row in the same transaction is what authenticates it. Anyone holding `deedglass_agent`'s credentials can write ledger rows through `begin_action`, so Deedglass witnesses what Postgres committed, not whether a compromised Deedglass told the truth ([POSTGRES.md](POSTGRES.md#the-ledger-and-the-marker)).
- **A REPLICATION role can decode every row change in the database,** not only the watched tables.
- **`row_limit` caps what comes back, not what Postgres reads;** `statement_timeout` bounds the work.
- **Triggers can write unwatched tables.** Those writes are invisible to L2 (S21 reports UNVERIFIABLE, never MISMATCH); `check` lists triggers on watched tables by name.
- **DDL is blocked, not witnessed.**
- **No L2 yet.** S20 creates the slot and decodes the stream, S21 turns it into verdicts. Until then, Postgres actions are L0, and `Result.Evidence` (`xid8`, `marker_lsn`, `sql_digest`, `command_tag`) isn't stored in the control plane; the witness reads the ledger in the customer database.
- **New tables aren't granted automatically:** run `init` again (no `ALTER DEFAULT PRIVILEGES` in v1).
- **Roles are cluster-wide.** Two watched databases on one cluster share `deedglass_agent` and `deedglass_witness`, and `init --rotate-passwords` on one changes the password for both.
- **No overloads** in `functions`, and arguments are scalars (strings, integers up to 2^53−1, booleans, null); send decimals as strings.
- **The denylist is defence in depth, not the boundary.** A function it doesn't list (`pg_sleep`, for example) runs, bounded by `statement_timeout` and the role's privileges.
- **Through a transaction pooler,** parameter types are inferred by the server from text, and `DISCARD ALL` is skipped (the pooler resets sessions).
- **`--probe` runs in local mode only** (`check --dsn-env`); the server-side check is read-only.
- **Managed providers are guidance only** until a smoke run exists ([CLOUD_COMPATIBILITY.md](CLOUD_COMPATIBILITY.md)).

## The MCP stdio wrapper (S14)

- **Edge-reported results are the edge's word.** A call the wrapper forwards is sealed L0 VERIFIED
  with method `edge_reported` when the server says it succeeded; a server that lies is believed
  until the Witness checks the system of record (Postgres from S21).
- **Only `tools/call` is governed.** `resources/*`, `prompts/*`, `completion/complete`,
  `subscriptions/listen` and the 2025-era server-to-client requests (`sampling/createMessage`,
  `elicitation/create`, `roots/list`) pass through ungoverned in v1 ([MCP.md](MCP.md)).
- **A server that acts outside MCP isn't stopped.** The wrapper governs the calls a client makes;
  it can't see what the server does on its own.
- **An approved call runs only in the wrapper process that created it.** After a restart the
  model is asked to make the call again; until S22 there is no `GET /v1/approvals/{id}` to look up
  an approval from another process.
- **A report that never lands leaves the action executing** until its deadline
  (`gate.edge_result_timeout`), when the Gate seals it UNVERIFIABLE (`edge_lost`).
- **Hiding tools is advisory.** `tools/list` leaves out tools the policy always denies, but a
  client can still call one by name; the Gate then denies it and seals a deed.
- **A changed tool definition is refused until restart.** There is no way yet to accept a new
  definition without restarting the wrapper.
- **Path rules compare text.** A policy can't see where a symlink points; the server resolves it.
- **No signed intents from MCP clients.** An agent in `required` intent mode can't act through
  the wrapper (S09).

## The MCP gateway, /mcp/native and mcp serve (S15)

- **Upstreams are configured in `deedglass.yaml` until S16.** There is no API or Console to add
  one, and their credentials are static headers from the environment; S16 moves them into
  connector rows (type `mcp_http`) with credentials in the vault.
- **The outbound guard is basic until S16:** private and loopback addresses are refused at dial
  time outside dev, redirects are never followed, TLS 1.2 or later; S16 brings the full egress
  guard (one door, DNS pinning, allow-lists).
- **No SSE streaming of progress** from upstream servers; answers are single JSON objects.
- **Resources and prompts are not governed** (`mcp.passthrough` forwards and logs them).
- **No OAuth token exchange with upstreams**; an upstream that only accepts OAuth can't be fronted.
- **Upstream results are the upstream's word** (L0 `reported`) until the Witness.
- **A Claude Desktop remote connector can't reach a local Deedglass**; it uses `deedglass mcp serve`.
- **The bridge doesn't send `Mcp-Param-*` headers**, so a modern client calling an upstream tool
  that declares `x-mcp-header` through `deedglass mcp serve --connector <name>` is refused
  (`-32020`). No native tool declares one today.
- **`deedglass.check_approval` scans the org's actions** for the approval id (no index on
  `actions.approval_id` yet); fine at demo scale, revisited with S22's approval routes.
- **The upstream pool's sessions are per instance and in memory**; a legacy upstream that loses its
  session gets one reconnect for `tools/list`, never for `tools/call`.

## The Acme demo stack (S15)

- **The demo's MCP entries use the database owner's credentials on purpose** (`acme_owner`), so the
  only difference between `orders` and `orders-direct` on screen is Deedglass in the path. Agents
  should never get owner credentials; S17's native Postgres connector uses a least-privilege role
  with its password in the vault.
- **Demo secrets are local-only**: `examples/demo/.env` and `.demo-keys` are random, 0600 and
  git-ignored, and every port binds to `127.0.0.1`. The stack is for local demos, never a server.
- **`orders-direct` bypasses Deedglass**: it exists for the contrast shot only.

## Signed intents, on-behalf-of and MCP limits (S09)

- **MCP clients can't sign intents in v1.** An agent in `required` mode can act only through the
  SDKs or the REST API ([INTENTS.md](INTENTS.md)).
- **A compromised runtime holds both keys.** Whoever controls the process that signs holds the API
  key and the intent key, and acts as the agent. Intents can't help there; policies, approvals and
  Stop still apply.
- **Unsigned on-behalf-of is a claim.** `Deedglass-On-Behalf-Of` and `Deedglass-Session` are
  recorded as the agent sends them. A signed `on_behalf_of` is bound to the agent's key, but
  Deedglass can't know the person is real until v2 checks tokens from your identity provider.
- **No authorization server in v1.** MCP clients authenticate with pre-issued API keys as bearer
  tokens; the protected-resource metadata omits `authorization_servers` on purpose, so clients that
  can only do OAuth can't connect yet ([MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md)).
- **Intent modes are enforced from S13.** In S09 the checks answered only
  `POST /v1/intents/verify`; since S13 the Gate checks the header on every `POST /v1/actions`.
- **A valid intent works once for whoever holds it,** within its window (at most 5 minutes plus
  60 seconds of skew). The nonce stops the second use, not the first.
- **Intent keys don't expire.** A registered key works until it is removed.

## Policy limits (S10)

What policies can't do ([POLICY_LANGUAGE.md](POLICY_LANGUAGE.md) section 12):
- **No loops and no functions of your own.** CEL has bounded comprehensions (`all`, `exists`,
  `map`, `filter`), nested at most two deep, and in practice a loop inside a loop over `args` is
  over the cost limit.
- **No network lookups, files or clocks.** Everything a rule can read is declared; the time is an
  input recorded with the action.
- **No state beyond `usage`.** A rule can't remember the previous action; budgets and the usage
  counters are the only memory.
- **No composition across files in v1.** One enforce policy is active per org; there is no
  `include:`.
- **CEL doesn't understand SQL.** The `sql` facts come from S11's classifier, which is conservative
  and is not a full Postgres parser. When it isn't sure it says `ambiguous`.
- **The cost estimate is an estimate.** It uses fixed size hints; the runtime cost limit is the
  backstop for larger real values.
- **`rule_unreachable` is conservative.** It only warns when coverage is certain from the `match`
  fields of a rule without `when`; it never reasons about conditions, so some dead rules go
  unflagged.
- **`secret_in_policy` knows eight prefixes.** Other secrets pasted into a policy are not detected.
- **`fmt` moves some comments.** Comments stay in the file, but a comment inside a `match` or
  `approval` keeps that mapping in block style, and a comment between keys moves with the key that
  follows it.

## Policy evaluation, sqlguard and budgets (S11)

- **Cost-based budgets can overshoot.** A `max_spend` budget without a `measure`, and an agent's
  `spend_per_day`, are checked before execution and counted after the tool reports its cost, so
  actions already in flight can pass the limit ([BUDGETS.md](BUDGETS.md) section 7). Call budgets
  and `measure` budgets are exact across instances.
- **No FX.** A budget counts only costs in its own currency; there is no conversion. An agent's
  `spend_per_day` compares usage in the snapshot's currency only.
- **Static SQL analysis can't see what Postgres runs on your behalf.** Triggers, rules, function
  bodies, `DO` bodies and settings changed earlier in a session are invisible to sqlguard. The L2
  witness (S20 and S21) sees what Postgres actually changed ([SQLGUARD.md](SQLGUARD.md) section 12).
- **sqlguard is conservative, so some valid SQL is refused as ambiguous**: a backslash in a plain
  `'…'` string, non-ASCII identifiers outside quotes, `BEGIN ATOMIC` function bodies, and any
  statement it doesn't recognise. A string with several statements counts as at least
  `transaction`, never `read`.
- **`where_trivial` is best effort.** It catches `WHERE true`, `WHERE 1 = 1` and a few similar
  shapes, not every condition that is always true.
- **`tables` is best effort.** `tables_complete` is false for functions in `FROM`, `LATERAL`,
  three-part names and DDL on things that aren't tables; a policy that allow-lists tables should
  require it.
- **Very long lists in `args` hit the time limit before the cost limit.** cel-go v0.32.0's runtime
  cost tracking grows quadratically with comprehension length, so for a list of tens of thousands
  of items the 50 ms evaluation deadline stops the evaluation before the cost limit does. Either
  way the action is denied (`policy_unavailable` or `policy_denied`), never allowed.
- **A 50-rule policy where every condition runs allocates about 630 objects per evaluation**, over
  the target of 200. The realistic postgres-safe policy allocates about 170.

## Policy versions, activation and the registry (S12)

- **One enforced policy and at most one shadow per org.** No includes, no per-agent policies (use
  `match.agent` and labels), no scheduled activations.
- **The activation deed check trusts the published seal keys.** On every load the registry
  recompiles the stored YAML, compares both hashes, and verifies the activation deed against the
  keys in `deedglass.seal_keys` (the same set `keys.json` publishes). Someone with superuser access
  who also registers a seal key of their own and appends a deed under it would pass that check; the
  new key would show in `keys.json`, and `deedglass verify` against your own copy of the key set
  would refuse those deeds. Keep a copy of `keys.json` outside Deedglass.
- **`version_hash` is frozen per language version.** It is computed over the normalized policy. A
  future build that normalized differently would see every stored version as changed and refuse
  them (fail closed). The normalizer can't change within `deedglass.policy/v1`; `hashes.json` and
  S10's lint goldens pin it.
- **Shadow decides nothing.** The registry loads the shadow version and `EvaluatePair` decides
  with it; since S13 the Gate records that decision on each action (`decision.shadow`), but
  nothing compares or reports shadow results until S27 (Insights).
- **A shadow version that fails its checks is dropped, not enforced against.** It is logged and
  counted; only an enforced version that fails makes the org refuse every action.
- **The dry run reads usage as zero** unless the request gives it; it never reserves. Real counters
  are read by the Gate (S13).
- **NOTIFY is best effort; the poll is the backstop.** A lost notification is caught by the 30 s
  safety poll, and a reconnect reloads everything. Within those windows an instance can serve the
  previous version for up to 30 s if Postgres drops a notification; the measured hot swap is about
  2 ms when it doesn't (`bench/results/2026-10-01-darwin-arm64-policy-registry.json`).
- **Rate limits on lint, evaluate and test are per instance** (60 a minute per person or key, in
  memory), like S07's limiter.
- **`policy diff` and `policy replay` aren't built** (S27). The Console editor is S26.

## Gate limits (S13)

What the Gate doesn't do ([GATE.md](GATE.md)):

- **A proxy-mode result is the edge's word.** In proxy mode the edge runs the tool and reports the
  result; the deed says `edge_reported` and records what the edge said. Deedglass can't tell a
  truthful edge from one that lies. S21's L2 witness checks Postgres on its own.
- **L0 means the tool reported success.** It is the tool's own answer (method `reported`), not an
  independent check. Until S19 every verdict is L0.
- **Timeouts and crashes become UNVERIFIABLE: it may have run.** When a tool doesn't answer within
  `gate.execute_timeout`, the edge never reports, or the server stops while a tool runs, the action
  is sealed with `execution.status: unknown` and the verdict UNVERIFIABLE. The deed says plainly
  that the tool may have run; check the target system by hand.
- **No retries.** The Gate never calls a tool a second time. A timeout is an unknown outcome, not
  a reason to try again, and a retry with the same Idempotency-Key replays the first answer.
- **It doesn't undo a tool that ran.** Stop and cancel refuse what hasn't run; S23 adds undo where
  a connector supports it.
- **`dg_test_` keys aren't blocked.** The action records `livemode` (and so does its context), but
  connectors have no test or live mode in v1, so a test key can reach a live system. Enforcement
  waits for connector modes (v0.2).
- **The fake connector is for development only.** `fake.echo`, `fake.write`, `fake.fail` and
  `fake.slow` exist only when `DEEDGLASS_ENV=dev` (and in tests). Real connectors arrive in S16 and
  later.
- **A gateway action keeps running when its HTTP request ends.** The tool call is detached from
  the request: a client that stops waiting, or a `wait` that ends, gets 202 `processing`, and the
  action is still recorded and sealed. Closing the connection doesn't cancel the tool.
- **20 open waits per agent, counted per instance.** Each agent may hold 20 open
  `GET /v1/actions/{id}?wait=` requests on one server; the 21st is 429 `rate_limited`. Behind N
  instances an agent can hold up to N times as many.
- **The Idempotency-Key comparison leaves out the signed intent.** The fingerprint is the request
  digest plus `Deedglass-On-Behalf-Of` and `Deedglass-Session`, so a retry signed again still
  replays, and a replay doesn't check the new intent. The original action's intent was checked
  when it was admitted.
- **An Idempotency-Key lasts 24 hours.** After that the key is free again, and the same request
  with the same key creates a new action.

## What this page does not do

It does not list bugs or missing features on the schedule. It lists limits of the design that stay true after they ship.
