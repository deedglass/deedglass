// policyfix.ts: policy lint shapes (S10's policy_lint_result) and the quick fix the Console offers.
//
// ADAPTER (S26): S26 §3.2 describes diagnostics with an optional `fix: {replace}`, but S10's
// diagnostics as shipped have no `fix` field. The Console derives the fix client side instead:
// when `hint` is exactly `Did you mean "X"?` and the diagnostic carries a full range (line, column,
// end_line, end_column, columns in runes, end exclusive), the fix replaces that range with X.
// On the server that exact hint comes from internal/policy/suggest.go's suggest() through:
//   - validate.go unknownKey → code `unknown_key`; the range is the key itself (keySpan), so the
//     replacement is the corrected key (lint-typo.json: 40:5..13, "desicion" → "decision").
//   - expr.go → code `cel_no_field`; the range is the field name after the dot in a `when`.
// Hints that carry more text (`Did you mean "X"? Available: …`, the backtick forms for decisions,
// values, side effects and connector types) aren't offered as fixes: their ranges can include
// quotes or a whole scalar, so a blind replace could be wrong. caseinput.go and testrunner.go use
// the same wording for cases files, which the editor doesn't lint. A future server `fix.replace`
// wins over the derived one.

import type { RangeFix } from './editormath.ts';

export type Severity = 'error' | 'warning';

export interface PolicyDiagnostic {
  severity: Severity;
  code: string;
  message: string;
  hint?: string | null;
  line?: number | null;
  column?: number | null;
  end_line?: number | null;
  end_column?: number | null;
  rule_id?: string | null;
  path?: string | null;
  doc_url?: string | null;
  fix?: { replace: string } | null;
}

export interface PolicyLintResult {
  object: 'policy_lint_result';
  file?: string;
  valid: boolean;
  name?: string | null;
  version_hash?: string | null;
  rules: number;
  budgets: number;
  errors: PolicyDiagnostic[];
  warnings: PolicyDiagnostic[];
}

const DID_YOU_MEAN = /^Did you mean "([^"\\]+)"\?$/;

function isPos(n: unknown): n is number {
  return typeof n === 'number' && Number.isInteger(n) && n >= 1;
}

/** quickFix: the range fix for a diagnostic, or null when there is none to offer. */
export function quickFix(d: PolicyDiagnostic): RangeFix | null {
  if (!isPos(d.line) || !isPos(d.column) || !isPos(d.end_line) || !isPos(d.end_column)) return null;
  if (d.end_line < d.line || (d.end_line === d.line && d.end_column < d.column)) return null;
  let replace: string | null = null;
  if (d.fix && typeof d.fix.replace === 'string') replace = d.fix.replace;
  else {
    const m = DID_YOU_MEAN.exec(d.hint ?? '');
    if (m) replace = m[1]!;
  }
  if (replace === null) return null;
  return { line: d.line, column: d.column, end_line: d.end_line, end_column: d.end_column, replace };
}

/** allDiagnostics: errors first, then warnings, each in line order. */
export function allDiagnostics(r: Pick<PolicyLintResult, 'errors' | 'warnings'>): PolicyDiagnostic[] {
  const byLine = (a: PolicyDiagnostic, b: PolicyDiagnostic) => (a.line ?? 0) - (b.line ?? 0) || (a.column ?? 0) - (b.column ?? 0);
  return [...(r.errors ?? [])].sort(byLine).concat([...(r.warnings ?? [])].sort(byLine));
}
