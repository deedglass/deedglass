# Threat model

Who Deedglass trusts for what, what each attacker can try, which control stops or records it, the
test that shows it, and what stays possible. Version 2 (S34, security pass 2), written with every
v1 feature built: the Gate and policies, the MCP wrapper and gateway, connectors, the vault and the
egress door, the Witness and the decoder, deeds and keys, people and sessions, approvals in email
and Slack, the Console and its event stream, Insights with its optional AI wording, the CLI, the
SDKs and `deedglass init`.

**Version 3 (2026-10-21, security pass 3)** adds the week's new surfaces: the A2A proxy, the
Debezium witness, the shell guard and its hooks, head timestamps, the independent verifiers, and
`deedglass connect` (planned, not merged). Their attack cases are group T (T01 to T13), declared in
`attack/attack_meta.json` before the A2A proxy's browser checks and the Debezium input cap were
written ([ATTACK_SUITE.md](ATTACK_SUITE.md)).

**The framing, unchanged since S18:** assume the model is already manipulated. Deedglass doesn't
measure how often a model falls for an injection; it limits and records what the model's tool calls
can do, what a stolen key can do, what a curious org neighbour can see, what a hostile upstream can
make Deedglass do, and what a network attacker can forge.

## Assets

| Asset | Where it lives | Why it matters |
|---|---|---|
| Seal keys (`sk_…` private halves) | A 0600 key file per instance (`DEEDGLASS_SEAL_KEY_FILE`) | Whoever holds one can seal deeds that verify |
| Vault KEK and data keys | `DEEDGLASS_VAULT_KEY` (1 to 4 keys); wrapped data keys per credential version in `deedglass.credentials` | They open every connector credential |
| Connector credentials | Encrypted in the vault (AES-256-GCM envelope), opened for one call | Access to the customer's systems |
| API key pepper and key hashes | `DEEDGLASS_API_KEY_PEPPER`; `sha256(pepper ‖ secret)` in `deedglass.api_keys` | Forging or testing keys offline |
| Session and CSRF cookies | `__Host-deedglass_session` (HttpOnly) and `__Host-deedglass_csrf`; hashes in `deedglass.sessions` | Acting as a person in the Console |
| Approval tokens | Email links (hash at rest, one use, bound to one person and one approval); Slack clicks (signed by Slack) | A countersign without the right person |
| The deed chain and amendment deeds | `deedglass.deeds`, exports, auditors' copies | The record itself |
| The Ledger | Actions, action events, admin events | What happened, searchable |
| The customer's database | Behind the Postgres connector (`deedglass_agent` writes, `deedglass_witness` reads the change log) | The target of agent writes |
| Decoder slots and replication credentials | Replication slots on the customer's database; the witness DSN in the vault | What L2 sees |
| Policy versions and activations | `deedglass.policy_versions`, activation deeds | What agents may do |
| The LLM channel and its key | `DEEDGLASS_ANTHROPIC_API_KEY`; suggestion phrasing only | Words shown to admins |
| Webhook secrets | Encrypted with the vault ring; shown once | Forging deliveries to a customer |
| `.deedglass/` local state | 0700 directory: dev keys, the init stack file, logs | A developer's local credentials |
| The A2A proxy's agent key | `DEEDGLASS_API_KEY` in the proxy's environment; callers present the same key | Acting as that agent towards the Gate and the peer |
| The peer credential | `--peer-header-env` names an environment variable; sent to the peer only | Access to the remote agent |
| Feeds and timestamps | Debezium JSON-lines files; `timestamps.json` beside an export or inside a bundle | What a foreign witness compares; when a head existed |
| Release artifacts | GitHub releases (archives, `checksums.txt` and its signature, SBOMs), GHCR, npm | What users run |

## Trust boundaries

```
 Agent process (untrusted output) --Bearer dg_...--> +----------- Deedglass (trusted computing base) -----------+
 MCP client (Claude, Cursor) --stdio/HTTP--> Gate    | httpx · Gate · policy · approve · witness · deed · vault   |
 Person (browser) --cookie + CSRF--> Console         | Postgres (schema deedglass, RLS per org)                    |
 Slack --signed interaction--> /v1/slack/interact    |                                                             |
 Email link --one-use token--> approval page (POST)  +--egress (one door)--> upstreams: MCP servers, REST, GitHub,
                                                                             Slack, Stripe (sandbox), Gmail, Notion,
                                                                             the Anthropic API
 Customer Postgres <--deedglass_agent (writes)-- connector     Customer Postgres --pgoutput (deedglass_witness)--> decoder
 Auditor --keys.json + deeds.jsonl--> deedglass verify (offline; trusts only the keys it is given)
```

