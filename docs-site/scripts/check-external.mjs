#!/usr/bin/env node
// check-external.mjs (S33 §3.6): every external link in dist/ answers 2xx or 3xx (HEAD, then GET
// when HEAD is refused), 10 s each, 4 at a time. Results are cached in .cache/links.json for 24 h.
// A failing link must be on external-allow.txt with a reason. --offline skips the network.
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { pages } from './check-dist-lib.mjs';
import { site } from './lib-pages.mjs';

if (process.argv.includes('--offline')) {
  console.log('○ external links · skipped (--offline)');
  process.exit(0);
}
const cacheFile = join(site, '.cache', 'links.json');
const cache = existsSync(cacheFile) ? JSON.parse(readFileSync(cacheFile, 'utf8')) : {};
const allow = new Map(readFileSync(join(site, 'external-allow.txt'), 'utf8').split('\n').filter((l) => l && !l.startsWith('#')).map((l) => { const [u, ...r] = l.split(/\s+/); return [u, r.join(' ')]; }));
const urls = new Set();
for (const [, , s] of pages) for (const m of s.matchAll(/\shref="(https?:\/\/[^"#]+)/g)) {
  const u = m[1].replace(/&amp;/g, '&');
  if (/^https?:\/\/(127\.0\.0\.1|localhost|deedglass\.example|hooks\.example|mcp\.example|db\.internal)/.test(u)) continue;
  if (/^https:\/\/(docs\.)?deedglass\.com/.test(u)) continue; // our own site, not live before launch
  urls.add(u);
}
const now = Date.now();
async function probe(u) {
  const c = cache[u];
  if (c && now - c.at < 24 * 3600e3) return c.status;
  let status = 0;
  for (const method of ['HEAD', 'GET']) {
    try {
      const r = await fetch(u, { method, redirect: 'follow', signal: AbortSignal.timeout(10_000), headers: { 'User-Agent': 'deedglass-docs-linkcheck/1.0' } });
      status = r.status;
      if (r.ok) break;
    } catch {
      status = 0;
    }
  }
  cache[u] = { status, at: now };
  return status;
}
const list = [...urls];
const fails = [];
let i = 0;
await Promise.all(Array.from({ length: 4 }, async () => {
  while (i < list.length) {
    const u = list[i++];
    const st = await probe(u);
    if (!(st >= 200 && st < 400) && !allow.has(u)) fails.push(`${u} → ${st || 'no answer'}`);
  }
}));
mkdirSync(join(site, '.cache'), { recursive: true });
writeFileSync(cacheFile, JSON.stringify(cache, null, 1));
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  console.error('  Next: fix the link, or add it to external-allow.txt with a reason.');
  process.exit(1);
}
console.log(`✓ external links · ${list.length} checked · ${allow.size} allowed with a reason`);
