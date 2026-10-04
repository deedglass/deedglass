import { describe, expect, it } from 'vitest';
import { applyEdit, applyFix, fixEdit, indent, lineCount, lineStarts, newline, offsetToPos, outdent, posToOffset, runes } from '../src/lib/editormath.ts';
import { quickFix, allDiagnostics, type PolicyDiagnostic, type PolicyLintResult } from '../src/lib/policyfix.ts';
import { read, repo } from './helpers.ts';

const fx = <T>(n: string): T => JSON.parse(read(repo(`web/test/fixtures/s26/${n}.json`))) as T;

describe('offset and position', () => {
  it('counts lines and columns from 1', () => {
    const t = 'ab\ncd\n\nef';
    expect(lineStarts(t)).toEqual([0, 3, 6, 7]);
    expect(lineCount(t)).toBe(4);
    expect(lineCount('')).toBe(1);
    expect(offsetToPos(t, 0)).toEqual({ line: 1, column: 1 });
    expect(offsetToPos(t, 2)).toEqual({ line: 1, column: 3 });
    expect(offsetToPos(t, 3)).toEqual({ line: 2, column: 1 });
    expect(offsetToPos(t, 6)).toEqual({ line: 3, column: 1 });
    expect(offsetToPos(t, 9)).toEqual({ line: 4, column: 3 });
    expect(posToOffset(t, 2, 2)).toBe(4);
    expect(posToOffset(t, 4, 1)).toBe(7);
  });
  it('a tab is one rune', () => {
    const t = '\t\tkey: v';
    expect(offsetToPos(t, 2)).toEqual({ line: 1, column: 3 });
    expect(posToOffset(t, 1, 3)).toBe(2);
  });
  it('CRLF ends a line and its \\r is never inside a column', () => {
    const t = 'ab\r\ncd\r\n';
    expect(offsetToPos(t, 4)).toEqual({ line: 2, column: 1 });
    expect(offsetToPos(t, 2)).toEqual({ line: 1, column: 3 });
    expect(offsetToPos(t, 3)).toEqual({ line: 1, column: 3 }); // between \r and \n
    expect(posToOffset(t, 2, 2)).toBe(5);
    expect(posToOffset(t, 1, 99)).toBe(2); // clamped before \r\n
    expect(posToOffset(t, 3, 1)).toBe(8);
    expect(posToOffset(t, 9, 1)).toBe(t.length);
  });
  it('astral characters are one rune and two UTF-16 units', () => {
    const t = 'a😀b · c\n𝒳y';
    expect(runes(t, 0, 3)).toBe(2);
    expect(offsetToPos(t, 3)).toEqual({ line: 1, column: 3 }); // after the emoji
    expect(posToOffset(t, 1, 3)).toBe(3);
    expect(posToOffset(t, 1, 5)).toBe(5); // "·" is one unit
    expect(posToOffset(t, 2, 2)).toBe(t.indexOf('y'));
    expect(offsetToPos(t, t.indexOf('y'))).toEqual({ line: 2, column: 2 });
    // Round trip over every code point boundary.
    for (let i = 0; i <= t.length; i++) {
      if (i > 0 && i < t.length && t.charCodeAt(i - 1) >= 0xd800 && t.charCodeAt(i - 1) <= 0xdbff) continue;
      const p = offsetToPos(t, i);
      expect(posToOffset(t, p.line, p.column)).toBe(i);
    }
  });
});

