// models/policy.ts: the Console's view of S12's policy objects (S26 §3.2): policy, policy_version,
// policy_active_set, policy_lint_result, policy_decision and policy_test_result, as shipped
// (test/fixtures/s26/policy*.json, lint-*.json, evaluate-*.json). The API never changes here.
//
// Adapters (recorded in the S26 report):
// - S10's diagnostics carry `hint` but no `fix`; lib/policyfix.ts derives a quick fix from a
//   `Did you mean "x"?` hint (S26's `fix.replace` shape is the Console's, not the API's).
// - The policy object has `status` (enforced | shadow | inactive), `active_version`,
//   `shadow_version` and `latest_version`; there is no rules count on the policy, so the list reads
//   it from the active (or latest) version.
// - The evaluate body is {policy: "active" | {id, version} | {source}, input, explain}; the dry-run
//   input is S12's case-input shape ({tool: {name, connector_type}, sql | args, agent, …}).

import { fill } from '../copy.ts';
import { policiesCopy as c } from '../copy-policies.ts';

export type PolicyStatus = 'enforced' | 'shadow' | 'inactive';

export interface Policy {
  object: 'policy';
  id: string;
  name: string;
  description: string | null;
  status: PolicyStatus | string;
  active_version: number | null;
  shadow_version: number | null;
  latest_version: number;
  created_at: string;
  updated_at: string;
  version?: PolicyVersion; // on POST /v1/policies
}

export interface PolicyVersion {
  object: 'policy_version';
  id: string;
  policy: string;
  version: number;
  name: string;
  status: PolicyStatus | string;
  version_hash: string;
  source_hash: string;
  lang: string;
  origin: string;
  author: { object: string; id: string | null; name?: string; email?: string } | null;
  notes: string | null;
  rules: number;
  budgets: number;
  warnings: string[];
  source?: string;
  created_at: string;
}

export interface ActiveRef { policy: string; name: string; version: number; version_id: string; version_hash: string; activation?: string; activated_at?: string }
export interface ActiveSet { object: 'policy_active_set'; enforce: ActiveRef | null; shadow: ActiveRef | null }

export interface Diagnostic {
  severity: 'error' | 'warning';
  code: string;
  message: string;
  hint?: string;
  line: number;
  column: number;
  end_line?: number;
  end_column?: number;
  rule_id?: string;
  path?: string;
  doc_url: string;
}

export interface LintResult { object: 'policy_lint_result'; valid: boolean; name: string; version_hash: string | null; rules: number; budgets: number; errors: Diagnostic[]; warnings: Diagnostic[] }

export interface TraceStep { step: string; id?: string; line?: number; result: string; detail?: string; cost?: number }
export interface PolicyDecision {
  object: 'policy_decision';
  decision: 'allow' | 'deny' | 'require_approval';
  code: string | null;
  source: string;
  rule_id: string | null;
  rule_line: number | null;
  reason: string | null;
  message: string;
  policy: { id: string | null; name: string; version: number | null; version_hash: string; mode: string };
  shadow: PolicyDecision | null;
  facts: { sql?: Record<string, unknown>; [k: string]: unknown };
  trace: TraceStep[];
  errors: string[];
  evaluated_in_us: number;
}

export interface TestCase { name: string; line: number; pass: boolean; expected: Record<string, unknown>; got: Record<string, unknown>; problems?: string[] }
export interface TestResult {
  object: 'policy_test_result';
  policy: string;
  ok: boolean;
  passed: number;
  failed: number;
  skipped: number;
  cases: TestCase[];
  coverage: { covered: number; total: number; uncovered: string[]; rules: { id: string; line: number; cases: number }[] };
}

export function statusOf(p: Pick<Policy, 'status'>): PolicyStatus {
  return p.status === 'enforced' || p.status === 'shadow' ? p.status : 'inactive';
}

export function statusLabel(s: PolicyStatus): string {
  return c.status[s];
}

/** shortHash: sha256:3f9a…c21e (brand §12). */
export function shortVersionHash(h: string | null | undefined): string {
  if (!h) return '·';
  const hex = h.replace(/^sha256:/, '');
  return `sha256:${hex.slice(0, 4)}…${hex.slice(-4)}`;
}

/** headerLine: `v4 · sha256:3f9a…c21e`. */
export function headerLine(v: Pick<PolicyVersion, 'version' | 'version_hash'>): string {
  return `v${v.version} · ${shortVersionHash(v.version_hash)}`;
}

const people = new Map<string, string>();

/** setPeople: user id → display name, from GET /v1/org/members, so authors read as people. */
export function setPeople(list: { user: { id: string; name: string; email: string } }[]): void {
  for (const m of list) people.set(m.user.id, m.user.name || m.user.email);
}

export function authorName(v: Pick<PolicyVersion, 'author'>): string {
  const a = v.author;
  if (!a || !a.id || a.object === 'system') return c.authorSystem;
  if (a.name) return a.name;
  const known = people.get(a.id);
  if (known) return known;
  if (a.email) return a.email;
  const short = `${a.id.slice(0, 8)}…${a.id.slice(-4)}`;
  if (a.object === 'api_key') return fill(c.authorKey, { id: short });
  return short;
}

export const MAX_SOURCE_BYTES = 256 * 1024;

export function sourceBytes(s: string): number {
  return new TextEncoder().encode(s).length;
}

