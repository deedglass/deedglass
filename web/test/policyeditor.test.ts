// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { column, createPolicyEditor, decorate, mountLintedEditor, type PolicyEditor } from '../src/controllers/policyeditor.ts';
import { cleanText, diagnosticsList, rowText, summaryText } from '../src/controllers/diagnostics.ts';
import { lineCount, posToOffset } from '../src/lib/editormath.ts';
import type { PolicyDiagnostic, PolicyLintResult } from '../src/lib/policyfix.ts';
import versionJson from './fixtures/s26/policy-version-1.json';
import lintTypoJson from './fixtures/s26/lint-typo.json';
import lintCleanJson from './fixtures/s26/lint-clean.json';

const source = versionJson.source;
const typoSource = (() => {
  const l = source.split('\n');
  l[39] = l[39]!.replace('decision', 'desicion');
  return l.join('\n');
})();
const lint = lintTypoJson as unknown as PolicyLintResult;
const typo = lint.errors[0]!;

let editors: PolicyEditor[] = [];
afterEach(() => {
  for (const e of editors) e.destroy();
  editors = [];
  document.body.replaceChildren();
});

function mount(value: string, o: Partial<Parameters<PolicyEditor['mount']>[1]> = {}) {
  const host = document.createElement('div');
  document.body.append(host);
  const ed = createPolicyEditor();
  editors.push(ed);
  ed.mount(host, { value, label: 'Policy source', ...o });
  const ta = host.querySelector('textarea')!;
  return { ed, host, ta };
}

function key(ta: HTMLTextAreaElement, k: string, mods: Partial<KeyboardEventInit> = {}): KeyboardEvent {
  const e = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...mods });
  ta.dispatchEvent(e);
  return e;
}

describe('decorations', () => {
  it('maps a diagnostic onto its line in runes, errors over warnings', () => {
    const m = decorate(typoSource, [typo]);
    expect([...m.keys()]).toEqual([40]);
    const line = m.get(40)!;
    expect(line.text.slice(line.marks[0]!.from, line.marks[0]!.to)).toBe('desicion');
    const astral = decorate('k: "😀x"', [{ severity: 'warning', code: 'w', message: 'm', line: 1, column: 6, end_line: 1, end_column: 7 }]);
    expect(astral.get(1)!.marks[0]).toEqual({ from: 6, to: 7, sev: 'warning' });
    const both = decorate('abcdef', [
      { severity: 'warning', code: 'w', message: 'w', line: 1, column: 1, end_line: 1, end_column: 5 },
      { severity: 'error', code: 'e', message: 'e', line: 1, column: 3, end_line: 1, end_column: 7 },
    ]);
    expect(both.get(1)!.marks).toEqual([{ from: 0, to: 4, sev: 'warning' }, { from: 4, to: 6, sev: 'error' }]);
    // No column: the line from its first non-space character.
    expect(decorate('  key: v', [{ severity: 'error', code: 'e', message: 'e', line: 1 }]).get(1)!.marks[0]).toEqual({ from: 2, to: 8, sev: 'error' });
    // A line past the end clamps to the last line.
    expect([...decorate('a\nb', [{ severity: 'error', code: 'e', message: 'e', line: 9 }]).keys()]).toEqual([2]);
  });
  it('column() keeps the line count with only decorated lines holding text', () => {
    const total = 2000;
    const nodes = column(total, new Map([[1, [document.createTextNode('first')]], [1000, [document.createTextNode('mid')]], [2000, [document.createTextNode('last')]]]));
    const t = nodes.map((n) => n.textContent).join('');
    expect(t.split('\n')).toHaveLength(total);
    expect(t.split('\n')[0]).toBe('first');
    expect(t.split('\n')[999]).toBe('mid');
    expect(t.split('\n')[1999]).toBe('last');
    expect(nodes.length).toBeLessThanOrEqual(6);
    expect(column(3, new Map()).map((n) => n.textContent).join('')).toBe('\n\n');
  });
});

