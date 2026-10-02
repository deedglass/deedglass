# web/: the Deedglass Console

A static Astro 7 build, served by `deedglass serve` under `/app` and embedded in the binary from
`internal/console/dist`. Vanilla TypeScript, no UI framework; nothing from npm runs in the shipped
Console except what Astro and Vite emit from our code (ADR-053).

```sh
make web-install     # npm ci --ignore-scripts (exact pins)
make web-build       # tokens → icons → astro build → .br/.gz → check-dist
make web-test        # tsc + Vitest (also in make ci)
make web-e2e         # Playwright + axe + CSP listener, real serve on a throwaway PG 18
make web-lighthouse  # Lighthouse 13, ≥ 95 accessibility and best practices
make web-dev         # astro dev on :4321 with a proxy to :8080
```

| Path | What |
|---|---|
| `tokens/tokens.json` | the only place a colour is written; generated into `src/styles/tokens.css`, the verify token block, the favicon and `docs/DESIGN_SYSTEM.md` |
| `scripts/` | `gen-tokens.mjs`, `contrast.mjs`, `icons.mjs`, `compress.mjs`, `check-dist.mjs` |
| `src/lib/` | `api`, `stream`, `dom`, `copy`, `format`, `theme`, `state`, `palette`, `shortcuts`, `chips`, `seal`, `recent`, `nav`, `types` |
| `src/controllers/` | the shell, the palette, overlays, toasts, tables, tabs, the stubs, the live panel, the kit |
| `src/components/`, `src/layouts/AppShell.astro`, `src/pages/` | static markup |
| `e2e/` | Playwright specs, the screenshot pass and `lighthouse.mjs` |
| `verify/` | the browser verifier (S05); its `src/ui/mirror.ts` is the one Mirror both use |

See [docs/CONSOLE.md](../docs/CONSOLE.md) and [docs/DESIGN_SYSTEM.md](../docs/DESIGN_SYSTEM.md).
