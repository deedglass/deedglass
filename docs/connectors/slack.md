# The Slack connector

The `slack` connector lets agents post and react in the Slack channels you allow, and read recent
messages in the ones you allow them to read. It also carries **approvals in Slack**: the approval
card, the Approve and Deny buttons, and the signed requests behind them
([APPROVALS.md › Approve in Slack](../APPROVALS.md)).

Deedglass acts as a **bot**, never as a person. Every post carries the action id in Slack's message
metadata, so the Witness can read the post back and confirm it at **L1**.

## Create the Slack app

1. Print the manifest with your public URL filled in:
   ```sh
   deedglass slack manifest --base-url https://YOUR-HOST
   ```
   For local development, interactivity needs a public HTTPS URL. A Cloudflare quick tunnel works:
   `cloudflared tunnel --url http://127.0.0.1:8080`, then use the `https://….trycloudflare.com` URL
   it prints. The quick-tunnel URL changes on every run; update the app's Interactivity URL when it
   does.
2. At [api.slack.com/apps](https://api.slack.com/apps): **Create New App › From a manifest**, pick
   the workspace, paste the manifest, then **Install to Workspace**.
3. Note the app id (**Basic Information**, `A…`) and the workspace id (`T…`; `deedglass connector
   test slack` prints it too).
4. Invite the bot to each channel it uses: `/invite @deedglass` in `#agent-updates` and in
   `#deedglass-approvals`.

## The two credentials

The connector's credential has kind `fields` with two values. Both are stored in the vault and
opened only when a call runs; neither is ever shown to an agent or written to a log.

| Field | Where Slack shows it | What Deedglass uses it for |
|---|---|---|
| `bot_token` | **OAuth & Permissions › Bot User OAuth Token** (`xoxb-…`) | Every Web API call: posts, reactions, reads, the read-backs, the approval card. Sent as `Authorization: Bearer`. |
| `signing_secret` | **Basic Information › App Credentials › Signing Secret** (32 hex characters) | Checking that a button click really comes from Slack (HMAC-SHA256 over the request). |

```sh
export SLACK_BOT_TOKEN=…          # in your shell, never in a file you commit
export SLACK_SIGNING_SECRET=…
deedglass connector create --config examples/connectors/slack.yaml
deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET
deedglass connector test slack
```

Deedglass refuses the wrong kinds of token before anything is sent:

- `xoxp-…`: "That is a user token. Deedglass acts as a bot, not as a person. Use the Bot User OAuth Token (xoxb-)."
- `xapp-…`: "That is an app-level token for Socket Mode, which Deedglass doesn't use yet."
- A signing secret that isn't 32 lowercase hex characters never verifies a request.

## Scopes, and why each is needed

The manifest asks for nine bot scopes and nothing more. `connector test` compares them with the
`x-oauth-scopes` header Slack returns.

| Scope | Why |
|---|---|
| `chat:write` | `post_message`, and posting and updating the approval card. |
| `channels:read` | Resolving `#name` to a channel id, checking the bot is a member, and the `request.slack` facts for public channels. |
| `groups:read` | The same for private channels the bot was invited to. |
| `channels:history` | `read_channel`, and reading a post back for L1, in public channels. |
| `groups:history` | The same in private channels. |
| `reactions:read` | Reading a reaction back for L1 (`reactions.get`). |
| `reactions:write` | `add_reaction`. |
| `users:read` | Approvals: `users.info` says whether the clicker is deactivated, a bot, or a guest. |
| `users:read.email` | Approvals: the confirmed email that matches a Slack user to a person in your org. |

## Config

```yaml
type: slack
name: slack
config:
  team_id: T0123ABCD               # checked against auth.test
  app_id: A0123ABCD                # from the app's Basic Information page; checked via bots.info
  channels: ["#agent-updates"]     # post allow-list (#name or C… id); ["*"] = any channel the bot is in
  read_channels: ["#agent-updates"]
  max_read_messages: 20            # ≤ 100
  approvals:
    enabled: true                  # at most one live Slack connector per org has this on
    channel: "#deedglass-approvals"
    require_email_domains: []      # e.g. [acme.com]; empty = any confirmed email
```

| Key | Rules |
|---|---|
| `team_id` | Required. `^T[A-Z0-9]{2,20}$`. The test fails when the token belongs to another workspace. |
| `app_id` | Required. `^A[A-Z0-9]{2,20}$`. The test fails when the token belongs to another app. |
| `channels` | Where `post_message` and `add_reaction` may act: `#name` (lowercased) or a `C…`/`G…` id, at most 100. `["*"]` alone means any channel the bot is a member of. Empty means none. |
| `read_channels` | Where `read_channel` may read, in the same form. |
| `max_read_messages` | 1 to 100, default 20. `read_channel`'s `limit` can't go higher. |
| `approvals.enabled` | Default `false`. |
| `approvals.channel` | Required when approvals are on: where the cards are posted. |
| `approvals.require_email_domains` | Lowercased. When set, only Slack users whose confirmed email is in one of these domains can approve. |
| `api_base_url` | Default `https://slack.com/api`, and nothing else outside dev. |

Unknown keys are refused with the key's path, so a typo never silently does nothing.

## Channels are compared by id

Names change; ids don't. Deedglass resolves `#name` (in your config and in an agent's arguments) to a
channel id through `conversations.list`, cached for 5 minutes per connector, and the allow-list
compares **ids**:

