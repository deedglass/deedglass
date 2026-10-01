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

## What this page doesn't do

It doesn't hold every error: the full catalog, with when each code happens, is
[api/errors.md](api/errors.md). Screens not built yet add their sections when their session writes
the copy.
