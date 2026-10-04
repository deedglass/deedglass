// filters.ts: the Actions feed's filters, one model for the URL, the API query and live matching
// (S25 §3.4). The URL names are the API's names (S06), so a shared link is a readable query.
// matches() mirrors internal/ledger/read.go: tool is exact, or a glob where * matches any run of
// characters (SQL LIKE with every other character literal, case-sensitive); enums are sets; time is
// created_at >= from and < to. Text search (q) runs on the server only.

import type { Action, Level, Verdict } from './types.ts';

export type Decision = 'allow' | 'deny' | 'require_approval';
export type TimeRange = '' | '1h' | '24h' | '7d' | 'custom';

export interface Filters {
  agent: string;
  tool: string;
  decision: Decision | '';
  verdict: Verdict[];
  level: Level[];
  state: string[];
  on_behalf_of: string;
  time: TimeRange;
  from: string; // ISO, custom range only
  to: string;
  q: string;
  shadow_differs: boolean; // S26: actions where the shadow policy decided differently
}

export const DECISIONS: readonly Decision[] = ['allow', 'deny', 'require_approval'];
export const VERDICTS: readonly Verdict[] = ['VERIFIED', 'MISMATCH', 'UNVERIFIABLE'];
export const LEVELS: readonly Level[] = ['L0', 'L1', 'L2'];
export const STATES: readonly string[] = [
  'received', 'authenticated', 'evaluated', 'denied', 'pending_approval', 'executing', 'executed', 'failed',
  'witnessing', 'verified', 'mismatch', 'unverifiable', 'sealed', 'expired', 'cancelled',
];
const RANGE_MS: Record<'1h' | '24h' | '7d', number> = { '1h': 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000 };

export function emptyFilters(): Filters {
  return { agent: '', tool: '', decision: '', verdict: [], level: [], state: [], on_behalf_of: '', time: '', from: '', to: '', q: '', shadow_differs: false };
}

const AGENT_RE = /^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$/;
const REF_RE = /^[A-Za-z0-9._~:@/+=-]{1,256}$/;

function list<T extends string>(v: string | null, allowed: readonly T[]): T[] {
  if (!v) return [];
  const out: T[] = [];
  for (const x of v.split(',')) if ((allowed as readonly string[]).includes(x) && !out.includes(x as T)) out.push(x as T);
  return out;
}

function iso(v: string | null): string {
  if (!v) return '';
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString();
}

/** parseFilters reads the page URL. Unknown or malformed values are dropped, never sent on. */
export function parseFilters(sp: URLSearchParams): Filters {
  const f = emptyFilters();
  const agent = sp.get('agent') ?? '';
  if (AGENT_RE.test(agent)) f.agent = agent;
  const tool = (sp.get('tool') ?? '').trim();
  if (tool.length <= 200 && !/[\u0000-\u001f\u007f]/.test(tool)) f.tool = tool;
  const d = sp.get('decision') ?? '';
  if ((DECISIONS as readonly string[]).includes(d)) f.decision = d as Decision;
  f.verdict = list(sp.get('verdict'), VERDICTS);
  f.level = list(sp.get('level'), LEVELS);
  f.state = list(sp.get('state'), STATES);
  const obo = sp.get('on_behalf_of') ?? '';
  if (REF_RE.test(obo)) f.on_behalf_of = obo;
  const t = sp.get('time') ?? '';
  if (t === '1h' || t === '24h' || t === '7d') f.time = t;
  const from = iso(sp.get('from'));
  const to = iso(sp.get('to'));
  if (from || to) {
    f.time = 'custom';
    f.from = from;
    f.to = to;
  }
  const q = (sp.get('q') ?? '').trim();
  if (q.length >= 2 && q.length <= 64) f.q = q;
  if (sp.get('shadow_differs') === 'true') f.shadow_differs = true;
  return f;
}

