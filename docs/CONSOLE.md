# The Console

The Console is Deedglass's web app at `/app` (`app.deedglass.com` in the cloud beta). It is a static
build embedded in the `deedglass` binary and served by `deedglass serve` from the same origin as the
API, so there is nothing else to deploy and no cross-origin request anywhere.

## Architecture

- **Source:** `web/` (Astro 7, static output, base `/app`, vanilla TypeScript; ADR-020, ADR-053).
  `make web-build` writes `internal/console/dist`, which `//go:embed` puts in the binary.
- **Data:** pages contain no data. Every value arrives through `web/src/lib/api.ts` (session cookie,
  `Deedglass-Version`, CSRF and an Idempotency-Key on every change, 15 s timeout, no redirects) and
  the live stream `web/src/lib/stream.ts` (one `EventSource` per tab on [`GET /v1/events`](EVENTS.md)).
- **Rendering:** `web/src/lib/dom.ts` builds DOM from data with text nodes only. Agent-controlled
  strings (tool names, reasons, labels, org names) are attacker input; nothing is parsed as HTML.
- **Server:** `internal/console` serves the pages and assets (ADR-054); the event hub, hydration and
  `GET /v1/events` live there too.

## Routes and the sign-in redirect

| URL | Page |
|---|---|
| `/app` | Overview |
| `/app/actions`, `/approvals`, `/agents`, `/policies`, `/connectors`, `/insights`, `/deeds`, `/settings` | the screen |
| `/app/<screen>/<id>` (actions `act_`, approvals `apr_`, agents `agt_`, policies `pol_`, connectors `con_`, deeds `deed_`) | the detail page; the id's prefix must match |
| `/app/settings/<section>` | Settings, on that section |
| `/app/_kit` | the component kit, dev builds only (404 in prod) |
| anything else | the Console 404, which never echoes the path |

Without a valid session every page answers `302 /login?next=<path and query>`. `next` must look like
`/app…` with no `//`, `\` or `..`; anything else is dropped. After signing in with the 6-digit code the
sign-in page returns to `next`. A link opened from the email in a new tab lands on `/app` (the return
path lives in the tab that asked for it).

## Security headers and why

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  font-src 'self'; connect-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none';
  base-uri 'none'; object-src 'none'; require-trusted-types-for 'script'; trusted-types 'none'
Cross-Origin-Opener-Policy: same-origin
Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(), usb=()
```

- No inline script or style anywhere (the build fails if one appears: `web/scripts/check-dist.mjs`).
- Trusted Types `'none'` makes every HTML-string sink throw in Chromium, which enforces the
  text-only rule at run time; the HTML-sink grep enforces it at build time.
- `frame-ancestors 'none'` and COOP stop clickjacking and cross-window access.
- Pages are `Cache-Control: no-cache` with an ETag; hashed assets are cached for a year.
- Browser storage holds UI preferences only: the theme (`localStorage`) and the palette's recent pages
  (`sessionStorage`, per org, cleared on sign-out). Never a key or a token.

## The dev loop

- `make web-build && make build-only && make dev` (or `deedglass serve`) is exactly what ships.
- `make web-dev` runs `astro dev` on `:4321` and proxies `/v1`, `/login`, `/auth` and `/.well-known`
  to `:8080`; start the server with `DEEDGLASS_BASE_URL=http://localhost:4321` so sign-in links agree.
- `make web-test` (in `make ci`): typecheck and Vitest. `make web-e2e`: Playwright in the installed
  Chrome against a throwaway PostgreSQL 18 and a real server (`scripts/web_e2e.sh`). `make
  web-lighthouse`: accessibility and best practices ≥ 95 on `/app`, `/app/_kit` and `/login`.
