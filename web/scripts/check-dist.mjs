// The build's guard (S24 §3.12): no inline <script>, no style= attribute or <style> element, no
// HTML-string sink in any bundle, no third-party URL, aurora hex only in the Mirror's CSS and the
// favicon, and the size budgets (JS ≤ 60 KB gzip per page, CSS ≤ 30 KB, fonts ≤ 120 KB).
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';

const dist = fileURLToPath(new URL('../../internal/console/dist/', import.meta.url));
const files = (d) => readdirSync(d).flatMap((f) => (statSync(join(d, f)).isDirectory() ? files(join(d, f)) : [join(d, f)]));
const all = files(dist).filter((f) => !/\.(br|gz)$/.test(f) && !f.endsWith('.keep'));
const fails = [];
const gz = (b) => gzipSync(b, { level: 9 }).length;
const kb = (n) => (n / 1024).toFixed(1) + ' KB';

const pages = all.filter((f) => f.endsWith('.html'));
const SINKS = ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'eval(', 'new Function', 'srcdoc'];
for (const p of pages) {
  const html = readFileSync(p, 'utf8');
  const rel = relative(dist, p);
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)) {
    if (!/\bsrc=/.test(m[1]) || m[2].trim() !== '') fails.push(`${rel}: an inline <script>`);
  }
  if (/<style[\s>]/.test(html)) fails.push(`${rel}: a <style> element`);
  if (/\sstyle=/.test(html)) fails.push(`${rel}: a style= attribute`);
  if (/\son[a-z]+=/i.test(html)) fails.push(`${rel}: an inline event handler`);
  if (/(src|href)="(https?:)?\/\/(?!docs\.deedglass\.com)/.test(html)) fails.push(`${rel}: a third-party URL`);
}
let maxJS = 0;
const budget = [];
for (const p of pages) {
  const html = readFileSync(p, 'utf8');
  const js = [...html.matchAll(/<script[^>]*src="\/app\/([^"?]+)/g)].map((m) => m[1]);
  // follow static imports one level at a time
  const seen = new Set();
  const queue = [...js];
  let total = 0;
  while (queue.length) {
    const f = queue.shift();
    if (seen.has(f)) continue;
    seen.add(f);
    let src;
    try { src = readFileSync(join(dist, f)); } catch { continue; }
    total += gz(src);
    for (const m of src.toString().matchAll(/(?:import|from)\s*["'](?:\.\/|\/app\/_astro\/)([^"']+\.js)["']/g)) queue.push(f.startsWith('_astro/') ? '_astro/' + m[1] : m[1]);
  }
  budget.push([relative(dist, p), total]);
  maxJS = Math.max(maxJS, total);
}
for (const f of all.filter((f) => f.endsWith('.js'))) {
  const s = readFileSync(f, 'utf8');
  for (const sink of SINKS) if (s.includes(sink)) fails.push(`${relative(dist, f)}: uses ${sink}`);
}
const css = all.filter((f) => f.endsWith('.css'));
const cssGz = css.reduce((n, f) => n + gz(readFileSync(f)), 0);
for (const f of css) {
  const s = readFileSync(f, 'utf8').toLowerCase();
  // aurora values may only be declared as the primitive variables
  for (const hex of ['#7c6cff', '#4f8cff', '#2fe6d1']) {
    const uses = s.split(hex).length - 1;
    if (uses > 1) fails.push(`${relative(dist, f)}: ${hex} (aurora) appears ${uses} times; use var(--aurora-*) in the Mirror only`);
  }
}
const fonts = all.filter((f) => /\.(woff2?|ttf)$/.test(f));
const fontBytes = fonts.reduce((n, f) => n + statSync(f).size, 0);
if (maxJS > 60 * 1024) fails.push(`JS ${kb(maxJS)} gzip on one page; the budget is 60 KB`);
if (cssGz > 30 * 1024) fails.push(`CSS ${kb(cssGz)} gzip; the budget is 30 KB`);
if (fontBytes > 120 * 1024) fails.push(`fonts ${kb(fontBytes)}; the budget is 120 KB`);
console.log(`check-dist: ${pages.length} pages · JS ≤ ${kb(maxJS)} gzip per page (budget 60 KB) · CSS ${kb(cssGz)} gzip (30 KB) · fonts ${kb(fontBytes)} (120 KB)`);
for (const [p, n] of budget.sort((a, b) => b[1] - a[1]).filter((x, i) => i < 4 || /insights/.test(x[0]))) console.log(`  ${p}: ${kb(n)} JS gzip`);
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log('✓ no inline script or style, no HTML sinks, no third-party URLs, within budget');