describe('PolicyEditor', () => {
  it('is a labelled textarea over an aria-hidden mirror, with the escape hint', () => {
    const { host, ta } = mount('a: 1');
    expect(ta.getAttribute('aria-label')).toBe('Policy source');
    expect(ta.getAttribute('spellcheck')).toBe('false');
    expect(ta.getAttribute('autocapitalize')).toBe('off');
    expect(ta.getAttribute('autocomplete')).toBe('off');
    expect(ta.getAttribute('wrap')).toBe('off');
    const hint = document.getElementById(ta.getAttribute('aria-describedby')!.split(' ')[0]!)!;
    expect(hint.textContent).toBe('Esc, then Tab, leaves the editor.');
    expect(hint.className).toBe('sr-only');
    expect(host.querySelector('pre.pe-mirror')!.getAttribute('aria-hidden')).toBe('true');
    expect(host.querySelector('.pe-gutter')!.getAttribute('aria-hidden')).toBe('true');
    expect(host.querySelector('[style]')).toBeNull();
    expect(host.querySelector('.pe-nums')!.textContent).toBe('1');
  });

  it('draws underlines, gutter markers with tooltips and line numbers', () => {
    const { ed, host, ta } = mount(typoSource);
    ed.setDiagnostics([typo, { severity: 'warning', code: 'w', message: 'A warning.', line: 3, column: 1, end_line: 3, end_column: 2 }]);
    ed.flush();
    const u = host.querySelector('.pe-u-error')!;
    expect(u.textContent).toBe('desicion');
    expect(host.querySelector('.pe-u-warning')).not.toBeNull();
    const mirror = host.querySelector('.pe-mirror')!.textContent!;
    expect(mirror.split('\n')).toHaveLength(lineCount(typoSource));
    expect(mirror.split('\n')[39]).toBe('    desicion: deny');
    expect(mirror.split('\n')[0]).toBe(''); // undecorated lines are empty
    const mk = host.querySelector('.pe-mk-error')!;
    expect(mk.textContent).toBe('✕');
    expect(mk.getAttribute('data-tip')).toBe('Unknown key "desicion" in rule "block-destructive-sql". Did you mean "decision"?');
    expect(host.querySelector('.pe-mk-warning')!.textContent).toBe('○');
    expect(host.querySelector('.pe-marks')!.textContent!.split('\n')[39]).toBe('✕');
    expect(host.querySelector('.pe-nums')!.textContent!.split('\n')).toHaveLength(lineCount(typoSource));
    expect(ta.getAttribute('aria-invalid')).toBe('true');
  });

  it('applies the quick fix as one edit and reports the change', () => {
    const onChange = vi.fn();
    const { ed, ta } = mount(typoSource, { onChange });
    ed.setDiagnostics([typo]);
    expect(ed.applyFix(typo)).toBe(true);
    expect(ta.value).toBe(source);
    expect(ed.value()).toBe(source);
    expect(onChange).toHaveBeenLastCalledWith(source);
    expect(ta.selectionStart).toBe(posToOffset(source, 40, 13));
    ed.flush();
    expect(document.querySelector('.pe-u-error')).toBeNull();
    expect(ed.applyFix({ ...typo, hint: 'Valid keys: "id".' })).toBe(false);
  });

  it('Tab indents, Shift+Tab outdents, Enter keeps the indent', () => {
    const onChange = vi.fn();
    const { ta } = mount('rules:\n  - id: a', { onChange });
    ta.focus();
    ta.setSelectionRange(ta.value.length, ta.value.length);
    const enter = key(ta, 'Enter');
    expect(enter.defaultPrevented).toBe(true);
    expect(ta.value).toBe('rules:\n  - id: a\n    ');
    const tab = key(ta, 'Tab');
    expect(tab.defaultPrevented).toBe(true);
    expect(ta.value).toBe('rules:\n  - id: a\n      ');
    key(ta, 'Tab', { shiftKey: true });
    expect(ta.value).toBe('rules:\n  - id: a\n    ');
    ta.setSelectionRange(0, ta.value.length);
    key(ta, 'Tab');
    expect(ta.value).toBe('  rules:\n    - id: a\n      ');
    expect(onChange).toHaveBeenCalledTimes(4);
  });

  it('Esc, then Tab leaves the editor; any other key re-arms Tab', () => {
    const { ta } = mount('x');
    ta.focus();
    key(ta, 'Escape');
    expect(key(ta, 'Tab').defaultPrevented).toBe(false);
    expect(ta.value).toBe('x');
    expect(key(ta, 'Tab').defaultPrevented).toBe(true);
    key(ta, 'Escape');
    key(ta, 'Shift');
    expect(key(ta, 'Tab', { shiftKey: true }).defaultPrevented).toBe(false);
    key(ta, 'Escape');
    key(ta, 'a');
    expect(key(ta, 'Tab').defaultPrevented).toBe(true);
  });

  it('⌘S and Ctrl+S save; read-only keeps the look and never edits', () => {
    const onSave = vi.fn();
    const { ta, ed, host } = mount('a: 1', { onSave });
    expect(key(ta, 's', { metaKey: true }).defaultPrevented).toBe(true);
    expect(key(ta, 'S', { ctrlKey: true }).defaultPrevented).toBe(true);
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(onSave).toHaveBeenCalledWith('a: 1');
    ed.setReadOnly(true);
    expect(ta.readOnly).toBe(true);
    expect(host.querySelector('.pe')!.getAttribute('data-readonly')).toBe('true');
    expect(host.querySelector('.pe-ro')!.hasAttribute('hidden')).toBe(false);
    expect(key(ta, 'Tab').defaultPrevented).toBe(false);
    expect(key(ta, 'Enter').defaultPrevented).toBe(false);
    expect(key(ta, 's', { metaKey: true }).defaultPrevented).toBe(true);
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(ed.applyFix(typo)).toBe(false);
  });

  it('reveals a line: caret at its start, scrolled, highlighted until the next edit', () => {
    const { ed, ta, host } = mount(source);
    ed.revealLine(47, true);
    expect(ta.selectionStart).toBe(posToOffset(source, 47, 1));
    expect(document.activeElement).toBe(ta);
    const hl = host.querySelector<HTMLElement>('.pe-hl')!;
    expect(hl.hidden).toBe(false);
    expect(hl.style.top).toBe(`${12 + 46 * 20 - ta.scrollTop}px`);
    expect(hl.parentElement!.getAttribute('aria-hidden')).toBe('true');
    ta.setRangeText('x', 0, 0);
    ta.dispatchEvent(new Event('input'));
    ed.flush();
    expect(hl.hidden).toBe(true);
    ed.revealLine(9999, false);
    expect(ta.selectionStart).toBe(posToOffset(ta.value, lineCount(ta.value), 1));
  });

  it('setValue replaces the text without reporting a change; the caret shows line and column', () => {
    const onChange = vi.fn();
    const { ed, host, ta } = mount('a', { onChange });
    ed.setValue('one\ntwo');
    expect(ta.value).toBe('one\ntwo');
    expect(onChange).not.toHaveBeenCalled();
    expect(host.querySelector('.pe-nums')!.textContent).toBe('1\n2');
    ta.setSelectionRange(6, 6);
    ta.dispatchEvent(new Event('keyup'));
    ed.flush();
    expect(host.querySelector('.pe-caret')!.textContent).toBe('Line 2, col 3');
  });

  it('keeps typing under a frame on a 2,000-line policy (happy-dom, rough)', () => {
    const big = Array.from({ length: 2000 }, (_, i) => (i % 5 === 0 ? `  - id: rule-${i}` : `    when: sql.kind == "update" && size(sql.tables) == ${i}`)).join('\n');
    const { ed, ta } = mount(big, { onChange: () => {} });
    const diags: PolicyDiagnostic[] = [10, 500, 1200, 1999].map((l) => ({ severity: 'error', code: 'e', message: 'e', line: l, column: 5, end_line: l, end_column: 9 }));
    ed.setDiagnostics(diags);
    ed.flush();
    const n = 200;
    const t0 = performance.now();
    for (let i = 0; i < n; i++) {
      const at = 30_000 + i;
      ta.setRangeText('x', at, at, 'end');
      ta.dispatchEvent(new Event('input'));
      ed.flush();
    }
    const per = (performance.now() - t0) / n;
    console.info(`policy editor: ${per.toFixed(3)} ms per keystroke (input + mirror render) on 2,000 lines in happy-dom`);
    expect(per).toBeLessThan(16);
  });
});