| Id | Boundary | What crosses it |
|---|---|---|
| B1 | Agent → Gate | Tool calls (REST `POST /v1/actions`, `/mcp/*`, the stdio wrapper), signed intents, on-behalf-of headers |
| B2 | Person → Console | Session cookies, CSRF tokens, every Console mutation, the event stream |
| B3 | Third-party callbacks → Deedglass | Slack interactions, email approval links, webhook test answers |
| B4 | Deedglass → upstreams | Connector calls, MCP sessions, webhook deliveries, the LLM phrasing call, `--keys` URLs, `version --check` |
| B5 | Customer database ↔ Deedglass | Agent writes as `deedglass_agent`; the change log as `deedglass_witness` |
| B6 | Deeds → auditor | Exports and bundles, `keys.json` |
| B7 | Deedglass → LLM provider | The allow-listed phrasing input; the reply |
| B8 | Operator → host | Out of scope: a compromised host |
| B9 | Local processes and browsers → the A2A proxy (loopback) | JSON-RPC requests, the agent's key as a bearer token, the Agent Card |
| B10 | A2A proxy → remote agent | Delegated messages, the chain, the peer credential; the peer's answers and card |
| B11 | Files → offline checks | Debezium feeds (`witness debezium`), bundles and `timestamps.json` (`verify`, `verifiers/`) |
| B12 | Coding agent → shell guard | The hook's JSON on stdin; the answer on stdout |
| B13 | Release workflow → users | Archives, checksums, signatures, SBOMs, images, npm packages, `install.sh`, and their copy on deedglass.com/dl |

## Attackers

| Id | Attacker | What they hold |
|---|---|---|
| A1 | A manipulated model (prompt injection succeeded) | An agent's valid key and whatever the policy lets it call |
| A2 | A stolen agent key | The key, maybe after it was revoked or the agent suspended; captured signed intents |
| A3 | A member of another org | Valid credentials of every kind in their own org on the same instance |
| A4 | A curious or malicious viewer in the same org | A viewer session |
| A5 | A hostile upstream | An MCP server or API an org connected: it chooses its tool names, descriptions, results and timing |
| A6 | A network attacker (no TLS break) | The ability to send requests to public routes and to observe timing |
| A7 | Someone who forwards or replays an approval link or a Slack payload | A link or a captured interaction |
| A8 | An auditor-facing forger | An exported chain they can edit before an auditor reads it |
| A9 | A web page open in a browser on the agent's machine | Script that can send requests to loopback ports, and DNS it controls (rebinding) |
| A10 | A hostile remote A2A agent | Its Agent Card (name, interfaces), its answers and their timing |
| A11 | A process or agent on the same machine | Write access to the user's files: hook settings, PATH, feed files, timestamps files |
| A12 | Someone who can change a release or its download | A swapped archive with matching checksums, or a moved image tag |

**Out of scope, stated:** a compromised Deedglass host or seal key, a malicious operator,
model-level injection rates, volumetric DDoS, and a compromised database superuser of the control
plane.

## STRIDE by component

Each row: S spoofing, T tampering, R repudiation, I information disclosure, D denial of service,
E elevation of privilege. "Test" names the test or attack case that exercises the control.

### Gate (B1)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A1 calls as another agent | Keys bind to one agent; ids never authorize | `TestRouteAuthCoverage`, J01, J02 | A stolen key acts as its agent until revoked (A2). |
| T | A1 smuggles a second statement or hides a write | sqlguard facts, policy rules, the connector's one-statement rule | H01–H07 | The policy's rules are the customer's; a rule that allows everything allows everything. |
| R | An agent denies a call it made | Every attempt (denied, stopped, refused) is sealed | U04, Q01, `TestGateLive` | None in Deedglass's record; a call that never reaches Deedglass isn't recorded. |
| I | A1 learns another tool's existence or the network | `tool_unknown` reveals nothing; `destination_blocked` is terse to agents | U04, K01 | Timing differences between unknown and denied tools are not equalized. |
| D | A1 floods calls or sends huge bodies | Budgets, the 1 MiB body limit, args depth 32, route timeouts | J04, R06, `TestLimits` | A flood within budget costs the customer's quota; volumetric DDoS is out of scope. |
| E | A1 calls a tool after Stop | Stop-all propagates in < 2 s; every later call is `org_stopped` and sealed | P10, `TestStopPropagation` | Calls already executing finish. |