- List channels by id (`C0123ABCD`) when renames matter. If `#agent-updates` is renamed to
  `#agent-news`, a post to `#agent-news` is still allowed, because the id is the same.
- If someone then creates a new `#agent-updates`, it has a new id, so it can't slip through.
- A channel outside the list is `validation_failed`, and nothing is sent to Slack.

## Catalog

The table below comes from `GET /v1/connector-types/slack` (`deedglass connector types --type slack`).

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**Slack** · beta · levels L0 L1

Posts, reactions and approvals in the Slack channels you allow.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `slack.list_channels` | read | L0 | Not read back |
| `slack.read_channel` | read | L0 | Not read back |
| `slack.post_message` | write | L0 L1 | Slack shows the message in that channel with the same text and this action's id in its metadata. |
| `slack.add_reaction` | write | L0 L1 | Slack lists the reaction on that message by this bot. |

| Tool | What it does |
|---|---|
| `slack.list_channels` | List the public and private channels the bot is in (at most 200). |
| `slack.read_channel` | Read recent messages of an allowed channel: ts, user, text (up to 4 KiB each) and thread_ts. |
| `slack.post_message` | Post a message as the bot in an allowed channel, optionally in a thread. Links are not unfurled. |
| `slack.add_reaction` | Add an emoji reaction as the bot to a message in an allowed channel. A reaction already there counts as done. |

### Credentials

Kind `fields`:

- `bot_token` (secret): The Bot User OAuth Token from the app's OAuth & Permissions page. Starts with `xoxb-`.
- `signing_secret` (secret): From the app's Basic Information page: 32 hex characters.

### Setup

1. **Create the Slack app from the manifest.** https://api.slack.com/apps
   `deedglass slack manifest --base-url https://your-host`
2. **Create the connector.**
   `deedglass connector create --config examples/connectors/slack.yaml`
3. **Store the bot token and signing secret.**
   `deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET`
4. **Test the connection.**
   `deedglass connector test slack`

### What it doesn't do

- Only channels in the config, compared by id.
- No direct messages to people, file uploads or message deletes.
- Approvals map a Slack account to a person only by a confirmed email address.

<!-- deedglass:generated end -->

## Tools

| Tool | Side effect | Slack call | L1 check |
|---|---|---|---|
| `slack.list_channels` | read | `conversations.list` (public and private channels the bot is in, at most 200) | none |
| `slack.read_channel` | read | `conversations.history` (`limit` ≤ `max_read_messages`); each message is `{ts, user, text, thread_ts}`, text cut at 4 KiB | none |
| `slack.post_message` | write | `chat.postMessage` with `unfurl_links: false`, `unfurl_media: false` and `metadata: {event_type: "deedglass_action", event_payload: {action_id}}`; text at most 4,000 characters; optional `thread_ts` | Slack shows the message in that channel with the same text and this action's id in its metadata. |
| `slack.add_reaction` | write | `reactions.add`; `already_reacted` counts as done | Slack lists the reaction on that message by this bot. |

