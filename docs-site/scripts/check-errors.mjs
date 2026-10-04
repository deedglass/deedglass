#!/usr/bin/env node
// check-errors.mjs (S33 §3.4): the error catalog, its prose and its pages agree, and every
// docs.deedglass.com URL the product prints (doc_url in Go, the OpenAPI spec, goldens, the SDKs,
// the Console, the CLI's links) resolves in dist/, through _redirects, anchors included.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
const repo = join(site, '..');
const dist = process.env.DIST ?? join(site, 'dist');
const { resolvePath } = await import('./check-dist-lib.mjs');

const errors = JSON.parse(readFileSync(join(site, 'src/data/errors.json'), 'utf8'));
const prose = JSON.parse(readFileSync(process.env.ERRORS_PROSE ?? join(site, 'src/data/errors-prose.json'), 'utf8'));
const fails = [];
const codes = new Set(errors.map((e) => e.code));
for (const c of codes) {
  if (!prose[c]) fails.push(`${c}: no "## ${c}" section in docs/api/errors.md`);
  if (resolvePath(`/errors/${c}/`)) fails.push(`${c}: no page /errors/${c}/`);
}
for (const c of Object.keys(prose)) if (!codes.has(c)) fails.push(`docs/api/errors.md has a section for ${c}, which isn't in the catalog`);
const cliCodes = JSON.parse(readFileSync(join(site, 'src/data/cli-errors.json'), 'utf8')).map((c) => c.code);
for (const c of cliCodes) if (resolvePath(`/errors/${c}/`)) fails.push(`CLI code ${c}: no page /errors/${c}/`);
const index = readFileSync(join(dist, 'errors/index.html'), 'utf8');
const rows = new Set([...index.matchAll(/href="\/errors\/([a-z0-9_]+)\/"/g)].map((m) => m[1]));
if (rows.size !== codes.size + cliCodes.length) fails.push(`the /errors/ index lists ${rows.size} codes; the catalog has ${codes.size} and the CLI ${cliCodes.length}`);

// every docs URL the product prints
const roots = ['internal', 'cmd', 'pkg', 'api', 'sdk/errors.json', 'sdk/typescript/src', 'sdk/python/src', 'testdata', 'web/src', 'npm', 'examples', 'README.md'];
const skipDir = new Set(['node_modules', 'dist', '.astro', 'coverage', '.venv', '__pycache__']);
function* files(p) {
  const full = join(repo, p);
  let st;
  try {
    st = statSync(full);
  } catch {
    return;
  }
  if (st.isFile()) {
    yield full;
    return;
  }
  for (const e of readdirSync(full)) if (!skipDir.has(e)) yield* files(join(p, e));
}
let urls = 0;
const seen = new Set();
for (const r of roots) {
  for (const f of files(r)) {
    if (!/\.(go|ts|tsx|astro|mjs|js|py|json|yaml|yml|md|txt|golden|html|sh)$/.test(f) || /\.min\.js$/.test(f)) continue;
    const s = readFileSync(f, 'utf8');
    for (const m of s.matchAll(/https:\/\/docs\.deedglass\.com(\/[^\s"'`)<>\\,;|]*)?/g)) {
      let path = (m[1] ?? '/').replace(/[.:]+$/, '');
      path = path.split(/\$\{|\{|%[sdv]|<|\[/)[0];
      if (path.endsWith('#')) path = path.slice(0, -1);
      if (/#[A-Z_]+$/.test(path)) path = path.replace(/#.*$/, ''); // a placeholder anchor in prose
      const key = path;
      if (seen.has(key)) continue;
      seen.add(key);
      urls++;
      const p = resolvePath(path);
      if (p) fails.push(`${relative(repo, f)}: https://docs.deedglass.com${path} → ${p}`);
    }
  }
}

if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log(`✓ ${codes.size} codes · ${codes.size} pages · every doc_url resolves · ${cliCodes.length} CLI codes · ${urls} distinct docs URLs in the product resolve`);