describe('applyFix and the client-side quick fix (S10 adapter)', () => {
  const source = fx<{ source: string }>('policy-version-1').source;
  const lines = source.split('\n');
  lines[39] = lines[39]!.replace('decision', 'desicion');
  const typo = lines.join('\n');
  const lint = fx<PolicyLintResult>('lint-typo');
  const d = lint.errors[0]!;

  it('derives the fix from lint-typo.json: 40:5..13, desicion → decision', () => {
    expect(d).toMatchObject({ code: 'unknown_key', line: 40, column: 5, end_line: 40, end_column: 13 });
    expect(quickFix(d)).toEqual({ line: 40, column: 5, end_line: 40, end_column: 13, replace: 'decision' });
    expect(typo.slice(posToOffset(typo, 40, 5), posToOffset(typo, 40, 13))).toBe('desicion');
    expect(applyFix(typo, quickFix(d)!)).toBe(source);
    const e = fixEdit(typo, quickFix(d)!);
    expect(e.selStart).toBe(posToOffset(typo, 40, 13));
  });
  it('offers no fix without the exact hint or a full range', () => {
    expect(quickFix({ ...d, hint: 'Valid keys: "id", "match".' })).toBeNull();
    expect(quickFix({ ...d, hint: 'Did you mean "decision"? Available: "a".' })).toBeNull();
    expect(quickFix({ ...d, hint: 'Did you mean `deny`? Decisions are allow, deny and require_approval.' })).toBeNull();
    expect(quickFix({ ...d, end_column: null })).toBeNull();
    expect(quickFix({ ...d, line: null })).toBeNull();
    expect(quickFix({ ...d, end_line: 39 })).toBeNull();
    expect(quickFix({ ...d, hint: null })).toBeNull();
  });
  it('a server fix.replace wins', () => {
    expect(quickFix({ ...d, hint: null, fix: { replace: 'decision' } })?.replace).toBe('decision');
  });
  it('applies over astral text and CRLF', () => {
    const t = 'k: "😀x" # ✓\r\nnext\r\n';
    expect(applyFix(t, { line: 1, column: 6, end_line: 1, end_column: 7, replace: 'y' })).toBe('k: "😀y" # ✓\r\nnext\r\n');
    expect(applyFix(t, { line: 2, column: 1, end_line: 2, end_column: 5, replace: 'last' })).toBe('k: "😀x" # ✓\r\nlast\r\n');
  });
  it('orders errors before warnings, by line', () => {
    const w = (line: number): PolicyDiagnostic => ({ severity: 'warning', code: 'w', message: 'w', line });
    const e = (line: number): PolicyDiagnostic => ({ severity: 'error', code: 'e', message: 'e', line });
    expect(allDiagnostics({ errors: [e(9), e(2)], warnings: [w(1)] }).map((x) => `${x.severity}${x.line}`)).toEqual(['error2', 'error9', 'warning1']);
  });
});

describe('indent, outdent and Enter', () => {
  it('Tab inserts two spaces at the caret, replacing a selection inside one line', () => {
    const t = 'rules:\n- id: a';
    const e = indent(t, 7, 7);
    expect(applyEdit(t, e)).toBe('rules:\n  - id: a');
    expect([e.selStart, e.selEnd]).toEqual([9, 9]);
    const s = indent('abcdef', 1, 3);
    expect(applyEdit('abcdef', s)).toBe('a  def');
  });
  it('Tab indents every selected line', () => {
    const t = 'a\nb\nc\nd';
    const e = indent(t, 2, 5); // from "b" into "c"
    expect(applyEdit(t, e)).toBe('a\n  b\n  c\nd');
    expect([e.selStart, e.selEnd]).toEqual([2, 9]);
    // A selection ending at the start of a line leaves that line alone.
    const f = indent(t, 2, 6);
    expect(applyEdit(t, f)).toBe('a\n  b\n  c\nd');
    expect(f.selStart).toBe(2);
  });
  it('Shift+Tab removes up to two leading spaces per line', () => {
    const t = '    a\n b\nc\n  d';
    const e = outdent(t, 0, t.length);
    expect(applyEdit(t, e)).toBe('  a\nb\nc\nd');
    expect(e.selEnd).toBe(applyEdit(t, e).length);
    const one = outdent('    key: v', 6, 6);
    expect(applyEdit('    key: v', one)).toBe('  key: v');
    expect(one.selStart).toBe(4);
    const inside = outdent('  x', 1, 1); // caret inside the removed spaces
    expect(applyEdit('  x', inside)).toBe('x');
    expect(inside.selStart).toBe(0);
  });
  it('Enter keeps the indent, and lines up after a list item', () => {
    const t = 'rules:\n  - id: a\n    when: x';
    const end = t.length;
    const e = newline(t, end, end);
    expect(applyEdit(t, e)).toBe(t + '\n    ');
    expect(e.selStart).toBe(end + 5);
    const afterItem = t.indexOf('id: a') + 5;
    expect(applyEdit(t, newline(t, afterItem, afterItem)).split('\n')[2]).toBe('    ');
    expect(applyEdit('\tk: v', newline('\tk: v', 5, 5))).toBe('\tk: v\n\t');
    expect(applyEdit('a\r\nb', newline('a\r\nb', 4, 4))).toBe('a\r\nb\r\n');
  });
});
