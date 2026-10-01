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

## What this page doesn't do

It doesn't hold every error: the full catalog, with when each code happens, is
[api/errors.md](api/errors.md). Screens not built yet add their sections when their session writes
the copy.
