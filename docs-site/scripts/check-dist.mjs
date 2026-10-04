#!/usr/bin/env node
// check-dist.mjs: gates over the built site (S33 §3.6, §7). Exit 1 on any failure.
//   links      every internal href in dist/**/*.html (path and #anchor) resolves to a page and an id
//   third      no <script src> or stylesheet <link> points off the site (no third-party requests)
//   secrets    no live or real-looking key, token or private key anywhere in dist
//   js         no page loads more than 150 KB of JS; a content page loads at most 60 KB
//   drafts     none of the draft pages is in dist
import { existsSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { walk } from './postbuild.mjs';
import { ids, pages, resolvePath } from './check-dist-lib.mjs';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
import { dist } from './check-dist-lib.mjs';

const fails = [];
let links = 0;
for (const [rel, , s] of pages) {
  for (const m of s.matchAll(/\shref="([^"]*)"/g)) {
    const href = m[1].replace(/&amp;/g, '&');
    if (/^(https?:|mailto:|data:|javascript:|cursor:|vscode:|lmstudio:|goose:)/.test(href) || href === '') continue;
    links++;
    const abs = href.startsWith('#') ? rel + href : href.startsWith('/') ? href : new URL(href, 'http://x' + rel).pathname;
    const p = resolvePath(abs);
    if (p) fails.push(`links: ${rel} → ${href}: ${p}`);
  }
  for (const m of s.matchAll(/<script[^>]*\bsrc="(https?:)?\/\/[^"]*"/g)) fails.push(`third: ${rel}: ${m[0]}`);
  for (const m of s.matchAll(/<link[^>]*rel="stylesheet"[^>]*href="(https?:)?\/\/[^"]*"/g)) fails.push(`third: ${rel}: ${m[0]}`);
}

// A key-shaped string fails unless it is one of the documented example values in secrets-allow.txt
// (each with its reason). A PEM block fails only with a body after its header.
const secretPatterns = [/dg_live_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}/g, /dg_test_[a-z0-9]{8}_[A-Za-z0-9]{32}/g, /sk_live_[A-Za-z0-9]{10,}/g, /xox[bap]-[A-Za-z0-9-]{10,}/g, /-----BEGIN [A-Z ]*PRIVATE KEY-----\s*[A-Za-z0-9+/=]{40,}/g];
const allowed = new Set(readFileSync(join(site, 'secrets-allow.txt'), 'utf8').split('\n').filter((l) => l && !l.startsWith('#')).map((l) => l.split(/\s+/)[0]));
for (const f of walk(dist)) {
  if (!/\.(html|txt|md|json|js|xml)$/.test(f)) continue;
  const s = readFileSync(f, 'utf8');
  for (const re of secretPatterns) for (const m of s.matchAll(re)) if (!allowed.has(m[0])) fails.push(`secrets: ${relative(dist, f)} holds ${m[0].slice(0, 12)}…`);
}

// JS per page: external module scripts it loads (sizes on disk) plus inline scripts.
const jsSize = (src) => {
  const p = join(dist, src.split('?')[0]);
  return existsSync(p) ? statSync(p).size : 0;
};
let maxJs = 0;
for (const [rel, , s] of pages) {
  let total = 0;
  for (const m of s.matchAll(/<script[^>]*\bsrc="([^"]+)"/g)) total += jsSize(m[1]);
  for (const m of s.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g)) total += Buffer.byteLength(m[1]);
  maxJs = Math.max(maxJs, total);
  const content = !rel.startsWith('/reference/api/');
  if (total > 150 * 1024) fails.push(`js: ${rel} loads ${Math.round(total / 1024)} KB of JS (limit 150 KB)`);
  else if (content && total > 60 * 1024) fails.push(`js: content page ${rel} loads ${Math.round(total / 1024)} KB of JS (limit 60 KB)`);
}

const drafts = ['/get-started/install/', '/get-started/playground/', '/guides/scan-every-pull-request/', '/guides/policy-packs/', '/reference/pro-and-licences/'];
for (const d of drafts) if (ids.has(d)) fails.push(`drafts: ${d} is in dist`);

if (fails.length) {
  for (const f of fails.slice(0, 80)) console.error('✕ ' + f);
  if (fails.length > 80) console.error(`… and ${fails.length - 80} more`);
  process.exit(1);
}
console.log(`✓ dist · ${pages.length} pages · ${links} internal links and anchors resolve · no third-party scripts or stylesheets · no secrets · max JS ${Math.round(maxJs / 1024)} KB · 0 drafts`);