### Policy and CEL (B1, B2)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | An admin's policy hides an allow-all | Lint, `default-deny`, `default_allow` needs acknowledged warnings and a deed | `TestLintGolden`, `policy.activated` deeds | An admin can still enforce a permissive policy; the deed records who. |
| D | A YAML or CEL bomb in a pushed policy | 256 KiB, 50,000 nodes, depth 32, no aliases; CEL cost and nesting limits at lint; runtime cost limit and a 50 ms deadline | R01, R02, `FuzzCompile`, `FuzzYAMLReader` | None known within the limits. |
| E | A1 sends args that make a rule error into an allow | Every evaluation error, cost stop or deadline is a deny | R02, `TestCostLimitFailsClosed`, `TestHugeArgsNeverAllow` | None. |

### MCP wrapper and gateway (B1, B4)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A5 names a tool like another (look-alikes, shadowing) | MCP charset only; names are qualified `<connector>.<tool>`; bare names refused | K04, Q03 | ASCII look-alikes stay visible and are judged by policy. |
| T | A5 changes a tool after it was listed (rug pull) | The first definition's digest is pinned; a changed tool is hidden and refused until restart, and sealed as `connector.tool_hidden` | Q02, `TestScreenHidesAndPins` | The first definition is trusted as listed. |
| T | A5 puts instructions in tool descriptions | Description scanning hides the tool, logs and seals it; the agent sees `tool_unknown` | Q01, `TestFlaggedDescription`, `internal/mcp/toolscan` | A heuristic: reworded instructions pass (LIMITATIONS). Tool results aren't scanned. |
| I | A client's token reaches an upstream | `StripInboundCredentials` on every forwarded request | `TestMCPGatewayLive` | None known. |
| D | A5 answers with 50 MiB, 10,000-deep JSON, or never answers | 4 MiB cap, depth 512 while reading and 128 after decoding, per-request timeouts; all unknown, never executed | Q04, Q05, `TestDeepStreamFailsFast`, `TestHangIsTimeout`, `FuzzReader`, `FuzzFrontEnvelope` | A slow upstream holds one call up to its timeout. |
| E | A5 asks the client's model for text (sampling) or the person for input (elicitation) | Refused and logged unless `--allow-server-requests`; the gateway never relays | Q06, `TestSamplingRefused`, `TestScreenHidesAndPins` | Once an operator allows them, they pass ungoverned. |

### Connectors and the egress door (B4)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A5 redirects to metadata or another host | One egress door: metadata, link-local, reserved and (in prod) private addresses refused at config and at dial; same-host redirects only | K01, K02, Q08, `TestCorpus` (egress), `FuzzCheckURL` | Allowed public hosts can still be malicious; they're the customer's choice. |
| T | A live Stripe key turns sandbox into production | Live keys refused when stored and before every call; a livemode answer trips the breaker | Q07, `TestStripeRefund` | Restricted test keys can still refund within the sandbox. |
| I | A5 echoes the credential back | Redaction in every output; credentials never logged | K03, `FuzzRedactor` | A credential an upstream transforms beyond recognition isn't redacted. |
| I | An admin pastes a credential into a description | Descriptions that the scrubber recognizes as credentials are refused (SR-013) | Q07, `TestDescriptionRefusesCredentials` | Unknown credential formats pass. |

### Vault (B4)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A ciphertext is moved to another org or connector | The envelope's AAD binds org, connector, version and kind | `TestGoldenEnvelope`, vault integrity tests | None known. |
| I | The database is copied | AES-256-GCM with the KEK outside the database | vault round-trip tests | A copy of the database and `DEEDGLASS_VAULT_KEY` together opens everything. |

