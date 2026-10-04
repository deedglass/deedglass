#!/usr/bin/env node
// check-pages.mjs: every page has a title and a description of at most 160 characters; every
// hand-written page has lastVerified; every Concept, Guide and SDK page ends with the
// "What it doesn't do" section as its last H2 (S33 §3.3); drafts name their owner session.
import { pages } from './lib-pages.mjs';

const fails = [];
let n = 0;
let drafts = 0;
for (const p of pages()) {
  n++;
  if (!p.fm.title) fails.push(`${p.rel}: no title`);
  if (!p.fm.description) fails.push(`${p.rel}: no description`);
  else if (p.fm.description.length > 160) fails.push(`${p.rel}: description is ${p.fm.description.length} characters (at most 160)`);
  if (p.generated) continue;
  if (!p.fm.lastVerified) fails.push(`${p.rel}: no lastVerified`);
  if (p.fm.draft) {
    drafts++;
    if (!p.fm.owner) fails.push(`${p.rel}: a draft needs owner: S47 or S48`);
  }
  if (/^(concepts|guides|sdks)\//.test(p.rel) && p.rel.endsWith('.mdx')) {
    const h2 = [...p.body.matchAll(/^## (.+)$/gm)].map((m) => m[1].trim());
    const limits = h2.lastIndexOf("What it doesn't do");
    const afterLimits = h2.slice(limits + 1).filter((h) => h !== 'Next');
    if (limits < 0) fails.push(`${p.rel}: no "## What it doesn't do"`);
    else if (afterLimits.length) fails.push(`${p.rel}: "What it doesn't do" must be the last H2 (before Next); found ${afterLimits.join(', ')} after it`);
    if (!/<Limits>/.test(p.body)) fails.push(`${p.rel}: the limits go inside <Limits>`);
  }
}
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log(`✓ pages · ${n} pages · descriptions ≤ 160 · lastVerified on every hand-written page · limits last · ${drafts} drafts with owners`);
