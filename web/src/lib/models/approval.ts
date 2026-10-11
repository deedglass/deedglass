// models/approval.ts: the Console's view of S22's approval object (S26 §3.1). The names are S22's,
// as shipped (captured in test/fixtures/s26/approval.json); this file adapts, the API never changes.
//
// Adapters (recorded in the S26 report):
// - S22 puts the policy name and version in context.policy; there is no top-level `policy`.
// - The self rule: S22 answers `you.can_decide: true` with a `why_not` sentence when the action
//   runs on your behalf. The person can still deny, so the Console shows the sentence and Deny only.
// - The SQL estimate is context.sql.estimate {rows, method, took_ms}; the statement is context.args.sql.
// - The effect line is built from server-computed context (sections, sql, args), never from the
//   agent's free text (S26 §7).

import { fill } from '../copy.ts';
import { approvalsCopy as c } from '../copy-approvals.ts';
import { number, plural } from '../format.ts';

export type ApprovalStatus = 'pending' | 'approved' | 'denied' | 'expired' | 'cancelled';

export interface ApprovalPerson { id: string; name: string }
export interface ApprovalVote { user: ApprovalPerson; at: string; via: string; reason: string | null; deed: string }
export interface RecentAction { action: string; tool: string; status: string; level: string | null; verdict: string | null; at: string }

export interface ApprovalSQL {
  kind: string;
  tables: string[];
  writes: boolean;
  has_where: boolean;
  estimate?: { rows: number | null; method: string; took_ms?: number } | null;
}

export interface ApprovalContext {
  v: number;
  action: { id: string; tool: string; connector: string | null; connector_type: string | null; side_effect: string | null; mode: string; request_digest: string };
  agent: { id: string; name: string; labels?: Record<string, string>; status?: string; recent?: RecentAction[] };
  on_behalf_of: string | null;
  session?: string | null;
  policy: { id?: string; name: string; version: number; version_id?: string; rule_id: string; reason?: string | null; required: number; approvers?: string[]; verify?: string };
  args: Record<string, unknown> | null;
  args_note: string | null;
  sql: ApprovalSQL | null;
  sections: Record<string, Record<string, unknown>>;
  summary?: string;
  requested_at?: string;
  expires_at?: string;
}

export interface ApprovalFull {
  object: 'approval';
  id: string;
  action: string;
  agent: { id: string; name: string };
  tool: string;
  status: ApprovalStatus;
  rule_id: string | null;
  reason?: string | null;
  required: number;
  approvers?: string[];
  countersigned: ApprovalVote[] | number;
  denied_by?: ApprovalPerson | null;
  decision_reason: string | null;
  summary: string;
  context?: ApprovalContext;
  context_digest?: string;
  you?: { eligible: boolean; can_decide: boolean; why_not: string | null };
  created_at: string;
  expires_at: string;
  decided_at: string | null;
  deed: string | null;
  livemode?: boolean;
}

const str = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');

/** votes returns the countersigns as a list (an agent key sees a count; the Console never does). */
export function votes(a: Pick<ApprovalFull, 'countersigned'>): ApprovalVote[] {
  return Array.isArray(a.countersigned) ? a.countersigned : [];
}

export function voteCount(a: Pick<ApprovalFull, 'countersigned'>): number {
  return Array.isArray(a.countersigned) ? a.countersigned.length : a.countersigned;
}

/** sortPending orders the inbox by expiry, soonest first; ties by creation. */
export function sortPending<T extends Pick<ApprovalFull, 'expires_at' | 'created_at' | 'id'>>(list: T[]): T[] {
  return [...list].sort((x, y) => x.expires_at.localeCompare(y.expires_at) || x.created_at.localeCompare(y.created_at) || x.id.localeCompare(y.id));
}

function rowsWord(n: number | null | undefined): string {
  if (n === null || n === undefined) return c.effect.rowsUnknown;
  return `~${plural(n, 'row')}`;
}

/**
 * effectLine is the one-line effect per connector type (S26 §3.1): Stripe `$120.00 to cus_Q3…`;
 * Postgres `UPDATE ~1 row in public.orders`; GitHub `merge PR #212 in acme/api`; otherwise the
 * side effect and the tool.
 */
export function effectLine(a: Pick<ApprovalFull, 'tool' | 'context'>): string {
  const ctx = a.context;
  const type = ctx?.action.connector_type ?? a.tool.split('.')[0] ?? '';
  const email = emailOf(a);
  if (email) return fill(c.effect.email, { to: email.review?.company || email.to, subject: email.subject });
  const stripe = ctx?.sections?.stripe;
  if (stripe && (stripe.amount_display || stripe.customer)) {
    const amt = str(stripe.amount_display);
    const to = str(stripe.customer);
    return amt && to ? fill(c.effect.money, { amount: amt, to }) : amt || to;
  }
  if (ctx?.sql) {
    const kind = (ctx.sql.kind || 'statement').toUpperCase();
    const tables = ctx.sql.tables?.length ? ctx.sql.tables.join(', ') : c.effect.tablesUnknown;
    return fill(c.effect.sql, { kind, rows: rowsWord(ctx.sql.estimate?.rows ?? null), tables });
  }
  const args = ctx?.args ?? {};
  if (type === 'github') {
    const pr = str(args.pr ?? args.pull_number ?? args.number);
    const repo = str(args.repo ?? args.repository);
    if (pr && repo) return fill(c.effect.github, { verb: a.tool.split('.').slice(1).join('.').replace(/_pr$/, '').replace(/_/g, ' ') || 'change', pr, repo });
  }
  const side = ctx?.action.side_effect;
  return side ? fill(c.effect.generic, { side, tool: a.tool }) : a.tool;
}

