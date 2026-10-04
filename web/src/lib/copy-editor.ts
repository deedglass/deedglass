// Copy for the policy editor, diagnostics, trace and line diff (S26). Part of the copy catalog: test/checks.test.ts walks it with copy.ts, so the voice and
// banned-word tests in test/checks.test.ts cover every string here.

export const editorCopy = {
  // PolicyEditor
  label: 'Policy source',
  escHint: 'Esc, then Tab, leaves the editor.',
  readOnly: 'Read only',
  readOnlyNote: 'Viewers can read policies. Editing needs the Admin role.',
  caret: 'Line {line}, col {col}',

  // Lint loop (S26 §3.2)
  checking: 'Checking…',
  lintPaused: 'Lint paused for 20 seconds (rate limit).',
  tooLarge: 'Policies are limited to 256 KiB. Split this one.',

  // DiagnosticsList
  clean: 'No problems · {rules} · {budgets}',
  ruleOne: '1 rule',
  rules: '{n} rules',
  budgetOne: '1 budget',
  budgets: '{n} budgets',
  errorOne: '1 error',
  errors: '{n} errors',
  warningOne: '1 warning',
  warnings: '{n} warnings',
  severity: { error: 'Error', warning: 'Warning' } as Record<string, string>,
  at: 'Line {line}, col {col}',
  atLine: 'Line {line}',
  applyFix: 'Apply fix',
  applyFixLabel: 'Apply fix on line {line}: use "{replace}"',
  docs: 'Docs',
  docsLabel: 'Docs for {code}, opens in a new tab',
  reveal: 'Show line {line} in the editor',
  more: 'And {n} more. Fix the ones above, then check again.',

  // TraceView (dry run)
  resultLabel: 'Dry run result',
  ruleLabel: 'Rule',
  line: 'line {line}',
  evaluatedIn: 'evaluated in {us} µs',
  defaultRule: 'No rule matched, so the default decision applies.',
  verify: 'verify {level}',
  steps: { stop: 'stop', budget: 'budget', rule: 'rule' } as Record<string, string>,
  results: {
    pass: 'passed',
    fail: 'failed',
    match: 'matched',
    no_match: 'no match',
    not_evaluated: 'not evaluated',
    skipped: 'skipped',
  } as Record<string, string>,
  sqlFacts: 'sql: {facts}',
  sqlUnbounded: 'unbounded',
  sqlMulti: 'multi-statement',
  traceErrors: 'The policy reported problems while deciding:',

  // Test results (S12 copy)
  allPassed: '{n} cases passed · every rule covered',
  allPassedOne: '1 case passed · every rule covered',
  casesPassed: '{n} cases passed',
  casePassedOne: '1 case passed',
  failed: '{n} failed',
  skipped: '{n} skipped',
  noCase: '{rule} has no case',
  noCases: '{rule} and {n} more rules have no case',
  testsLabel: 'Test results',
  cols: { case: 'Case', expected: 'Expected', got: 'Got', result: 'Result', detail: 'Detail' },
  passedWord: 'Passed',
  failedWord: 'Failed',
  caseLine: 'line {line}',

  // LineDiff
  diffLabel: 'Changes from {from} to {to}',
  diffLabelPlain: 'Changes between two versions',
  hunk: '@@ lines {from} to {to} @@',
  hunkOne: '@@ line {line} @@',
  added: 'added',
  removed: 'removed',
  diffSummary: '{added} added · {removed} removed',
  diffSame: 'No changes between these versions.',
  diffCrlf: 'One version has CRLF line endings. Lines are compared as LF.',
  diffCols: { old: 'Old line', new: 'New line', change: 'Change', text: 'Text' },
} as const;
