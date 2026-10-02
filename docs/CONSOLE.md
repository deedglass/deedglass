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
- The S24 screens are stubs that list the first page of each object; S25 and S26 build them out.