- `scripts/web_e2e.sh serve` keeps that stack up (Acme with an owner, an admin and a viewer) for
  manual looks; `deedglass org login-link --email owner@acme.example --org acme` from `.tmp/web-e2e`.
  Since S25 it seeds real data first (`WEB_E2E_SEED=0` skips it): 600 fake actions, then the L2 hero
  on a second throwaway cluster (the Acme Store orders database on `:55469`; the egress door
  refuses Deedglass's own cluster): the postgres connector and its decoder, the `acme-demo` policy,
  support-bot ships order 42 (L2 VERIFIED), the legacy trigger goes on, and it ships 42 again (L2
  MISMATCH, 301 rows). The ids land in `.tmp/web-e2e/fixtures.json` for Playwright.
- `make web-dev` with `DEEDGLASS_DEV_PROXY=http://127.0.0.1:18241` proxies to that throwaway stack
  instead of `:8080` (reads work; changes need the Console and the server on one origin).

## The screens (S25)

Every screen has a loading state (a skeleton in the final geometry; after 8 s, `Still loading. The
server is slow to answer.`), an empty state that teaches, a filtered-empty state, an error state (the
server's message, the request id, Retry), a viewer state (controls visible, changes disabled with
the reason), the Console 404 (the same for a missing id and another org's id) and the offline
banner (content stays; the Overview says `Last updated 14:02`). Dark and light, down to 360 px.

### Overview (`/app`)

- **Tiles:** Actions, Blocked, Waiting for approval and Verification, from
  [`GET /v1/overview`](OVERVIEW_METRICS.md) for the window in the toggle (`24 h` or `7 d`, in the URL
  as `?window=7d`). Every number says its window; deltas are neutral words, never a verdict colour.
  The Verification bar has its labels and counts in text; each verdict links to the feed, filtered.
- **Needs attention:** the server's attention items (kinds and data, at most 8), written as words by
  `copy.ts`. `Nothing needs you right now.` when empty, with a neutral ✓ (teal means a deed was
  verified, so it isn't used here).
- **Agents:** the 8 most recently active agents, status, calls today against `calls_per_day`.
- **Live actions:** the last 10 actions, then new ones as Postgres commits them. The Mirror plays
  only for a verdict that arrives while you watch.
- **Chain:** `GET /v1/ledger/check` over the last 24 hours, after first paint; the head's body hash
  with the note to keep it somewhere outside Deedglass.
- **Live counters:** `action.created` adds an action, a deny adds a block, `action.witnessed` moves
  one action into its verdict, approvals move Waiting. The page refetches every 60 s to correct
  drift (an event seen twice, one outside the window).
- **First run:** an org with no agents gets the three steps instead: create the demo agent and its
  key (shown once, S08's sheet), the MCP client config for Claude Code, Cursor or Claude Desktop with
  the key filled in (the same bytes `deedglass mcp snippet` prints; `web/src/lib/snippets.ts`), and
  a wait for the agent's first action, which turns ✓ when it seals. The key fills the snippet on this
  page only; it is never stored, and a reload shows the placeholder again.

### Actions (`/app/actions`)

- **Filters** live in the URL with the API's names, so a shared link is a readable query: `agent`,
  `tool` (exact, or a glob with `*`), `decision`, `verdict`, `level`, `state`, `on_behalf_of`,
  `time` (`1h`, `24h`, `7d`) or `from`/`to`, and `q` (2 to 64 characters, server-side).
- **Live:** new actions that match the filters go to the top; existing rows update in place. While
  paused or scrolled away from the top, they wait behind `12 new actions · Show`. **Live updates
  pause while searching by text** (the server owns `q`). The DOM keeps at most 500 rows and writes
  once per animation frame; the live region announces new actions at most once every 5 s.
- **Keys:** `p` pause, `j`/`k` move, `Enter` opens the selected row, `/` search, `f` more filters,
  `Esc` clears the selection. `g` sequences still navigate (`g p` is Approvals, not pause).
- **Under 640 px** each row is a card: the tool and the verdict, then agent, decision and time.

### Action detail (`/app/actions/{id}`)

- **The sentence:** the API's `witness.summary`, verbatim, after a prefix (`MISMATCH · `,
  `Witnessed by Postgres · `, `Confirmed by a read-back · `); denials use the Gate's message.
- **Witness:** level, method, verdict, every check (✓ pass, ✕ violation, ○ gap, i note) and the
  **DiffTable**: expected against observed, changed cells tinted at 12 % with `●` and the words
  "unexpected change", sensitive values `•••` with `masked`. The Witness keeps the first
  `witness.diff_rows` changed rows (20 by default), so the table shows `Showing 20 of 301 rows`;
  raise the setting on the connector to keep more. `Download diff (JSON)` saves the L2 evidence.
- **Timeline** (every lifecycle event, ms, deltas), **Decision** (policy, rule, reason, mode, the
  shadow when it differs), **Request and result** (rendered only when opened; pruned payloads say
  when and that the deed still verifies), **Amendments**.
- **Deed panel:** `Check in this browser` reads the deed's exact bytes and this server's keys and
  runs S05's verifier (`✓ Seal valid · key sk_…`, or the verifier's sentence, or `○` when the browser
  has no Ed25519). **The check uses the keys this server serves**; for an independent check, pin the
  keys and run the CLI (`Copy command`). `Download deed` saves the exact bytes.
- **Approval** (S22) and **Undo** (S23, its strings verbatim): `Preview undo` (admins) opens the
  plan as a DiffTable (`Now` · `After undo`), the twelve checks, the policy line and the reason;
  `Request undo` is never red: asking isn't destructive until a person countersigns.

### Deeds (`/app/deeds`, `/app/deeds/{id}`)

- The chain card, then every deed newest first (`kind` filter in the URL), with seq, kind, a
  one-line summary, issued, the id and the body hash.
- **Export:** a date range in the org's time zone downloads the bundle and prints the
  `deedglass verify` command to check it offline.
- **A deed:** contract §7's sections (who, action, policy, approval, execution, witness, cost, admin,
  chain with a link to the previous deed), the raw JCS exactly as sealed (a formatted view only on
  request, labelled), the deed panel, and ← → by seq.

### Agents (`/app/agents`, `/app/agents/{id}`)

- The list reads `GET /v1/agents?expand[]=usage`: status, labels, calls today against the daily
  budget (amber from 80 %, red at the limit, always with the numbers), spend today, last action and
  active keys. `Create agent` (admins) validates the name live with S08's rule.
- **Tabs** (in the URL as `?tab=`): Overview (calls today, blocked and the verdict mix over the last
  24 hours, and the last 20 actions), Keys (create, shown once; roll with an overlap; revoke),
  Budgets (`PATCH`, a deed per change), Security (intent mode and intent keys, S09) and Danger zone
  (suspend or resume; stop with the typed confirmation; start with a reason). Status changes arrive
  live through `agent.*` events.

## Adding a screen

1. The page in `web/src/pages/` (and `<screen>/detail.astro` if it has ids), on `AppShell`.
2. Every string in `web/src/lib/copy.ts`.
3. The four states: loading (a skeleton in the final shape), empty (one teaching sentence, one
   action), error (the server's message, the request id, Retry) and no permission.
4. Live updates through `stream().on(...)`, and a refetch on `onResync`.
5. Tests: Vitest for any pure logic, a Playwright case, the axe pass, a screenshot, Lighthouse.
6. Tokens only: if a component needs a value that doesn't exist, add it to `DESIGN.md` and
   `web/tokens/tokens.json` first (`TestTokensMatchDesignMD` keeps them in step).

## Browser support

Current Chrome, Edge, Firefox and Safari (the last two versions). The Popover API and anchor
positioning are Baseline but not needed: menus and tooltips are positioned in JavaScript. `<dialog
closedby>` isn't in stable Safari, so sheets implement their own light dismiss.

## What it doesn't do

- No offline mode: without the network the Console says so and reconnects when it can.
- No mobile app; the Console works down to 360 px wide.
- No third-party analytics, fonts, scripts or CDNs, ever.
- Approvals, Policies, Connectors and Settings are still S24's stubs (S26 builds them).
- The browser seal check trusts the keys this server serves; it is a convenience, not an
  independent check.
- The REST action detail carries the request and result (S13); the Console renders them only when
  you open them, but it doesn't avoid loading them.
- The Overview's counters move live between refetches and can drift for up to 60 s.
- The feed shows the newest 500 rows in the page; older ones are a click on `Load older actions`.
