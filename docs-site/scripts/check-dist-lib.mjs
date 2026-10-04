// The built site as data, for the gates: every page's ids, the redirects, and resolvePath().
import { existsSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { walk } from './postbuild.mjs';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
export const dist = process.env.DIST ?? join(site, 'dist');

export const ids = new Map(); // page path → Set of ids
export const pages = []; // [path, file, html]
for (const f of walk(dist)) {
  if (!f.endsWith('.html')) continue;
  const rel = '/' + relative(dist, f).replace(/index\.html$/, '').replace(/\.html$/, '/');
  const s = readFileSync(f, 'utf8');
  ids.set(rel, new Set([...s.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
  pages.push([rel, f, s]);
}

export const redir = new Map();
if (existsSync(join(dist, '_redirects'))) {
  for (const line of readFileSync(join(dist, '_redirects'), 'utf8').split('\n')) {
    const [from, to] = line.trim().split(/\s+/);
    if (from && to && !from.startsWith('#')) redir.set(from, to);
  }
}

/** null when a site path (with an optional #anchor) resolves to a page and id, else the problem. */
export function resolvePath(href) {
  let [path, anchor] = href.split('#');
  path = decodeURI(path || '/');
  if (redir.has(path)) path = redir.get(path);
  else if (redir.has(path.replace(/\/$/, ''))) path = redir.get(path.replace(/\/$/, ''));
  if (!path.endsWith('/') && !/\.[a-z0-9]+$/i.test(path)) path += '/';
  if (/\.[a-z0-9]+$/i.test(path)) return existsSync(join(dist, path)) ? null : `no file ${path}`;
  if (!ids.has(path)) return `no page ${path}`;
  if (anchor && !ids.get(path).has(decodeURIComponent(anchor))) return `no #${anchor} on ${path}`;
  return null;
}
