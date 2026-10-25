#!/usr/bin/env node
// check-seo.mjs (VIRAL-3 DOC-03, DOC-05): gates over the built site for search engines.
//   sitemap   no thin per-code page and no /_kit/ in it; lastmod on (almost) every URL, not all one date
//   noindex   every thin page says noindex; no other page does
//   titles    every indexed page's <title> is at most 60 characters
//   json-ld   every indexed page has one JSON-LD block: TechArticle (WebSite on the home page); step-by-step pages
//             carry it in an @graph with a HowTo whose steps are anchors on the page (seo-aeo-v1)
//   canonical every indexed page's canonical is its own URL (seo-aeo-v1)
//   answer    an indexed page doesn't open with the imported-file note: its first paragraph is its own (seo-aeo-v1)
//   robots    named groups for search, AI retrieval and AI training, each repeating Disallow: /_kit/
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { dist, pages } from './check-dist-lib.mjs';
import { SITE, TITLE_MAX, isThin } from './seo.mjs';

const decode = (s) => s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');

/** Problems with a robots.txt: each group needs Disallow: /_kit/, and the named bots and * must have a group. */
export function robotsProblems(text, mustName = ['Googlebot', 'OAI-SearchBot', 'GPTBot', '*']) {
  const groups = [];
  let cur = null;
  for (const raw of text.split('\n')) {
    const line = raw.replace(/#.*/, '').trim();
    if (!line) continue;
    const [k, ...v] = line.split(':');
    const key = k.trim().toLowerCase();
    const val = v.join(':').trim();
    if (key === 'user-agent') {
      if (!cur || cur.rules.length) groups.push((cur = { agents: [], rules: [] }));
      cur.agents.push(val);
    } else if (key === 'allow' || key === 'disallow') {
      if (!cur) return ['a rule comes before any User-agent line'];
      cur.rules.push(`${key}:${val}`);
    }
  }
  const out = [];
  for (const g of groups) if (!g.rules.includes('disallow:/_kit/')) out.push(`the group for ${g.agents.join(', ')} doesn't repeat Disallow: /_kit/`);
  for (const n of mustName) if (!groups.some((g) => g.agents.includes(n))) out.push(`no group names ${n}`);
  if (groups.length < 4) out.push(`${groups.length} groups; want search, AI retrieval, AI training and *`);
  if (!/^Sitemap: https:\/\/\S+$/m.test(text)) out.push('no Sitemap line');
  return out;
}

/** Problems with one built page (path, html). */
export function pageProblems(path, html) {
  const out = [];
  const noindex = /<meta name="robots" content="noindex/.test(html);
  if (isThin(path)) {
    if (!noindex) out.push(`${path}: a thin page without noindex`);
    return out;
  }
  if (noindex) out.push(`${path}: noindex on a page that should be indexed`);
  const title = decode(/<title>([^<]*)<\/title>/.exec(html)?.[1] ?? '');
  if (!title) out.push(`${path}: no <title>`);
  else if (title.length > TITLE_MAX) out.push(`${path}: <title> is ${title.length} characters (at most ${TITLE_MAX}): ${title}`);
  const ld = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
  if (ld.length !== 1) out.push(`${path}: ${ld.length} JSON-LD blocks (want 1)`);
  else {
    let o;
    try {
      o = JSON.parse(ld[0][1]);
    } catch (e) {
      out.push(`${path}: JSON-LD doesn't parse: ${e.message}`);
    }
    const want = path === '/' ? 'WebSite' : 'TechArticle';
    const nodes = o ? (o['@graph'] ?? [o]) : [];
    const main = nodes.find((n) => n['@type'] === want);
    if (o && !main) out.push(`${path}: JSON-LD has no ${want}`);
    if (main && want === 'TechArticle' && (!main.headline || main.url !== SITE + path)) out.push(`${path}: JSON-LD needs a headline and url ${SITE + path}`);
    for (const n of nodes.filter((x) => x['@type'] === 'HowTo')) {
      if (!Array.isArray(n.step) || n.step.length < 2) out.push(`${path}: a HowTo needs at least two steps`);
      for (const st of n.step ?? []) {
        const id = String(st.url ?? '').split('#')[1];
        if (!id || !html.includes(`id="${id}"`)) out.push(`${path}: HowTo step "${st.name}" points at #${id}, which isn't on the page`);
      }
    }
    if (o && nodes.some((n) => !['WebSite', 'TechArticle', 'HowTo'].includes(n['@type']))) out.push(`${path}: unexpected JSON-LD type`);
  }
  const canonical = /<link rel="canonical" href="([^"]+)"/.exec(html)?.[1];
  if (canonical !== SITE + path) out.push(`${path}: canonical ${canonical ?? '(none)'} is not ${SITE + path}`);
  const content = /<div class="sl-markdown-content">([\s\S]*)/.exec(html)?.[1] ?? '';
  const first = /<(p|aside|ul|ol|h2|h3|pre|table)[\s>][\s\S]*?<\/\1>/.exec(content)?.[0] ?? '';
  if (/imported at build time/.test(first)) out.push(`${path}: opens with the imported-file note, not its own first paragraph`);
  return out;
}

