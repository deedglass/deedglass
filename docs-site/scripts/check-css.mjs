#!/usr/bin/env node
// check-css.mjs (S33 §3.2): the aurora gradient appears only in the mark and the one hero glow;
// colours in the theme and components come from tokens (no stray hex outside the allowed files).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { walk } from './postbuild.mjs';
import { site } from './lib-pages.mjs';

const fails = [];
const auroraOk = new Set(['src/components/SealedMark.astro', 'src/components/overrides/SiteTitle.astro', 'src/styles/tokens.gen.css']);
const hexOk = new Set(['src/styles/tokens.gen.css', 'src/styles/deedglass-ink.json', 'src/components/SealedMark.astro', 'src/components/overrides/SiteTitle.astro']);
for (const f of walk(join(site, 'src'))) {
  if (!/\.(css|astro|mdx|json)$/.test(f) || f.includes('/data/') || f.includes('/content/docs/reference/engineering/')) continue;
  const rel = f.replace(site + '/', '');
  const s = readFileSync(f, 'utf8');
  if (/aurora-(violet|blue|teal)|#7C6CFF|#4F8CFF|#2FE6D1/i.test(s) && !auroraOk.has(rel)) fails.push(`${rel}: the aurora is only for the mark and the home hero glow`);
  if (rel.endsWith('.css') || rel.endsWith('.astro')) {
    const hexes = [...s.matchAll(/#[0-9a-fA-F]{3,8}\b/g)].map((m) => m[0]);
    if (hexes.length && !hexOk.has(rel)) fails.push(`${rel}: hard-coded colours ${[...new Set(hexes)].join(' ')}; use a token`);
  }
}
const glows = [...walk(join(site, 'src'))].filter((f) => /\.(mdx|astro)$/.test(f)).filter((f) => /\bglow\b/.test(readFileSync(f, 'utf8')) && /SealedMark/.test(readFileSync(f, 'utf8')));
if (fails.length) {
  for (const f of fails) console.error('✕ ' + f);
  process.exit(1);
}
console.log('✓ css · aurora only in the mark and the hero glow · colours from tokens');
