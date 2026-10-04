// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest';
import { diffLines, splitLines, type LineDiff } from '../src/lib/linediff.ts';
import { hunkHeader, lineDiffView } from '../src/controllers/linediffview.ts';
import versionJson from './fixtures/s26/policy-version-1.json';

const lines = (n: number, p = 'line') => Array.from({ length: n }, (_, i) => `${p} ${i + 1}`);
const text = (ls: string[]) => ls.join('\n') + (ls.length ? '\n' : '');

// With unlimited context the single hunk holds every line, so both sides can be rebuilt.
function sides(d: LineDiff): { old: string[]; new: string[] } {
  const o: string[] = [], n: string[] = [];
  for (const k of d.hunks) for (const l of k.lines) {
    if (l.kind !== 'add') o.push(l.text);
    if (l.kind !== 'del') n.push(l.text);
  }
  return { old: o, new: n };
}

function lcs(a: string[], b: string[]): number {
  const dp = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = 1; i <= a.length; i++) for (let j = 1; j <= b.length; j++) {
    dp[i]![j] = a[i - 1] === b[j - 1] ? dp[i - 1]![j - 1]! + 1 : Math.max(dp[i - 1]![j]!, dp[i]![j - 1]!);
  }
  return dp[a.length]![b.length]!;
}

// Line numbers on both sides must count up with the text they point at.
function checkNumbers(d: LineDiff, a: string[], b: string[]) {
  for (const k of d.hunks) for (const l of k.lines) {
    if (l.old !== null) expect(a[l.old - 1]).toBe(l.text);
    if (l.new !== null) expect(b[l.new - 1]).toBe(l.text);
  }
}

let seed = 42;
const rand = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);

describe('linediff', () => {
  it('splits lines: a final newline adds no line, "" has none, CRLF is LF', () => {
    expect(splitLines('')).toEqual([]);
    expect(splitLines('a')).toEqual(['a']);
    expect(splitLines('a\n')).toEqual(['a']);
    expect(splitLines('a\r\nb\r\n')).toEqual(['a', 'b']);
    expect(splitLines('\n')).toEqual(['']);
  });

  it('empty to text and text to empty', () => {
    const add = diffLines('', 'a\nb\n');
    expect(add).toMatchObject({ added: 2, removed: 0, identical: false, crlf: false });
    expect(add.hunks).toHaveLength(1);
    expect(add.hunks[0]).toMatchObject({ oldStart: 0, oldLines: 0, newStart: 1, newLines: 2 });
    expect(add.hunks[0]!.lines.map((l) => [l.kind, l.old, l.new])).toEqual([['add', null, 1], ['add', null, 2]]);
    const del = diffLines('a\nb\n', '');
    expect(del).toMatchObject({ added: 0, removed: 2 });
    expect(del.hunks[0]).toMatchObject({ oldStart: 1, oldLines: 2, newStart: 0, newLines: 0 });
    expect(diffLines('', '')).toMatchObject({ identical: true, hunks: [] });
  });

  it('identical texts have no hunks', () => {
    const t = text(lines(50));
    expect(diffLines(t, t)).toEqual({ hunks: [], added: 0, removed: 0, identical: true, crlf: false });
  });

  it('inserts and deletes at both edges, with three lines of context', () => {
    const a = lines(20);
    const first = diffLines(text(a), text(['new 0', ...a]));
    expect(first.hunks).toHaveLength(1);
    expect(first.hunks[0]!.lines.map((l) => l.kind)).toEqual(['add', 'ctx', 'ctx', 'ctx']);
    expect(first.hunks[0]).toMatchObject({ oldStart: 1, oldLines: 3, newStart: 1, newLines: 4 });
    const last = diffLines(text(a), text([...a, 'new 21']));
    expect(last.hunks[0]!.lines.map((l) => l.kind)).toEqual(['ctx', 'ctx', 'ctx', 'add']);
    expect(last.hunks[0]).toMatchObject({ oldStart: 18, oldLines: 3, newStart: 18, newLines: 4 });
    const dropFirst = diffLines(text(a), text(a.slice(1)));
    expect(dropFirst.hunks[0]!.lines.map((l) => [l.kind, l.old, l.new])).toEqual([['del', 1, null], ['ctx', 2, 1], ['ctx', 3, 2], ['ctx', 4, 3]]);
    const dropLast = diffLines(text(a), text(a.slice(0, -1)));
    expect(dropLast.hunks[0]!.lines.map((l) => [l.kind, l.old, l.new])).toEqual([['ctx', 17, 17], ['ctx', 18, 18], ['ctx', 19, 19], ['del', 20, null]]);
    for (const d of [first, last, dropFirst, dropLast]) expect(d.added + d.removed).toBe(1);
  });

  it('a change reads as removed then added, numbered on both sides', () => {
    const a = lines(10), b = [...a];
    b[4] = 'changed 5';
    const d = diffLines(text(a), text(b));
    expect(d).toMatchObject({ added: 1, removed: 1 });
    const k = d.hunks[0]!;
    expect(k).toMatchObject({ oldStart: 2, oldLines: 7, newStart: 2, newLines: 7 });
    expect(k.lines.map((l) => [l.kind, l.old, l.new])).toEqual([
      ['ctx', 2, 2], ['ctx', 3, 3], ['ctx', 4, 4], ['del', 5, null], ['add', null, 5], ['ctx', 6, 6], ['ctx', 7, 7], ['ctx', 8, 8],
    ]);
    checkNumbers(d, a, b);
  });

  it('merges changes within six lines and splits farther ones', () => {
    const a = lines(40), near = [...a], far = [...a];
    near[5] = 'x'; near[11] = 'y'; // 5 unchanged lines between
    far[5] = 'x'; far[20] = 'y';
    expect(diffLines(text(a), text(near)).hunks).toHaveLength(1);
    expect(diffLines(text(a), text(far)).hunks).toHaveLength(2);
    expect(diffLines(text(a), text(far), 0).hunks[1]!.lines).toHaveLength(2);
  });

  it('CRLF against LF is identical and flagged', () => {
    const lf = text(lines(5));
    const d = diffLines(lf, lf.replace(/\n/g, '\r\n'));
    expect(d).toMatchObject({ identical: true, crlf: true, hunks: [] });
    const e = diffLines(lf.replace(/\n/g, '\r\n'), lf.replace('line 3', 'line three'));
    expect(e).toMatchObject({ added: 1, removed: 1, crlf: true });
    expect(diffLines(lf, lf).crlf).toBe(false);
  });

  it('is minimal and rebuilds both sides (random edits against an LCS oracle)', () => {
    for (let t = 0; t < 300; t++) {
      const alphabet = 'abcdef'.slice(0, 2 + Math.floor(rand() * 4));
      const gen = () => Array.from({ length: Math.floor(rand() * 25) }, () => alphabet[Math.floor(rand() * alphabet.length)]!);
      const a = gen(), b = rand() < 0.5 ? gen() : a.filter(() => rand() < 0.8).concat(gen().slice(0, 3));
      const d = diffLines(text(a), text(b), 1e9);
      const s = d.identical ? { old: a, new: a } : sides(d);
      expect(s.old).toEqual(a);
      expect(s.new).toEqual(b);
      const common = lcs(a, b);
      expect(d.removed, `${a.join('')} → ${b.join('')}`).toBe(a.length - common);
      expect(d.added).toBe(b.length - common);
      checkNumbers(d, a, b);
    }
  });

  it('diffs a 2,000-line reorder exactly, and two 2,000-line versions within budget', () => {
    const a = lines(2000);
    // Move the first 200 lines to the end: 200 removed, 200 added, nothing else.
    const moved = [...a.slice(200), ...a.slice(0, 200)];
    const d = diffLines(text(a), text(moved));
    expect(d).toMatchObject({ added: 200, removed: 200 });
    checkNumbers(d, a, moved);
    const s = sides(diffLines(text(a), text(moved), 1e9));
    expect(s.old).toEqual(a);
    expect(s.new).toEqual(moved);

    // Budgets (S26 §3.6: < 50 ms; asserted with a margin for CI): a realistic edit (5 % of lines
    // changed, a block moved), and the worst case, a full shuffle.
    const edited = a.map((l) => (rand() < 0.05 ? l + ' # edited' : l));
    edited.splice(1500, 0, ...edited.splice(100, 60));
    const shuffled = [...a].sort(() => rand() - 0.5);
    const time = (b: string[]) => {
      diffLines(text(a), text(b)); // warm
      const t0 = performance.now();
      const r = diffLines(text(a), text(b));
      const ms = performance.now() - t0;
      checkNumbers(r, a, b);
      return ms;
    };
    const tEdit = time(edited), tMove = time(moved), tShuffle = time(shuffled);
    console.info(`linediff 2,000 lines: edited ${tEdit.toFixed(2)} ms · moved block ${tMove.toFixed(2)} ms · full shuffle ${tShuffle.toFixed(2)} ms`);
    expect(tEdit).toBeLessThan(200);
    expect(tMove).toBeLessThan(200);
    expect(tShuffle).toBeLessThan(1000);
  });

  it('diffs the real acme-demo policy against an edited copy', () => {
    const src = versionJson.source;
    const next = src.replace('max_calls: 30', 'max_calls: 60').replace('    verify: L0\n', '');
    const d = diffLines(src, next);
    expect(d).toMatchObject({ added: 1, removed: 2 });
    expect(d.hunks).toHaveLength(2);
    expect(hunkHeader(d.hunks[0]!)).toBe('@@ lines 31 to 37 @@');
  });
});

