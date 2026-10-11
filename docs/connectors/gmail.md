# The Gmail connector

Deedglass lets agents write and read **drafts** in one Gmail mailbox. No tool sends, and the
connector can't reach a send endpoint, so sending stays with people. That's the point: an agent
prepares the email, a person reads it and presses Send in Gmail.

Facts on this page were checked on 2026-10-04 (rows C-078 and C-079 in `CLAIMS_REGISTER.md`).

## Drafts only, and how it's enforced

Deedglass's Gmail connector can only draft. It never sends. (Sending exists only in the separate
`outbound_mail` connector of the ops build, which release builds don't contain; see
`docs/connectors/outbound_mail.md`.)

Google's `gmail.compose` scope permits creating drafts **and sending**. So drafts only is a
property of Deedglass's code, not of Google's scope:

- There is no send tool. A send tool name is `tool_unknown`, and the starter policy
  `gmail-drafts-only` denies `gmail.*send*`, `gmail.*forward*` and `gmail.*reply*` as well.
- The request builder has a compiled allow-list: `POST|GET …/drafts`, `PUT|GET …/drafts/{id}`,
  `GET …/threads/{id}`, `GET …/profile`. Anything else (`drafts/send`, `messages/send`,
  `messages/{id}/modify`) is refused before a byte leaves, with `internal_error` and an alert.
- A source test fails the build if a request path in the package names a `send` segment.

Nothing about Deedglass goes into the message: when a person sends the draft, recipients see only
what the agent wrote. Deeds correlate by draft id.

## Set up Google (once, about 10 minutes, free)

1. At console.cloud.google.com, create a project, say `deedglass-dev`, and enable the **Gmail API**.
2. OAuth consent screen: **External**, publishing status **Testing**, and add yourself as a test user.
3. Credentials › Create credentials › OAuth client ID › **Desktop app**. Download the JSON to
   `~/.deedglass-secrets/` and `chmod 600` it.

## Authorize

```
$ deedglass connector create --config examples/connectors/gmail.yaml
$ deedglass connector gmail authorize gmail --client-file ~/.deedglass-secrets/client_secret.json
◐ Waiting for Google · a browser tab is open (http://127.0.0.1:53117/callback · 5 min)
✓ Connected · gmail · arjun@example.com · scopes: gmail.compose
  Deedglass writes drafts only. People send them.
  ○ Google apps in Testing get 7-day refresh tokens. Authorize again before Oct 17.
```

What happens: Deedglass listens on `127.0.0.1` only (never another interface), creates a PKCE
S256 verifier (64 random bytes) and a 32-byte `state`, and opens Google's consent page. On the
callback it compares `state` in constant time, exchanges the code with the verifier, reads your
profile for the email address, and stores `client_id`, `client_secret` and the refresh token in the
vault (kind `fields`), which seals the usual admin deed. The email, scopes and time go into the
connector's config. The tab shows a plain page with no external assets. It gives up after
5 minutes. `--readonly` also asks for `gmail.readonly`, which turns on `gmail.get_thread`;
`--no-browser` prints the link instead.

If Google returns no refresh token: remove Deedglass at myaccount.google.com › Security ›
Third-party access, then authorize again.

**The 7-day note.** Google apps in Testing get refresh tokens that expire after 7 days. When one
expires, calls fail with `upstream_auth_failed` ("Google revoked or expired this connection…") and
`deedglass connector test gmail` warns from day 6. Authorize again, or publish the app.

## Google Workspace: a service account

For a Workspace mailbox, use a service account with domain-wide delegation for `gmail.compose`:

```yaml
config:
  auth: service_account
  mailbox: ops@acme.com     # the user it acts as
```

Store the key with `deedglass connector credentials set gmail --field service_account_json=file:./sa.json`.
Deedglass signs `{iss, scope, aud, iat, exp: iat+3600, sub: mailbox}` with the key and exchanges it
for an access token. Access tokens live only in memory, refreshed 5 minutes before they expire.

## Config

```yaml
type: gmail
name: gmail
config:
  auth: oauth                # oauth | service_account
  mailbox: me
  internal_domains: [acme.com]
  max_recipients: 20
  allow_bcc: false
```

## Catalog

The table below comes from `GET /v1/connector-types/gmail` (`deedglass connector types --type gmail`).

<!-- deedglass:generated start -->
<!-- Generated from the connector catalog by go run ./internal/connector/cmd/gendocs. Don't edit by hand. -->

**Gmail** · beta · levels L0 L1 · drafts only

Drafts in one Gmail mailbox. People send them.

