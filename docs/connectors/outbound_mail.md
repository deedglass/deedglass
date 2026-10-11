# Outbound mail (ops build only)

`outbound_mail` sends plain-text email from one Google Workspace mailbox on a sending domain,
through the Gmail API's `messages.send`, and reads Gmail's Sent copy back. It exists for one
customer: Deedglass's own growth engine. It is **not** in release builds.

## Who it's for, and how it stays out of everyone else's way

- **Build tag.** Only `go build -tags outboundmail` (`make ops-build`, `bin/deedglass-ops`)
  contains it. `TestNoOutboundMailInRelease` checks that a default build imports nothing of it; a
  planted import doesn't even compile.
- **Org gate.** Only the org ids in `connectors.outbound_mail.orgs` (`docs/CONFIG.md`) may use
  it. For any other org the type doesn't exist (404 `connector_type_unknown`), and it is never in
  the public catalog. A release build with the setting refuses to start.
- **The Gmail connector stays drafts only.** Nothing here changes `internal/connector/gmail`.

## The From rule

From is always the authorised mailbox. `deedglass.com` and its subdomains are always refused, plus
every domain in `deny_from_domains` (required, at least one). The rule runs at create and update
(400 `from_denied`), at authorize (a profile on a refused domain, or any account other than
`mailbox`, stores nothing), before every send, and in the read-back, where a Sent copy with a
refused From is a mismatch **and** disables the connector with a `connector.disabled` deed.

## Config

```yaml
type: outbound_mail
name: mail
config:
  mailbox: arjun@trydeedglass.com
  from_name: Arjun Prabhakaran
  sending_domain: trydeedglass.com        # must be the mailbox's domain
  deny_from_domains: [deedglass.com]      # required; subdomains included
  unsubscribe_base: https://trydeedglass.com/u/
  require_window: true                    # default
```

Credentials (kind `fields`): `client_id`, `client_secret`, `refresh_token`, stored by
`bin/deedglass-ops connector outbound authorize mail --client-file ./client_secret.json` (an
Internal Desktop OAuth client in a project owned by the Workspace organisation; PKCE through a
loopback page; exactly `gmail.send` and `gmail.readonly`).

## Tools

| Tool | Side effect | Gmail call | Notes |
|---|---|---|---|
| `mail.send` | write | `POST …/messages/send` | read back (L1); never retried |
| `mail.get_message` | read | `GET …/messages/{id}?format=raw` | body ≤ 16 KiB; bounce fields from `multipart/report` |
| `mail.list_history` | read | `GET …/history` | ≤ 100; a 404 is `history_expired` |
| `mail.list_messages` | read | `GET …/messages?q=` | ≤ 50; only `in:inbox newer_than:<1..14>d`, `in:sent after:YYYY/MM/DD before:YYYY/MM/DD`, `rfc822msgid:<id>` |
| `mail.get_thread` | read | `GET …/threads/{id}?format=metadata` | ≤ 20 messages, headers only |

Every other path (drafts, modify, trash, untrash, delete, settings, import, insert, batch, watch)
is refused before a byte leaves (`TestForbiddenEndpointsMakeNoRequest`).

`mail.send` checks, in order and before any request: the From guard; the window (`args.window`,
else failed `outside_send_window`); one recipient with no display name; a subject of 1 to 120
characters, no `Re:` on a first email; a plain-text body ≤ 16 KiB; and `body_sha256` equal to the
sha256 of the normalised body (CRLF and CR to LF, trailing spaces and tabs stripped, one final LF,
NFC). A mismatch is `validation_failed` and nothing is sent.

## The message

Plain text, quoted-printable, with `List-Unsubscribe` (the https one-click URL and a mailto) and
`List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058). The Message-ID is derived from the
action and connector ids, so a read-back can find it. No HTML part, image, attachment, tracking
header or `X-Mailer`.

## Read-back and unknown outcomes

Gmail's send has no idempotency key, so the connector never retries it. A 5xx or a lost answer
is `unknown`. The read-back reads the Sent copy (by its id, or after an unknown by
`rfc822msgid:`), and compares the `SENT` label, To, subject, the normalised body hash, From and the
two unsubscribe headers. All equal: L1 VERIFIED. Any difference: MISMATCH. Not found: a gap
(UNVERIFIABLE); the growth engine then marks the message `failed_unknown` and never sends it again.

## What it doesn't do

Drafts, HTML, attachments, images, open or click tracking, cc or bcc, retries, more than one
mailbox per connector, or anything at all in a release build.
