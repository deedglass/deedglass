# Copy catalog

One place for the words people see in the Console, the API and the CLI, so every surface says the
same thing the same way. The Console lifts its strings verbatim from here into
`web/src/lib/copy.ts` (S25, S26). API messages are rendered by `internal/httpx/errors.go` and the
call sites named below; when the code and this page differ, the code is right and this page is
fixed. Voice rules: `DEEDGLASS_BRAND.md` §3 and §4 (calm, exact, sentence case, a next step on every
error, no exclamation marks, no emoji).

## Agents & Stop

### Console (S08 §5.2; S25 and S26 build the screens)

| Surface | Copy |
|---|---|
| Agents, empty | `No agents yet. Connect your first agent in about two minutes.` · button `Create agent` |
| Chips | `Active` (neutral) · `Suspended` (neutral, pause icon) · `Stopped` (red outline, Stop icon) · `Test` (outline badge on test keys) |
| New key sheet | title `Copy your new key` · `This is the only time Deedglass shows it. Deedglass stores only a hash, so it can't show it again.` · checkbox `I've stored this key in a secret manager` · `Done` (disabled until the box is checked) |
| Stop sheet | title `Stop support-bot?` · `Deedglass will refuse every call from support-bot, on every instance, within 2 seconds. Calls already running at a tool finish; nothing new starts.` · `Type support-bot to confirm` · `Reason (optional, recorded in the deed)` · red `Stop agent` · `Cancel` |
| After a stop | toast `support-bot stopped · deed sealed` · link `View deed` |
| Stop all sheet | title `Stop all agents in Acme?` · `Deedglass will refuse every agent call in Acme within 2 seconds. Only an owner can start them again.` · `Type acme to confirm` · `Reason (optional, recorded in the deed)` · red `Stop all agents` · `Cancel` |
| Org stopped banner | `All agents in Acme are stopped · since 14:05 · by Priya Raman · "Investigating unexpected refunds"` · `Start all agents` (owners only) |
| Start all sheet | title `Start all agents in Acme?` · `Agents that were stopped one by one stay stopped. Everything else can act again as soon as you confirm.` · required `Why is it safe to start again?` · primary (not red) `Start all agents` · `Cancel` |
| Roll sheet | `The old key keeps working for` [`Now` · `1 hour` · `24 hours` · `7 days`] `so you can deploy the new one first.` · primary `Roll key` · `Cancel` |
| Agents, loading | skeleton rows in the table's shape; no copy |
| Agents, error | `Deedglass couldn't load agents. Retry, and share request {req_…} if it keeps happening.` · button `Retry` |
| Agents, no permission (viewer) | Stop, Suspend and key buttons are shown disabled with the tooltip `Stopping agents needs Admin. Ask an owner to change your role.` (the API's `insufficient_role` sentence) |
| During Stop all, admin-key callers | API only: reads still answer; changes get the `org_stopped` admin-key message below |

**Destructive-confirm rules** (S08 Design QA, from the `linear.app` and `sentry` references:
one primary action per sheet, functional copy that reads like a log, status in text and not only
colour):
- The confirm button repeats the verb and object (`Stop agent`, `Stop all agents`), never
  `Confirm` or `OK`.
- The typed-confirmation field must match exactly (case-sensitive) before the red button enables;
  `Cancel` has the initial focus, and Escape closes the sheet without acting.
- The body says what happens, how fast, and what it doesn't touch, in at most two sentences.
- The reason field says where it goes (`recorded in the deed`) so people know it's permanent.
- Chips and the banner always pair the colour with a word and an icon (brand §6.3).
- **Kept as written:** the empty state's `Connect your first agent` and its button `Create agent`
  use two verbs; the sentence is the brand doc's binding example (§4), so it stays.

Stop uses red only on the button, the chip and the banner rule, never as a fill across the page
(brand §6.3). The "Sealed" motion isn't used for Stop; it belongs to verified deeds.

### API errors, as rendered

`{…}` is filled at the call site. Names are clipped to 64 characters, org names to 100.

| Code | Status | Message |
|---|---|---|
| `invalid_api_key` | 401 | `The API key isn't valid. Check that you copied all of it (it starts with dg_live_ or dg_test_), or create a new one in Console › Agents.` |
| `invalid_api_key` (another scheme) | 401 | `The Authorization header must be Bearer followed by an API key (dg_live_… or dg_test_…).` |
| `api_key_revoked` | 401 | `The API key has been revoked. Create a new key in Console › Agents.` |
| `api_key_expired` | 401 | `The API key expired at {2026-10-04 10:03} UTC after it was rolled. Use the new key, or create one in Console › Agents.` |
| `agent_suspended` | 403 | `Agent "{name}" is suspended. An admin can resume it in Console › Agents.` |
| `agent_stopped` | 403 | `Agent "{name}" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.` |
| `org_stopped` | 403 | `All agents in {org} are stopped. An owner can start them again in Console › Settings.` |
| `org_stopped` (an admin key changing something during Stop all) | 403 | `All agents in {org} are stopped, so admin keys can only read until an owner starts them again in Console › Settings. To change agents now, use a Console session.` |
| `insufficient_scope` | 403 | `This key doesn't have the {scope} scope. Create a key with {scope} in Console › Agents.` |
| `insufficient_scope` (an agent key on a management route) | 403 | `This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server.` |
| `rate_limited` (invalid keys) | 429 | `Too many requests with invalid API keys from this address. Try again in {n} seconds.` |
| `validation_failed` (name) | 400 | `Agent names use lowercase letters, digits and hyphens, 2 to 63 characters, like support-bot.` |
| `validation_failed` (reserved name) | 400 | `{name} is reserved for Deedglass. Pick another name, like support-bot.` |
| `validation_failed` (rename) | 422 | `Agent names can't change, because policies match on them. To rename, create a new agent with the new name and move its keys.` |
| `validation_failed` (start reason) | 400 | `Starting needs a reason of 3 to 280 characters, saying why it's safe now. It goes into the deed.` |
| `validation_failed` (start-all reason) | 400 | `Starting all agents needs a reason, saying why it's safe now. It goes into the deed.` |
| `conflict` (name) | 409 | `An agent named {support-bot} already exists in {Acme}. Pick another name.` |
| `conflict` (keys) | 409 | `{support-bot} already has 10 active keys. Revoke one before creating another.` |
| `conflict` (resume a stopped agent) | 409 | `{support-bot} is stopped, not suspended. Start it with POST /v1/agents/{id}/start.` |
| `conflict` (roll a revoked key) | 409 | `Key {dg_live_jJbEEMWk} is revoked, so it can't be rolled. Create a new key instead.` |
| `insufficient_role` (admin key on start-all) | 403 | `This is an admin key, which acts as Admin, and starting all agents needs an owner signed in to the Console. Sign in to the Console, or run the operator command on the server.` |
| `insufficient_role` (admin key on `POST /v1/org/keys`) | 403 | `This is an admin key, which acts as Admin, and creating admin keys needs an admin signed in to the Console. Sign in to the Console, or run the operator command on the server.` |
| `insufficient_role` (admin key rolls an admin key) | 403 | `Admin keys can't roll admin keys, because that creates one. Sign in to the Console as an admin, or run deedglass org keys create on the server.` |
| `internal_error` (stop or stop all didn't commit) | 500 | `Deedglass couldn't record the stop, so nothing changed. Try again. If it keeps failing, revoke the agent's keys.` |
| `service_unavailable` (key check, database unreachable) | 503 | `Deedglass can't check API keys right now because its database is unreachable. Retry in a few seconds.` |

### CLI

The CLI's own strings live in `cmd/deedglass/agentcmd.go`; `cmd/deedglass/testdata/agent/*.golden`
pins them. Examples are in [AGENTS.md](AGENTS.md) and [STOP.md](STOP.md).

## Agents › Security (S09; S25 builds it)

### Console

| Surface | Copy |
|---|---|
| Section title | `Signed intents` |
| Explainer | `When an agent signs its intents, Deedglass checks that each call is exactly what the agent's runtime asked for, and refuses replays. A leaked API key alone can't act for this agent.` |
| Mode control | segmented `Off` · `Optional` · `Required` · helper under Required: `Calls without a valid signature are refused. MCP clients can't sign yet.` |
| Keys, empty | `No intent keys yet. Generate one on the machine that runs the agent: deedglass agent intent keygen support-bot` |
| Key row | `kid 21fe…d227 · added 2h ago by Priya Raman` · `Remove` |
| Deed detail | `Intent · signed · sha256:87c7…3089` or `Intent · not signed` (a neutral chip, never red: an unsigned call in `off` or `optional` mode is normal) |

Kids and hashes show their first and last four hex characters in the Console; the full value is
one click away (copy button), never truncated in exports or JSON.

### API errors, as rendered

The signed-intent refusals (`intent_required`, `intent_invalid`, `intent_replay`) and the
on-behalf-of `validation_failed` messages are listed with their reasons and params in
[INTENTS.md](INTENTS.md) §6 and [api/errors.md](api/errors.md). The source is `IntentCopy` and
`ErrOBOInvalid` in `internal/httpx/errors.go`. Intent-key registration adds:

| Code | Status | Message |
|---|---|---|
| `validation_failed` (small-order key) | 422 | `That public key is a small-order point, so a single signature would verify for every message. Generate a fresh key with deedglass agent intent keygen {support-bot}.` |
| `validation_failed` (non-canonical key) | 422 | `That public key isn't a canonical Ed25519 encoding, so two different strings would name the same key. Generate a fresh key with deedglass agent intent keygen {support-bot}.` |
| `validation_failed` (off the curve) | 422 | `That public key isn't a point on the Ed25519 curve. Generate a fresh key with deedglass agent intent keygen {support-bot}.` |
| `validation_failed` (format) | 422 | `The public key must be "ed25519:" followed by 43 base64url characters (32 bytes), like ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo. Generate a fresh key with deedglass agent intent keygen {support-bot}.` |
| `validation_failed` (no key sent) | 400 | `Send the public key as "ed25519:" followed by 43 base64url characters. deedglass agent intent keygen prints it.` |
| `conflict` (third key) | 409 | `{support-bot} already has two intent keys. Remove one first; two lets you rotate without downtime.` |
| `conflict` (same key twice) | 409 | `{support-bot} already has the intent key {21fe…d227}. Keys are identified by their kid, so the same key can't be added twice.` |
| `conflict` (required with no keys) | 409 | `{support-bot} has no intent keys yet, so requiring signed intents would refuse every call. Register a key first: deedglass agent intent add {support-bot}.` |
| `conflict` (removing the last key while required) | 409 | `{support-bot} requires signed intents, so removing its last key would refuse every call. Add the new key first, or set intent_mode to optional.` |
| `not_found` (unknown kid) | 404 | `{support-bot} has no intent key {kid}. List its keys: GET /v1/agents/{agt_…}/intent-keys.` |
| `validation_failed` (mode) | 400 | `intent_mode must be off, optional or required.` |
| `validation_failed` (verify, no header) | 422 | `Send a Deedglass-Intent header to check.` |
| `validation_failed` (verify, tool) | 400 | `tool must be the connector.tool name the Gate uses, like stripe.refund.` |
| `validation_failed` (verify, args) | 400 | `Send args: the exact JSON arguments the intent was signed over ({} for none).` |

### CLI

The strings live in `cmd/deedglass/intentcmd.go`. Hashes and kids show in the brand short form
(`21fe…d227`) except in `--json` and the `--explain` detail lines, which print them in full.

| Command | Copy |
|---|---|
| `agent intent keygen` | `✓ Intent key created · {support-bot} · kid {kid}` · `Private key  {.deedglass/intent/support-bot.pem} (mode 0600). Keep it with the agent; it never goes to Deedglass.` · `Public key   {ed25519:…}` · `Next: deedglass agent intent add {support-bot} --public-key {ed25519:…}` |
| `agent intent keygen`, file exists | `✕ {path} already exists.` · `Keep using it, or pass --force to replace it (register the new key before you remove the old one).` |
| `agent intent add` | `✓ Intent key registered · {support-bot} · kid {21fe…d227} · {deed_…} sealed` |
| `agent intent add`, mode off | `{support-bot} doesn't check signed intents yet. Check them when sent, or require them:` · `deedglass agent intent mode {support-bot} optional\|required` |
| `agent intent add`, mode optional | `Signed intents stay optional for {support-bot} until you require them:` · `deedglass agent intent mode {support-bot} required` |
| `agent intent add`, mode required | `{support-bot} already requires signed intents; calls signed with this key are accepted now.` |
| `agent intent add`, no key | `✕ deedglass agent intent add needs --public-key.` · `Generate a key with deedglass agent intent keygen {support-bot}, then pass the public key it prints.` |
| `agent intent list` | `{support-bot} · intent mode {optional}`, then `KID  PUBLIC KEY  ADDED  BY` |
| `agent intent list`, empty | `No intent keys yet. Generate one on the machine that runs the agent: deedglass agent intent keygen {support-bot}` |
| `agent intent remove` | `✓ Intent key removed · {support-bot} · kid {21fe…d227} · {deed_…} sealed` · `Every Deedglass instance refuses intents signed with it within 2 seconds.` |
| `agent intent mode … required` | `✓ {support-bot} now requires a signed intent on every call · {deed_…} sealed` · `Calls without a valid Deedglass-Intent header are refused with intent_required.` · `MCP clients can't sign intents yet, so {support-bot} can act only through the SDKs or the REST API.` |
| `agent intent mode … optional` | `✓ {support-bot} now checks signed intents when they are sent · {deed_…} sealed` · `Calls without a Deedglass-Intent header are still accepted; an invalid one is refused.` |
| `agent intent mode … off` | `✓ {support-bot} no longer checks signed intents · {deed_…} sealed` · `Deedglass-Intent headers from {support-bot} are ignored, and its deeds record intent_signed false.` |
| `agent intent mode`, unchanged | the same first line, ending `· no change, no new deed` |
| `agent intent mode`, bad mode | `✕ "{maybe}" isn't a mode.` · `Use off, optional or required.` |
| `dev sign-intent --explain`, last line | `✓ Valid for {60 s} · nonce {05c1…0729} · works once` |
| `dev sign-intent`, no org or agent | `✕ deedglass dev sign-intent needs --org and --agent, or the agent's API key.` · `Pass --org org_… --agent agt_…, or set DEEDGLASS_API_KEY to the agent's key so whoami can name them.` |
| `dev sign-intent`, admin key | `✕ this API key is an org admin key, and intents are signed for one agent.` · `Pass --agent agt_…, or use the agent's own key.` |
| `dev sign-intent`, `--ttl` | `✕ --ttl must be more than 0 and at most 5m.` · `Sign one intent per call; the default is 60s.` |
| `dev sign-intent`, `--obo` | `✕ --obo must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -.` · `Use an opaque user id such as usr_ext_4821, not a name or email.` |
| `dev sign-intent`, key unreadable | `✕ Can't read {x.pem}: {no such file or directory}.` · `Generate a key with deedglass agent intent keygen <agent>.` |

## MCP tool results (S14)

What a model reads when `deedglass mcp wrap` answers for the server. Source of truth:
`internal/gate/present.go` (the `MCP*` constants and functions); goldens for every result, in both
MCP eras, in `testdata/mcp/present/`.

| Situation | Text (`isError: true` unless noted) |
|---|---|
| Denied by a rule | `Blocked by policy "<rule>": <reason> Nothing was run. Deedglass sealed a deed of this attempt (deed_…). If this should be allowed, a person can change the policy in Console › Policies.` |
| Waiting for approval | `Waiting for approval under policy "<rule>": <reason> Nothing has run yet. Approval apr_… expires at HH:MM UTC. To continue, call deedglass.check_approval with {"approval_id": "apr_…"}.` |
| Still waiting (`isError: false`) | `Still waiting for approval apr_…. It expires at HH:MM UTC. Nothing has run yet. Check again in a minute.` |
| Approved and ran | `Approved by a person. Ran <connector>.<tool>.`, then the tool's own content |
| Agent stopped, budget | S08's and S11's sentences, then `Nothing was run.` |
| Gate unreachable | `Deedglass could not be reached at <url>, so this was not run. Start it with "deedglass serve", then try again.` |
| Bad key | `Deedglass did not accept the API key for this MCP server, so nothing was run. Create a key in Console › Agents and update your MCP client config.` |
| Definition changed | `The server changed the definition of <tool> after it was first listed, so Deedglass hid it. Nothing was run. Restart the MCP server to accept the new definition after a person reviews it.` |
| Server stopped mid-call | `The MCP server stopped while running <tool>. It may have run. Deedglass recorded it as UNVERIFIABLE (act_…).` |
| Server keeps crashing | `The MCP server stopped 5 times in a minute, so Deedglass stopped restarting it. Nothing was run. Check the server's log, then restart your MCP client.` |
| Instructions line | `Tool calls on this server pass through Deedglass. Some may be blocked by policy or wait for a person to approve them; the result will say so. Use deedglass.check_approval to continue after an approval.` |

## MCP HTTP endpoints (S15)

Source of truth: `internal/gate/present.go` (the S15 block) and `internal/identity/mcp.go`
(`MCPOriginMessage`, `MCPNeedKeyMessage`); `httpgw`'s conformance test pins each one.

| Situation | Copy |
|---|---|
| 401 (no key) | `Deedglass needs an agent API key. Send it as "Authorization: Bearer dg_…".` |
| 403 Origin | `This origin is not allowed to call Deedglass MCP endpoints. Add it to mcp.allowed_origins if it should be.` |
| `-32022` | `Deedglass supports MCP 2026-07-28, 2025-11-25, 2025-06-18 and 2025-03-26. Retry with one of those.` |
| `-32020` | `Header mismatch: Mcp-Name is "echo" but the body names "delete_all". Send the same tool name in both.` (and the missing and undecodable forms) |
| Unknown connector | `No MCP connector named "evrything". Check the URL; configured connectors are listed in Console › Connectors.` |
| Upstream down (tool result) | `The "everything" MCP server did not answer, so nothing was run. Deedglass will keep trying; check that it is running.` |
| Still running | `Still running. Deedglass will record the outcome (act_…).` |
| Too many in flight (429) | `This agent already has 32 MCP requests in flight. Wait for one to finish, then try again.` |
| 406 | `Deedglass answers MCP requests as application/json. Send Accept: application/json, text/event-stream.` |
| Batch or response body | `Send one JSON-RPC request or notification per POST. Batches and responses are not accepted.` |
| Unknown approval | `No approval apr_… for this agent. Use the approval_id from the Waiting for approval message.` |
| Unknown method | `Deedglass doesn't serve "tasks/list" on this endpoint. Tools are available through tools/list and tools/call.` |
| Client cancelled (error message on the action) | `The MCP client went away while the tool ran, so Deedglass cancelled it. It may have run.` |
| `deedglass mcp serve`, server down | the S14 `could not be reached` sentence |

## Connectors (S16; S26 builds the screen)

### Console

| Where | String |
|---|---|
| Empty state | No connectors yet. Add your first API in about two minutes: a base URL, one tool, one credential. |
| Credential row | Set · bearer · …9f2c · updated 2h ago, with **Replace** and **Remove** |
| Credential row, none | No credentials yet. Agents' calls fail as credentials_missing until you store one. |
| Under the credential form | Agents never see this value. Deedglass adds it only when the Gate runs a call. |
| Health | Passed · 4 checks · tested 5m ago / Failed · destination blocked |

### API errors, as rendered

| Code | Message |
|---|---|
| `credentials_missing` | The connector "orders" has no credentials yet. Run deedglass connector credentials set orders --from-env ORDERS_TOKEN. |
| `destination_blocked` (agent) | The connector's destination is blocked by Deedglass's network rules. An admin can see why in Console › Connectors. |
| `destination_blocked` (admin) | base_url resolves to 169.254.169.254, a cloud metadata address. Deedglass never connects to cloud metadata addresses, even in dev. Point base_url at the service's public address. |
| `vault_unavailable` (agent) | Deedglass couldn't open this connector's credentials. An admin has been alerted. |
| `vault_unavailable` (admin) | 2 credentials use kek:77c0e1a9b442, which isn't in DEEDGLASS_VAULT_KEY. Add it back as the second key, restart, then run deedglass vault rotate. |
| `validation_failed` (side effect) | tools[2] is a DELETE, so its side effect can't be "read". Use "destructive", or leave side_effect out. |
| insufficient_scope (agent key) | Agents can't list, create or read connectors. Use the Console or an admin key. |

### CLI

| When | String |
|---|---|
| Empty `--from-env` | ORDERS_TOKEN is empty or unset. Export it in this shell and run the command again. Deedglass never reads secrets from flags. |
| Created | ✓ Connector created · orders · con_… / 2 tools · orders.get (read) · orders.update_status (write) / ○ No credentials yet. Next: deedglass connector credentials set orders --from-env ORDERS_TOKEN |
| Stored | ✓ Credentials stored · orders · bearer · …9f2c · encrypted with kek:3f9a1c2b7d8e / Agents never see this value. Deedglass adds it only when the Gate runs a call. |
| Rotation | ◐ Rewrapping data keys · 2 of 2 / ✓ All 42 credentials use kek:3f9a1c2b7d8e. You can remove kek:77c0e1a9b442 from DEEDGLASS_VAULT_KEY. |

## Postgres readiness (S17; S26 builds the panel)

### Console

| Where | String |
|---|---|
| Panel title, ready | Ready for L2 |
| Panel title, not ready | Not ready for L2 · 2 items to fix |
| Empty state | Connect a Postgres database to see what your agents actually changed. |
| A check with `fix_sql` | **Copy SQL** button under the fix sentence |
| Vocabulary | the **witness** role · **Ready for L2** · **watched tables** |

Each warning says what breaks and what to do next, as `check` prints it (`internal/connector/postgres/setup/check.go` is the one source).

### Agent messages (execution.error_message)

| Code | Message |
|---|---|
| `statement_refused` | Refused by the Postgres connector: the statement has 2 commands. Deedglass runs one statement per action. |
| `statement_refused` | postgres.query runs read-only statements. Use postgres.execute for UPDATE. |
| `statement_refused` | Statements can't reference schema deedglass. |
| `row_limit_exceeded` | The statement changed 1,204 rows, over this connector's limit of 1,000. Nothing was committed. Narrow the WHERE clause. |
| `database_role_unsafe` | The connector's database role is a superuser. Deedglass refuses to run agent SQL with it. Run deedglass connector postgres init to create deedglass_agent. |
| `database_not_ready` | Deedglass isn't set up in this database yet. Run deedglass connector postgres init --connector postgres. |
| `timeout` | The statement ran past this connector's 5 s limit, so Postgres stopped it. Nothing was committed. |

### CLI

| When | String |
|---|---|
| Apply | ◐ Applying 7 changes in one transaction · lock_timeout 5s / ✓ Applied · 7 changes · 212 ms |
| Stored | ✓ Credentials stored · postgres · deedglass_agent@127.0.0.1:5432/demo · deedglass_witness@127.0.0.1:5432/demo / Passwords were generated here and sent to Postgres as SCRAM verifiers. They weren't printed or logged. |
| Unchanged | Nothing to change. |
| Remote without --confirm | demo on db.example.com is remote and not marked throwaway. / Review the plan, then run again with --apply --confirm demo. |

## The decoder (S20)

Source of truth: `internal/decoder/present.go` (Console) and `cmd/deedglass/decoder.go`
(`decoderWords`: the CLI's status lines). The user-facing noun is **decoder**; what it produces are
**frames**, and a frame is **bound** to an action. Say **unattributed**, not "unbound". No verdict
words: the decoder doesn't decide.

### Console (S26 renders them)

| Surface | Copy |
|---|---|
| Decoder row | `Witness · healthy · 3.9 kB behind · last commit 4s ago` |
| Empty state | `This connector isn't witnessed yet. Turn on Witness to see what Postgres actually committed for every agent write. It needs wal_level=logical and a publication; the check below walks you through it.` |
| Alert toast (forged marker) | `Forged marker on postgres: a transaction claimed to be a Deedglass action without a ledger row. Nothing was bound to it.` |
| Alert toast (forged message) | `Forged message on postgres: a message outside any transaction claimed to come from Deedglass. Nothing was bound to it.` |
| Alert toast (foreign ledger) | `Ledger row from another org on postgres: a transaction named an action of a different org. Nothing was bound to it.` |

### CLI and halts (one line plus a next step)

| Reason | Copy |
|---|---|
| `publication_missing` (16, 17) | `Postgres can't read past a point where deedglass_pub didn't exist. Re-create it (deedglass connector postgres init postgres), then drop and re-create the slot: deedglass decoder drop --connector postgres.` |
| `publication_changed` (18) | `deedglass_pub changed while Deedglass was reading. Postgres 18 skips row changes it can't publish, so actions from that moment are marked with an evidence gap.` |
| `malformed_evidence` | `Deedglass stopped reading at 0/3E65020: a message didn't match the pgoutput format (type M, byte 41). Nothing after it was used. This is rare; please send deedglass decoder status --json to support.` |
| `session_settings_changed` | `The decoder's session settings changed (TimeZone is "Pacific/Chatham"). Deedglass stopped so every value stays exact. Restart deedglass serve; check ALTER ROLE deedglass_witness.` |
| `lag_high` | `lag 611 MB, above the 512 MB alert` / `The slot is holding WAL on the watched database. Deedglass is still reading; it is behind.` / `Next: check deedglass serve is running and healthy, or raise DEEDGLASS_SLOT_LAG_ALERT_MB if this is expected.` |
| `slot_invalidated` | `slot invalidated at 2026-10-07 07:40 UTC (wal_status lost)` / `Actions waiting on L2 for analytics won't get that evidence (slot_invalidated).` / `Next: deedglass decoder drop --connector analytics, then check max_slot_wal_keep_size (docs.deedglass.com/decoder#sizing)` |
| Drop | `✓ Slot dropped · deedglass_01j9zq7c… · admin deed deed_01j9zv8k… · a new slot starts on the next sync` |
| API, not witnessed | `This connector isn't witnessed, so it has no decoder. Turn on witness.enabled in its config first.` |

## L2: expected and observed (S21; S25 builds the Witness panel)

The strings live in `internal/witness/l2/summary.go` (`Copy*`) and `internal/witness/l2/render.go`.

| Where | Copy |
|---|---|
| Diff column headers | `Expected` · `Observed` |
| Row badges | `expected` / `unexpected` |
| Footer | `Witnessed by Postgres · commit {lsn} · {time}` |
| Truncation line | `Showing the first 20 of 301 changed rows.` |
| Amendment banner | `This action has a later verdict. The original deed is unchanged; the amendment deed records what Postgres showed afterwards.` |
| Empty state (a Postgres connector without the witness) | `Turn on Witness to compare what the agent reported with what Postgres committed.` |
| After an UNVERIFIABLE that may still change | `Deedglass keeps watching. If Postgres's change log shows it later, an amendment deed records the result.` |
| MCP tool result, not VERIFIED | `Deedglass witness · L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.` |
| MCP tool result, still waiting | `Deedglass witness · pending · Postgres's change log hasn't shown this write yet. The verdict will appear in Console › Actions.` |

The "Sealed" moment (brand §10) for S25 and the videos: a VERIFIED verdict closes the Mirror's two
halves with one soft teal pulse; a MISMATCH stops the halves short with a hairline red gap, and
never shakes.

## What this page doesn't do

It doesn't hold every error: the full catalog, with when each code happens, is
[api/errors.md](api/errors.md). Screens not built yet add their sections when their session writes
the copy.
