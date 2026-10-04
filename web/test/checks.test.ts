import { execFileSync } from 'node:child_process';
import { describe, expect, it } from 'vitest';
import { copy, lifted } from '../src/lib/copy.ts';
import { approvalsCopy } from '../src/lib/copy-approvals.ts';
import { connectorsCopy } from '../src/lib/copy-connectors.ts';
import { editorCopy } from '../src/lib/copy-editor.ts';
import { formsCopy } from '../src/lib/copy-forms.ts';
import { policiesCopy } from '../src/lib/copy-policies.ts';
import { settingsCopy } from '../src/lib/copy-settings.ts';
import { iconNames } from '../src/icons/names.gen.ts';
import { files, read, repo, web } from './helpers.ts';

const node = process.execPath;

describe('tokens and contrast', () => {
  it('every generated token file is current', () => {
    expect(() => execFileSync(node, [web + 'scripts/gen-tokens.mjs', '--check'], { stdio: 'pipe' })).not.toThrow();
    expect(() => execFileSync(node, [web + 'scripts/icons.mjs', '--check'], { stdio: 'pipe' })).not.toThrow();
  });
  it('contrast.mjs ends with 0 failing pairs and reproduces S24 §3.2', () => {
    const out = execFileSync(node, [web + 'scripts/contrast.mjs'], { encoding: 'utf8' });
    expect(out).toMatch(/· 0 failing pairs/);
    const t = JSON.parse(execFileSync(node, [web + 'scripts/contrast.mjs', '--json'], { encoding: 'utf8' }).split('\n\n')[0]!);
    const f = Object.fromEntries(t.findings.map((x: { label: string; ratio: number }) => [x.label, x.ratio]));
    expect(f['white on dark accent']).toBe(3.03);
    expect(f['white on dark danger']).toBe(2.54);
    expect(f['slate-500 on mist-100']).toBe(4.26);
    expect(f['slate-300 input border on white']).toBe(1.88);
    const r = (theme: string, fg: string, bg: string, kind = 'text') => t.rows.find((x: Record<string, unknown>) => x.theme === theme && x.fg === fg && x.bg === bg && x.kind === kind).ratio;
    expect(r('dark', 'on-accent', 'accent')).toBe(6.61);
    expect(r('dark', 'on-danger', 'danger')).toBe(7.88);
    expect(r('light', 'text-2-on-alt', 'surface-alt')).toBe(8.37);
    expect(r('light', 'border-input', 'surface', 'ui')).toBe(3.18);
    expect(r('dark', 'border-input', 'surface', 'ui')).toBe(3.85);
    expect(r('light', 'verified-fg', 'verified-bg')).toBe(4.91);
    expect(r('dark', 'verified-fg', 'verified-bg')).toBe(9.39);
    expect(r('light', 'accent', 'bg', 'ui')).toBe(5.14);
  });
  it('writes no colour outside tokens.json', () => {
    const hex = /#[0-9a-fA-F]{3,8}\b(?![-\w])/;
    for (const f of files(web + 'src', /\.(css|astro|ts)$/)) {
      if (/tokens\.css$|\.gen\.ts$/.test(f)) continue;
      const s = read(f).replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, '');
      const m = s.match(new RegExp(`(?<![&\\w])${hex.source}`));
      expect(m, `${f} writes ${m?.[0]}`).toBeNull();
      expect(/rgba?\(\s*\d/.test(s), `${f} writes an rgb() colour`).toBe(false);
    }
  });
  it('aurora appears only in the Mirror', () => {
    for (const f of files(web + 'src', /\.(css|astro|ts)$/)) {
      if (/tokens\.css$|\.gen\.ts$/.test(f)) continue;
      const s = read(f);
      for (const m of s.matchAll(/var\(--aurora-[a-z]+\)/g)) {
        const line = s.slice(s.lastIndexOf('\n', m.index) + 1, s.indexOf('\n', m.index));
        expect(line, `${f}: aurora outside the Mirror`).toMatch(/dg-mirror/);
      }
    }
  });
});