describe('DiagnosticsList', () => {
  it('rows read `Line 40, col 5 · message hint` with Apply fix and Docs', () => {
    const onReveal = vi.fn();
    const onFix = vi.fn();
    const el = diagnosticsList([typo], { onReveal, onFix });
    expect(rowText(typo)).toBe('Line 40, col 5 · Unknown key "desicion" in rule "block-destructive-sql". Did you mean "decision"?');
    expect(el.querySelector('.dl-main')!.textContent).toBe(rowText(typo));
    expect(el.querySelector('.dl-sev')!.textContent).toBe('Error');
    expect(el.querySelector('.dl-sev svg')).not.toBeNull();
    expect(el.querySelector('.dl-summary')!.textContent).toBe('1 error');
    const fix = [...el.querySelectorAll('button')].find((b) => b.textContent === 'Apply fix')!;
    expect(fix.getAttribute('aria-label')).toBe('Apply fix on line 40: use "decision"');
    fix.click();
    expect(onFix).toHaveBeenCalledWith(typo);
    expect(onReveal).not.toHaveBeenCalled();
    const docs = el.querySelector('a')!;
    expect(docs.getAttribute('href')).toBe('https://docs.deedglass.com/policies/errors/unknown_key');
    expect(docs.getAttribute('rel')).toBe('noopener noreferrer');
    expect(docs.textContent).toBe('Docs');
    (el.querySelector('.dl-main') as HTMLButtonElement).click();
    expect(onReveal).toHaveBeenCalledWith(40);
    (el.querySelector('.dl-row') as HTMLElement).click();
    expect(onReveal).toHaveBeenCalledTimes(2);
  });
  it('offers no fix without one, no docs link off the docs site, and none when read-only', () => {
    const w: PolicyDiagnostic = { severity: 'warning', code: 'x', message: 'Careful.', hint: 'Valid keys: "a".', line: 2, column: 1, doc_url: 'https://evil.example/x' };
    const el = diagnosticsList([w], { onFix: () => {}, onReveal: () => {} });
    expect(el.querySelector('.dl-actions')).toBeNull();
    expect(el.querySelector('.dl-sev')!.textContent).toBe('Warning');
    expect(diagnosticsList([typo], { onFix: () => {}, readOnly: true }).textContent).not.toContain('Apply fix');
    const file = diagnosticsList([{ severity: 'error', code: 'yaml', message: 'Not YAML.' }]);
    expect(file.querySelector('span.dl-main')!.textContent).toBe('Not YAML.');
  });
  it('clean only from a lint result, pluralised', () => {
    expect(cleanText(12, 2)).toBe('No problems · 12 rules · 2 budgets');
    expect(cleanText(1, 1)).toBe('No problems · 1 rule · 1 budget');
    expect(cleanText(0, 0)).toBe('No problems · 0 rules · 0 budgets');
    expect(diagnosticsList([], { counts: { rules: 16, budgets: 1 } }).textContent).toBe('✓No problems · 16 rules · 1 budget');
    expect(diagnosticsList([]).textContent).toBe('');
    expect(summaryText([typo, typo, { ...typo, severity: 'warning' }])).toBe('2 errors · 1 warning');
  });
});

