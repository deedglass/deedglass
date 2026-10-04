// linediff.ts: a line diff for policy versions (S26 §3.2 Versions). Myers' O(ND) algorithm in its
// linear-space form (the middle snake, divide and conquer), so two 2,000-line versions cost a few
// milliseconds and memory stays O(N + M) however far apart they are. Lines are compared as integer
// ids. CRLF is normalised to LF before comparing, and `crlf` reports that either input had it, so a
// version saved from Windows doesn't diff as "every line changed".

export type DiffKind = 'ctx' | 'add' | 'del';

export interface DiffLine {
  kind: DiffKind;
  text: string;
  /** 1-based line number in the old version (null for an added line). */
  old: number | null;
  /** 1-based line number in the new version (null for a removed line). */
  new: number | null;
}

export interface Hunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: DiffLine[];
}

export interface LineDiff {
  hunks: Hunk[];
  added: number;
  removed: number;
  identical: boolean;
  /** Either input had CRLF line endings (normalised to LF before comparing). */
  crlf: boolean;
}

/** splitLines: LF lines; a final newline doesn't make an extra empty line; "" has no lines. */
export function splitLines(text: string): string[] {
  if (text === '') return [];
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/**
 * changes marks which lines of a are removed and which lines of b are added, by Myers' linear-space
 * O(ND) algorithm over integer ids. Lines left unmarked are the longest common subsequence.
 */
export function changes(a: Int32Array, b: Int32Array): { del: Uint8Array; add: Uint8Array } {
  const del = new Uint8Array(a.length);
  const add = new Uint8Array(b.length);
  const stack: number[] = [0, a.length, 0, b.length];
  while (stack.length) {
    let bHi = stack.pop()!, bLo = stack.pop()!, aHi = stack.pop()!, aLo = stack.pop()!;
    while (aLo < aHi && bLo < bHi && a[aLo] === b[bLo]) { aLo++; bLo++; }
    while (aLo < aHi && bLo < bHi && a[aHi - 1] === b[bHi - 1]) { aHi--; bHi--; }
    if (aLo === aHi) { add.fill(1, bLo, bHi); continue; }
    if (bLo === bHi) { del.fill(1, aLo, aHi); continue; }
    const mid = middleSnake(a, aLo, aHi, b, bLo, bHi);
    if (mid === null) { del.fill(1, aLo, aHi); add.fill(1, bLo, bHi); continue; }
    stack.push(aLo, aLo + mid[0], bLo, bLo + mid[1], aLo + mid[0], aHi, bLo + mid[1], bHi);
  }
  return { del, add };
}

// middleSnake finds where an optimal path crosses the middle diagonal band, searching forward from
// the top left and backward from the bottom right at once (Myers 1986 §4b). Returns the split point
// relative to (aLo, bLo).
function middleSnake(a: Int32Array, aLo: number, aHi: number, b: Int32Array, bLo: number, bHi: number): [number, number] | null {
  const n = aHi - aLo, m = bHi - bLo;
  const maxD = Math.ceil((n + m) / 2);
  const off = maxD + 1;
  const len = 2 * maxD + 3;
  const v1 = new Int32Array(len).fill(-1);
  const v2 = new Int32Array(len).fill(-1);
  v1[off + 1] = 0;
  v2[off + 1] = 0;
  const delta = n - m;
  const front = (delta & 1) !== 0;
  let k1s = 0, k1e = 0, k2s = 0, k2e = 0;
  for (let d = 0; d < maxD; d++) {
    for (let k1 = -d + k1s; k1 <= d - k1e; k1 += 2) {
      const i1 = off + k1;
      let x1 = k1 === -d || (k1 !== d && v1[i1 - 1]! < v1[i1 + 1]!) ? v1[i1 + 1]! : v1[i1 - 1]! + 1;
      let y1 = x1 - k1;
      while (x1 < n && y1 < m && a[aLo + x1] === b[bLo + y1]) { x1++; y1++; }
      v1[i1] = x1;
      if (x1 > n) k1e += 2;
      else if (y1 > m) k1s += 2;
      else if (front) {
        const i2 = off + delta - k1;
        if (i2 >= 0 && i2 < len && v2[i2] !== -1 && x1 >= n - v2[i2]!) return [x1, y1];
      }
    }
    for (let k2 = -d + k2s; k2 <= d - k2e; k2 += 2) {
      const i2 = off + k2;
      let x2 = k2 === -d || (k2 !== d && v2[i2 - 1]! < v2[i2 + 1]!) ? v2[i2 + 1]! : v2[i2 - 1]! + 1;
      let y2 = x2 - k2;
      while (x2 < n && y2 < m && a[aHi - 1 - x2] === b[bHi - 1 - y2]) { x2++; y2++; }
      v2[i2] = x2;
      if (x2 > n) k2e += 2;
      else if (y2 > m) k2s += 2;
      else if (!front) {
        const i1 = off + delta - k2;
        if (i1 >= 0 && i1 < len && v1[i1] !== -1) {
          const x1 = v1[i1]!;
          const y1 = x1 - (i1 - off);
          if (x1 >= n - x2) return [x1, y1];
        }
      }
    }
  }
  return null;
}

/** diffLines: unified hunks with `context` lines (3 by default) around each change. */
export function diffLines(oldText: string, newText: string, context = 3): LineDiff {
  const crlf = oldText.includes('\r\n') || newText.includes('\r\n');
  const a = splitLines(oldText), b = splitLines(newText);
  const ids = new Map<string, number>();
  const id = (s: string) => {
    let v = ids.get(s);
    if (v === undefined) { v = ids.size; ids.set(s, v); }
    return v;
  };
  const { del, add } = changes(Int32Array.from(a, id), Int32Array.from(b, id));
  const all: DiffLine[] = [];
  let i = 0, j = 0, added = 0, removed = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && del[i]) { all.push({ kind: 'del', text: a[i]!, old: i + 1, new: null }); i++; removed++; }
    else if (j < b.length && add[j]) { all.push({ kind: 'add', text: b[j]!, old: null, new: j + 1 }); j++; added++; }
    else { all.push({ kind: 'ctx', text: a[i]!, old: i + 1, new: j + 1 }); i++; j++; }
  }
  return { hunks: hunksOf(all, context), added, removed, identical: added === 0 && removed === 0, crlf };
}