Every input schema is closed: an argument the tool doesn't take is refused. A post is never retried
(Slack has no idempotency key for it), so one action is at most one message.

## Policy facts: `request.slack`

Before the decision, the connector adds facts your rules can use (cached for 5 minutes through
`conversations.info`):

| Fact | Meaning |
|---|---|
| `channel_id` | The channel the call names, as an id. |
| `channel_name` | Its name, without the `#`. |
| `is_private`, `is_im`, `is_ext_shared` | What Slack says about the channel. |
| `broadcast` | The text contains `<!channel>`, `<!here>` or `<!everyone>`. |
| `user_mentions` | How many `<@U…>` mentions the text holds. |
| `enriched` | `false` when Slack couldn't be asked. The lookup never blocks the decision; write rules so that `!request.slack.enriched` is treated as unknown, never as safe. |

The `slack-post-with-approval` starter uses `request.slack.broadcast` to send `@here` posts for
approval.

## L1: reading a post back

After a post, the Witness asks Slack for exactly that message:
`conversations.history?channel=C…&oldest=<ts>&latest=<ts>&inclusive=true&limit=1&include_all_metadata=true`.
It compares three things:

- **text**: Slack stores `&`, `<` and `>` as `&amp;`, `&lt;` and `&gt;`, so both sides are compared
  after undoing those three entities. A post of `a & b <c>` matches what Slack shows.
- **channel**: the channel Slack answered with is the one the call was allowed to post in.
- **metadata.action_id**: the message's `deedglass_action` metadata names this action.

A reaction is read back with `reactions.get`, which must list the reaction by this bot (the bot's
user id comes from `auth.test`, cached).

Each read-back is one request. A message Slack doesn't show yet is retried by the Witness; a rate
limit ("Slack rate-limited the read-back") or any other failure is a gap, so the verdict is
`UNVERIFIABLE`, never `MISMATCH`.

## Errors

| Slack says | Deedglass answers |
|---|---|
| `not_in_channel` | `failed`: "Invite @deedglass to #agent-updates, then try again." (with the real channel name) |
| `channel_not_found` | `upstream_not_found` |
| `ratelimited` or HTTP 429 | `upstream_rate_limited`, with Slack's `Retry-After` |
| `invalid_auth`, `token_revoked`, `account_inactive` | `upstream_auth_failed`; `missing_scope` names the scope to add |
| Anything else with `ok: false` | `upstream_error` naming Slack's code |

## Rate limits

Slack limits each Web API method by tier. Custom apps installed in your own workspace keep Tier 3
limits for `conversations.history`; the stricter limits Slack announced in May 2025 apply to
commercially distributed apps that aren't in the Slack Marketplace. A Deedglass read-back is one
`conversations.history` call per attempt, and channel lookups are cached for 5 minutes, so a
normal agent stays far below the limit.

## Check it

```
$ deedglass connector test slack
◐ slack · 4 passed · 1 warning · 233 ms
  ✓ auth          workspace Arjun Sandbox (T0123ABCD) · bot @deedglass
  ✓ app           A0123ABCD matches
  ✓ channels      #agent-updates · member
  ○ approvals     #deedglass-approvals · the bot isn't a member
    Invite @deedglass to #deedglass-approvals, then run this test again.
  ✓ scopes        9 of 9 granted
```

The test changes nothing in Slack. It calls `auth.test`, `bots.info` and `conversations.list` (and
`conversations.info` for a channel id the list doesn't show).

## What it doesn't do

- No Socket Mode: approvals need a public HTTPS URL for interactivity.
- No user tokens: Deedglass never acts as a person.
- No direct messages it starts, no file uploads, no editing or deleting messages, no slash commands.
- No Enterprise Grid org-wide installs (`org_deploy_enabled: false`).
- The L1 comparison undoes only `&amp;`, `&lt;` and `&gt;`. If Slack rewrites other formatting in a
  message, the read-back can show a different text; the witness detail shows both sides.
- Slack may store a reaction under another name of the same emoji (an alias); the read-back then
  can't find it by the name the agent used.
- Email matching for approvals trusts the workspace's admins, who control emails. Use an
  SSO-enforced workspace and `require_email_domains`.
- L1 shows Slack's state, not whether the message was wise.
