// traceview.ts: TraceView (S26 §3.2 Dry run): the result card for a policy_decision (S12 shape). The
// decision chip and the server's message verbatim (S13 wording), `Rule block-unscoped-writes · line
// 47` with the line as a button that reveals it in the editor, the trace as rows, the SQL facts and
// `evaluated in 48 µs`. Also the Tests tab's result: S12's summary line and the cases table.

import { decisionChip, type Decision } from '../lib/chips.ts';
import { fill } from '../lib/copy.ts';
import { editorCopy as c } from '../lib/copy-editor.ts';
import { h, type Child } from '../lib/dom.ts';

export interface TraceStep {
  step: string; // stop | budget | rule
  id?: string;
  line?: number;
  result: string; // pass | fail | match | no_match | not_evaluated | …
  detail?: string;
  cost?: number;
}

export interface SqlFacts {
  present?: boolean;
  kind?: string;
  category?: string;
  multi?: boolean;
  unbounded_write?: boolean;
  tables?: string[];
  [k: string]: unknown;
}

export interface PolicyDecision {
  object: 'policy_decision';
  decision: Decision;
  code?: string | null;
  source?: string | null; // rule | default | budget | stop
  rule_id?: string | null;
  rule_line?: number | null;
  reason?: string | null;
  message?: string | null;
  verify?: string | null;
  trace?: TraceStep[];
  facts?: { sql?: SqlFacts | null; [k: string]: unknown } | null;
  errors?: { message: string }[] | null;
  evaluated_in_us?: number | null;
}

export interface TraceOptions {
  onReveal?: (line: number) => void;
}

type Glyph = { g: string; cls: string; word: string };

/** stepGlyph: ✓ passed or an allow match, ✕ failed or a deny match, ◐ an approval match, · no match. */
export function stepGlyph(s: TraceStep, d: Pick<PolicyDecision, 'decision' | 'rule_id'>): Glyph {
  const word = c.results[s.result] ?? s.result.replace(/_/g, ' ');
  switch (s.result) {
    case 'pass':
      return { g: '✓', cls: 'tv-ok', word };
    case 'fail':
      return { g: '✕', cls: 'tv-deny', word };
    case 'match': {
      const decision = s.id && d.rule_id && s.id !== d.rule_id ? null : d.decision;
      if (decision === 'deny') return { g: '✕', cls: 'tv-deny', word };
      if (decision === 'require_approval') return { g: '◐', cls: 'tv-pending', word };
      return { g: '✓', cls: 'tv-ok', word };
    }
    case 'no_match':
    case 'not_evaluated':
    case 'skipped':
      return { g: '·', cls: 'tv-quiet', word };
    default:
      return { g: '○', cls: 'tv-warn', word };
  }
}

/** stepText: `stop · agent active · org running`, `budget agent-rate · 0 of 30 calls this minute`. */
export function stepText(s: TraceStep): string {
  if (s.result === 'not_evaluated' && !s.id) return s.detail ?? '';
  const head = [c.steps[s.step] ?? s.step, s.id].filter(Boolean).join(' ');
  return s.detail ? `${head} · ${s.detail}` : head;
}

/** sqlFactsText: `sql: delete · write · unbounded · public.orders`, or null without SQL. */
export function sqlFactsText(f: SqlFacts | null | undefined): string | null {
  if (!f || f.present === false || !f.kind) return null;
  const parts = [f.kind, f.category, f.unbounded_write ? c.sqlUnbounded : null, f.multi ? c.sqlMulti : null, ...(f.tables ?? [])]
    .filter((x): x is string => typeof x === 'string' && x !== '');
  return fill(c.sqlFacts, { facts: parts.join(' · ') });
}

function lineButton(line: number, o: TraceOptions, cls = 'tv-line'): Child {
  const label = fill(c.line, { line });
  return o.onReveal
    ? h('button', { type: 'button', class: `${cls} mono`, title: fill(c.reveal, { line }), onclick: () => o.onReveal!(line) }, label)
    : h('span', { class: `${cls} mono` }, label);
}

function stepRow(s: TraceStep, d: PolicyDecision, o: TraceOptions): HTMLLIElement {
  const g = stepGlyph(s, d);
  const text = stepText(s);
  let body: Child[] = [text];
  // A rule's id reveals its line in the editor.
  if (s.step === 'rule' && s.id && s.line && o.onReveal) {
    const head = `${c.steps.rule ?? 'rule'} `;
    const rest = s.detail ? ` · ${s.detail}` : '';
    body = [head, h('button', { type: 'button', class: 'tv-id mono', title: fill(c.reveal, { line: s.line }), onclick: () => o.onReveal!(s.line!) }, s.id), rest];
  }
  return h('li', { class: `tv-step ${g.cls}` },
    h('span', { class: 'tv-glyph', 'aria-hidden': 'true' }, g.g),
    h('span', { class: 'sr-only' }, `${g.word}: `),
    h('span', { class: 'tv-text' }, ...body));
}

