#!/usr/bin/env node
// check-drafts.mjs (S33 §3.6): every draft names its owner; none is in dist/; every built page
// that names a CLI command names one help --json lists as built.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { pages, site } from './lib-pages.mjs';

const cli = JSON.parse(readFileSync(join(site, 'src/data/cli.json'), 'utf8'));
const built = new Set();
const unbuilt = new Set();
for (const g of cli.groups) for (const c of g.commands) (c.built ? built : unbuilt).add(c.path.join(' '));
const planned = ['licence']; // S47B built policy packs, policy install, mcp install-links and deeds share
export function draftProblems(list) {
const fails = [];
const owners = {};
for (const p of list) {
  if (p.fm.draft) {
    if (!p.fm.owner) fails.push(`${p.rel}: draft without owner`);
    owners[p.fm.owner] = (owners[p.fm.owner] ?? 0) + 1;
    if (existsSync(join(site, 'dist', p.slug, 'index.html'))) fails.push(`${p.rel}: a draft is in dist/`);
    continue;
  }
  if (p.generated) continue;
  for (const cmd of planned) if (new RegExp(`deedglass ${cmd}\\b`).test(p.body)) fails.push(`${p.rel}: a built page uses "deedglass ${cmd}", which isn't built`);
  for (const u of unbuilt) if (new RegExp(`\\bdeedglass ${u}\\b`).test(p.body)) fails.push(`${p.rel}: a built page uses "deedglass ${u}", which help --json marks unbuilt`);
}
return { fails, owners };
}
if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
const { fails, owners } = draftProblems(pages());
const total = Object.values(owners).reduce((a, b) => a + b, 0);
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log(`✓ ${total} drafts (${Object.entries(owners).map(([k, v]) => `${k}: ${v}`).join(', ')}) · 0 in dist`);
}
