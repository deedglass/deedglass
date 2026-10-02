// WCAG 2.2 contrast for every declared token pair, in both themes (S24 §3.2). Text pairs need
// 4.5:1, UI pairs (focus rings, input borders) 3:1; hairlines are decorative and listed as such.
// Exit 1 on any failure. `--json` prints the table as JSON (gen-tokens embeds it in the kit).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));

export function loadTokens() {
  return JSON.parse(readFileSync(here + '../tokens/tokens.json', 'utf8'));
}

/** Parses #RGB, #RRGGBB or rgba(r,g,b,a) into [r, g, b, a] (0–255, a 0–1). */
export function parseColor(s) {
  const t = String(s).trim();
  let m = /^#([0-9a-f]{6})$/i.exec(t);
  if (m) {
    const n = parseInt(m[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1];
  }
  m = /^#([0-9a-f]{3})$/i.exec(t);
  if (m) return [...m[1]].map((c) => parseInt(c + c, 16)).concat(1);
  m = /^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/i.exec(t);
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3]), m[4] === undefined ? 1 : Number(m[4])];
  throw new Error(`contrast: can't read colour ${s}`);
}

/** Composites fg (with alpha) over an opaque bg. */
export function over(fg, bg) {
  const a = fg[3];
  return [0, 1, 2].map((i) => fg[i] * a + bg[i] * (1 - a)).concat(1);
}

function channel(c) {
  const s = c / 255;
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

export function luminance([r, g, b]) {
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

export function ratio(a, b) {
  const [l1, l2] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (l1 + 0.05) / (l2 + 0.05);
}

/** Resolves a role in a theme to a colour string (a primitive name, a role name, or a literal). */
export function resolve(tokens, theme, name, seen = new Set()) {
  if (seen.has(name)) throw new Error(`contrast: ${name} refers to itself`);
  seen.add(name);
  const roles = tokens.themes[theme];
  if (name in roles) return resolve(tokens, theme, roles[name], seen);
  if (name in tokens.primitives) return tokens.primitives[name];
  return name;
}

export function table(tokens = loadTokens()) {
  const rows = [];
  for (const theme of Object.keys(tokens.themes)) {
    for (const p of tokens.pairs) {
      const bgBase = p.over ? parseColor(resolve(tokens, theme, p.over)) : null;
      let bg = parseColor(resolve(tokens, theme, p.bg));
      if (bg[3] < 1) bg = over(bg, bgBase ?? parseColor(resolve(tokens, theme, 'bg')));
      let fg = parseColor(resolve(tokens, theme, p.fg));
      if (fg[3] < 1) fg = over(fg, bg);
      const r = ratio(fg, bg);
      const need = p.kind === 'text' ? 4.5 : p.kind === 'ui' ? 3 : 0;
      rows.push({ theme, fg: p.fg, bg: p.bg, kind: p.kind, note: p.note ?? '', ratio: Math.round(r * 100) / 100, need,
        pass: p.kind === 'decorative' ? true : r >= need });
    }
  }
  const findings = tokens.findings.map((f) => ({ ...f, ratio: Math.round(ratio(parseColor(f.fg), parseColor(f.bg)) * 100) / 100 }));
  return { rows, findings };
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const { rows, findings } = table();
  if (process.argv.includes('--json')) {
    process.stdout.write(JSON.stringify({ rows, findings }, null, 2) + '\n');
  } else {
    const pad = (s, n) => String(s).padEnd(n);
    console.log(pad('theme', 6), pad('foreground', 16), pad('background', 16), pad('kind', 11), pad('ratio', 7), 'result');
    for (const r of rows) {
      const result = r.kind === 'decorative' ? 'decorative' : r.pass ? `pass (${r.need}:1)` : `FAIL (needs ${r.need}:1)`;
      console.log(pad(r.theme, 6), pad(r.fg, 16), pad(r.bg, 16), pad(r.kind, 11), pad(r.ratio.toFixed(2), 7), result, r.note ? '· ' + r.note : '');
    }
    console.log('\nRole fixes (brand palette pairs that fail, and the role used instead):');
    for (const f of findings) console.log(`  ${f.label}: ${f.ratio.toFixed(2)} · ${f.decision}`);
  }
  const failing = rows.filter((r) => !r.pass).length;
  console.log(`\n${rows.length} pairs · ${failing} failing pairs`);
  process.exit(failing > 0 ? 1 : 0);
}