describe('copy', () => {
  const catalog = read(repo('docs/COPY_CATALOG.md'));
  it('lifts the catalog strings verbatim', () => {
    for (const s of lifted) expect(catalog.includes(s), s).toBe(true);
  });
  const all: string[] = [];
  const walk = (o: unknown) => {
    if (typeof o === 'string') all.push(o);
    else if (o && typeof o === 'object') Object.values(o).forEach(walk);
  };
  walk(copy);
  for (const area of [approvalsCopy, connectorsCopy, editorCopy, formsCopy, policiesCopy, settingsCopy]) walk(area);
  it('follows the voice: no exclamation marks, no em or en dashes, no emoji', () => {
    for (const s of all) {
      expect(s.includes('!'), s).toBe(false);
      expect(/[—–]/.test(s), s).toBe(false);
      expect(/\p{Extended_Pictographic}/u.test(s.replace(/[⌘↑↓←→›·…◐]/g, '')), s).toBe(false);
    }
  });
  it('uses no banned word', () => {
    const list = read(repo('scripts/banned_words.txt')).split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#')).map((l) => l.replace(/^@until-public /, ''));
    const text = all.join('\n').toLowerCase();
    for (const w of list) {
      const re = new RegExp(`(^|[^a-z0-9_])${w.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}($|[^a-z0-9_])`);
      expect(re.test(text), `banned: ${w}`).toBe(false);
    }
  });
});

describe('icons', () => {
  it('every icon used exists and every icon shipped is used', () => {
    const src = files(web + 'src', /\.(astro|ts)$/).filter((f) => !/names\.gen\.ts$/.test(f)).map(read).join('\n');
    const used = new Set<string>();
    for (const m of src.matchAll(/<(?:Icon|IconButton)\b[^>]*\b(?:name|icon)=(?:"|\{')([a-z0-9-]+)/g)) used.add(m[1]!);
    for (const m of src.matchAll(/<(?:Button|EmptyState)\b[^>]*\bicon=(?:"|\{')([a-z0-9-]+)/g)) used.add(m[1]!);
    for (const m of src.matchAll(/icon\(\s*['"]([a-z0-9-]+)['"]/g)) used.add(m[1]!);
    for (const m of src.matchAll(/\bicon:\s*['"]([a-z0-9-]+)['"]/g)) used.add(m[1]!);
    for (const m of src.matchAll(/['"]((?:[a-z]+-)*[a-z]+)['"]\]/g)) if ((iconNames as readonly string[]).includes(m[1]!)) used.add(m[1]!);
    const shipped = new Set<string>(iconNames);
    for (const u of used) if (/^[a-z]/.test(u) && !shipped.has(u) && !['true', 'false', 'button', 'submit', 'md', 'sm', 'lg'].includes(u)) {
      expect(shipped.has(u), `icon ${u} is used but not in src/icons/used.json`).toBe(true);
    }
    for (const s of shipped) expect(src.includes(`'${s}'`) || src.includes(`"${s}"`), `icon ${s} ships but nothing uses it`).toBe(true);
  });
});

describe('sinks', () => {
  const banned = ['inner' + 'HTML', 'outer' + 'HTML', 'insertAdjacent' + 'HTML', 'document.' + 'write', 'eval(', 'new ' + 'Function', 'set:' + 'html', 'srcdoc'];
  it('no HTML-string sink in web/src or the shared Mirror', () => {
    for (const f of [...files(web + 'src', /\.(ts|astro|css)$/), web + 'verify/src/ui/mirror.ts']) {
      const s = read(f);
      for (const b of banned) expect(s.includes(b), `${f} uses ${b}`).toBe(false);
    }
  });
  it('browser storage holds UI preferences only', () => {
    for (const f of files(web + 'src', /\.ts$/)) {
      const s = read(f);
      if (/localStorage/.test(s)) expect(f, 'localStorage outside theme.ts').toMatch(/theme\.ts$/);
      if (/sessionStorage/.test(s)) expect(f, 'sessionStorage outside recent.ts').toMatch(/recent\.ts$/);
      expect(/document\.cookie|indexedDB/.test(s), f).toBe(false);
    }
  });
});
