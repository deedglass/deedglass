---
title: "What your agents did this week"
description: "Every Deedglass server can tell an org's owners, once a week, what its agents did: how many actions ran, what the rules allowed, held for a person or blocked…"
imported: "docs/WEEKLY.md"
editUrl: false
---
Every Deedglass server can tell an org's owners, once a week, what its agents did: how many actions
ran, what the rules allowed, held for a person or blocked, what the Check found, what Deedglass caught
and the free way back for each, and whether the record chain is intact. It is built from the org's
own records on the org's own server. **Nothing goes to deedglass.com.**

- **The Monday email (free):** every Monday from 08:00 in the org's time zone, the week that just
  ended goes to the org's owners whose weekly email is on (it is on by default).
- **The This week card (free):** Console › Overview shows the same summary. Without a mailer it is
  the summary.
- **`deedglass report` (free):** one org's week or month as Markdown, a print-ready HTML page, or
  JSON (`weekly_summary` v1).
- **`GET /v1/weekly` (free):** the summary for the Console and your own tools.

Each client's report in one run, with an agency's name on it, is the Agency plan: see
[AGENCIES.md](/reference/agencies/).

## The Monday email

```
Subject: What your agents did this week · 412 actions · 1 mistake caught

What your agents did this week · acme · 12 to 18 Oct
412 actions · 371 allowed · 22 waited for a person · 19 blocked by rules
Check: 388 match · 0 don't match · 24 can't check
Deedglass caught 1 thing: one transaction changed 301 rows in public.orders.
  Undo it yourself, free: deedglass observe show obs_01k… --undo-sql   ·   Undo the whole session in one step (Pro)
Records chain intact. Open the Console: https://deedglass.acme.internal/app
Stop these emails: https://deedglass.acme.internal/weekly/off?t=…
```

- **Who gets it:** owners with an active membership whose account isn't disabled and whose weekly
  email is on. Admins and viewers never get it. An owner who has never signed in still gets it: the
  operator named them when creating the org.
- **How it is sent:** through the mailer the server already uses for sign-in and approvals
  (`DEEDGLASS_RESEND_API_KEY` in production; the log mailer prints it in development). A production
  server without Resend sends nothing; the Console card is the summary.
- **Exactly once:** one `learn_runs` row (kind `weekly`, the ISO week) per org, insert-or-skip, so
  several `serve` instances send one email. Each message carries a Resend idempotency key
  `weekly/<org>/<week>/<user>`.
- **The Pro line:** `Undo the whole session in one step (Pro)` appears only next to a caught watched
  session, and only when this server has no `undo_session`. Every caught item shows its free way back
  on the same line. There is no other upsell.
- **No tracking:** no pixel, no click tracking, no link to deedglass.com except the footer.

### Turning it off

- **One click:** each email's **Stop these emails** link opens a page on your server with one button.
  Opening the page changes nothing (a link scanner can't turn it off); the button's POST does. The
  link carries a token the server signs for this one purpose (HMAC-SHA-256 with a key derived from
  `DEEDGLASS_API_KEY_PEPPER`); it turns off that person's weekly email in that org and nothing else.
- **In the Console:** the This week card's **Turn it off** / **Turn it on**, or
  `POST /v1/weekly/email {"weekly_email": false}` with a session.

## The summary

Built by `internal/weekly` from one REPEATABLE READ, READ ONLY transaction in the org (row-level
security on), plus the Ledger's chain check over the period:

| Part | From |
|---|---|
| Actions by decision (allowed, waited for a person, blocked by rules), by agent (top 8) and by tool (top 8) | the org's own agents' actions created in the period (the system agent that runs a person's undo is left out) |
| Check: matches, doesn't match, can't check, not checked | the actions' verdicts |
| Blocked most by (top 3) | the rule that decided each deny and the rule's own reason; a deny without a rule is named by its source (`@default`, `@budget`) |
| Approvals | requested, approved, denied, expired and still waiting, and the median time to decide |
| Undo and Stop | per-action undos that ran, watched sessions undone; Stop and Stop all events |
| Watched sessions | sessions opened in the period, and how many caught something (S50) |
| Caught | each sealed session's caught items, and the actions whose L1 or L2 check didn't match or that a person undid (`internal/caught`), each with its free way back; the first 10 are listed |
| Records | the chain check over the period's records: intact, broken (with the first broken record), not checked, or no records yet |
| Security review pack | the newest pack built in the period (S51), by its sealed `index.html` |

It holds integers, ids, dates and the names of the org's own agents, tools and rules: **never action
arguments, results, row values, emails or secrets.** A canary test writes known values into
arguments, results and a member's email and checks that no rendering contains them.

## `deedglass report`

```sh
deedglass report                                   # the last complete week, Markdown, printed
deedglass report --week 2026-W42 --format html --out week.html
deedglass report --month 2026-10 --format json
deedglass --org globex report --month 2026-10      # another org on this server
```

It reads the database directly (`DEEDGLASS_DATABASE_URL`, or `.deedglass/stack.json` after
`deedglass init`), like `deedglass actions list`. Weeks are ISO weeks and months are calendar months,
in the org's time zone. `--out` never overwrites a file. The HTML page is self-contained (no script,
no font or image request) and prints on A4 or Letter.

`--all-clients`, `--brand` and `--exclude` are the Agency plan ([AGENCIES.md](/reference/agencies/)).

## `GET /v1/weekly`

`?period=2026-W42`, `?period=2026-10` or `?period=current` (this week so far); the default is the last
complete week. A person's session or an org admin key; agent keys get 403. The answer is a
`weekly_view`: the `weekly_summary`, the signed-in person's email state (`null` for a key) and
`pro_undo_line`.

## What it doesn't do

- It doesn't send anything to deedglass.com, and nothing from deedglass.com reaches it.
- It doesn't email admins or viewers, or anyone outside the org.
- It doesn't summarise agents or tools that never go through Deedglass, or tables observe mode
  doesn't watch.
- It reports the Check's verdicts; it doesn't re-check anything when the email is built.
- Actions are counted by when they were created; an action that finishes in the next week counts in
  the week it started.

:::note[Source]
This page is `docs/WEEKLY.md` in the Deedglass repo, imported at build time.
:::