### Witness and decoder (B5)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A trigger or a second session changes more than the agent reported | L2 compares Postgres's change log with the expected footprint | W01–W13, H08–H11 | Writes to unwatched or unlogged tables are unseen. |
| S | A forged marker names a live action | A frame binds only with one ledger row and one transactional marker of the same transaction | W05, H10, `TestForgedMarkers` | A database superuser can do anything in that database. |
| D | Malformed pgoutput bytes | `SafeParse` checks the layout before `pglogrepl.Parse`; failure halts the runner before anything is stored | `FuzzSafeParse`, `TestSafeParsePrefixes` | A halted runner leaves actions UNVERIFIABLE until fixed. |
| R | Evidence arrives late or never | UNVERIFIABLE with the gap's reason; late evidence is an amendment deed | W10, W11, W12 | None: gaps are recorded as gaps. |

### Deeds and seal keys (B6)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A8 edits, reorders, splices or forks a chain | Ed25519 seals over JCS bodies; `prev_hash` links; `UNIQUE (org_id, prev_hash)`; offline verify | R03, F01–F04, `FuzzParseEnvelope`, `FuzzVerifyEnvelope`, `FuzzVerifyStream`, `FuzzArchiveOpen` | Deeds cut from the end aren't visible in the chain alone; keep the heads you receive. |
| S | A forger presents another key | Verify trusts only the keys it is given; small-order keys refused | A13, `deedglass verify --pin` | An auditor who fetches `keys.json` from a compromised host trusts that host. |
| E | The seal key is stolen | 0600 files, rotation, revocation | `TestKeyFiles` | A compromised Deedglass host can sign false deeds. Keep seal keys on a separate volume, rotate them, and have auditors pin keys.json from a copy they fetched earlier. |

### People, sessions and identity (B2)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A6 forges a cross-site request | Sec-Fetch-Site and Origin checks, the double-submit CSRF token bound to the session (constant time), `SameSite` | P07, `TestCookieFlags` | A browser that sends neither Sec-Fetch-Site nor Origin relies on the token alone. |
| E | A4 reaches admin actions | Every route declares its role; viewers never mutate outside a reasoned list | P06, `TestAuthzMatrix`, `TestAuthzMatrixServe`, `TestRoleMatrix` | An admin is trusted with admin actions. |
| I | A3 reads another org's data | RLS on every org table; the runtime role owns nothing and can't bypass RLS | `TestOrgIsolation`, `TestRuntimeRoleCannotBypassRLS`, P08 | A control-plane superuser sees every org. |
| D | Login and magic-link flooding | Per-IP and per-email limits, identical answers | `TestLoginEnumeration`, limiter tests | Volumetric floods are out of scope. |

### Approvals, email links and Slack (B3)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A7 replays or reuses a link | Token hash at rest, one use, bound to one person and one approval, voided when the approval ends | L01, L02, P02, `FuzzApprovalToken` | A forwarded unused link works for whoever holds it, once, as its person. |
| S | A6 forges a Slack click | Slack's v0 signature with the org's signing secret, 5-minute window, replay table | P03, `FuzzSlackVerify` (both) | A leaked signing secret lets its holder click as any mapped person. |
| E | The requester approves their own action | The self rule (web and Slack), four-eyes as distinct people, keys never vote | L03, L04, P04, P05 | `self_countersign: allow` is an org's explicit choice. |
| T | A decision on an out-of-date view | `context_digest` must match | L06, P02 | None. |

### Console and the event stream (B2)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| I | A3 resumes another org's stream with a guessed id | Streams are per principal; a cursor never crosses orgs | P08, `TestIsolationProperty` | Event ids are a per-instance sequence; their numbers aren't secret. |
| T | Stored text renders as HTML or moves a terminal | Text nodes only (Trusted Types 'none'), JSON escaping, CLI escaping | R07, `TestPages` (CSP), the web sink grep | Bidirectional characters in names can reorder text in the Console (SR-016). |
| D | Many streams | Per-org and per-user connection limits | `TestEventsPerUserLimit`, `TestLimits` (console hub) | |