describe('the editor with its lint loop', () => {
  it('lints on mount, shows the typo, and Apply fix re-lints to clean', async () => {
    const lintFn = vi.fn(async (src: string): Promise<PolicyLintResult> => (src.includes('desicion') ? lint : (lintCleanJson as unknown as PolicyLintResult)));
    const host = document.createElement('div');
    document.body.append(host);
    const le = mountLintedEditor(host, { value: typoSource, lint: lintFn, name: 'acme-demo.yaml' });
    await vi.waitFor(() => expect(host.querySelector('.dl-row')).not.toBeNull());
    expect(host.querySelector('.pe-status')!.textContent).toBe('1 error');
    expect(host.querySelector('.pe-status')!.getAttribute('role')).toBe('status');
    const fix = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Apply fix')!;
    fix.click();
    expect(le.editor.value()).toBe(source);
    expect(host.querySelector('.dl-clean')).toBeNull(); // not clean until the server says so
    await vi.waitFor(() => expect(host.querySelector('.dl-clean')).not.toBeNull());
    expect(host.querySelector('.dl-clean')!.textContent).toBe('✓No problems · 16 rules · 1 budget');
    expect(lintFn).toHaveBeenCalledTimes(2);
    expect(lintFn.mock.calls[1]![0]).toBe(source);
    expect(le.result()).toMatchObject({ valid: true });
    le.destroy();
  });
  it('read-only shows the viewer note and does not lint by default', () => {
    const lintFn = vi.fn();
    const host = document.createElement('div');
    const le = mountLintedEditor(host, { value: source, lint: lintFn, readOnly: true });
    expect(host.textContent).toContain('Viewers can read policies. Editing needs the Admin role.');
    expect(host.querySelector('textarea')!.readOnly).toBe(true);
    expect(lintFn).not.toHaveBeenCalled();
    le.destroy();
  });
});
