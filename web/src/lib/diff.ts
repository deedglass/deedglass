// diff.ts: the Witness's L2 evidence as rows for DiffTable (S25 §3.5; brand §12). Pure, tested
// against S21's fixtures. It reads witness_detail v1's levels.L2 exactly as S21 stores it:
// expected (the statement's footprint), observed (counts from the WAL) and rows (the first
// witness.diff_rows changed rows, expected ones first). A row the statement didn't name is
// "unexpected"; a sensitive value is "•••" and never shown. When L2 holds no frame (a gap),
// diffModel returns null and the Witness's reasons say why.

import type { L2Evidence, L2ExpectedTable, L2Row, WitnessDetail } from './types.ts';
import { number, plural } from './format.ts';

export const MASK = '•••';

export interface DiffRow {
  table: string;
  key: string; // "id = 42"
  op: L2Row['op'];
  column: string;
  expected: string | null; // what the statement wrote (expected rows only)
  observed: string | null; // what Postgres committed (the after image; null for a delete)
  before: string | null;
  unexpected: boolean; // the statement didn't name this row
  masked: boolean;
  first: boolean; // the row's first cell (the table and key print once per row)
  cells: number; // cells in this row
}

export interface DiffModel {
  expected: string; // "UPDATE 1 row in public.orders (id = 42) · columns status"
  observed: string; // "UPDATE 301 rows in public.orders"
  rows: DiffRow[];
  changedRows: number; // rows Postgres changed in total
  shown: number; // rows the Witness kept (witness.diff_rows)
  truncated: boolean;
  columnsMore: number;
  redacted: string[];
}

export function keyText(key: Record<string, string>): string {
  return Object.entries(key).map(([k, v]) => `${k} = ${v}`).join(', ');
}

const OP = (op: string) => op.toUpperCase();

function expectedLine(t: L2ExpectedTable): string {
  const ops = t.ops.length ? t.ops.map(OP).join(' or ') : 'CHANGE';
  const rows = t.rows !== undefined ? plural(t.rows, 'row') : t.max_rows !== undefined ? `up to ${plural(t.max_rows, 'row')}` : 'rows';
  const keys = t.keys?.length ? ` (${t.keys.slice(0, 3).map(keyText).join('; ')}${t.keys.length > 3 ? '; …' : ''})` : '';
  const cols = t.columns?.length ? ` · ${t.columns.length === 1 ? 'column' : 'columns'} ${t.columns.join(', ')}` : '';
  return `${ops} ${rows} in ${t.table}${keys}${cols}`;
}

function observedLine(l2: L2Evidence): string {
  const parts: string[] = [];
  for (const t of l2.observed?.tables ?? []) {
    for (const op of ['update', 'insert', 'delete', 'truncate'] as const) {
      const n = t[op] ?? 0;
      if (n > 0) parts.push(`${OP(op)} ${plural(n, 'row')} in ${t.table}`);
    }
  }
  if (parts.length === 0) return 'No changes in the watched tables';
  return parts.map((p, i) => (i === 0 ? p : `plus ${p}`)).join(' · ');
}

function isMasked(v: string | null, table: string, column: string, redacted: string[]): boolean {
  return v === MASK || redacted.includes(`${table}.${column}`) || redacted.includes(column);
}

/** diffModel builds the DiffTable from a witness_detail; null when L2 has no evidence to show. */
export function diffModel(detail: WitnessDetail | null | undefined): DiffModel | null {
  const l2 = detail?.levels?.L2;
  if (!l2 || !l2.observed || !Array.isArray(l2.rows)) return null;
  const ev = l2 as L2Evidence;
  const redacted = ev.redacted ?? [];
  const rows: DiffRow[] = [];
  for (const r of ev.rows) {
    const key = keyText(r.key);
    const cells = r.cells.length ? r.cells : [{ column: '', before: null, after: null }];
    cells.forEach((c, i) => {
      const masked = isMasked(c.after, r.table, c.column, redacted) || isMasked(c.before, r.table, c.column, redacted);
      rows.push({
        table: r.table,
        key,
        op: r.op,
        column: c.column,
        expected: r.expected ? (masked ? MASK : c.after) : null,
        observed: masked ? MASK : c.after,
        before: masked ? MASK : c.before,
        unexpected: !r.expected,
        masked,
        first: i === 0,
        cells: cells.length,
      });
    });
  }
  const changed = ev.observed?.total ?? ev.rows.length;
  return {
    expected: (ev.expected?.tables ?? []).map(expectedLine).join(' · ') || 'The statement named no rows Deedglass could derive',
    observed: observedLine(ev),
    rows,
    changedRows: changed,
    shown: ev.rows_shown ?? ev.rows.length,
    truncated: (ev.rows_more ?? 0) > 0,
    columnsMore: ev.columns_more ?? 0,
    redacted,
  };
}

/** showingLine: "Showing 20 of 301 rows". */
export function showingLine(m: DiffModel): string {
  return m.truncated ? `Showing ${number(m.shown)} of ${plural(m.changedRows, 'row')}` : `${plural(m.changedRows, 'row')} changed`;
}
