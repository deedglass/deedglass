// The kit's S26 section. Dev builds only, like the rest of /app/_kit.
// The PolicyEditor with a live (local) lint loop and the typo's quick fix, a read-only editor,
// DiagnosticsList in every state, the lint-loop messages, TraceView for a deny and an allow, the
// test results (passing and failing), and LineDiff between two versions. Samples follow the
// acme-demo policy and the real S10/S12 shapes in test/fixtures/s26.
import { editorCopy as c } from '../lib/copy-editor.ts';
import { h, maybe } from '../lib/dom.ts';
import { offsetToPos } from '../lib/editormath.ts';
import { diffLines } from '../lib/linediff.ts';
import type { PolicyDiagnostic, PolicyLintResult } from '../lib/policyfix.ts';
import { diagnosticsList } from './diagnostics.ts';
import { lineDiffView } from './linediffview.ts';
import { createPolicyEditor, mountLintedEditor } from './policyeditor.ts';
import { testResultView, traceView, type PolicyDecision, type PolicyTestResult } from './traceview.ts';

const SAMPLE = `# acme-demo · reads run, writes wait for a person, destructive SQL never runs.
deedglass: policy/v1
name: acme-demo
description: Acme Store demo. Reads run, writes wait for a person.

defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.

budgets:
  - id: agent-rate
    per: agent
    window: minute
    max_calls: 30

rules:
  - id: block-destructive-sql
    match: { tool: [orders.execute_sql, postgres.query, postgres.execute] }
    when: sql.kind == "drop"
    desicion: deny
    reason: DROP is never allowed for agents.
  - id: block-unscoped-writes
    match: { tool: [orders.execute_sql, postgres.query, postgres.execute] }
    when: sql.unbounded_write
    decision: deny
    reason: UPDATE and DELETE need a WHERE clause.
  - id: allow-order-reads
    match: { tool: [orders.execute_sql, postgres.query] }
    when: sql.category == "read" && !sql.writes
    decision: allow
    verify: L0
`;

/** The typo diagnostic, positioned on the sample the way S10 reports it (runes, end exclusive). */
function typo(source: string): PolicyDiagnostic | null {
  const at = source.indexOf('desicion:');
  if (at === -1) return null;
  const p = offsetToPos(source, at);
  return {
    severity: 'error', code: 'unknown_key',
    message: 'Unknown key "desicion" in rule "block-destructive-sql".', hint: 'Did you mean "decision"?',
    line: p.line, column: p.column, end_line: p.line, end_column: p.column + 8,
    rule_id: 'block-destructive-sql', path: 'rules[0].desicion', doc_url: 'https://docs.deedglass.com/policies/errors/unknown_key',
  };
}

const lintResult = (source: string): PolicyLintResult => {
  const d = typo(source);
  return { object: 'policy_lint_result', valid: !d, name: 'acme-demo', rules: 3, budgets: 1, errors: d ? [d] : [], warnings: [] };
};

const warning: PolicyDiagnostic = {
  severity: 'warning', code: 'unknown_connector_type', message: '`postgress` is not a known connector type.', hint: 'Did you mean `postgres`?',
  line: 58, column: 46, end_line: 58, end_column: 55, rule_id: 'undo-needs-approval', doc_url: 'https://docs.deedglass.com/policies/errors/unknown_connector_type',
};

const trace = (last: { id: string; line: number; result: string; detail: string }, more: number) => [
  { step: 'stop', result: 'pass', detail: 'agent active · org running' },
  { step: 'budget', id: 'agent-rate', result: 'pass', detail: '0 of 30 calls this minute' },
  { step: 'rule', id: 'block-destructive-sql', line: 37, result: 'no_match', detail: 'when → false', cost: 3 },
  { step: 'rule', id: 'block-schema-changes', line: 42, result: 'no_match', detail: 'when → false', cost: 6 },
  { step: 'rule', ...last, cost: 2 },
  { step: 'rule', result: 'not_evaluated', detail: `${more} more rules not evaluated` },
];

const deny: PolicyDecision = {
  object: 'policy_decision', decision: 'deny', code: 'policy_denied', source: 'rule', rule_id: 'block-unscoped-writes', rule_line: 47,
  reason: 'UPDATE and DELETE need a WHERE clause.', message: 'Blocked by policy "block-unscoped-writes": UPDATE and DELETE need a WHERE clause.',
  trace: trace({ id: 'block-unscoped-writes', line: 47, result: 'match', detail: 'when → true' }, 13),
  facts: { sql: { present: true, kind: 'delete', category: 'write', unbounded_write: true, multi: false, tables: ['public.orders'] } },
  evaluated_in_us: 48,
};
const allow: PolicyDecision = {
  object: 'policy_decision', decision: 'allow', source: 'rule', rule_id: 'allow-order-status-updates', rule_line: 64, message: '', verify: 'L2',
  trace: trace({ id: 'allow-order-status-updates', line: 64, result: 'match', detail: 'when → true' }, 10),
  facts: { sql: { present: true, kind: 'update', category: 'write', unbounded_write: false, multi: false, tables: ['public.orders'] } },
  evaluated_in_us: 52,
};
const approval: PolicyDecision = {
  object: 'policy_decision', decision: 'require_approval', source: 'rule', rule_id: 'orders-writes-need-approval', rule_line: 76,
  message: 'Writes to the orders database need a person to countersign.',
  trace: trace({ id: 'orders-writes-need-approval', line: 76, result: 'match', detail: 'when → true' }, 8),
  evaluated_in_us: 61,
};

