# Deedglass for agencies

An agency or consultant builds agents for many clients. On Deedglass each client is its own org, with
its own sealed record chain and row-level security, and one person can belong to many orgs. The
agency view (licence feature `agency`; free while we grow, with no key) adds the agency's view on top,
without weakening that separation:

- **Clients:** one row per org you look after: this week's actions, Doesn't match, caught, approvals
  waiting, Stop and the record chain. `deedglass clients`, `GET /v1/clients`, Console › Clients (from
  the org switcher's **All clients**).
- **Reports for every client:** `deedglass report --all-clients --month 2026-10 --brand brand.yaml
  --out reports/` writes each client's report plus `index.md`.
- **Your name on them:** `brand.yaml` puts **Prepared by <agency> with Deedglass** and your logo on
  each report.

What stays free: every client's own Deedglass, its own `deedglass report`, its Monday email
([WEEKLY.md](WEEKLY.md)), per-action Undo and the security review readiness list.

## Who counts as a client

Every org you belong to as **owner or admin**, with an active membership in an active org. Viewers
aren't clients. Leave out your own org with `--exclude <slug>` (repeatable, or comma-separated in the
CLI) or `?exclude=<slug>` on the API. There is no new table and no new relationship between orgs: the
list is your memberships.

## How the separation holds

- The server lists your memberships with the same narrow lookup `GET /v1/me` and the org switcher use.
- Then, **for each client, it opens that org's own read-only transaction** (row-level security keyed
  to that org), checks there again that you are still an active owner or admin, and only then reads
  the counts. A client you lost access to disappears on the next load.
- No query spans two orgs, and `ee/agency` never opens a system transaction or calls a cross-org
  function: a test reads its source and fails on either, and an integration test wraps the store and
  checks that every read ran under its own org's setting.
- At most 200 clients a page (`limit`, `next_cursor`).

## `deedglass clients`

```
Clients · 3 · this week
acme        412 actions   0 don't match   1 caught   2 waiting   chain intact
globex       88 actions   0 don't match   0 caught   0 waiting   chain intact
initech       0 actions   no activity this week                              no records yet
Monthly reports for all: deedglass report --all-clients --month 2026-10 --brand brand.yaml --out reports/
```

It reads the database directly, like `deedglass report`. `--as you@agency.com` names the person (the
default is the owner `deedglass init` set up). `--json` prints the list.

## Reports for every client

```sh
deedglass report --all-clients --month 2026-10 --brand brand.yaml --out reports/ --exclude northwind
```

writes `reports/<client-slug>-2026-10.html`, `.md` and `.json` for each client (`--format` picks), and
`reports/index.md`. Each report is exactly the client's own `deedglass report --org <client>` plus the
brand lines (a test compares them byte for byte), built in that client's own transaction. A client's
report links its security review pack when one was built in the period. `--out` must not already hold
those files.

### `brand.yaml`

```yaml
name: Northwind Automation
contact_line: ops@northwind.example · +1 555 0100
logo_file: logo.svg      # optional, next to brand.yaml
```

- `name`: one line, up to 80 characters. `contact_line`: one line, up to 120.
- `logo_file`: a PNG or an SVG of at most 200 KB, inlined into the HTML as a data URI (the report
  still makes no request). An SVG with a script, an event handler, a `foreignObject`, a DOCTYPE or
  entity, or any `href` or `url()` that points outside the file is **refused, never rewritten**.

## Entitlement

Every feature is free while we grow ([LICENSING.md](LICENSING.md)): `agency` is on without a key,
and nothing reads a licence. The rest of this section is the key path, kept for a later paid option.

`serve` asks `cmd/deedglass/licence.go` for `agency` at start and once a day; the CLI asks when
`clients`, `--all-clients` or `--brand` runs. Without it they answer `pro_required`:

```
✕ Agency reports for every client need a licence on this build. Each client's own report stays free: deedglass report --org <client>.
```

The `agency_monthly` sku is plan `pro` with features Pro plus `agency`. The site signs `agency` only
when the client's token request lists it in `features_known`, so older installs keep receiving tokens
they understand ([LICENSING.md](LICENSING.md)). A Pro or founding licence doesn't carry `agency`.

## What it doesn't do

- It doesn't move a client between servers or hand a client's org over (not built yet).
- It doesn't let one client see another, or let a client see the agency's other clients.
- It doesn't host anything: the clients' data stays on the server you or they run.