/** EmailPreview is S40's email_preview section, as the server computed it. */
export interface EmailPreview {
  from: string; to: string; subject: string; body_text: string; words: number; links: number; questions: number;
  step: number; threaded: boolean;
  window?: { local: string; ist: string };
  review?: { company?: string; signal_summary?: string; signal_url?: string; signal_date?: string; lint_summary?: string; judge_summary?: string; cta?: string };
}

/** emailOf returns the email_preview section (outbound mail and Gmail drafts), or null. */
export function emailOf(a: Pick<ApprovalFull, 'context'>): EmailPreview | null {
  const e = a.context?.sections?.email_preview as unknown as EmailPreview | undefined;
  return e && typeof e === 'object' && typeof e.subject === 'string' ? e : null;
}

/** emailChips is the one line of checks under the body. */
export function emailChips(e: EmailPreview): string {
  const links = e.links === 1 ? '1 link' : `${number(e.links || 0)} links`;
  const qs = e.questions === 1 ? '1 question' : `${number(e.questions || 0)} questions`;
  return fill(c.detail.emailChips, { words: number(e.words || 0), links, questions: qs, lint: e.review?.lint_summary || '·', judge: e.review?.judge_summary || '·' });
}

/** sqlOf returns the statement to show (context.args.sql) and the estimate line, for Postgres. */
export function sqlOf(a: Pick<ApprovalFull, 'context'>): { statement: string; estimate: string | null; kind: string; tables: string[]; hasWhere: boolean } | null {
  const sql = a.context?.sql;
  if (!sql) return null;
  const statement = str(a.context?.args?.sql);
  const rows = sql.estimate?.rows;
  const estimate = rows === null || rows === undefined ? null : fill(c.detail.estimate, { rows: plural(rows, 'row'), method: sql.estimate?.method === 'explain' ? 'EXPLAIN estimate' : (sql.estimate?.method ?? 'estimate') });
  return { statement, estimate, kind: sql.kind, tables: sql.tables ?? [], hasWhere: sql.has_where };
}

/** whereOf pulls the WHERE clause out of a statement for display only (never parsed for meaning). */
export function whereOf(statement: string): string | null {
  const m = /\bwhere\b([\s\S]*?)(\breturning\b|;|$)/i.exec(statement);
  const w = m?.[1]?.trim();
  return w ? w : null;
}

export type DecideState =
  | { kind: 'can' }
  | { kind: 'self'; text: string } // deny only
  | { kind: 'already'; text: string } // you countersigned; waiting for others
  | { kind: 'not_eligible'; text: string }
  | { kind: 'closed'; text: string }; // decided, expired or cancelled

/** decideState shapes the buttons from S22's `you`; the server stays the authority (S26 §3.1). */
export function decideState(a: Pick<ApprovalFull, 'status' | 'you' | 'countersigned' | 'required' | 'expires_at'>, me: { id: string } | null, now = new Date(), tz = 'UTC'): DecideState {
  if (a.status !== 'pending') return { kind: 'closed', text: c.closed[a.status] ?? 'Decided' };
  if (new Date(a.expires_at).getTime() <= now.getTime()) return { kind: 'closed', text: c.closed.expired ?? '' };
  const mine = me ? votes(a).find((v) => v.user.id === me.id) : undefined;
  if (mine) {
    const left = Math.max(0, a.required - voteCount(a));
    return { kind: 'already', text: fill(left === 1 ? c.youCountersignedOne : c.youCountersigned, { time: clockOf(mine.at, tz), n: left }) };
  }
  const y = a.you;
  if (!y) return { kind: 'not_eligible', text: c.notEligible };
  if (!y.eligible) return { kind: 'not_eligible', text: y.why_not ?? c.notEligible };
  if (y.can_decide && y.why_not) return { kind: 'self', text: c.self };
  if (!y.can_decide) return { kind: 'not_eligible', text: y.why_not ?? c.notEligible };
  return { kind: 'can' };
}

export function clockOf(iso: string, tz: string): string {
  try {
    return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz }).format(new Date(iso));
  } catch {
    return new Date(iso).toISOString().slice(11, 16);
  }
}

/** foureyes: `1 of 2 countersigned · Priya Raman`, or null for a single approver. */
export function fourEyes(a: Pick<ApprovalFull, 'required' | 'countersigned'>): string | null {
  if (a.required < 2) return null;
  const names = votes(a).map((v) => v.user.name);
  const base = fill(c.fourEyes, { n: voteCount(a), required: a.required });
  return names.length ? `${base} · ${names.join(', ')}` : base;
}

/** raceSentence: `Priya Raman approved this 20 seconds ago.` */
export function raceSentence(a: Pick<ApprovalFull, 'status' | 'countersigned' | 'denied_by' | 'decided_at'>, now = new Date()): string {
  const secs = a.decided_at ? Math.max(0, Math.round((now.getTime() - new Date(a.decided_at).getTime()) / 1000)) : 0;
  const ago = secs < 60 ? `${plural(secs, 'second')} ago` : `${plural(Math.round(secs / 60), 'minute')} ago`;
  if (a.status === 'approved') {
    const v = votes(a);
    const who = v[v.length - 1]?.user.name ?? c.someone;
    return fill(c.race.approved, { who, ago });
  }
  if (a.status === 'denied') return fill(c.race.denied, { who: a.denied_by?.name ?? c.someone, ago });
  if (a.status === 'expired') return c.closed.expired ?? '';
  return c.closed.cancelled ?? '';
}

export function validDenyReason(s: string): boolean {
  const n = [...s.trim()].length;
  return n >= 3 && n <= 280;
}

/** Follow-through phases after an approve: Countersigned → Running → Executed · L1 · VERIFIED. */
export type Follow = 'countersigned' | 'running' | 'executed' | 'sealed' | 'failed' | 'denied';