/** dirty: the buffer differs from the version it was opened from (CRLF counts as LF). */
export function dirty(buffer: string, base: string): boolean {
  return buffer.replace(/\r\n/g, '\n') !== base.replace(/\r\n/g, '\n');
}

/** hasDefaultAllow: the version's warnings include default_allow (the enforce sheet asks to acknowledge). */
export function hasDefaultAllow(v: Pick<PolicyVersion, 'warnings'> | null | undefined, source?: string): boolean {
  if (v?.warnings?.some((w) => /default_allow/.test(w))) return true;
  if (!source) return false;
  // Only the top-level defaults block: its indented lines up to the next top-level key.
  const lines = source.split(/\r?\n/);
  const i = lines.findIndex((l) => /^defaults:\s*(#.*)?$/.test(l));
  if (i < 0) return /^defaults:\s*\{[^}]*\bdecision:\s*allow\b/m.test(source);
  for (const l of lines.slice(i + 1)) {
    if (/^\S/.test(l)) break;
    if (/^\s+decision:\s*allow\s*(#.*)?$/.test(l)) return true;
  }
  return false;
}

/** The dry-run input from the form (S12 case-input shape). */
export interface DryRunForm { tool: string; argsText: string; sql: string; agent: string; onBehalfOf: string; at: string }

export function dryRunInput(f: DryRunForm, connectorType?: string): { input: Record<string, unknown> } | { error: string; line?: number } {
  const input: Record<string, unknown> = { tool: connectorType ? { name: f.tool.trim(), connector_type: connectorType } : f.tool.trim() };
  if (!f.tool.trim()) return { error: c.dryRun.toolRequired };
  if (f.sql.trim()) input.sql = f.sql;
  if (f.argsText.trim()) {
    const parsed = strictJSON(f.argsText);
    if ('error' in parsed) return parsed;
    if (typeof parsed.value !== 'object' || parsed.value === null || Array.isArray(parsed.value)) return { error: c.dryRun.argsObject };
    input.args = parsed.value;
  }
  if (f.agent.trim()) input.agent = { name: f.agent.trim() };
  if (f.onBehalfOf.trim()) input.on_behalf_of = f.onBehalfOf.trim();
  if (f.at.trim()) input.time = new Date(f.at).toISOString();
  return { input };
}

/** strictJSON parses JSON and reports the line of a syntax error (and refuses duplicate keys). */
export function strictJSON(text: string): { value: unknown } | { error: string; line?: number } {
  try {
    const value = JSON.parse(text);
    const dup = duplicateKey(text);
    if (dup) return { error: fill(c.dryRun.duplicateKey, { key: dup }) };
    return { value };
  } catch (e) {
    const m = /position (\d+)/.exec(String(e));
    const pos = m ? Number(m[1]) : null;
    const line = pos === null ? undefined : text.slice(0, pos).split('\n').length;
    return { error: line ? fill(c.dryRun.jsonAt, { line }) : c.dryRun.json, line };
  }
}

function duplicateKey(text: string): string | null {
  // A small scanner: tracks object key sets by depth; strings are skipped exactly.
  const stack: Set<string>[] = [];
  let i = 0;
  let expectKey = false;
  while (i < text.length) {
    const ch = text[i]!;
    if (ch === '{') { stack.push(new Set()); expectKey = true; i++; continue; }
    if (ch === '}') { stack.pop(); expectKey = false; i++; continue; }
    if (ch === '[' || ch === ']') { expectKey = false; i++; continue; }
    if (ch === ',') { expectKey = text.lastIndexOf('{', i) > text.lastIndexOf('[', i) && stack.length > 0; i++; continue; }
    if (ch === '"') {
      let j = i + 1;
      let s = '';
      while (j < text.length && text[j] !== '"') {
        if (text[j] === '\\') { s += text[j]! + (text[j + 1] ?? ''); j += 2; continue; }
        s += text[j]!;
        j++;
      }
      const after = text.slice(j + 1).trimStart();
      if (expectKey && after.startsWith(':') && stack.length) {
        const set = stack[stack.length - 1]!;
        if (set.has(s)) return s;
        set.add(s);
      }
      expectKey = false;
      i = j + 1;
      continue;
    }
    i++;
  }
  return null;
}

/** testSummary: S12's copy: `24 cases passed · every rule covered` / `1 failed · allow-describe has no case`. */
export function testSummary(r: Pick<TestResult, 'passed' | 'failed' | 'coverage'>): string {
  const head = r.failed ? fill(r.failed === 1 ? c.tests.failedOne : c.tests.failed, { n: r.failed }) : fill(r.passed === 1 ? c.tests.passedOne : c.tests.passed, { n: r.passed });
  const unc = r.coverage?.uncovered ?? [];
  const cov = unc.length === 0 ? c.tests.allCovered : unc.length === 1 ? fill(c.tests.noCase, { rule: unc[0]! }) : fill(c.tests.noCases, { n: unc.length });
  return `${head} · ${cov}`;
}

/** shadowEvidence: `Shadow disagreed on 3 of 212 actions in the last 24 h`. */
export function shadowEvidence(disagreed: number, total: number, more: boolean): string {
  return fill(c.shadowEvidence, { n: more ? `${disagreed}+` : String(disagreed), total: String(total) });
}

export function enforceTitle(name: string, version: number): string {
  return fill(c.enforce.title, { name, v: version });
}
