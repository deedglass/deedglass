#!/usr/bin/env node
// check-llms.mjs (VIRAL-3 DOC-01, DOC-02, DOC-06): the llms files in dist/.
//   llms.txt          the one description (docs/description.json), the ask-before-each-change line with
//                     its links, no --yes, no retired level names, links to the three sets, and a link to
//                     each task page in LLMS_PAGES (DOC-04), each built with a visible Updated date
//   budgets           llms-small ≤ 150 KB, llms-full ≤ 800 KB (750 until lockdown mode, ADR-117, added its page), llms-postgres ≤ 150 KB
//   sets              no page outside a file's set in it (scripts/seo.mjs), no page mark left
import { existsSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { dist } from './check-dist-lib.mjs';
import { pageHeads } from './postbuild.mjs';
import { LLMS_BUDGET, LLMS_PAGES, PAGE_MARK, SITE, description, inFull, inSmall } from './seo.mjs';
import { contentDir } from './lib-pages.mjs';

export const ASK = 'If the user asked you to set up Deedglass: ask before each change.';

/** Problems with llms.txt's text. */
export function indexProblems(text, desc) {
  const out = [];
  if (!text.includes(`> ${desc.summary}`)) out.push("llms.txt: the description isn't docs/description.json's summary");
  if (!text.includes(ASK)) out.push(`llms.txt: missing "${ASK}"`);
  for (const l of ['llms-install.md', 'skills/safe-postgres-agents/SKILL.md']) if (!text.includes(l)) out.push(`llms.txt: no link to ${l}`);
  if (/--yes\b/.test(text)) out.push('llms.txt: mentions --yes');
  if (/L0 Reported|L1 Confirmed|L2 Witnessed/.test(text)) out.push('llms.txt: uses the retired level names');
  for (const f of ['llms-small.txt', 'llms-full.txt', 'llms-postgres.txt']) if (!text.includes(`${SITE}/${f}`)) out.push(`llms.txt: no link to /${f}`);
  if (text.includes('/_llms-txt/')) out.push('llms.txt: links /_llms-txt/ (postbuild copies the set to /llms-postgres.txt)');
  for (const p of LLMS_PAGES) if (!text.includes(`](${SITE}${p.path})`)) out.push(`llms.txt: no link to ${p.path}`);
  return out;
}

/** Problems with a task page llms.txt names: it must be built, with a visible Updated date that is a real date. */
export function taskPageProblems(path, html) {
  if (html === null) return [`${path}: llms.txt links it, but it isn't built`];
  const m = /<p class="dg-updated[^"]*"[^>]*>Updated <time datetime="(\d{4}-\d{2}-\d{2})"/.exec(html);
  return m ? [] : [`${path}: no visible Updated date (<Updated /> under the title)`];
}

/** Problems with one llms set file: a page mark left, or the head of a page outside the set. */
export function setProblems(name, text, heads, keep) {
  const out = [];
  if (text.includes(PAGE_MARK.trim())) out.push(`${name}: a page mark is left`);
  for (const [head, ids] of heads) {
    if (ids.some(keep)) continue;
    if (text.startsWith(head + '\n') || text.includes('\n\n' + head + '\n')) out.push(`${name}: holds ${ids.join(', ')}, which is outside its set`);
  }
  return out;
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const fails = [];
  const read = (f) => (existsSync(join(dist, f)) ? readFileSync(join(dist, f), 'utf8') : null);
  const index = read('llms.txt');
  if (index === null) fails.push('dist/llms.txt is missing');
  else fails.push(...indexProblems(index, description()));
  for (const p of LLMS_PAGES) fails.push(...taskPageProblems(p.path, read(p.path.slice(1) + 'index.html')));
  const heads = pageHeads(contentDir);
  const sizes = [];
  for (const [f, keep] of [['llms-small.txt', inSmall], ['llms-full.txt', inFull], ['llms-postgres.txt', null]]) {
    const t = read(f);
    if (t === null) {
      fails.push(`dist/${f} is missing`);
      continue;
    }
    const n = statSync(join(dist, f)).size;
    sizes.push(`${f.replace('.txt', '')} ${Math.round(n / 1024)} KB`);
    if (n > LLMS_BUDGET[f]) fails.push(`${f}: ${Math.round(n / 1024)} KB (at most ${LLMS_BUDGET[f] / 1024} KB)`);
    if (keep) fails.push(...setProblems(f, t, heads, keep));
    else if (t.includes(PAGE_MARK.trim())) fails.push(`${f}: a page mark is left`);
  }
  if (fails.length) {
    for (const f of fails.slice(0, 40)) console.error('✕ ' + f);
    process.exit(1);
  }
  console.log(`✓ llms · ${sizes.join(' · ')} · the one description · ask before each change · ${LLMS_PAGES.length} task pages linked, each with its Updated date · each set holds only its pages`);
}
