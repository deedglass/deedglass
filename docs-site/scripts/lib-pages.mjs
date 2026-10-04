// The hand-written and generated content pages with their frontmatter, for the gates.
import { readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { walk } from './postbuild.mjs';
import yaml from 'js-yaml';

export const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
export const contentDir = join(site, 'src', 'content', 'docs');
const generated = new Set(JSON.parse(readFileSync(join(site, 'src/data/.generated-pages.json'), 'utf8')));

export function pages() {
  const out = [];
  for (const f of walk(contentDir)) {
    if (!/\.mdx?$/.test(f)) continue;
    const rel = relative(contentDir, f);
    const text = readFileSync(f, 'utf8');
    const m = /^---\n([\s\S]*?)\n---\n/.exec(text);
    const fm = m ? yaml.load(m[1]) ?? {} : {};
    const slug = rel.replace(/\.mdx?$/, '').replace(/(^|\/)index$/, '');
    out.push({ file: f, rel, slug, fm, body: m ? text.slice(m[0].length) : text, generated: generated.has(rel) });
  }
  return out;
}