const passing: PolicyTestResult = {
  object: 'policy_test_result', ok: true, passed: 3, failed: 0, skipped: 0,
  coverage: { uncovered: [], covered: 3, total: 3 },
  cases: [
    { name: "the native connector's read runs", line: 9, pass: true, expected: { decision: 'allow', rule: 'allow-order-reads' }, got: { decision: 'allow', rule: 'allow-order-reads', verify: 'L0' } },
    { name: 'a status update of every order never runs', line: 18, pass: true, expected: { decision: 'deny', rule: 'block-unscoped-writes' }, got: { decision: 'deny', rule: 'block-unscoped-writes' } },
    { name: 'a DROP gets the brand sentence', line: 24, pass: true, expected: { decision: 'deny', rule: 'block-destructive-sql' }, got: { decision: 'deny', rule: 'block-destructive-sql' } },
  ],
};
const failing: PolicyTestResult = {
  object: 'policy_test_result', ok: false, passed: 2, failed: 1, skipped: 0,
  coverage: { uncovered: ['allow-describe'], covered: 2, total: 3 },
  cases: [
    { name: "the native connector's read runs", line: 9, pass: true, expected: { decision: 'allow', rule: 'allow-order-reads' }, got: { decision: 'allow', rule: 'allow-order-reads' } },
    {
      name: 'a DROP hidden behind a comment never runs', line: 67, pass: false,
      expected: { decision: 'deny', rule: 'block-destructive-sql' }, got: { decision: 'allow', rule: 'block-destructive-sql', verify: 'L0' },
      problems: ['decision: expected deny, got allow'],
    },
    { name: 'a status update of every order never runs', line: 18, pass: true, expected: { decision: 'deny', rule: 'block-unscoped-writes' }, got: { decision: 'deny', rule: 'block-unscoped-writes' } },
  ],
};

const V3 = SAMPLE.replace('desicion', 'decision');
const V4 = V3
  .replace('    max_calls: 30\n', '    max_calls: 60\n')
  .replace('    reason: DROP is never allowed for agents.\n', '    reason: DROP is never allowed for agents.\n  - id: block-schema-changes\n    match: { tool: [orders.execute_sql, postgres.execute] }\n    when: sql.category in ["ddl", "privilege"] || sql.multi\n    decision: deny\n')
  .replace('    verify: L0\n', '');

function block(title: string, ...body: Node[]): HTMLElement {
  return h('div', { class: 'grid gap-3 min-w-0' }, h('h3', { class: 't-headline' }, title), ...body);
}

export function mountKitEditor(): void {
  const host = maybe('kit-s26-editor');
  if (!host) return;

  // The editor with its lint loop, linted locally: Apply fix makes it clean.
  const live = h('div', { class: 'min-w-0' });
  mountLintedEditor(live, {
    value: SAMPLE, name: 'acme-demo.yaml', label: c.label,
    lint: async (source) => {
      await new Promise((r) => setTimeout(r, 120));
      return lintResult(source);
    },
    onSave: () => {},
  });

  // Read-only: same look, no fixes.
  const ro = h('div', { class: 'min-w-0' });
  mountLintedEditor(ro, { value: V3, name: 'acme-demo.yaml · v3', readOnly: true, lintOnMount: false });

  // A bare editor with a warning, revealed and highlighted.
  const bare = h('div', { class: 'min-w-0' });
  const ed = createPolicyEditor();
  ed.mount(bare, { value: V4, name: 'acme-demo.yaml · draft', label: c.label });
  const d = typo(SAMPLE)!;
  ed.setDiagnostics([{ ...warning, line: 12, column: 5, end_line: 12, end_column: 14 }]);
  ed.revealLine(12, true, { focus: false });

  const reveal = (line: number) => ed.revealLine(line, true);
  const states = h('div', { class: 'grid gap-4 min-w-0' },
    diagnosticsList([d, warning], { onReveal: reveal, onFix: () => {} }),
    diagnosticsList([], { counts: { rules: 12, budgets: 2 } }),
    diagnosticsList([], { counts: { rules: 1, budgets: 1 } }),
    h('p', { class: 'pe-status t-footnote' }, c.lintPaused),
    h('p', { class: 'pe-status t-footnote' }, c.tooLarge));

  host.append(
    block('PolicyEditor with the lint loop', live),
    block('PolicyEditor, read only', ro),
    block('PolicyEditor, a revealed line and a warning', bare),
    block('DiagnosticsList and lint messages', states),
    block('TraceView', h('div', { class: 'grid gap-4 min-w-0' }, traceView(deny, { onReveal: reveal }), traceView(allow, { onReveal: reveal }), traceView(approval))),
    block('Test results', testResultView(passing), testResultView(failing)),
    block('LineDiff', lineDiffView(diffLines(V3, V4), { from: 'v3', to: 'v4' }), lineDiffView(diffLines(V3, V3.replace(/\n/g, '\r\n')), { from: 'v3', to: 'v3 (CRLF)' })),
  );
}
