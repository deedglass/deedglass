# docs-site: docs.deedglass.com

Astro 7 + Starlight, static output in `dist/`. ADR-064 records the versions and why. The repo's
`docs/README.md` explains where every page's text lives and how the gates work.

```sh
npm ci --ignore-scripts      # exact pins, no install scripts
npm run dev                  # http://127.0.0.1:4321, drafts shown
npm run build                # sync-docs → astro build → postbuild (headers, redirects)
node scripts/check-all.mjs --offline   # every offline gate (make docs-check from the repo root)
npm run gen                  # regenerate src/data from the Go sources (needs Go)
```

From the repo root: `make docs`, `make docs-dev`, `make docs-check`, `make docs-snippets` (the demo
stack first: `make demo-up DEMO_PORT=8091`), `make docs-lighthouse`.

## Deploy (the founder, on launch day)

Cloudflare Pages, a new project from the GitHub repo:

- Root directory: `docs-site`
- Build command: `npm ci --ignore-scripts && npm run build`
- Output directory: `dist`
- Environment: `NODE_VERSION=22` (22.12 or later)
- Custom domain: `docs.deedglass.com`

The build needs only Node: `src/data/` is committed, and the pages imported from `../docs` are read
from the same checkout. `dist/_headers` sets the CSP (script hashes computed at build) and the other
security headers; `dist/_redirects` maps the short paths the product prints (`/doctor`, `/scan`,
`/connectors/postgres`, …) and the old `docs/*.md` names. The site sets no cookies and loads nothing
from another origin.