/** traceView: the dry-run result card. */
export function traceView(d: PolicyDecision, o: TraceOptions = {}): HTMLElement {
  const facts = sqlFactsText(d.facts?.sql ?? null);
  const rule = d.rule_id
    ? h('p', { class: 'tv-rule' }, `${c.ruleLabel} `, h('code', { class: 'tv-ruleid', translate: 'no' }, d.rule_id),
      d.rule_line ? h('span', { class: 'tv-sep', 'aria-hidden': 'true' }, ' · ') : null, d.rule_line ? lineButton(d.rule_line, o) : null,
      d.verify ? h('span', { class: 't-2' }, ` · ${fill(c.verify, { level: d.verify })}`) : null)
    : d.source === 'default' ? h('p', { class: 'tv-rule t-2' }, c.defaultRule) : null;
  const steps = d.trace ?? [];
  return h('section', { class: 'card card-pad tv', 'aria-label': c.resultLabel },
    h('div', { class: 'tv-head' },
      decisionChip(d.decision),
      typeof d.evaluated_in_us === 'number' ? h('span', { class: 'tv-time t-footnote t-2 num' }, fill(c.evaluatedIn, { us: d.evaluated_in_us.toLocaleString('en-US') })) : null),
    d.message ? h('p', { class: 'tv-msg' }, d.message) : null,
    rule,
    steps.length ? h('ol', { class: 'tv-trace mono' }, ...steps.map((s) => stepRow(s, d, o))) : null,
    d.errors?.length ? h('div', { class: 'tv-errors t-footnote' }, h('p', null, c.traceErrors), h('ul', null, ...d.errors.map((e) => h('li', null, e.message)))) : null,
    facts ? h('p', { class: 'tv-facts mono t-2' }, facts) : null);
}

// ---------- policy test results ----------

export interface TestOutcome {
  decision?: string;
  rule?: string | null;
  message?: string;
  verify?: string;
  [k: string]: unknown;
}

export interface TestCase {
  name: string;
  line?: number;
  pass: boolean;
  expected?: TestOutcome;
  got?: TestOutcome;
  problems?: string[];
}

export interface PolicyTestResult {
  object: 'policy_test_result';
  ok: boolean;
  passed: number;
  failed: number;
  skipped?: number;
  cases: TestCase[];
  coverage?: { uncovered?: string[]; covered?: number; total?: number } | null;
}

const num = (x: number) => x.toLocaleString('en-US');

/** testSummary: `24 cases passed · every rule covered` or `1 failed · allow-describe has no case` (S12). */
export function testSummary(r: PolicyTestResult): { ok: boolean; text: string } {
  const uncovered = r.coverage?.uncovered ?? [];
  const parts: string[] = [];
  if (r.failed > 0) parts.push(fill(c.failed, { n: num(r.failed) }));
  else if (uncovered.length === 0) {
    return { ok: true, text: r.passed === 1 ? c.allPassedOne : fill(c.allPassed, { n: num(r.passed) }) };
  } else parts.push(r.passed === 1 ? c.casePassedOne : fill(c.casesPassed, { n: num(r.passed) }));
  if (uncovered.length === 1) parts.push(fill(c.noCase, { rule: uncovered[0]! }));
  else if (uncovered.length > 1) parts.push(fill(c.noCases, { rule: uncovered[0]!, n: num(uncovered.length - 1) }));
  if (r.skipped) parts.push(fill(c.skipped, { n: num(r.skipped) }));
  return { ok: false, text: parts.join(' · ') };
}

/** outcomeText: `deny · block-destructive-sql`. */
export function outcomeText(x: TestOutcome | undefined): string {
  if (!x) return '';
  return [x.decision, x.rule, x.verify ? fill(c.verify, { level: x.verify }) : null].filter(Boolean).join(' · ');
}

/** testResultView: the summary line and the cases table (failed cases first). */
export function testResultView(r: PolicyTestResult): HTMLElement {
  const sum = testSummary(r);
  const cases = [...r.cases].sort((a, b) => Number(a.pass) - Number(b.pass));
  const head = h('tr', null, ...[c.cols.case, c.cols.expected, c.cols.got, c.cols.result, c.cols.detail].map((t) => h('th', { scope: 'col' }, t)));
  const rows = cases.map((k) => h('tr', { class: k.pass ? 'pt-pass' : 'pt-fail' },
    h('td', { class: 'pt-case' }, h('span', { class: 'pt-name' }, k.name), k.line ? h('span', { class: 'pt-line t-2 mono' }, fill(c.caseLine, { line: k.line })) : null),
    h('td', { class: 'mono' }, outcomeText(k.expected)),
    h('td', { class: 'mono' }, outcomeText(k.got)),
    h('td', { class: 'pt-res' }, h('span', { class: `tv-glyph ${k.pass ? 'tv-ok' : 'tv-deny'}`, 'aria-hidden': 'true' }, k.pass ? '✓' : '✕'), h('span', { class: 'sr-only' }, k.pass ? c.passedWord : c.failedWord)),
    h('td', { class: 'pt-detail' }, ...(k.problems ?? []).map((p) => h('span', { class: 'pt-problem' }, p)))));
  return h('section', { class: 'pt', 'aria-label': c.testsLabel },
    h('p', { class: `pt-sum ${sum.ok ? 'tv-ok' : 'tv-deny'}`, role: 'status' }, h('span', { class: 'tv-glyph', 'aria-hidden': 'true' }, sum.ok ? '✓' : '✕'), sum.text),
    h('div', { class: 'pt-frame', role: 'region', tabindex: '0', 'aria-label': c.testsLabel },
      h('table', { class: 'dt pt-table' }, h('thead', null, head), h('tbody', null, ...rows))));
}