describe('LineDiff view', () => {
  it('renders hunks, both line numbers and sr-only change words in a focusable region', () => {
    const a = lines(12), b = [...a];
    b[5] = 'changed 6';
    const el = lineDiffView(diffLines(text(a), text(b)), { from: 'v3', to: 'v4' });
    const frame = el.querySelector('.ld-frame')!;
    expect(frame.getAttribute('role')).toBe('region');
    expect(frame.getAttribute('tabindex')).toBe('0');
    expect(frame.getAttribute('aria-label')).toBe('Changes from v3 to v4');
    expect(el.querySelector('.ld-hunk')!.textContent).toBe('@@ lines 3 to 9 @@');
    const add = el.querySelector('.ld-add')!;
    expect([...add.querySelectorAll('td')].map((t) => t.textContent)).toEqual(['', '6', '+added', 'changed 6']);
    expect(add.querySelector('.sr-only')!.textContent).toBe('added');
    expect(el.querySelector('.ld-del .sr-only')!.textContent).toBe('removed');
    expect(el.querySelectorAll('.ld-ctx')).toHaveLength(6);
    expect(el.querySelector('.ld-summary')!.textContent).toBe('1 added · 1 removed');
  });
  it('says so when nothing changed, and flags CRLF', () => {
    const t = text(lines(3));
    const el = lineDiffView(diffLines(t, t.replace(/\n/g, '\r\n')));
    expect(el.querySelector('table')).toBeNull();
    expect(el.textContent).toContain('No changes between these versions.');
    expect(el.textContent).toContain('CRLF');
  });
  it('a one-line hunk header names one line', () => {
    expect(hunkHeader({ oldStart: 4, oldLines: 1, newStart: 0, newLines: 0, lines: [] })).toBe('@@ line 4 @@');
  });
});
