#!/usr/bin/env node
// check-pricing.mjs (S33 §3.6, §5): no retired offer, no founding band, no licence-category
// phrase before the repo is public (DEEDGLASS_REPO_PUBLIC=1 lifts that one), prices only where §5
// puts them, and the FAQ answer exactly as approved.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { walk } from './postbuild.mjs';
import { pages, site } from './lib-pages.mjs';

// DL-131 (free while we grow): every feature is free, no paid tier is named in the docs, and the
// FAQ answer is the approved sentence. P-086 (the ee/ licence) still holds.
export const FAQ_FREE = "Yes. Every Deedglass feature is free while we grow, with no licence key: the core is Apache-2.0, and the features in `ee/` are free to use and stay FSL-1.1-ALv2. We plan paid options for teams and a hosted service later; anything free today stays free for people who run Deedglass themselves.";
export const banned = [/\$199/, /\$390/, /deedglass\.com\/check/, /[Ff]ounding band/, /deedglass\.com\/pro\b/, /deedglass\.com\/agencies/]; // DL-131 withdrew the paid tiers and their buy pages; the retired $390 band stays banned
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
const pricePages = new Set(); // DL-131: no page names a price
for (const p of pages()) {
  if (p.generated) continue;
  if (priceRe.test(p.body) && !pricePages.has(p.rel)) fails.push(`${p.rel}: names a price; every feature is free while we grow (DL-131)`);
}
const faq = pages().find((p) => p.rel === 'faq.mdx');
if (!faq || !faq.body.includes(FAQ_FREE)) fails.push('faq.mdx: the "Is it free?" answer must be exactly the approved sentence');
const pro = pages().find((p) => p.rel === 'reference/pro-and-licences.mdx');
for (const want of ['Every Deedglass feature is free to use, with no licence key', 'Anything free today stays free for people who run Deedglass themselves']) if (!pro?.body.includes(want)) fails.push(`pro-and-licences.mdx: missing "${want}"`);
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log('✓ pricing and free wording · no retired offer · FAQ answer exact');
}
