// editormath.ts: the PolicyEditor's text maths (S26 §3.2), pure functions over the textarea's
// string. Offsets are UTF-16 indexes (what a textarea's selectionStart uses); positions are 1-based
// {line, column} where the column counts Unicode code points ("runes"), because that is what the Go
// server reports in policy diagnostics (internal/policy keySpan uses runeLen). A tab is one rune; a
// CRLF ends a line like LF and its \r is never inside a column range.

export interface Pos {
  line: number;
  column: number;
}

/** An edit: replace [start, end) with insert, then select [selStart, selEnd) (UTF-16 offsets). */
export interface Edit {
  start: number;
  end: number;
  insert: string;
  selStart: number;
  selEnd: number;
}

export const INDENT = '  ';

/** lineStarts: the offset where each line begins (index 0 is line 1). */
export function lineStarts(text: string): number[] {
  const out = [0];
  for (let i = text.indexOf('\n'); i !== -1; i = text.indexOf('\n', i + 1)) out.push(i + 1);
  return out;
}

/** lineEnd: the offset of the end of the line that starts at `start`, before any \r\n or \n. */
export function lineEnd(text: string, start: number): number {
  let e = text.indexOf('\n', start);
  if (e === -1) e = text.length;
  if (e > start && text.charCodeAt(e - 1) === 13) e--;
  return e;
}

function lineStartOf(text: string, offset: number): number {
  return text.lastIndexOf('\n', offset - 1) + 1;
}

function isHigh(c: number): boolean {
  return c >= 0xd800 && c <= 0xdbff;
}
function isLow(c: number): boolean {
  return c >= 0xdc00 && c <= 0xdfff;
}

/** runes counts code points in text[from, to). A lone surrogate counts as one. */
export function runes(text: string, from = 0, to = text.length): number {
  let n = 0;
  for (let i = from; i < to; i++) {
    n++;
    if (isHigh(text.charCodeAt(i)) && i + 1 < to && isLow(text.charCodeAt(i + 1))) i++;
  }
  return n;
}

/** offsetToPos: a UTF-16 offset to a 1-based line and rune column. */
export function offsetToPos(text: string, offset: number): Pos {
  const o = Math.max(0, Math.min(offset, text.length));
  let line = 1;
  for (let i = text.indexOf('\n'); i !== -1 && i < o; i = text.indexOf('\n', i + 1)) line++;
  const start = lineStartOf(text, o);
  return { line, column: runes(text, start, Math.min(o, lineEnd(text, start))) + 1 };
}

/** posToOffset: a 1-based line and rune column to a UTF-16 offset, clamped to the text and the line. */
export function posToOffset(text: string, line: number, column: number): number {
  if (line < 1) return 0;
  let start = 0;
  for (let l = 1; l < line; l++) {
    const nl = text.indexOf('\n', start);
    if (nl === -1) return text.length;
    start = nl + 1;
  }
  const end = lineEnd(text, start);
  let i = start;
  for (let c = 1; c < column && i < end; c++) {
    i += isHigh(text.charCodeAt(i)) && i + 1 < end && isLow(text.charCodeAt(i + 1)) ? 2 : 1;
  }
  return i;
}

/** A replacement over [line:column, end_line:end_column), columns in runes, end exclusive. */
export interface RangeFix {
  line: number;
  column: number;
  end_line: number;
  end_column: number;
  replace: string;
}

/** fixEdit: the Edit a fix makes; the caret lands after the replacement. */
export function fixEdit(text: string, fix: RangeFix): Edit {
  const start = posToOffset(text, fix.line, fix.column);
  const end = Math.max(start, posToOffset(text, fix.end_line, fix.end_column));
  const caret = start + fix.replace.length;
  return { start, end, insert: fix.replace, selStart: caret, selEnd: caret };
}

/** applyEdit: the text after an Edit. */
export function applyEdit(text: string, e: Edit): string {
  return text.slice(0, e.start) + e.insert + text.slice(e.end);
}

/** applyFix: the text after a fix replaces [line:col, end_line:end_col). */
export function applyFix(text: string, fix: RangeFix): string {
  return applyEdit(text, fixEdit(text, fix));
}

// The lines a selection touches: a selection that ends at the very start of a line doesn't include it.
function touched(text: string, s: number, e: number): { first: number; last: number } {
  const first = lineStartOf(text, s);
  let endAt = e;
  if (e > s && lineStartOf(text, e) === e) endAt = e - 1;
  const last = lineStartOf(text, endAt);
  return { first, last };
}

function blockEnd(text: string, lastStart: number): number {
  const nl = text.indexOf('\n', lastStart);
  return nl === -1 ? text.length : nl;
}

/**
 * indent: Tab. With no selection, or a selection inside one line, two spaces replace it at the
 * caret. With a selection across lines, every touched line gets two spaces in front.
 */
export function indent(text: string, s: number, e: number): Edit {
  if (s === e || !text.slice(s, e).includes('\n')) {
    return { start: s, end: e, insert: INDENT, selStart: s + INDENT.length, selEnd: s + INDENT.length };
  }
  const { first, last } = touched(text, s, e);
  const end = blockEnd(text, last);
  const block = text.slice(first, end);
  const lines = block.split('\n');
  const insert = lines.map((l) => INDENT + l).join('\n');
  const selStart = s === first ? s : s + INDENT.length;
  return { start: first, end, insert, selStart, selEnd: e + INDENT.length * lines.length };
}

/** outdent: Shift+Tab removes up to two leading spaces from every touched line. */
export function outdent(text: string, s: number, e: number): Edit {
  const { first, last } = touched(text, s, e);
  const end = blockEnd(text, last);
  const lines = text.slice(first, end).split('\n');
  let removedBeforeS = 0;
  let removedBeforeE = 0;
  let at = first;
  const out = lines.map((l) => {
    let n = 0;
    while (n < INDENT.length && l.charCodeAt(n) === 32) n++;
    // A caret inside the removed spaces moves to the line's new start.
    removedBeforeS += Math.max(0, Math.min(n, s - at));
    removedBeforeE += Math.max(0, Math.min(n, e - at));
    at += l.length + 1;
    return l.slice(n);
  });
  return { start: first, end, insert: out.join('\n'), selStart: s - removedBeforeS, selEnd: e - removedBeforeE };
}

/**
 * newline: Enter keeps the current line's indent (spaces and tabs before the caret's line text).
 * After a YAML list item ("  - id: x") the next line lines up with the item's keys.
 */
export function newline(text: string, s: number, e: number): Edit {
  const start = lineStartOf(text, s);
  const before = text.slice(start, s);
  const lead = /^[ \t]*/.exec(before)![0];
  const item = /^[ \t]*- (?=\S)/.exec(before);
  const pad = item ? lead + INDENT : lead;
  // A textarea's value is always LF; plain strings with CRLF keep their line endings.
  const eol = text.includes('\r\n') ? '\r\n' : '\n';
  const insert = eol + pad;
  return { start: s, end: e, insert, selStart: s + insert.length, selEnd: s + insert.length };
}

/** lineCount: lines in the text (an empty text has one empty line, as in a textarea). */
export function lineCount(text: string): number {
  let n = 1;
  for (let i = text.indexOf('\n'); i !== -1; i = text.indexOf('\n', i + 1)) n++;
  return n;
}
