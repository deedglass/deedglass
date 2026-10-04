// policyeditor.ts: PolicyEditor (S26 §3.2). No dependency: a <textarea> is the only editable
// surface (native undo, IME, screen readers, spellcheck off), with a transparent-text <pre
// aria-hidden> mirror behind it at identical metrics (Geist Mono 13/20, tab-size 2, white-space
// pre, the same padding) that draws diagnostic underlines and the revealed line, and a gutter with
// line numbers and severity markers (✕ error, ○ warning, the message in a data-tip tooltip).
//
// Typing stays under a frame on a 2,000-line policy: the mirror is rebuilt at most once per frame,
// and only lines that carry a decoration hold text and spans. Every other line is an empty line in
// one Text node (the mirror's text is transparent, so only its line count matters there). Scroll
// offsets follow the textarea through a transform, synced in requestAnimationFrame.
//
// Keys: Tab inserts two spaces (indents selected lines), Shift+Tab outdents, Enter keeps the
// indent, Esc then Tab leaves the editor, ⌘/Ctrl+S saves. Edits go through
// execCommand('insertText') so the native undo stack survives; setRangeText is the fallback.

import { fill } from '../lib/copy.ts';
import { editorCopy as c } from '../lib/copy-editor.ts';
import { h, replace } from '../lib/dom.ts';
import { fixEdit, indent, lineCount, lineStarts, newline, offsetToPos, outdent, posToOffset, type Edit } from '../lib/editormath.ts';
import { createLintLoop, defaultLint, type LintDeps, type LintState } from '../lib/lintloop.ts';
import { allDiagnostics, quickFix, type PolicyDiagnostic, type PolicyLintResult, type Severity } from '../lib/policyfix.ts';
import { diagnosticsList, summaryText } from './diagnostics.ts';

export const LINE_PX = 20;
export const PAD_PX = 12;

export interface PolicyEditorOptions {
  value: string;
  readOnly?: boolean;
  /** The textarea's accessible name (defaults to "Policy source"). */
  label?: string;
  /** A filename or policy name for the header strip, e.g. postgres-safe.yaml. */
  name?: string;
  onChange?: (v: string) => void;
  onSave?: (v: string) => void;
  /** Extra ids for aria-describedby (the escape hint is always first). */
  describedBy?: string;
}

export interface PolicyEditor {
  el: HTMLElement | null;
  mount(host: HTMLElement, o: PolicyEditorOptions): void;
  setDiagnostics(d: readonly PolicyDiagnostic[]): void;
  /** Puts the caret at the line's start, scrolls it to the middle, highlights it; focus: false leaves focus alone. */
  revealLine(line: number, highlight?: boolean, o?: { focus?: boolean }): void;
  /** Applies the diagnostic's quick fix; false when there is none (or the editor is read-only). */
  applyFix(d: PolicyDiagnostic): boolean;
  value(): string;
  setValue(v: string): void;
  setReadOnly(on: boolean): void;
  focus(): void;
  /** Renders a pending mirror update now (tests and measurements). */
  flush(): void;
  destroy(): void;
}

// ---------- decorations (pure) ----------

export interface Mark {
  from: number; // UTF-16 offsets within the line
  to: number;
  sev: Severity;
}

/** A decorated line: its text (without the line ending) and its underline ranges. */
export interface DecoratedLine {
  text: string;
  marks: Mark[];
}

/** decorate: the underline ranges per line for diagnostics, mapped onto the current text. */
export function decorate(text: string, diags: readonly PolicyDiagnostic[]): Map<number, DecoratedLine> {
  const out = new Map<number, DecoratedLine>();
  const withLine = diags.filter((d) => d.line);
  if (withLine.length === 0) return out;
  const starts = lineStarts(text);
  const total = starts.length;
  const lineText = (l: number): string => {
    const s = starts[l - 1]!;
    const e = l < total ? starts[l]! - 1 : text.length;
    return text.slice(s, e).replace(/\r$/, '');
  };
  for (const d of withLine) {
    const first = Math.min(d.line!, total);
    const last = Math.min(Math.max(d.end_line ?? first, first), total, first + 50);
    for (let l = first; l <= last; l++) {
      const entry = out.get(l) ?? { text: lineText(l), marks: [] };
      const t = entry.text;
      let from = l === first && d.column ? posToOffset(t, 1, d.column) : 0;
      if (!d.column && l === first) from = t.length - t.trimStart().length;
      let to = l === last && d.end_column && (d.end_line ?? first) === l ? posToOffset(t, 1, d.end_column) : t.length;
      if (to < from) to = from;
      entry.marks.push({ from, to, sev: d.severity === 'error' ? 'error' : 'warning' });
      out.set(l, entry);
    }
  }
  for (const e of out.values()) e.marks = normalise(e.marks);
  return out;
}