### Insights and the LLM channel (B7)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A1 names tools and rules to steer the AI wording | Names are placeholders on the wire; numbers, length, placeholders and words are validated; the diff never goes to the model | R05, `TestPrivacySentinels`, `TestPrivacyProperty`, the `ValidatePhrasing` tests | A reply that passes the checks can still be persuasive; it is labelled as AI wording and a person decides. |
| E | A suggestion enforces itself | `PolicyWriter` has two methods, neither enforces | `TestNeverEnforceMethodSet`, `TestNeverEnforceBehaviour` | None. |
| I | The org's data reaches the provider | An allow-list of aggregate keys | `TestPhrasingInputKeysMatchStruct`, `TestPrivacySentinels` | Aggregates (counts, windows) do leave when an org turns phrasing on. |

### CLI, SDKs and `init` (B6, local)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A deed or a name moves the terminal | `clean()` escapes controls and bidi overrides in human output | R07, `TestClipCleans`, `TestVerifyGolden` | `--json` output is raw JSON by design. |
| I | `init` and `scan` leak credentials | `scan` reads environment names, never values, and one DSN from a named variable; `init` writes 0600 files and takes passwords on stdin | `TestInitLive`, scan read-only tests | A developer's own machine is trusted. |
| S | `verify --keys` from an attacker's URL | https only (loopback http in dev), size and time caps, same-host redirects | `TestVerifyGolden` (keys URL rules) | The auditor chooses the URL. |
| I | SDKs print keys | Keys masked in `repr`, errors and logs | `sdk/python` and `sdk/typescript` tests | |