/** Problems with the sitemap files' text. */
export function sitemapProblems(xml) {
  const out = [];
  const urls = [...xml.matchAll(/<url>([\s\S]*?)<\/url>/g)].map((m) => m[1]);
  let dated = 0;
  const seen = new Set();
  for (const u of urls) {
    const loc = /<loc>([^<]*)<\/loc>/.exec(u)?.[1] ?? '';
    const path = loc.startsWith(SITE) ? loc.slice(SITE.length) || '/' : loc;
    if (isThin(path) || path.startsWith('/_kit/')) out.push(`sitemap lists ${path}`);
    const lm = /<lastmod>([^<]*)<\/lastmod>/.exec(u)?.[1];
    if (lm && !/^\d{4}-\d{2}-\d{2}$/.test(lm)) out.push(`sitemap: ${path} has lastmod ${lm}`);
    if (lm) {
      dated++;
      seen.add(lm);
    }
  }
  // Every URL on one day means the dates came from one commit (a shallow clone), not from each source.
  if (dated >= 20 && seen.size === 1) out.push(`sitemap: all ${dated} lastmod dates are ${[...seen][0]}; each should be its source's last commit (a shallow checkout?)`);
  // A page added since the last commit has no date yet; more than a few missing means lastmod broke.
  if (urls.length && dated < urls.length * 0.95) out.push(`sitemap: only ${dated} of ${urls.length} URLs have a lastmod`);
  return { problems: out, urls: urls.length, dated };
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const fails = [];
  const xml = readdirSync(dist)
    .filter((f) => /^sitemap-\d+\.xml$/.test(f))
    .map((f) => readFileSync(join(dist, f), 'utf8'))
    .join('\n');
  const sm = sitemapProblems(xml);
  fails.push(...sm.problems);
  let checked = 0;
  for (const [path, , html] of pages) {
    // 404 and the design kit aren't content; /samples/ holds sealed sample files copied as they are
    // (editing one would break its seal), and the sitemap never lists them.
    if (path === '/404/' || path.startsWith('/_kit/') || path.startsWith('/samples/')) continue;
    checked++;
    fails.push(...pageProblems(path, html));
  }
  fails.push(...robotsProblems(readFileSync(join(dist, 'robots.txt'), 'utf8')).map((p) => `robots.txt: ${p}`));
  if (fails.length) {
    for (const f of fails.slice(0, 60)) console.error('✕ ' + f);
    if (fails.length > 60) console.error(`… and ${fails.length - 60} more`);
    process.exit(1);
  }
  console.log(`✓ seo · sitemap ${sm.urls} URLs, ${sm.dated} with lastmod, no thin page · ${checked} pages: thin ones noindex, titles ≤ ${TITLE_MAX}, one JSON-LD each · robots groups`);
}