// Sorted, non-overlapping; an error wins where an error and a warning overlap.
function normalise(list: Mark[]): Mark[] {
  list.sort((a, b) => a.from - b.from || (a.sev === 'error' ? -1 : 1));
  const out: Mark[] = [];
  for (const m of list) {
    const prev = out[out.length - 1];
    if (prev && m.from < prev.to) {
      if (m.to <= prev.to) continue;
      out.push({ from: prev.to, to: m.to, sev: m.sev });
      continue;
    }
    out.push({ ...m });
  }
  return out;
}

/** column: the nodes of a column of `total` lines where only `lines` have content. */
export function column(total: number, lines: Map<number, Node[]>): Node[] {
  const out: Node[] = [];
  let buf = '';
  let at = 1;
  for (const l of [...lines.keys()].sort((a, b) => a - b)) {
    if (l < at || l > total) continue;
    buf += '\n'.repeat(l - at);
    if (buf) out.push(document.createTextNode(buf));
    out.push(...lines.get(l)!);
    buf = l < total ? '\n' : '';
    at = l + 1;
  }
  if (at <= total) buf += '\n'.repeat(total - at);
  if (buf) out.push(document.createTextNode(buf));
  return out;
}

function lineNodes(t: string, marks: Mark[]): Node[] {
  const out: Node[] = [];
  let at = 0;
  for (const m of marks) {
    if (m.from > at) out.push(document.createTextNode(t.slice(at, m.from)));
    // A zero-width range still gets a visible underline: one character, or a space past the end.
    const to = m.to > m.from ? m.to : Math.min(m.from + 1, t.length);
    const seg = to > m.from ? t.slice(m.from, to) : ' ';
    out.push(h('span', { class: `pe-u pe-u-${m.sev}` }, seg));
    at = Math.max(at, to);
  }
  if (at < t.length) out.push(document.createTextNode(t.slice(at)));
  return out;
}

const raf = (fn: () => void): number =>
  typeof requestAnimationFrame === 'function' ? requestAnimationFrame(() => fn()) : (setTimeout(fn, 16) as unknown as number);

let seq = 0;