/** filtersToSearch writes only what is set, in a fixed order, so equal filters give equal URLs. */
export function filtersToSearch(f: Filters): URLSearchParams {
  const sp = new URLSearchParams();
  if (f.agent) sp.set('agent', f.agent);
  if (f.tool) sp.set('tool', f.tool);
  if (f.decision) sp.set('decision', f.decision);
  if (f.verdict.length) sp.set('verdict', f.verdict.join(','));
  if (f.level.length) sp.set('level', f.level.join(','));
  if (f.state.length) sp.set('state', f.state.join(','));
  if (f.on_behalf_of) sp.set('on_behalf_of', f.on_behalf_of);
  if (f.time === 'custom') {
    if (f.from) sp.set('from', f.from);
    if (f.to) sp.set('to', f.to);
  } else if (f.time) sp.set('time', f.time);
  if (f.q) sp.set('q', f.q);
  if (f.shadow_differs) sp.set('shadow_differs', 'true');
  return sp;
}

/** bounds turns the time filter into [from, to) at `now`. */
export function bounds(f: Filters, now: Date): { from: Date | null; to: Date | null } {
  if (f.time === 'custom') return { from: f.from ? new Date(f.from) : null, to: f.to ? new Date(f.to) : null };
  if (f.time === '') return { from: null, to: null };
  return { from: new Date(now.getTime() - RANGE_MS[f.time]), to: null };
}

/** toApiQuery is GET /v1/actions's query for the filters. */
export function toApiQuery(f: Filters, now: Date): Record<string, string> {
  const q: Record<string, string> = {};
  if (f.agent) q.agent = f.agent;
  if (f.tool) q.tool = f.tool;
  if (f.decision) q.decision = f.decision;
  if (f.verdict.length) q.verdict = f.verdict.join(',');
  if (f.level.length) q.level = f.level.join(',');
  if (f.state.length) q.state = f.state.join(',');
  if (f.on_behalf_of) q.on_behalf_of = f.on_behalf_of;
  const b = bounds(f, now);
  if (b.from) q['created_at[gte]'] = b.from.toISOString();
  if (b.to) q['created_at[lt]'] = b.to.toISOString();
  if (f.q) q.q = f.q;
  if (f.shadow_differs) q.shadow_differs = 'true';
  return q;
}

export function isFiltered(f: Filters): boolean {
  return filtersToSearch(f).toString() !== '';
}

/** globMatch is the Ledger's tool filter: exact without *, otherwise * matches any run of characters. */
export function globMatch(pattern: string, tool: string): boolean {
  if (!pattern.includes('*')) return pattern === tool;
  const parts = pattern.split('*');
  const first = parts[0]!;
  const last = parts[parts.length - 1]!;
  if (!tool.startsWith(first)) return false;
  let at = first.length;
  for (let i = 1; i < parts.length - 1; i++) {
    const idx = tool.indexOf(parts[i]!, at);
    if (idx < 0) return false;
    at = idx + parts[i]!.length;
  }
  return tool.length - last.length >= at && tool.endsWith(last);
}

/** decisionOf is the action's policy decision (decision.outcome, as the Ledger filters it). */
export function decisionOf(a: Pick<Action, 'decision' | 'policy'>): Decision | null {
  const d = a.decision?.outcome ?? a.policy?.decision ?? null;
  return d === 'allow' || d === 'deny' || d === 'require_approval' ? d : null;
}

/**
 * matches answers whether a live action belongs in the current view. With q set the answer is
 * always false: text search is the server's, so live insertion pauses (S25 §3.4).
 */
export function matches(f: Filters, a: Action, now: Date): boolean {
  if (f.q) return false;
  if (f.agent && a.agent?.id !== f.agent) return false;
  if (f.tool && !globMatch(f.tool, a.tool)) return false;
  if (f.decision && decisionOf(a) !== f.decision) return false;
  if (f.verdict.length && (!a.witness || !f.verdict.includes(a.witness.verdict))) return false;
  if (f.level.length && (!a.witness || !f.level.includes(a.witness.level))) return false;
  if (f.state.length && !f.state.includes(a.state)) return false;
  if (f.on_behalf_of && a.on_behalf_of !== f.on_behalf_of) return false;
  if (f.shadow_differs && (!a.decision?.shadow?.outcome || a.decision.shadow.outcome === decisionOf(a))) return false;
  const b = bounds(f, now);
  const t = new Date(a.created_at).getTime();
  if (b.from && t < b.from.getTime()) return false;
  if (b.to && t >= b.to.getTime()) return false;
  return true;
}