### A2A proxy (B9, B10)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| S | A9 posts a simple cross-site request (`text/plain`, `?A2A-Version=` in the query) | `Origin`, or `Sec-Fetch-Site` other than `none`, is refused; the body must be `application/json`; the version is read from the header only | T01, `TestBrowserShapedRefused` | A browser that sends neither header and a JSON body is still stopped by the key (T03). |
| S | A9 reads answers through DNS rebinding | `Host` must be `127.0.0.1`, `[::1]` or `localhost`, on every path, the card included | T02, `TestLoopbackHost`, `TestCardBrowserRefused` | None known. |
| S | A11 calls the proxy without the agent's key | Every JSON-RPC request needs `Authorization: Bearer <the agent's key>`, compared in constant time over digests | T03 | Any local process that holds the key acts as the agent, as it could against the server directly. |
| I | A9 reads an answer through CORS | Preflights get 403; no `Access-Control-*` header on any answer | T04 | None known. |
| R | A captured request is replayed | Each replay is a new action that the Gate decides and records | T05 | The proxy keeps no replay table; policies, budgets and approvals apply to every replay. |
| S | A10's card claims another agent's name | The chain's ref is the operator's `--peer` URL; the card's name is recorded as given | T06 | The name is the peer's claim; card signatures are dropped, not checked (LIMITATIONS). |
| I | The agent's key reaches the peer, or the caller's credentials do | The key goes to the Deedglass server only; inbound `Authorization` and cookies are never forwarded | `TestGoldens` | `--api-key` shows in `ps`; the proxy warns and the docs say to use `DEEDGLASS_API_KEY`. |
| E | A10 redirects the proxy to metadata or a private host | The egress door, as for connectors; redirects are not followed | `TestA2ALive`, the egress corpus | `--card-url` may name another public host. |

### Debezium witness (B11)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A11 drops or reorders events in the action's transaction | A transaction without BEGIN, END, matching counts and an unbroken order is `feed_incomplete` and never compared | T07, `TestCutFeedNeverDecides`, `TestResumedFeed` | A consistent forged feed (counts kept) is compared as given: the result is only as good as the feed. |
| R | A result can't be tied to the file it read | `--json` carries `feed.input_sha256` and `feed.input_bytes` of the events as read | `TestWitnessDebeziumGoldens` | The digest names a file; it doesn't say who wrote it. |
| D | A gzip bomb or an endless stdin | 16 MiB per line and 1 GiB per feed after gunzip (`feed_too_large`) | T08 | Up to 1 GiB is read into memory as parsed events. |
| S | Any writer's transaction is taken as the action's | Bound only by a ledger row naming the action; otherwise the note `any_writer` | `TestRecordedScene`, `TestNoMatchAndNamed` | The ledger row is written by the customer's code, not authenticated as on Postgres. |

### Shell guard and hooks (B12)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A1 hides a destructive command inside another | Every command a line runs is checked (`&&`, pipes, subshells, `sh -c`, heredocs, scripts and SQL files it reads) | `TestIncidents`, `TestAsks`, `FuzzCheck` | A heuristic over text; a command it doesn't parse runs (`TestLimits`, SHELL_GUARD.md). |
| D | A11 removes the hook, edits PATH, or feeds it unreadable input | None: the guard fails open by design, so a broken guard never blocks work | T09, `TestPluginHook*` | An agent that can edit `.claude/settings.json` or PATH can turn it off. It is a second check, not a sandbox. |
| E | The guard answers allow | It never answers allow and never exits 2 | `TestGuardShell*` | None. |

### Head timestamps (B11)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A8 edits a token's time | The token's signature covers its time | T10, `TestOpenSSLTokens` | None known. |
| S | A8 makes a token with a TSA of their own | `--tsa-roots` pins the TSAs the reader trusts | T10 | Without `--tsa-roots` a token is checked against the chain it carries; `verify` warns `tsa_not_pinned` (T11). |
| R | Records sealed after the last timestamp are rewritten | `chain_tail_missing`, `timestamp_mismatch` | `internal/deed/stamp` tests | Only what was timestamped is covered; timestamp often. TSA revocation isn't checked. |

### Independent verifiers (B11)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A8 hands a changed bundle to a reader without the Go binary | `verifiers/python` and `verifiers/js`, written from `spec/` alone, refuse tampered copies; the workflow is SHA-pinned with `contents: read` | `independent-verifiers` (`crosscheck.py`), T12 (declared) | They skip timestamp checks. Not a required check yet. |
| D | A crafted archive (gzip bomb, path traversal) | gzip and path-traversal guards in the Python reader (`verifiers/python/bundle.py`) | the crosscheck's tampered copies | Standard-library only; no fuzzing of these readers yet. |

### `deedglass connect` (planned)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| I | A Postgres password passed as an argument shows in `ps` and shell history | Declared: the password comes from an environment variable or stdin, never argv | T13 (declared, waits on the code) | Not merged; this row is the requirement it ships against. |

### Releases (B13)

| | Threat | Control | Test | Residual |
|---|---|---|---|---|
| T | A12 swaps a release with matching checksums | `checksums.txt` signed with keyless cosign by `release.yml` for a `v*` tag; `install.sh` checks it when cosign is installed | `TestInstallScript`, `TestInstallScriptMirror` | Without cosign the script checks sha256 only and prints the signature command. The deedglass.com/dl copy is checked the same way; without cosign, trusting it is trusting deedglass.com, which served the script. |
| T | A release workflow change or an action update ships code | Every action pinned to a commit SHA; least-privilege tokens; CodeQL on the workflows | `make workflows-check`, `codeql` | A write credential can still push a tag until the founder's tag ruleset and npm reviewer exist (audit H-01, H-02). |
| E | macOS's quarantine is cleared for an unnotarized binary | The cask no longer clears it | `.goreleaser.yaml` | Notarization isn't set up yet. |

## Residual risks, in plain words

- A compromised Deedglass host can sign false deeds. Keep seal keys on a separate volume, rotate
  them, and have auditors pin keys.json from a copy they fetched earlier.
- A manipulated model acting within policy succeeds. Policies, budgets, approvals and Stop limit
  what that means; Deedglass records it.
- Description scanning is a heuristic. A reworded instruction in a tool description can reach the
  model; poisoned tool results aren't scanned at all.
- Writes outside the watched tables, or outside Postgres, are not seen by L2.
- A forwarded, unused approval link works once for whoever holds it, as the person it was sent to.
- A copy of the control-plane database together with `DEEDGLASS_VAULT_KEY` opens every credential.
- Any local process holding an agent's key acts as that agent, through the A2A proxy or directly.
- A foreign feed is compared as given: a consistent forged feed gives a consistent result.
- The shell guard fails open; an agent that can edit its own hook settings can turn it off.
- A timestamp checked without `--tsa-roots` shows the token is intact, not who signed it.

## Detail from earlier sessions

### Deeds (S04)

| Threat | What Deedglass does |
|---|---|
| A stored deed is edited (database superuser, backup restore, a bad migration) | Normal paths can't: the append-only trigger and the app role's grants refuse it. If they are bypassed, any verifier sees `body_hash_mismatch` or `chain_prev_hash_mismatch`; S06's `ledger check` names the first break. |
| A deed is removed from the middle of a chain | `chain_seq_gap` names the missing seq. |
| Deeds are removed from the end of a chain | Not visible in the chain alone. Exports record the head (S05, S06); keep the heads you receive. |
| Two deeds claim the same parent (a fork) | The chain's advisory lock and `UNIQUE (org_id, prev_hash)`: the second insert aborts. |
| A signature made for something else is presented as a seal | Domain tags (`deedglass/deed/v1\n`, `deedglass/bundle/v1\n`, `deedglass/intent/v1\n`); seal keys are never intent keys. |
| A small-order or malformed public key enters a key set | Refused at key-file load, at registration and when a verifier parses `keys.json` (A13). |
| The seal key is stolen | Out of scope to prevent. The thief can seal false deeds with any `issued_at`. Mitigations: 0600 key files, the seed is never logged, prod refuses test keys, rotation, and revocation (a revoked key fails every deed). KMS or HSM signing is planned. |
| A payload leaks through a deed | The schema is closed and holds digests only; free text is capped (agent name, tool, references, reason). |
| Canonicalization ambiguity (duplicate keys, 2^53+1, `1.0`, lone surrogates) | The strict profile refuses it before JCS; nothing is repaired. |
| Clocks disagree between instances | `issued_at` comes from the database clock under the chain lock and never decreases. |
| An instance keeps sealing after a rotation | It refuses a key that retires within 30 s; a restart with a retired key is refused. |
| `keys.json` served partially during an outage | 503 `service_unavailable`, never a partial list. |

### Signed intents, on-behalf-of and MCP (S09)

Detail: [INTENTS.md](INTENTS.md) §13, [IDENTITY.md](IDENTITY.md) (threat notes) and
[MCP_AUTHORIZATION.md](MCP_AUTHORIZATION.md).

| Threat | What Deedglass does |
|---|---|
| An agent's API key leaks (`.env`, CI logs, an MCP config) | For an agent in `required` mode, every call without a valid signed intent is refused (`intent_required`). Otherwise the holder acts as the agent until the key is revoked. |
| A proxy or middlebox changes an argument | `args_digest` covers the exact arguments: `args_mismatch`. |
| A captured request is replayed | The nonce's primary key: `intent_replay`. Nonces are pruned only after the intent can no longer be accepted (`expired` first). |
| Algorithm confusion | No `alg` field; Ed25519 only; the signed input is domain-separated (`deedglass/intent/v1\n`). |
| Weak or malicious intent keys | Small-order, non-canonical and off-curve keys are refused at registration (422), and stored keys are parsed again on every read. |
| JSON ambiguity (duplicates, big integers, non-canonical bytes) | The strict parser runs before JCS; the intent's bytes must equal their JCS form: `intent_invalid`. |
| Clock abuse | 60 seconds of skew; a lifetime of at most 5 minutes. |
| On-behalf-of spoofing | A signed `on_behalf_of` is bound to the agent's key and a differing header is refused; an unsigned one is recorded as the agent's claim. v2 verifies people against the org's identity provider. |
| Header smuggling | One value per OBO header, a strict grammar (422), and Go refuses CR and LF. |
| A client's token passed to an upstream MCP server | `StripInboundCredentials`, which S15 must call on every forwarded request. |
| DNS rebinding against `/mcp/*` | A browser `Origin` other than the base URL's is refused with 403. |
| Enumerating connectors through the metadata | The same document for every well-formed id, with no database read. |
| An intent error starts an OAuth flow in a client | Intent errors are 403 `permission_error`, never 401. |
| **The agent's runtime is compromised** | **Not defended by intents:** the attacker holds both keys. Policies, budgets, approvals and Stop still apply. |
| A prompt-injected agent signs a harmful, well-formed action | Not an intent problem: the signature records what was meant. Policy decides (S10 to S12). |

## What this page does not do

It is not a penetration-test report. Each row names the control and the test that exercises it;
the review ([SECURITY_REVIEW_2026-10.md](SECURITY_REVIEW_2026-10.md)) records what was checked and
found, and [LIMITATIONS.md](LIMITATIONS.md) every known limit.
