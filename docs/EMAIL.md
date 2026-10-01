# Email

Deedglass sends two emails today: the sign-in email (a link and a 6-digit code) and the invite.
S22 adds approval requests. Every email is plain, 600 px wide, with an HTML part and a plain-text
part, and loads nothing from the internet: no images, fonts, tracking pixels or click tracking.

## Providers

| Setting | Mailer | Where it goes |
|---|---|---|
| `DEEDGLASS_RESEND_API_KEY` set | Resend | `POST https://api.resend.com/emails` |
| no key, `DEEDGLASS_ENV=dev` | log | the server's stderr, as a fenced block with the link and code |
| no key, prod | none | nothing; `serve` warns once at startup. Use `deedglass org login-link`. |

`DEEDGLASS_MAIL_FROM` is the From address, for example `Deedglass <signin@deedglass.com>`. It is
required when the Resend key is set; config refuses to start without it.

How the Resend mailer behaves:

- **One email per sign-in, even when retried.** Each request carries an `Idempotency-Key`
  (`login/<token id>`, `invite/<token id>`), so a retried attempt never sends a second email.
- **Bounded retries.** At most 3 attempts with a 10 s timeout each. A `429` waits for `Retry-After`
  (at most 10 s); a `5xx` or network error backs off 0.5 s, then 1 s. Other `4xx` answers aren't
  retried.
- **No threading.** A unique `X-Entity-Ref-ID` header per email keeps Gmail from grouping sign-in
  emails into one conversation.
- **A `kind` tag** (`login` or `invite`) on every email, for the provider's dashboard.
- **Logs carry ids only:** the kind, the user id, the request id, the HTTP status and Resend's email
  id. Never the API key, the address, the subject, a body, a link or a code. A test checks this.

Mail is never sent inside a database transaction or on the request path. A bounded queue (1,024
jobs, 2 workers, drained for 10 s at shutdown) sends it. When the queue is full the job is dropped
and logged as `mail.dropped`; the request still succeeds (the sign-in answer is 202 either way).

## Your own sending domain (SPF, DKIM, DMARC)

Deedglass's own cloud sends from the root domain, `signin@deedglass.com`, with Resend's records on
the `send.` subdomain. Self-hosters can do the same with their domain (or verify a subdomain such as
`mail.example.com` to keep sign-in mail's reputation apart from the team's mail; the steps are the
same with the subdomain added to each record name).

1. Add the domain in Resend and create the DNS records it shows: an MX record and an SPF `TXT`
   record on `send` (`send.example.com`), and the DKIM `TXT` record `resend._domainkey`. With
   Cloudflare, turn the proxy off for these records. The SPF record on `send.` covers the bounce
   (Return-Path) domain, so your root SPF record for your normal mailbox provider stays as it is.
2. Add DMARC at the root: `TXT _dmarc "v=DMARC1; p=none; rua=mailto:dmarc@example.com; adkim=r; aspf=r"`.
   Relaxed alignment (`adkim=r; aspf=r`) is what lets `send.example.com` and the DKIM signature
   align with a From address at `example.com`. After two clean weeks of reports, move to
   `p=quarantine`.
3. Turn **off** open and click tracking for the domain. Click tracking rewrites links, which would
   send the sign-in token through a third party's redirect; open tracking adds a remote image.
4. Create an API key with sending access only, limited to the domain. Put it in the environment as
   `DEEDGLASS_RESEND_API_KEY`, with `DEEDGLASS_MAIL_FROM="Deedglass <signin@example.com>"`.
5. Send yourself a sign-in email. In Gmail, "Show original" should say SPF, DKIM and DMARC: PASS.

## Why tracking stays off

A sign-in link is a one-time secret. Click tracking would route it through the provider's redirect
domain, and an open-tracking pixel tells a third party when you read your email. Neither helps
anyone sign in.

## Previewing the templates

```
$ deedglass dev mail-preview login --out /tmp/login.html
✓ Sign in to Deedglass · /tmp/login.html
  Plain text: /tmp/login.txt
  Open the HTML file in a browser to see it.
$ deedglass dev mail-preview invite --out /tmp/invite.html
```

The templates are `internal/mailer/templates/{login,invite}.{html,txt}.tmpl` (with a shared
`layout.html.tmpl`). Their goldens are in `internal/mailer/testdata/`; regenerate them with
`go test ./internal/mailer -run TestGoldens -update` after checking the change.

**Design rules** (brand §13.5, S08 Design QA; `TestEmailSafety` checks them):
- A 600 px table frame on `paper-50`, one card, one primary button, and a plain-text part.
- The mark is the flat Split Orb at 32 px, drawn with three table cells, so no image is needed to
  read or act on the email. The left half is solid aurora blue (`#4F8CFF`); email clients don't
  render gradients reliably.
- Every colour is an inline style (the light theme). One `<style>` block in the head holds only two
  media queries: dark mode (`prefers-color-scheme: dark`, `ink-950`/`ink-900`/`snow-text`) and a
  narrow-screen padding. A client that strips `<style>` still shows the full light email. The
  button keeps `#2F5BEA` with white text in both themes (5.5:1).
- Under 102 KB (Gmail clips above that); today about 4.5 KB.
- Screenshots at 600 and 360 px, light and dark: `ops/plan/prompts-v7/reports/screens/S08/email-*`.

## What it doesn't do

- **No SMTP mailer yet.** Self-hosters without Resend issue links with `deedglass org login-link`.
- **No bounce or complaint handling.** A bounced sign-in email just doesn't arrive; ask for a new one.
- **Delivery isn't confirmed to the caller.** The API answers before the email is sent, and a full queue or a provider
  outage drops the email (logged). Ask again, or use an operator link.
- **English only**, and no per-org branding.
