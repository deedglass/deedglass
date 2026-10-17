#!/usr/bin/env node
// check-pricing.mjs (S33 §3.6, §5): no retired offer, no founding band, no licence-category
// phrase before the repo is public (DEEDGLASS_REPO_PUBLIC=1 lifts that one), prices only where §5
// puts them, and the FAQ answer exactly as approved.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { walk } from './postbuild.mjs';
import { pages, site } from './lib-pages.mjs';

// P-044 (Pro is $49 a month, DL-086) and P-086 (the ee/ licence).
export const FAQ_FREE = 'The core is free and Apache-2.0; the Pro features in `ee/` are FSL-1.1-ALv2. Pro is $49 a month and adds the Action on private repos, the full report and Pro packs.';
export const banned = [/\$199/, /\$390/, /deedglass\.com\/check/, /[Ff]ounding band/]; // the $99 founding partner place is the master plan's (DL-077); the retired $390 band stays banned
// The repo is public (docs/PUBLIC_REPO_SWITCH.md): "open source" is allowed now.
export const pricingProblems = (text) => banned.filter((re) => re.test(text)).map(String);
if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) main();
function main() {
const fails = [];
for (const f of walk(join(site, 'src'))) {
  if (!/\.(mdx?|astro|ts|mjs|json|css)$/.test(f)) continue;
  const s = readFileSync(f, 'utf8');
  for (const re of banned) if (re.test(s)) fails.push(`${f.replace(site + '/', '')}: matches ${re}`);
}
const priceRe = /\$\d[\d,]*(?:\.\d\d)?\s*(?:a month|a year|once)/;
const pricePages = new Set(['faq.mdx', 'reference/pro-and-licences.mdx', 'get-started/scan.mdx']);
for (const p of pages()) {
  if (p.generated) continue;
  if (priceRe.test(p.body) && !pricePages.has(p.rel)) fails.push(`${p.rel}: names a price; prices live on the FAQ, Pro and licences, and the scan footer`);
}
const faq = pages().find((p) => p.rel === 'faq.mdx');
if (!faq || !faq.body.includes(FAQ_FREE)) fails.push('faq.mdx: the "Is it free?" answer must be exactly the approved sentence');
const pro = pages().find((p) => p.rel === 'reference/pro-and-licences.mdx');
for (const want of ['$29 a month or $290 a year', '$29 once', 'Reviews from $750']) if (!pro?.body.includes(want)) fails.push(`pro-and-licences.mdx: missing "${want}"`);
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log('✓ pricing and free wording · no retired offer · FAQ answer exact');
}