function hunksOf(all: DiffLine[], context: number): Hunk[] {
  const hunks: Hunk[] = [];
  let k = 0;
  while (k < all.length) {
    while (k < all.length && all[k]!.kind === 'ctx') k++;
    if (k >= all.length) break;
    const start = Math.max(0, k - context);
    let end = k; // exclusive end of the last change seen
    let p = k;
    while (p < all.length) {
      if (all[p]!.kind !== 'ctx') { end = ++p; continue; }
      let q = p;
      while (q < all.length && all[q]!.kind === 'ctx') q++;
      if (q < all.length && q - p <= 2 * context) { p = q; continue; }
      break;
    }
    const stop = Math.min(all.length, end + context);
    const lines = all.slice(start, stop);
    hunks.push(hunkOf(lines, all, start));
    k = stop;
  }
  return hunks;
}

function hunkOf(lines: DiffLine[], all: DiffLine[], start: number): Hunk {
  let oldLines = 0, newLines = 0;
  for (const l of lines) {
    if (l.kind !== 'add') oldLines++;
    if (l.kind !== 'del') newLines++;
  }
  // The line before the hunk tells where an empty side sits (unified diff convention).
  const firstOld = lines.find((l) => l.old !== null)?.old;
  const firstNew = lines.find((l) => l.new !== null)?.new;
  let prevOld = 0, prevNew = 0;
  for (let p = start - 1; p >= 0; p--) {
    const l = all[p]!;
    if (!prevOld && l.old !== null) prevOld = l.old;
    if (!prevNew && l.new !== null) prevNew = l.new;
    if (prevOld && prevNew) break;
  }
  return { oldStart: firstOld ?? prevOld, oldLines, newStart: firstNew ?? prevNew, newLines, lines };
}