export function createPolicyEditor(): PolicyEditor {
  let o: PolicyEditorOptions = { value: '' };
  let root: HTMLElement | null = null;
  let ta!: HTMLTextAreaElement;
  let mirror!: HTMLPreElement;
  let hl!: HTMLElement;
  let gutterInner!: HTMLElement;
  let marksCol!: HTMLPreElement;
  let nums!: HTMLPreElement;
  let caretEl!: HTMLElement;
  let roTag!: HTMLElement;
  let diags: PolicyDiagnostic[] = [];
  let highlight: number | null = null;
  let readOnly = false;
  let escArmed = false;
  let renderQueued = false;
  let scrollQueued = false;
  let caretQueued = false;
  let shownLines = 0;
  let shownX = 0;
  let shownY = 0;
  const cleanups: (() => void)[] = [];

  const queueRender = () => {
    if (renderQueued) return;
    renderQueued = true;
    raf(render);
  };

  function render() {
    renderQueued = false;
    if (!root) return;
    const text = ta.value;
    const total = lineCount(text);
    if (total !== shownLines) {
      shownLines = total;
      let s = '1';
      for (let i = 2; i <= total; i++) s += '\n' + i;
      nums.textContent = s;
      root.classList.toggle('pe-wide', total >= 10_000);
    }
    const mirrorLines = new Map<number, Node[]>();
    for (const [l, d] of decorate(text, diags)) mirrorLines.set(l, lineNodes(d.text, d.marks));
    replace(mirror, ...column(total, mirrorLines));
    hl.hidden = highlight === null || highlight > total;
    placeHighlight();
    // Gutter markers: the worst severity per line, every message in the tooltip.
    const byLine = new Map<number, PolicyDiagnostic[]>();
    for (const d of diags) {
      if (!d.line) continue;
      const l = Math.min(d.line, total);
      byLine.set(l, [...(byLine.get(l) ?? []), d]);
    }
    const markerLines = new Map<number, Node[]>();
    for (const [l, list] of byLine) {
      const err = list.some((d) => d.severity === 'error');
      const tip = list.map((d) => [d.message, d.hint].filter(Boolean).join(' ')).join(' ');
      markerLines.set(l, [h('span', { class: `pe-mk pe-mk-${err ? 'error' : 'warning'}`, 'data-tip': tip, 'data-tip-side': 'right', 'data-line': l }, err ? '✕' : '○')]);
    }
    replace(marksCol, ...column(total, markerLines));
    syncScroll();
  }

  // The revealed line's band spans the visible width, so it follows vertical scroll only.
  function placeHighlight() {
    if (highlight !== null) hl.style.top = `${PAD_PX + (highlight - 1) * LINE_PX - ta.scrollTop}px`;
  }

  function syncScroll() {
    scrollQueued = false;
    if (!root) return;
    const x = ta.scrollLeft, y = ta.scrollTop;
    if (x === shownX && y === shownY) return;
    shownX = x;
    shownY = y;
    mirror.style.transform = x || y ? `translate(${-x}px, ${-y}px)` : '';
    gutterInner.style.transform = y ? `translateY(${-y}px)` : '';
    placeHighlight();
  }

  function updateCaret() {
    caretQueued = false;
    if (!root) return;
    const p = offsetToPos(ta.value, ta.selectionStart ?? 0);
    caretEl.textContent = fill(c.caret, { line: p.line, col: p.column });
  }
  const queueCaret = () => {
    if (caretQueued) return;
    caretQueued = true;
    raf(updateCaret);
  };

  function onInput() {
    if (highlight !== null) highlight = null;
    queueRender();
    queueCaret();
    o.onChange?.(ta.value);
  }

  /** perform: one undoable edit. */
  function perform(e: Edit) {
    const before = ta.value;
    const expected = before.slice(0, e.start) + e.insert + before.slice(e.end);
    if (expected === before) {
      ta.setSelectionRange(e.selStart, e.selEnd);
      return;
    }
    if (document.activeElement !== ta) ta.focus({ preventScroll: true });
    ta.setSelectionRange(e.start, e.end);
    let ok = false;
    try {
      ok = typeof document.execCommand === 'function' && document.execCommand(e.insert === '' ? 'delete' : 'insertText', false, e.insert);
    } catch {
      ok = false;
    }
    if (!ok || ta.value !== expected) {
      if (ta.value === before) ta.setRangeText(e.insert, e.start, e.end, 'end');
      else ta.value = expected;
      onInput();
    }
    ta.setSelectionRange(e.selStart, e.selEnd);
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.isComposing || e.keyCode === 229) return;
    const mod = e.metaKey || e.ctrlKey;
    if (mod && !e.altKey && (e.key === 's' || e.key === 'S')) {
      e.preventDefault();
      if (!readOnly) o.onSave?.(ta.value);
      return;
    }
    if (e.key === 'Escape') {
      escArmed = true;
      return;
    }
    if (e.key === 'Shift' || e.key === 'Control' || e.key === 'Alt' || e.key === 'Meta') return;
    if (e.key === 'Tab') {
      // After Esc the next Tab moves focus, like any field. Read-only never traps Tab.
      if (escArmed || readOnly || mod || e.altKey) {
        escArmed = false;
        return;
      }
      e.preventDefault();
      const s = ta.selectionStart, en = ta.selectionEnd;
      perform(e.shiftKey ? outdent(ta.value, s, en) : indent(ta.value, s, en));
      return;
    }
    escArmed = false;
    if (e.key === 'Enter' && !mod && !e.altKey && !e.shiftKey && !readOnly) {
      e.preventDefault();
      perform(newline(ta.value, ta.selectionStart, ta.selectionEnd));
    }
  }

  const api: PolicyEditor = {
    el: null,
    mount(host, opts) {
      o = opts;
      readOnly = !!opts.readOnly;
      const id = `pe${++seq}`;
      const hintId = `${id}-esc`;
      ta = h('textarea', {
        class: 'pe-input', id: `${id}-input`, 'aria-label': opts.label ?? c.label,
        'aria-describedby': [hintId, opts.describedBy].filter(Boolean).join(' '),
        spellcheck: 'false', autocapitalize: 'off', autocomplete: 'off', autocorrect: 'off', wrap: 'off',
        translate: 'no', 'data-gramm': 'false', rows: 1, cols: 1,
      });
      ta.value = opts.value;
      ta.setSelectionRange(0, 0);
      ta.readOnly = readOnly;
      hl = h('mark', { class: 'pe-hl', hidden: true });
      mirror = h('pre', { class: 'pe-mirror', 'aria-hidden': 'true' });
      marksCol = h('pre', { class: 'pe-marks' });
      nums = h('pre', { class: 'pe-nums' });
      gutterInner = h('div', { class: 'pe-gutter-inner' }, marksCol, nums);
      caretEl = h('span', { class: 'pe-caret num' });
      roTag = h('span', { class: 'pe-ro', hidden: !readOnly }, c.readOnly);
      root = h('div', { class: 'pe', 'data-readonly': readOnly ? 'true' : 'false' },
        h('div', { class: 'pe-head' },
          h('span', { class: 'pe-name mono', translate: 'no' }, opts.name ?? ''),
          h('span', { class: 'pe-head-meta' }, roTag, caretEl)),
        h('div', { class: 'pe-main' },
          h('div', { class: 'pe-gutter', 'aria-hidden': 'true' }, gutterInner),
          h('div', { class: 'pe-body' }, h('div', { class: 'pe-clip', 'aria-hidden': 'true' }, hl, mirror), ta)),
        h('p', { class: 'sr-only', id: hintId }, c.escHint));
      api.el = root;
      const on = <K extends keyof HTMLElementEventMap>(el: EventTarget, type: K, fn: (e: HTMLElementEventMap[K]) => void) => {
        el.addEventListener(type, fn as EventListener);
        cleanups.push(() => el.removeEventListener(type, fn as EventListener));
      };
      on(ta, 'input', onInput);
      on(ta, 'keydown', onKeyDown);
      on(ta, 'blur', () => { escArmed = false; });
      on(ta, 'scroll', () => {
        if (scrollQueued) return;
        scrollQueued = true;
        raf(syncScroll);
      });
      for (const t of ['keyup', 'click', 'select', 'focus'] as const) on(ta, t, queueCaret);
      on(marksCol, 'click', (e) => {
        const l = Number((e.target as HTMLElement).getAttribute?.('data-line'));
        if (l) api.revealLine(l, true);
      });
      host.append(root);
      render();
      updateCaret();
    },
    setDiagnostics(d) {
      diags = [...d];
      if (root) ta.setAttribute('aria-invalid', diags.some((x) => x.severity === 'error') ? 'true' : 'false');
      queueRender();
    },
    revealLine(line, hi = true, ro = {}) {
      if (!root) return;
      const text = ta.value;
      const l = Math.max(1, Math.min(Math.floor(line), lineCount(text)));
      const at = posToOffset(text, l, 1);
      const focus = ro.focus ?? true;
      if (focus) ta.focus({ preventScroll: true });
      ta.setSelectionRange(at, at);
      const view = ta.clientHeight || 400;
      ta.scrollTop = Math.max(0, PAD_PX + (l - 1) * LINE_PX - Math.max(0, (view - LINE_PX) / 2));
      ta.scrollLeft = 0;
      if (focus && typeof root.scrollIntoView === 'function') {
        const reduce = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
        root.scrollIntoView({ block: 'nearest', behavior: reduce ? 'auto' : 'smooth' });
      }
      highlight = hi ? l : null;
      render();
      updateCaret();
    },
    applyFix(d) {
      if (!root || readOnly) return false;
      const fix = quickFix(d);
      if (!fix) return false;
      perform(fixEdit(ta.value, fix));
      diags = diags.filter((x) => x !== d);
      queueRender();
      return true;
    },
    value: () => (root ? ta.value : o.value),
    setValue(v) {
      if (!root) {
        o = { ...o, value: v };
        return;
      }
      ta.value = v;
      ta.setSelectionRange(0, 0);
      highlight = null;
      render();
      updateCaret();
    },
    setReadOnly(on) {
      readOnly = on;
      if (!root) return;
      ta.readOnly = on;
      root.setAttribute('data-readonly', on ? 'true' : 'false');
      roTag.hidden = !on;
    },
    focus() {
      ta?.focus();
    },
    flush() {
      if (renderQueued) render();
      if (caretQueued) updateCaret();
    },
    destroy() {
      for (const f of cleanups.splice(0)) f();
      root?.remove();
      root = null;
      api.el = null;
    },
  };
  return api;
}

