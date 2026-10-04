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
- **A REST write without a read-back stays at L0:** a service that answers 200 without saving is `L0 · VERIFIED`. Add a `readback` to the tool to reach L1 ([WITNESS.md](WITNESS.md)).
- **`sensitive` args are redacted from outputs and logs, not digested** in the action row (S22).
- **Credentials aren't cached.** Every call reads its credential row (one indexed read); a 30 s cache is a stretch item, taken only if the benchmark asks for it.
- **The MCP HTTP gateway's upstreams still take their headers from the environment** (S15's `mcp.upstreams`), not from the vault; their connections already go through egress. Moving them to `mcp_http` connector rows is S18 carry-over.
- **`/mcp/native` serves the built-in tools only.** An org's REST connectors are called through `POST /v1/actions`; listing them as MCP tools comes later.

## The Postgres connector (S17)

- **The marker alone can be forged.** `pg_logical_emit_message` is open to PUBLIC by default, so any role can write a message that looks like Deedglass's; the ledger row in the same transaction is what authenticates it. Anyone holding `deedglass_agent`'s credentials can write ledger rows through `begin_action`, so Deedglass witnesses what Postgres committed, not whether a compromised Deedglass told the truth ([POSTGRES.md](POSTGRES.md#the-ledger-and-the-marker)).
- **A REPLICATION role can decode every row change in the database,** not only the watched tables.
- **`row_limit` caps what comes back, not what Postgres reads;** `statement_timeout` bounds the work.
- **Triggers can write unwatched tables.** Those writes are invisible to L2: the note `triggers_present` names the triggers, and `witness.strict_triggers: true` makes it the gap `triggers_unwatched` (UNVERIFIABLE, never MISMATCH); `check` lists triggers on watched tables by name.
- **DDL is blocked, not witnessed.**
- **L2 needs the decoder.** A write on a witnessed connector waits for its frame (S21); when the decoder is dropped or disabled there is no L2 and the claim stays at L0 or L1, and when it is halted the gap `decoder_halted` is listed at once.
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
  until the Witness checks the system of record (Postgres, in gateway mode).
- **Only `tools/call` is governed.** `resources/*`, `prompts/*`, `completion/complete`,
  `subscriptions/listen` and the 2025-era server-to-client requests (`sampling/createMessage`,
  `elicitation/create`, `roots/list`) pass through ungoverned in v1 ([MCP.md](MCP.md)).
- **A server that acts outside MCP isn't stopped.** The wrapper governs the calls a client makes;
  it can't see what the server does on its own.
- **An approved call runs only in the wrapper process that created it.** After a restart the
  wrapper looks the approval up (`GET /v1/approvals/{id}`, S22) and asks the model to make the call
  again; it never runs the earlier call.
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

- **Two ways to declare an upstream.** Since S18 an org adds a remote MCP server as an `mcp_http`
  connector with its credential in the vault. A `deedglass.yaml` upstream (`mcp.upstreams`) is
  served to every org on the instance; its `headers_from_env` is dev only (prod refuses it), and
  its name wins over an org's connector of the same name.
- **`mcp_http` connectors don't listen for `notifications/tools/list_changed`**: the pool is
  sessionless, so a renamed or new tool appears when the tool cache expires (`tools_ttl_ms`).
- **No SSE streaming of progress** from upstream servers; answers are single JSON objects.
- **Resources and prompts are not governed** (`mcp.passthrough` forwards and logs them).
- **No OAuth token exchange with upstreams**; an upstream that only accepts OAuth can't be fronted.
- **Upstream results are the upstream's word** (L0 `reported`) until the Witness.
- **A Claude Desktop remote connector can't reach a local Deedglass**; it uses `deedglass mcp serve`.
- **The bridge doesn't send `Mcp-Param-*` headers**, so a modern client calling an upstream tool
  that declares `x-mcp-header` through `deedglass mcp serve --connector <name>` is refused
  (`-32020`). No native tool declares one today.
- **`deedglass.check_approval` looks the action up by approval id** through the
  `actions_approval` index (S22, migration 0021).
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
  witness sees what Postgres actually changed ([SQLGUARD.md](SQLGUARD.md) section 12; [WITNESS.md](WITNESS.md)).
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
  truthful edge from one that lies. The L2 witness checks Postgres on its own (gateway mode).
- **L0 means the tool reported success.** It is the tool's own answer (method `reported`), not an
  independent check. Without a read-back (S19) or L2, a verdict stays at L0.
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

## The attack suite (S18)

- **It starts after the model was fooled.** The cases test what Deedglass does with the calls a
  manipulated model makes; they don't measure how often a model falls for an injection.
- **The W cases and H08 to H11 run on PostgreSQL 18 only** (`make attack`). W12's publication gap is
  what PostgreSQL 18 decodes; 16 and 17 halt the decoder instead (`decoder_halted`), which the decoder
  suite covers on all three.
- **It runs in dev with the private-network override**, so the server can reach the harness's
  loopback upstreams. Metadata, link-local and cross-host targets stay blocked even then (K01,
  K02); the prod-only rules for private ranges are covered by the egress corpus, not by the suite.
- **The harness's DNS answers come from a file**, read only by a `-tags attackhooks` build in dev.
  The release binary has no such hook.
- **Not covered:** a compromised Deedglass host, database superuser or seal key; malicious upstream
  behaviour beyond tool naming (S34); denial of service at scale; the supply chain beyond
  `govulncheck` (S35).

## The Witness (S19)

What the Witness doesn't do ([WITNESS.md](WITNESS.md)):

- **L1 confirms what the target reports now, not what it reported at commit time.** A concurrent
  change can make a REST read-back disagree, and the diff says so. For a Postgres row, a later
  writer makes the read-back a gap (`row_superseded`), never a `MISMATCH`.
- **L1 checks only the fields you name.** A tool that also changed something else is invisible to
  L1; L2 sees every row change of the watched tables for Postgres.
- **A read-back target that lies consistently can't be caught by reading it back.** L1 trusts the
  read tool as much as the system it reads.

## The decoder (S20)

What the decoder doesn't do ([DECODER.md](DECODER.md), [OBSERVATION_BOUNDARY.md](OBSERVATION_BOUNDARY.md)):

- **`deedglass_witness` could decode every table in its database.** REPLICATION isn't scoped to
  tables (spike S1(h)). Deedglass asks only for the publication's tables and stores only those; the
  credential lives in the vault. Treat `witness_dsn` as a read-everything credential.
- **Only the publication is watched.** Changes to unpublished, unlogged or temporary tables,
  sequences and large objects are invisible, and so are effects outside the WAL.
- **DDL leaves only a shape change** (`relation_changed:<table>`) on the next change to that table.
- **Failover slots and point-in-time recovery are not tested.** A new timeline degrades the decoder;
  a different cluster halts it.
- **Each read re-reads WAL from the slot's restart point.** A long transaction elsewhere makes reads
  slower (`peek_slow`, `restart_retained`); the streaming transport is the fix.
- **Stored row images are customer data.** They stay for `decoder.retention` (7 days; frames with no
  Deedglass action 30 days), under row-level security, with `witness.sensitive_columns` stored only
  as a digest. A frame keeps at most 1,000 images; its counts and digest cover every change.
- **A deleted connector whose database can't be reached leaves its slot behind** (`disabled ·
  slot_orphaned`, with the SQL to drop it by hand).
- **A logical message's content is held in memory whole.** A role that writes a huge message makes
  the decoder hold it once.

## The L2 witness (S21)

- **Expectations only for simple statements.** A single-table `UPDATE`, `DELETE` or `INSERT … VALUES`
  gets a footprint; a JOIN, `UPDATE … FROM`, a WITH query, `ON CONFLICT`, `MERGE` or a subquery in
  SET gets the gap `footprint_unknown`, plus `witness.max_rows` when the connector sets it.
- **Column checks need `REPLICA IDENTITY FULL`.** Without it, counts and keys are still compared and
  column checks are the gap `old_image_missing`.
- **The value check is narrow.** Only plain string, integer and boolean SET literals, only for text,
  varchar, char, integer and boolean columns. Numerics, timestamps, JSON and expressions aren't
  compared (that isn't a gap).
- **Keys of other types aren't matched.** A primary key whose type isn't text, an integer, a boolean
  or a uuid is checked by count only.
- **The diff keeps 20 rows** (`witness.diff_rows`, at most 100) and the decoder keeps at most 1,000
  row images per transaction; counts and the digest cover every row.
- **No L2 in proxy mode** (a wrapped third-party Postgres MCP server): there is no marker to bind.
- **No L2 for actions recovered after a crash** (executing past its deadline, sealed `unknown` by the
  sweeper): the sweeper has no connector at hand, so it seals at L0. The ledger row and the frame
  stay, and reconcile reports them.
- **At most three amendment deeds per action**, while the decoder keeps the frames (7 days).
- **Trigger bodies aren't read.** Deedglass sees what a trigger did to watched tables, not what it
  might do elsewhere.
- **The 100 writes/s benchmark's tail varies.** Commit to verdict p50 met the contract (< 1 s) in every
  run on an Apple M1, and the aim (< 300 ms) in most; p99 ranged from about 1 s to several seconds,
  in both the decoder's SQL peek and the witness step under sustained load (`make bench-l2`,
  `bench/results/2026-10-02-darwin-arm64-l2.json`). S35 re-measures on an idle machine and decides on
  the streaming transport.

## Approvals and webhooks (S22)

- **An approval link is a bearer token for one person and one approval.** Whoever holds the email
  can use the link until it is used, voided or expires (at most 24 hours, never past the
  approval). The token is in the URL fragment, so it doesn't reach logs, proxies or referrers, but
  a forwarded email forwards the link. Orgs that want a session for every decision set
  `email_links: review`.
- **The page shows the arguments** (with secrets scrubbed) to whoever opens a valid link, so the
  person sees what they countersign. `email_detail: summary` keeps them out of the email itself.
- **The row estimate is the planner's**, from table statistics: "about 1 row" can be wrong, which
  is why the Witness still compares what actually changed.
- **The policy isn't evaluated again at approval time.** A rule changed while an approval waits
  applies to the next action.
- **Webhook payloads are thin**: ids and states. Receivers fetch the object for anything else.
- **Webhook deliveries are at least once and unordered.** Dedupe by `Deedglass-Event-Id`; a retry
  can arrive after a later event.
- **A disabled webhook endpoint stays disabled.** After 72 hours of failures it is turned off;
  create it again to resume. Events in between aren't replayed.
- **Email only.** Approvals notify by email in S22; Slack arrives with S29, MCP tasks later.

## Undo (S23)

The whole story is in [UNDO.md](UNDO.md). Undo puts rows back from Postgres's own row images, and
it refuses rather than guess:

- **Postgres rows only, witnessed at L2.** Actions witnessed at L0 or L1 (REST, GitHub, Stripe and
  the other connectors without a change log) can't be undone, and neither can proxy-mode actions,
  which carry no Deedglass marker to bind the evidence.
- **No DDL, no TRUNCATE.** A transaction that changed a table's definition or truncated a table is
  refused; undo never runs DDL.
- **Sequences are not rewound.** Rows get their original ids back (`OVERRIDING SYSTEM VALUE` for
  identity columns); the sequence stays where it is.
- **Rows changed since are refused.** Every row must still hold exactly what the action wrote, byte
  for byte as Postgres prints it, so a `numeric` that reads `1.00` where the action wrote `1.0`
  counts as changed. That is on purpose.
- **A primary key on every table**, at most 1,000 rows (`undo.max_rows`), and actions sealed in the
  last 7 days whose row images weren't pruned (`undo.max_age`).
- **Sensitive values can't be written back.** The decoder kept only their digest. A sensitive value
  that is only compared is compared by its digest and shown as `•••`.
- **Rows that swapped keys** (a cycle) are refused: no order of single-row steps puts them back.
- **Triggers, rules and cascading foreign keys** on the tables are refused by default.
  `allow_triggers` (admins, with a reason) lets triggers fire again, and the L2 witness then shows
  what they did; the undo is `MISMATCH` if they changed anything outside the plan.
- **Nothing outside Postgres.** Emails sent, payments made, webhooks fired, caches and downstream
  copies of the data stay as they are; those need new actions a person chooses.
- **One live undo per action**, and a person always starts it: agent keys are refused, and the
  starter policy asks another person to countersign, so a one-admin org can't run an undo until it
  has a second admin or sets `self_countersign: allow` in its approval settings.

## The Console's control screens (S26)

- **An org can't be deleted from the Console.** There is no route for it yet (owner only, the typed
  slug, a 7-day grace period you can cancel, an admin deed, and a purge worker). It is on the v0.2
  backlog; until then an operator removes an org in the database.
- **Policy editing is single-writer.** Two people editing the same policy each save their own
  version; the last save is the newest version. Versions never change and the diff shows exactly
  what each one changed.
- **The editor is plain text** with diagnostics underlined from the server's lint; it has no syntax
  colouring and no completion, and quick fixes exist only where the lint names the replacement
  (`Did you mean "decision"?`).
- **Readiness and the Witness card show what Deedglass last recorded.** The readiness report runs
  when you ask (it connects to your database, at most 10 times a minute); the Witness card reads
  the decoder's own record and never connects on page load, so it can be up to one decoder poll
  behind.

## Insights (S27)

- **Insights never changes anything by itself.** It reads and reports. No policy is activated or
  edited, and no action is blocked, unless a person does it; the only input into decisions is the
  opt-in anomaly facts, and what they do is written in the org's own policy ([INSIGHTS.md](INSIGHTS.md)).
- **No machine learning and no learning across orgs.** Anomaly checks are fixed thresholds over one
  agent's own hourly counts in one org.
- **Baselines need 72 hours.** A younger agent gets only the `new_tool` check.
- **A slow ramp can move a baseline.** Growth a little at a time raises the mean and never reads as
  a spike. Budgets and rate limits are the hard controls; anomalies are signals.
- **No time-of-day seasonality.** `volume_spike` compares an hour with all of the agent's hours, so
  a batch packed into one or two hours can read as a spike; spread it, or expect findings.
- **Detection runs every 5 minutes per instance**, over orgs with actions in the last 2 hours or
  with anomaly facts on. A finding can arrive up to 5 minutes late; with anomaly facts on, a
  detector more than `anomaly_stale_after_minutes` (15) behind makes every agent anomalous
  (`detector_stale`) until it catches up.
- **Replay re-decides the past; it doesn't re-run it.** Inputs that were pruned, never stored,
  in a newer format or unreadable are counted `unknown`, never guessed. SQL facts are recomputed
  with the current classifier; the result names both versions ([REPLAY.md](REPLAY.md)).
- **Replay limits:** a window of at most 90 days and 1,000,000 actions, 10 replays created per org
  an hour, one running per org and two per instance. A replay whose runner dies restarts from zero.
- **Replay replays stored versions only over the API.** Draft sources replay in the engine (S28's
  suggestions); there is no HTTP route for them yet.
- **Stats use the budgets enforced now.** `budgets` measures past days against the current
  enforced version's day budgets; minute and hour budgets are left out. A `window=7d` answer can be
  up to 60 s old: repeats within a minute share the first request's answer.
- **Simulated history is dev only and labelled.** `deedglass dev simulate` refuses outside
  `DEEDGLASS_ENV=dev`, writes only into orgs it flagged simulated, scripts the tools and the
  Witness (`witness.method: simulated`), and runs actions one at a time, so it is no load test
  ([SIMULATOR.md](SIMULATOR.md)).

## Suggestions, phrasing and the digest (S28)

- **Suggestions never change anything by themselves.** Accepting creates a version in shadow;
  promoting it to enforce is a separate act by a person. `internal/learn` has no way to enforce
  (three tests pin it; [SUGGESTIONS.md](SUGGESTIONS.md)).
- **Template wording only, unless opted in.** Claude rewords suggestions only when the operator set
  `DEEDGLASS_ANTHROPIC_API_KEY` and the org turned `llm_phrasing` on; a reply that fails any check
  is discarded ([INSIGHTS_PRIVACY.md](INSIGHTS_PRIVACY.md)).
- **One org's data, 14 days.** Suggestions read only the org's own decisions in the last 14 days,
  under the enforced version. No learning across orgs, and no machine learning: fixed thresholds
  over counts.
- **They can't see intent.** Approving every order update doesn't say why people approved, or how
  closely they looked.
- **Budget replays show 0 changes.** Replay keeps each action's recorded usage, so it can't show
  what a higher budget would have let through. Budget suggestions rest on the near-limit days.
- **Agent budgets are advice only.** They live on the agent; the suggestion names the command and
  changes nothing.
- **The patcher refuses some files:** flow-style `rules` or `budgets` lists and files that mix LF
  and CRLF line endings. `deedglass policy fmt -w` fixes both.
- **The schedule runs on every instance.** Each `serve` checks every 5 minutes; `learn_runs` rows
  make the daily pass and the weekly digest happen once per org and period. A manual generate
  refuses a second pass for the same org on the same instance only; two instances can run one each,
  and the unique index keeps one open suggestion per change.
- **At most 10 open suggestions per org.** The rest wait for a later pass.
- **The digest is numbers only, in English, weekly.** No per-person schedule, no other languages.

## GitHub, Slack and approvals in Slack (S29)

- **L1, not L2.** GitHub and Slack writes are read back from the service's own API (L1). That shows
  what GitHub and Slack report now, not whether the change was wise, and not a system-of-record
  change stream.
- **The GitHub marker is visible.** Issue, comment and pull request bodies end with
  `<!-- deedglass:act_… -->` (hidden when rendered, visible in the raw body). `issue_marker: false`
  removes it, and with it the reconciliation of an uncertain create.
- **An uncertain GitHub create is looked up once.** When a create times out after sending, the
  connector reads the newest items once; if the marker isn't there yet, the action is UNVERIFIABLE
  ("It may have run"), never retried.
- **GitHub REST only.** No GraphQL, no Actions runs, no org administration; GitHub Enterprise Server
  is untested. File paths must be ASCII or contain no combining marks (a conservative NFC check
  without a Unicode library).
- **Slack identity is the workspace's word.** A Slack user maps to a person by an email Slack
  reports as confirmed. Use an SSO workspace and `approvals.require_email_domains`; a compromised
  Slack account can approve like a compromised Console session.
- **Slack sees the card.** The summary, the rule, the reason and the arguments (sensitive ones as
  digests) are sent to Slack.
- **One approvals channel per org, a public URL.** No per-rule channels and no Socket Mode yet;
  Slack must reach `POST /v1/slack/interact` over HTTPS.
- **Cards can lag.** An edit is a job that retries 4 times; after that the card says nothing new and
  the Console is the source of truth.

## Stripe and Gmail (S30)

- **Stripe sandboxes only.** Live keys are refused and a live-mode answer disables the connector.
  There is no switch: live mode needs a founder-approved ADR and its own review.
- **L1 shows Stripe's record, not whether the refund was right.**
- **Stripe's own approvals are invisible.** A refund Stripe holds for an agent-tagged key's
  approval rule is UNVERIFIABLE; Deedglass doesn't poll for the outcome yet (a reconcile job is a
  stretch goal).
- **The refund budget counts USD.** The starter's `refunds-per-day` measures USD refunds Stripe
  confirmed; other currencies go to a person.
- **Drafts only is Deedglass's rule, not Google's.** The `gmail.compose` scope itself permits
  sending; Deedglass enforces drafts only in code (no tool, an endpoint allow-list, a source test).
- **Google apps in Testing need re-authorization every 7 days.**
- **No attachments, labels or deletes** in Gmail, and HTML bodies are stored, never rendered.
- **Notion writes plain paragraphs under the listed parent pages only** (S31). No databases, data
  source rows, files, comments, edits or deletes; the read-back compares plain text, not rich
  formatting; Notion has no idempotency key, so a create whose answer was lost is UNVERIFIABLE and is
  never sent again.

## The TypeScript SDK (S31)

- **`wrapTool` decides whether your code runs; it doesn't sandbox what it does.** Results your code
  reports are L0 (edge-reported); independent L1 and L2 checks need a Deedglass connector.
- **The Claude Agent SDK hooks see only the tools the SDK routes through hooks.**
- **No framework call ids reach Deedglass:** the 2026-10-01 API's `context` takes only the client and
  the transport.
- **Agent keys are secrets, so the client refuses to start in a browser page** unless told otherwise;
  short-lived browser tokens are a v2 item.
- **Offline verification checks seals and the chain, not whether the action was wise.**

## What this page does not do

It does not list bugs or missing features on the schedule. It lists limits of the design that stay true after they ship.
