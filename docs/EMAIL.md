# Email

Deedglass sends the sign-in email (a link and a 6-digit code), the invite, approval requests (S22)
and the weekly Insights digest (S28). Every email is plain, 600 px wide, with an HTML part and a plain-text
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

## The weekly Insights digest

A summary of the org's week for the people who run it (S28; `internal/learn/digest.go`, templates
`internal/mailer/templates/digest.{html,txt}.tmpl`, renderer `internal/mailer/digest.go`).

- **When:** Mondays from 09:00 in the org's time zone, for the ISO week that just ended. The
  schedule loop in every `serve` instance checks every 5 minutes; a `learn_runs` row
  `(digest, 2026-W41)`, claimed with insert-on-conflict-skip, makes it happen exactly once per org
  and week, however many instances run.
- **Who:** the org's active members by `digest.recipients` in the Insights settings: `owners` (the
  default), `admins` (owners and admins) or `off`. One email per person.
- **What:** numbers only, never payloads, arguments, SQL or customer refs. Actions, the verified
  share, denials and mismatches; the open suggestions (the count and the top title); open anomalies
  and how many opened and resolved; the shadow's agreement; and the three rules that denied most.
- **Subject:** `Acme · week 41 · 5,612 agent actions, 2 suggestions to review` (no suggestions:
  `Acme · week 41 · 5,612 agent actions`; one: `1 suggestion to review`).
- **One button,** "Open Insights", to `DEEDGLASS_BASE_URL` + `/app/insights`, and the same link in
  the text part.
- **Footer:** "You get this weekly summary because you're an owner of Acme on Deedglass. Change who
  receives it in Console › Insights › Settings." (an admin reads "an admin of Acme").
- **Idempotent:** each email carries the key `digest/<org>/<ISO week>/<user>`, so a retried send
  never delivers twice. It goes through the same bounded queue as every email, never inside a
  transaction.
- **No tracking:** no pixel, no remote image, no click tracking, like every Deedglass email.

The text part, from the golden `internal/mailer/testdata/digest.txt.golden`:

```
Deedglass · Acme · week 41 (5 to 11 Oct 2026)

5,612 agent actions · 88.9% verified · 311 denied · 9 mismatches

To review
  2 suggestions: "Allow updates to public.orders without approval" and 1 more
  1 open anomaly: 3 MISMATCH verdicts in 1 hour for ops-agent
Shadow
  acme-sim v3 has run in shadow for 2 days and agrees on 1,180 of 1,204 actions.
Denied most by
  refund-limit 15 · @default 2

Open Insights: https://app.deedglass.com/app/insights

You get this weekly summary because you're an owner of Acme on Deedglass.
Change who receives it in Console › Insights › Settings.
```

A quiet week reads "Nothing to review this week." Preview this week's digest for an org without
sending anything (it reads the database directly):

```
$ deedglass insights digest --preview --org acme-sim --html /tmp/digest.html
Subject: Acme (simulated) · week 41 · 5,612 agent actions, 3 suggestions to review
To: 1 recipient (not sent: preview)
…
✓ HTML written to /tmp/digest.html
```

The numbers above are illustrative. The goldens are regenerated with
`go test ./internal/mailer -run TestDigest -update` after checking the change.

## What it doesn't do

- **No SMTP mailer yet.** Self-hosters without Resend issue links with `deedglass org login-link`.
- **No bounce or complaint handling.** A bounced sign-in email just doesn't arrive; ask for a new one.
- **Delivery isn't confirmed to the caller.** The API answers before the email is sent, and a full queue or a provider
  outage drops the email (logged). Ask again, or use an operator link.
- **English only**, and no per-org branding.