// ---------- the editor with its lint loop and diagnostics ----------

export interface LintedEditorOptions extends PolicyEditorOptions {
  /** The lint call (defaults to POST /v1/policies/lint); tests and the kit pass their own. */
  lint?: LintDeps['lint'];
  /** Lint once on mount (default: when editable). */
  lintOnMount?: boolean;
  onLint?: (s: LintState) => void;
}

export interface LintedEditor {
  editor: PolicyEditor;
  /** Lint the current text now. */
  lintNow(): void;
  /** The latest lint result, or null before the first one. */
  result(): PolicyLintResult | null;
  destroy(): void;
}

/**
 * mountLintedEditor: the Policies › Editor tab body. The editor, a status line (role=status: the
 * summary, a pause or the size limit) and the DiagnosticsList, wired to the lint loop.
 */
export function mountLintedEditor(host: HTMLElement, o: LintedEditorOptions): LintedEditor {
  const editor = createPolicyEditor();
  const listId = `pel${++seq}`;
  const statusId = `pes${seq + 1}`;
  const status = h('p', { class: 'pe-status t-footnote', role: 'status', id: statusId });
  const busy = h('span', { class: 'pe-busy t-footnote t-2', 'aria-hidden': 'true', hidden: true }, c.checking);
  const list = h('div', { class: 'pe-diags', id: listId });
  let last: PolicyLintResult | null = null;
  let current: PolicyDiagnostic[] = [];

  const drawList = (counts: { rules: number; budgets: number } | null) => {
    replace(list, diagnosticsList(current, {
      counts,
      summary: false,
      readOnly: !!o.readOnly,
      onReveal: (line) => editor.revealLine(line, true),
      onFix: (d) => {
        if (!editor.applyFix(d)) return;
        // Not clean until the server says so (S26 invariant 5): drop the row, lint again now.
        current = current.filter((x) => x !== d);
        drawList(null);
        loop.flush(editor.value());
      },
    }));
  };
  const setStatus = (t: string) => {
    if (status.textContent !== t) status.textContent = t;
  };

  const loop = createLintLoop({
    lint: o.lint ?? defaultLint,
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id as number),
  }, (s) => {
    busy.hidden = !(s.kind === 'pending' || s.kind === 'linting');
    list.setAttribute('aria-busy', busy.hidden ? 'false' : 'true');
    switch (s.kind) {
      case 'result':
        last = s.result;
        current = allDiagnostics(s.result);
        editor.setDiagnostics(current);
        setStatus(summaryText(current) ?? '');
        drawList({ rules: s.result.rules, budgets: s.result.budgets });
        break;
      case 'too_large':
      case 'paused':
        setStatus(s.message);
        break;
      case 'error':
        setStatus(s.message);
        break;
      case 'idle':
        if (status.textContent === c.lintPaused) setStatus('');
        break;
    }
    o.onLint?.(s);
  });

  editor.mount(host, {
    ...o,
    describedBy: [statusId, o.describedBy].filter(Boolean).join(' '), // the short status line, not the whole list
    onChange: (v) => {
      loop.change(v);
      o.onChange?.(v);
    },
  });
  const foot = h('div', { class: 'pe-foot' }, status, busy);
  host.append(foot, list);
  if (o.readOnly) foot.prepend(h('p', { class: 'pe-ro-note t-footnote t-2' }, c.readOnlyNote));
  if (o.lintOnMount ?? !o.readOnly) loop.flush(editor.value());
  return {
    editor,
    lintNow: () => loop.flush(editor.value()),
    result: () => last,
    destroy() {
      loop.dispose();
      editor.destroy();
      foot.remove();
      list.remove();
    },
  };
}
