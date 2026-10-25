// seo.mjs: what search engines and LLMs get from the docs (VIRAL-3 DOC-02, DOC-05). One table, read by
// astro.config.mjs (the llms-small set), src/routeData.ts (noindex, titles, JSON-LD), postbuild.mjs
// (the sitemap and the llms files) and the gates (check-seo.mjs, check-llms.mjs).
import { spawnSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
const repo = join(site, '..');

export const SITE = 'https://docs.deedglass.com';

/** The one description of Deedglass (docs/description.json, checked by docs/description_test.go). Read on
 *  call, not at import: src/routeData.ts imports this module into Astro's bundle, where paths move. */
export const description = () => JSON.parse(readFileSync(join(repo, 'docs', 'description.json'), 'utf8'));

// ---- index hygiene (DOC-05) ----
// Thin pages: one per API error code, policy diagnostic and API operation. They stay on the site (the
// product links each error's page) but get noindex and stay out of the sitemap. Their index pages
// (/errors/, /policies/errors/, /reference/api/) are indexed.
export const THIN = [/^\/errors\/[^/]+\/$/, /^\/policies\/errors\/[^/]+\/$/, /^\/reference\/api\/operations\//];
export const isThin = (path) => THIN.some((re) => re.test(path));
/** Never in the sitemap: the thin pages and the design kit. */
export const notInSitemap = (path) => isThin(path) || path.startsWith('/_kit/');

/** The longest <title> an indexed page may have, " | Deedglass Docs" included. */
export const TITLE_MAX = 60;
export const SITE_TITLE = 'Deedglass Docs';
/** The <title> for a page: "Title | Deedglass Docs", or the title alone when that is too long or is
 *  the site's name (the home page). */
export function pageTitle(title, path = '') {
  if (title === SITE_TITLE) return title;
  // freshness-1019: an engineering note often shares its name with the guide page on the same subject
  // (Stop, Insights, The Ledger …), so its <title> says which one it is, when that still fits.
  if (/^\/reference\/engineering\/[^/]+\/$/.test(path)) {
    const named = `${title} (engineering notes)`;
    if (`${named} | ${SITE_TITLE}`.length <= TITLE_MAX) return `${named} | ${SITE_TITLE}`;
    if (named.length <= TITLE_MAX) return named;
  }
  const full = `${title} | ${SITE_TITLE}`;
  return full.length <= TITLE_MAX ? full : title;
}

/** The TechArticle JSON-LD for a content page (the home page gets WebSite). Pure data. */
export function jsonLd({ path, title, description, dateModified, tagline }) {
  const publisher = { '@type': 'Organization', name: 'Deedglass', url: 'https://deedglass.com' };
  if (path === '/') {
    return { '@context': 'https://schema.org', '@type': 'WebSite', name: SITE_TITLE, url: `${SITE}/`, description: tagline, publisher };
  }
  const ld = {
    '@context': 'https://schema.org',
    '@type': 'TechArticle',
    headline: title,
    description,
    url: SITE + path,
    inLanguage: 'en',
    isPartOf: { '@type': 'WebSite', name: SITE_TITLE, url: `${SITE}/` },
    publisher,
  };
  if (dateModified) ld.dateModified = dateModified;
  return ld;
}
/** JSON for an inline <script type="application/ld+json">: "<" escaped so no string can end the tag. */
export const ldScript = (obj) => JSON.stringify(obj).replace(/</g, '\\u003c');

// ---- first paint (DOC-LCP) ----
/** Vite's assetsInlineLimit as a function (astro.config.mjs): true inlines. Stylesheets and scripts under
 *  4 KB go inline, so first paint waits on fewer requests; postbuild hashes every inline script into the
 *  CSP. Nothing else is ever inlined: a font or image as a data: URL would break font-src 'self'. */
export function inlineSmallCode(file, content) {
  return /\.(css|js)$/.test(file) && content.length < 4096;
}

// ---- llms files (DOC-02) ----
// Page ids are content slugs without a trailing slash ('' or 'index' is the home page).
// llms-full.txt: every page except generated reference, dated reports and engineering notes, each of
// which stays on the site (llms.txt says where). Order matters only for the note.
export const FULL_OUT = [
  { re: /^errors(\/|$)/, what: 'the per-code error pages', at: '/errors/' },
  { re: /^policies\/errors(\/|$)/, what: 'the policy diagnostics', at: '/policies/errors/' },
  { re: /^reference\/(http\/endpoints|api)(\/|$)/, what: 'the API reference', at: '/reference/api/' },
  { re: /^reference\/cli(\/|$)/, what: 'the CLI reference (also deedglass help)', at: '/reference/cli/' },
  { re: /^reference\/config$/, what: 'the settings table', at: '/reference/config/' },
  { re: /^security\/attack-suite$/, what: 'the attack-suite results', at: '/security/attack-suite/' },
  { re: /^security\/review$/, what: 'the dated security review', at: '/security/review/' },
  { re: /^reference\/engineering(\/|$)/, what: 'the engineering notes', at: '/reference/engineering/' },
  // mostly code, one page per framework; the table that links them all is in Install in any client
  { re: /^frameworks\//, what: 'the agent framework recipes', at: '/reference/engineering/install-clients/#agent-frameworks' },
];
// llms-small.txt: what an agent needs to explain, install and start using Deedglass. The SDK
// references, the limits in full and the specialist guides are in llms-full.txt.
const SMALL_IN = [
  /^(index)?$/, /^quickstart$/, /^faq$/, /^get-started\//, /^concepts\//,
  /^guides\/(?!(stripe-refunds|github|fhir-server|healthcare-fhir-pack|medplum|phone-approvals|replay|rotate-keys|production|multi-agent|slack-approvals|supabase|neon)$)/,
  /^security\/(overview|data-handling|disclosure)$/,
];
// llms-postgres.txt: checking and undoing what an agent changed in Postgres.
export const POSTGRES_SET = {
  label: 'Postgres',
  description: 'checking what an AI agent changed in Postgres, from setup to undo',
  paths: [
    'guides/check-what-an-agent-changed-in-postgres', 'quickstart', 'get-started/postgres', 'concepts/verification-levels',
    'concepts/what-matches-means', 'guides/catch-a-mismatch', 'guides/block-destructive-sql', 'guides/undo', 'guides/supabase', 'guides/neon',
    'guides/observe-beside-another-gate', 'reference/connectors/postgres', 'reference/observe', 'security/limitations',
    'security/limitations/postgres',
  ],
};
export const inFull = (id) => !FULL_OUT.some((x) => x.re.test(id));
/** llms.txt's note on what llms-full.txt leaves out, and where each part is. */
export const fullNote = () =>
  `- The complete documentation leaves out ${FULL_OUT.map((x) => x.what).join(', ').replace(/, ([^,]*)$/, ' and $1')}. Each is on the site: ${FULL_OUT.map((x) => SITE + x.at).filter((u, i, a) => a.indexOf(u) === i).join(', ')}`;
export const inSmall = (id) => inFull(id) && SMALL_IN.some((re) => re.test(id));
/** Byte budgets the gate enforces (check-llms.mjs). */
export const LLMS_BUDGET = { 'llms-small.txt': 150 * 1024, 'llms-full.txt': 750 * 1024, 'llms-postgres.txt': 150 * 1024 };
/** The plugin joins pages with this; postbuild splits on it, drops pages and removes it. */
export const PAGE_MARK = '\n\n<!-- deedglass:llms-page -->\n\n';

/** Task pages llms.txt names by URL (DOC-04), so an agent can open the one it needs without the full set.
 *  check-llms.mjs fails when llms.txt leaves one out or its page isn't built. */
export const LLMS_PAGES = [
  { path: '/guides/check-what-an-agent-changed-in-postgres/', title: 'How to check what an AI agent changed in Postgres', what: 'the commands, whether or not the agent goes through Deedglass' },
  { path: '/reference/scan-grade/', title: 'How the scan grade is calculated', what: 'the formula behind the A to F grade of `deedglass scan`, with worked examples' },
];

/** The llms.txt "details" block (DOC-06): what an agent setting Deedglass up must do, and where to read. */
export const llmsDetails = (licence) => [
  "- If the user asked you to set up Deedglass: ask before each change. Starting Postgres in Docker, starting a local server, writing `.deedglass/` and editing an MCP client's config are each the user's call. [llms-install.md](https://github.com/deedglass/deedglass/blob/main/llms-install.md) lists the steps and what each one changes; [SKILL.md](https://github.com/deedglass/deedglass/blob/main/integrations/claude-code/skills/safe-postgres-agents/SKILL.md) covers giving an agent Postgres access carefully.",
  '- `npx -y deedglass scan --no-write` and `npx -y deedglass play` (deedglass 0.1.1 or newer) send nothing and need no Docker (npx itself downloads the package from npm).',
  "- People read plain names: a check is Claimed, Checked or Proven, and its result is Matches, Doesn't match or Can't check. The API, JSON and policy files keep the short names (L0, L1, L2; VERIFIED, MISMATCH, UNVERIFIABLE).",
  '- Start with the Quickstart, then "What Matches means" for exactly what a result rests on. Every feature page ends with "What it doesn\'t do".',
  ...LLMS_PAGES.map((p) => `- [${p.title}](${SITE}${p.path}): ${p.what}.`),
  `- ${licence}`,
].join('\n');

// ---- lastmod (DOC-05) ----
/** True when the checkout is shallow (actions/checkout's default fetch-depth of 1). */
export function isShallow(cwd = repo) {
  const r = spawnSync('git', ['rev-parse', '--is-shallow-repository'], { cwd, encoding: 'utf8' });
  return r.status === 0 && r.stdout.trim() === 'true';
}

/** The last commit date (YYYY-MM-DD) of each path under the given repo paths, newest first wins. One git call;
 *  an empty map without git or history. Deterministic for a given checkout. A shallow clone's oldest
 *  commit lists every file, so git log there would give every page that commit's date: it throws instead
 *  (CI and the deploy check out with fetch-depth: 0), unless DOCS_UNDATED=1 asks for a build without dates. */
export function gitDates(paths, { cwd = repo, env = process.env } = {}) {
  if (isShallow(cwd)) {
    if (env.DOCS_UNDATED === '1') return new Map();
    throw new Error(
      'this checkout is shallow, so git log would date every page with one commit. Fetch the history (git fetch --unshallow, ' +
        'or actions/checkout with fetch-depth: 0), or set DOCS_UNDATED=1 to build without lastmod and dateModified',
    );
  }
  const r = spawnSync('git', ['log', '--format=%x00%cs', '--name-only', '--no-renames', '--', ...paths], { cwd, encoding: 'utf8', maxBuffer: 256 * 1024 * 1024 });
  const out = new Map();
  if (r.status !== 0) return out;
  let date = '';
  for (const line of r.stdout.split('\n')) {
    if (line.startsWith('\0')) date = line.slice(1);
    else if (line && date && !out.has(line)) out.set(line, date);
  }
  return out;
}

/** Every file under dir (recursive), as absolute paths. */
export function walkFiles(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) walkFiles(p, out);
    else out.push(p);
  }
  return out;
}
