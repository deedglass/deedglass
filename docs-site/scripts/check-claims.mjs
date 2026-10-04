#!/usr/bin/env node
import { fileURLToPath } from 'node:url';
// check-claims.mjs (S33 §3.6): a built page that lists claims: [C-…] needs each row in
// the ops repo's CLAIMS_REGISTER.md approved, with its "Do not use until" condition met ("none", or
// a value starting "met"). Drafts are checked by their owner session when it turns them on.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { pages } from './lib-pages.mjs';

export function usable(row) {
  return /^APPROVED/.test(row.approved) && (/^(none|—|-)$/i.test(row.until) || /^met\b/i.test(row.until));
}
if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) main();
function main() {
const path = process.env.CLAIMS_REGISTER ?? join(fileURLToPath(new URL('../../..', import.meta.url)), 'ops', 'CLAIMS_REGISTER.md');
if (!existsSync(path)) {
  console.log(`○ claims · ${path} isn't on this machine; skipped (CI checks the frontmatter shape only)`);
  return;
}
const rows = new Map();
for (const line of readFileSync(path, 'utf8').split('\n')) {
  const m = /^\| (C-\d{3}) \|/.exec(line);
  if (!m) continue;
  const c = line.split(' | ');
  rows.set(m[1], { approved: c[5] ?? '', until: (c[6] ?? '').trim() });
}
const fails = [];
let used = 0;
for (const p of pages()) {
  if (p.fm.draft || !p.fm.claims?.length) continue;
  for (const id of p.fm.claims) {
    used++;
    const r = rows.get(id);
    if (!r) fails.push(`${p.rel}: ${id} isn't in the claims register`);
    else if (!usable(r)) fails.push(`${p.rel}: ${id} isn't usable yet (approved: "${r.approved}", do not use until: "${r.until}")`);
  }
}
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log(`✓ claims · ${used} claims on built pages, each approved and usable`);
}