### Tools

| Tool | Side effect | Levels | What L1 checks |
|---|---|---|---|
| `gmail.create_draft` | write | L0 L1 | The saved draft has the same recipients, subject and body. |
| `gmail.update_draft` | write | L0 L1 | The updated draft has the same recipients, subject and body. |
| `gmail.list_drafts` | read | L0 | Not read back |
| `gmail.get_draft` | read | L0 | Not read back |
| `gmail.get_thread` | read | L0 | Not read back Needs gmail.readonly. |

| Tool | What it does |
|---|---|
| `gmail.create_draft` | Save a draft in the mailbox. It doesn't send: a person reviews and sends it from Gmail. |
| `gmail.update_draft` | Replace a draft's recipients, subject and body. It doesn't send. |
| `gmail.list_drafts` | List the mailbox's draft ids (at most 25). |
| `gmail.get_draft` | Read a draft: to, cc, subject, the plain-text body (up to 64 KiB) and the thread id. |
| `gmail.get_thread` | Read a thread: up to 20 messages, each body up to 16 KiB. Needs the gmail.readonly scope. |

### Credentials

Kind `fields`:

- `client_id` (not secret): The Desktop OAuth client's id. deedglass connector gmail authorize stores it.
- `client_secret` (secret): The Desktop OAuth client's secret.
- `refresh_token` (secret): Written by deedglass connector gmail authorize; never pasted by hand.
- `service_account_json` (secret): Google Workspace only: a service-account key with domain-wide delegation for gmail.compose.

### Setup

1. **Create a Google Cloud project.** Enable the Gmail API, set the OAuth consent screen to External and Testing, and add yourself as a test user. https://console.cloud.google.com/apis/library/gmail.googleapis.com
2. **Create a Desktop OAuth client.** Credentials › Create credentials › OAuth client ID › Desktop app. Download the JSON and chmod 600 it. https://console.cloud.google.com/apis/credentials
3. **Create the connector.**
   `deedglass connector create --config examples/connectors/gmail.yaml`
4. **Authorize.** Sign in with Google in the browser tab it opens. Apps in Testing need this again every 7 days.
   `deedglass connector gmail authorize gmail --client-file ./client_secret.json`
5. **Test the connection.**
   `deedglass connector test gmail`

### What it doesn't do

- Drafts only. Nothing sends: people send drafts from Gmail.
- Google's gmail.compose scope itself permits sending, so drafts only is enforced by Deedglass, not by Google.
- No attachments, labels or deletes.
- Google apps in Testing get 7-day refresh tokens: authorize again every week.

<!-- deedglass:generated end -->

## Drafts

Arguments: `to` (1 to `max_recipients` in all), `cc`, `bcc` (only with `allow_bcc: true`), `subject`
(one line, at most 250 characters), `body_text` (at most 256 KiB), optional `body_html` (stored as
is, never rendered by Deedglass), `thread_id` and `in_reply_to` (a Message-ID).

- Each address entry is parsed as exactly one address, so `"Smith, John" <john@acme.com>` is one
  recipient and `a@acme.com, b@evil.test` in one entry is refused.
- A line break in any header value is refused: "Email headers can't contain line breaks."
- The message is UTF-8 quoted-printable, `multipart/alternative` when there's HTML, with a
  Q-encoded subject when it isn't ASCII, and `In-Reply-To`/`References` for replies. At most 1 MiB,
  no attachments.

## Policy facts: `request.email`

| Fact | Meaning |
|---|---|
| `recipient_count` | to + cc + bcc |
| `domains`, `external_domains` | lowercase; external means not in `internal_domains` |
| `has_external`, `has_bcc`, `is_reply` | |
| `enriched` | false when the recipients couldn't be read |

## Read-back (L1)

The saved draft is read back as raw and parsed with `net/mail`. Deedglass compares the `to` and
`cc` sets (lowercase addresses), the decoded subject, the body's digest after normalization
(CRLF to LF, trailing spaces trimmed on each line, one final newline) and the thread id. A
different subject or body is MISMATCH.

## Connector test

`config`, `credentials` (the email and when it was authorized; a warning after 6 days),
`destination`, `auth` (the token refresh works), `profile` (the email matches), `scopes`, and
`send guard`, which always passes and says "Sending isn't possible through Deedglass. People send
drafts themselves."

## What it doesn't do

- Send, forward or reply. People send drafts from Gmail.
- Attachments, labels, archiving or deleting.
- Render HTML bodies.
- Keep a Testing app connected for more than 7 days without authorizing again.
