// models/insights.ts: the Console's view of S28's Insights API (internal/learn suggest.go,
// lifecycle.go, shadow.go, anomalies.go, stats.go, settings.go) as shipped
// (test/fixtures/s28/*.json are captured from a seed-7 simulated org). The fetchers go through
// api.ts (cookie session, CSRF, an Idempotency-Key on every POST); the helpers are pure, so Vitest
// pins the numbers and sentences the screen shows.

import { api, ApiError } from '../api.ts';
import { fill } from '../copy.ts';
import { insightsCopy as c } from '../copy-insights.ts';
import { date, money, number } from '../format.ts';
import type { DiffLine, Hunk, LineDiff } from '../linediff.ts';
import type { HumanOutcome, Transition } from './replay.ts';
import type { Policy, PolicyVersion } from './policy.ts';

// ---------- types ----------

export type SuggestionStatus = 'open' | 'accepting' | 'accepted' | 'rejected' | 'superseded';
export type SuggestionKind = 'loosen' | 'tighten' | 'budget' | 'explicit_rule' | 'advisory';

export interface Phrasing { source: 'template' | 'claude' | string; model?: string; reason?: string }
export interface SuggestionPolicy { id: string; name: string; base_version: number; base_version_id: string; base_version_hash: string }
export interface ServerHunk { old_start: number; old_lines: number; new_start: number; new_lines: number; lines: { op: ' ' | '+' | '-' | string; text: string }[] }
export interface ServerDiff { hunks: ServerHunk[]; text: string; added: number; removed: number }
export interface SuggestionReplay {
  id: string;
  window: { from: string; to: string };
  summary: { actions: number; evaluated: number; unchanged: number; changed: number; rule_changed: number; unknown: number };
  transitions: Transition[] | null;
  result_digest: string;
}
export interface Diagnostic { severity: string; code: string; message: string; hint?: string; line: number }

export interface Suggestion {
  object: 'suggestion';
  id: string;
  status: SuggestionStatus | string;
  kind: SuggestionKind | string;
  generator: string;
  title: string;
  rationale: string;
  phrasing: Phrasing;
  policy: SuggestionPolicy;
  target_rule_id: string | null;
  evidence: Record<string, unknown>;
  evidence_digest: string;
  diff: ServerDiff | null;
  warnings: Diagnostic[] | null;
  replay: SuggestionReplay | null;
  risk_note: string;
  impact: number;
  stale_reason: string | null;
  created_at: string;
  decided_by: { type: string; id: string } | null;
  decided_at: string | null;
  decision_reason: string | null;
  resulting_version: { id: string; version: number; version_hash: string } | null;
  decision_deed: string | null;
  // the accept answer only
  deeds?: string[];
  replaced_shadow_version?: number;
  note?: string;
}

export interface ShadowTransition {
  enforce: string;
  shadow: string;
  count: number;
  human_outcome?: HumanOutcome | null;
  by_rule: { rule_id: string; count: number }[] | null;
  by_tool: { tool: string; count: number }[] | null;
  by_agent: { agent: string; name: string; count: number }[] | null;
  samples: string[] | null;
}
export interface ShadowReport {
  object: 'shadow_report';
  shadow: { policy: string; name: string; version: number; version_id: string; live: boolean; since?: string };
  enforce: { name: string; version: number; version_id: string; actions: number }[];
  window: { from: string | null; to: string | null };
  summary: { evaluated: number; agree: number; disagree: number; agreement: number };
  transitions: ShadowTransition[] | null;
  sentence: string;
}

export interface Anomaly {
  object: 'anomaly';
  id: string;
  agent: { id: string; name: string };
  kind: string;
  tool: string | null;
  severity: string;
  score: number;
  title: string;
  status: 'open' | 'acknowledged' | 'resolved' | string;
  active: boolean;
  active_until: string;
  window: { start: string; end: string };
  occurrences: number;
  evidence: Record<string, unknown>;
  acknowledged: { by: string; at: string } | null;
  resolved: { by: string; at: string; reason: string | null; deed: string | null } | null;
  created_at: string;
  updated_at: string;
}
export interface AnomalyList {
  object: 'list';
  data: Anomaly[];
  has_more: boolean;
  next_cursor: string | null;
  active_now: number;
  anomaly_facts: boolean;
  notice: string | null;
  baseline_building: { agent: string; name: string; hours: number; sentence: string }[] | null;
}

export interface SeriesRow { date: string; actions: number; denied: number; verified: number; mismatch: number; unverifiable: number }
export interface Stats {
  object: 'insights_stats';
  window: { from: string; to: string; days: number };
  timezone: string;
  totals: {
    actions: number;
    decisions: { allow: number; deny: number; require_approval: number };
    approval: { requested: number; approved: number; denied: number; expired: number; cancelled: number; pending: number; rate: number | null; median_seconds: number | null; p95_seconds: number | null };
    verdicts: { VERIFIED: number; MISMATCH: number; UNVERIFIABLE: number; none: number };
  };
  series: SeriesRow[] | null;
  empty: string | null;
}

export type Recipients = 'owners' | 'admins' | 'off';
export interface InsightsSettings {
  object: 'insights_settings';
  anomaly_facts: boolean;
  anomaly_active_minutes: number;
  anomaly_stale_after_minutes: number;
  llm_phrasing: boolean;
  llm_monthly_cap_cents: number;
  digest: { recipients: Recipients | string };
  active_policy_reads_anomaly: boolean;
  notice: string | null;
  phrasing_available: boolean;
  /** Not in the S27 object yet: read when the server adds it (S28 asks for it). */
  simulated?: boolean;
  deed?: string;
}
export interface SettingsPatch {
  anomaly_facts?: boolean;
  llm_phrasing?: boolean;
  digest?: { recipients: Recipients };
  reason: string;
}

export interface LearnRun {
  object: 'learn_run';
  created: { id: string; kind: string; title: string; impact: number }[] | null;
  superseded: number;
  unchanged: number;
  waiting: number;
  dropped: { generator: string; target?: string; reason: string }[] | null;
  notice: string | null;
}

interface List<T> { object: 'list'; data: T[]; has_more: boolean; next_cursor: string | null }

// ---------- fetchers ----------

/** listSuggestions: the open suggestions, ordered by impact (the server's order, kept stable here). */
export async function listSuggestions(status: SuggestionStatus | '' = 'open'): Promise<Suggestion[]> {
  const out: Suggestion[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < 5; page++) {
    const r = await api<List<Suggestion>>('GET', '/v1/suggestions', { query: { status: status || undefined, limit: '50', cursor } });
    out.push(...r.data);
    if (!r.has_more || !r.next_cursor) break;
    cursor = r.next_cursor;
  }
  return byImpact(out);
}

/** getShadow: the live shadow's report, or null when nothing runs in shadow (404 not_found). */
export async function getShadow(): Promise<ShadowReport | null> {
  try {
    const r = await api<ShadowReport>('GET', '/v1/insights/shadow');
    return r.shadow?.live === false ? null : r;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export async function listAnomalies(): Promise<AnomalyList> {
  const r = await api<AnomalyList>('GET', '/v1/insights/anomalies', { query: { limit: '50' } });
  return { ...r, data: newestFirst(r.data.filter((a) => a.status !== 'resolved')) };
}

export const getStats = (): Promise<Stats> => api<Stats>('GET', '/v1/insights/stats', { query: { window: '30d', by: 'none' } });
export const getSettings = (): Promise<InsightsSettings> => api<InsightsSettings>('GET', '/v1/insights/settings');
export const patchSettings = (p: SettingsPatch): Promise<InsightsSettings> => api<InsightsSettings>('PATCH', '/v1/insights/settings', { body: p });
export const generate = (): Promise<LearnRun> => api<LearnRun>('POST', '/v1/suggestions/generate');

export function accept(id: string, o: { reason?: string; replaceShadow: boolean }, idempotencyKey?: string): Promise<Suggestion> {
  const body: Record<string, unknown> = { replace_shadow: o.replaceShadow };
  if (o.reason) body.reason = o.reason;
  return api<Suggestion>('POST', `/v1/suggestions/${encodeURIComponent(id)}/accept`, { body, idempotencyKey });
}
export const reject = (id: string, reason: string): Promise<Suggestion> =>
  api<Suggestion>('POST', `/v1/suggestions/${encodeURIComponent(id)}/reject`, { body: { reason } });
export const acknowledge = (id: string): Promise<Anomaly> => api<Anomaly>('POST', `/v1/insights/anomalies/${encodeURIComponent(id)}/acknowledge`);
export const resolve = (id: string, reason: string): Promise<Anomaly> =>
  api<Anomaly>('POST', `/v1/insights/anomalies/${encodeURIComponent(id)}/resolve`, { body: { reason } });
export const getPolicy = (id: string): Promise<Policy> => api<Policy>('GET', `/v1/policies/${encodeURIComponent(id)}`);
export const getVersion = (id: string, v: number): Promise<PolicyVersion> => api<PolicyVersion>('GET', `/v1/policies/${encodeURIComponent(id)}/versions/${v}`);

// ---------- pure helpers ----------

export function byImpact(list: Suggestion[]): Suggestion[] {
  return [...list].sort((a, b) => b.impact - a.impact || b.created_at.localeCompare(a.created_at) || a.id.localeCompare(b.id));
}

export function newestFirst(list: Anomaly[]): Anomaly[] {
  return [...list].sort((a, b) => b.window.end.localeCompare(a.window.end) || b.created_at.localeCompare(a.created_at));
}

export const kindLabel = (k: string): string => c.kinds[k] ?? k.replace(/_/g, ' ');
export const isAdvisory = (s: Pick<Suggestion, 'kind' | 'diff'>): boolean => s.kind === 'advisory' || !s.diff;
export const isDecidable = (s: Pick<Suggestion, 'status'>): boolean => s.status === 'open';

/** diffLabel: "+8 lines" from the server's count of added lines. */
export function diffLines(d: ServerDiff | null): string {
  if (!d) return '';
  return d.added === 1 ? c.card.diffLinesOne : fill(c.card.diffLines, { n: number(d.added) });
}

/** toLineDiff turns the server's unified hunks into S26's LineDiff, numbering old and new lines. */
export function toLineDiff(d: ServerDiff): LineDiff {
  const hunks: Hunk[] = d.hunks.map((k) => {
    let o = k.old_start;
    let n = k.new_start;
    const lines: DiffLine[] = k.lines.map((l) => {
      if (l.op === '+') return { kind: 'add', text: l.text, old: null, new: n++ };
      if (l.op === '-') return { kind: 'del', text: l.text, old: o++, new: null };
      return { kind: 'ctx', text: l.text, old: o++, new: n++ };
    });
    return { oldStart: k.old_start, oldLines: k.old_lines, newStart: k.new_start, newLines: k.new_lines, lines };
  });
  return { hunks, added: d.added, removed: d.removed, identical: hunks.length === 0, crlf: false };
}

/** outcomeLine: "398 approved · 0 denied · 5 expired · 8 pending" (zeros dropped, except denied). */
export function outcomeLine(h: HumanOutcome | null | undefined): string {
  if (!h) return '';
  const o = c.outcome;
  const parts = [fill(o.approved, { n: number(h.approved) }), fill(o.denied, { n: number(h.denied) })];
  if (h.expired > 0) parts.push(fill(o.expired, { n: number(h.expired) }));
  if (h.pending > 0) parts.push(fill(o.pending, { n: number(h.pending) }));
  if (h.cancelled > 0) parts.push(fill(o.cancelled, { n: number(h.cancelled) }));
  if ((h.not_requested ?? 0) > 0) parts.push(fill(o.notRequested, { n: number(h.not_requested!) }));
  return parts.join(' · ');
}

/** replayDays: the replay window in whole days (14 for a suggestion). */
export function replayDays(w: { from: string; to: string }): number {
  return Math.max(1, Math.round((Date.parse(w.to) - Date.parse(w.from)) / 86_400_000));
}

/** waitText: 1123 s → "19 min", 6578 s → "1 h 50 min", 42 s → "42 s". */
export function waitText(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return '·';
  const s = Math.round(seconds);
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const hh = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${hh} h ${mm} min` : `${hh} h`;
}

const pct = (n: number, total: number): string => {
  if (!total) return '0%';
  const v = (n / total) * 100;
  return `${v >= 10 || v === 0 ? v.toFixed(1).replace(/\.0$/, '') : v.toFixed(1)}%`;
};
export const percent = pct;

export interface EvidenceRow { key: string; label: string; value: string; mono?: boolean; num?: boolean; ids?: string[] }

const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const num = (v: unknown): number | null => {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v);
  return null;
};

/** patternText: {"sql.kind":"update","sql.tables":["public.orders"]} → "sql.kind update · sql.tables public.orders". */
export function patternText(p: unknown): string {
  if (!p || typeof p !== 'object') return '';
  return Object.entries(p as Record<string, unknown>)
    .filter(([k]) => k !== 'agents' && k !== 'tool' && k !== 'rule')
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k} ${Array.isArray(v) ? v.join(', ') : String(v)}`)
    .join(' · ');
}

/**
 * evidenceRows reads the generator's evidence object as a key/value list: the known keys in a fixed
 * order with plain labels, then any other scalar the server adds, so a new generator never hides
 * its numbers. Ids and hashes stay mono; counts are tabular.
 */
export function evidenceRows(ev: Record<string, unknown>, tz = 'UTC'): EvidenceRow[] {
  const L = c.evidence;
  const rows: EvidenceRow[] = [];
  const seen = new Set<string>(['sample_actions', 'pattern', 'days', 'points', 'action_ids', 'timezone']);
  const add = (key: string, value: string, o: Partial<EvidenceRow> = {}) => { if (value) rows.push({ key, label: L[key] ?? key, value, ...o }); };
  const w = ev.window as { from?: string; to?: string } | undefined;
  if (w?.from && w.to) add('window', fill(L.windowRange!, { from: date(w.from, tz), to: date(w.to, tz) }));
  seen.add('window');
  const ap = ev.approvals as Record<string, unknown> | undefined;
  if (ap && typeof ap === 'object') {
    for (const k of ['requested', 'approved', 'denied', 'expired', 'pending', 'cancelled']) {
      const n = num(ap[k]);
      if (n !== null && (n > 0 || k === 'denied' || k === 'requested')) add(k, number(n), { num: true });
    }
  }
  seen.add('approvals');
  const wait = num(ev.median_wait_seconds);
  if (wait !== null) add('medianWait', waitText(wait), { num: true });
  seen.add('median_wait_seconds');
  const approvers = num(ev.approvers);
  if (approvers !== null) add('approvers', number(approvers), { num: true });
  seen.add('approvers');
  const mism = num(ev.mismatches);
  if (mism !== null) add('mismatches', number(mism), { num: true });
  seen.add('mismatches');
  if (typeof ev.budget === 'string') add('budget', ev.budget, { mono: true });
  seen.add('budget');
  const lim = ev.limit as { amount?: number; calls?: number; currency?: string } | undefined;
  if (lim && typeof lim === 'object') {
    if (typeof lim.amount === 'number' && lim.currency) add('limit', fill(L.perDay!, { v: money(lim.amount, lim.currency) }), { num: true });
    else if (typeof lim.calls === 'number') add('limit', fill(L.perDay!, { v: number(lim.calls) }), { num: true });
  }
  seen.add('limit');
  const mx = num(ev.max_utilisation);
  if (mx !== null) add('maxUse', pct(mx, 1), { num: true });
  seen.add('max_utilisation');
  const near = num(ev.near_days);
  if (near !== null) add('nearDays', number(near), { num: true });
  seen.add('near_days');
  const agents = Array.isArray(ev.agents) ? (ev.agents as unknown[]).map(String) : [];
  if (agents.length) add('agents', agents.join(', '));
  seen.add('agents');
  if (typeof ev.tool === 'string') add('tool', ev.tool, { mono: true });
  seen.add('tool');
  if (typeof ev.rule === 'string') add('rule', ev.rule, { mono: true });
  seen.add('rule');
  const pat = patternText(ev.pattern);
  if (pat) add('pattern', pat, { mono: true });
  // Anything else the generator adds: scalars only, label from the key.
  for (const [k, v] of Object.entries(ev).sort(([a], [b]) => a.localeCompare(b))) {
    if (seen.has(k) || k === 'kind') continue;
    const n = num(v);
    if (typeof v === 'number' && n !== null) rows.push({ key: k, label: cap(k.replace(/_/g, ' ')), value: number(n), num: true });
    else if (typeof v === 'string' && v.length <= 80) rows.push({ key: k, label: cap(k.replace(/_/g, ' ')), value: v });
  }
  const ids = Array.isArray(ev.sample_actions) ? (ev.sample_actions as unknown[]).map(String).filter((x) => x.startsWith('act_')) : [];
  if (ids.length) rows.push({ key: 'samples', label: c.card.samples, value: '', ids: ids.slice(0, 5) });
  return rows;
}

const cap = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

/** phrasingNote: the line under a card worded by Claude; '' for the template. */
export function phrasingNote(p: Phrasing | null | undefined): string {
  if (!p || p.source !== 'claude') return '';
  return fill(c.card.phrasing, { model: p.model || 'Claude' });
}

/** nextVersion: the version accept creates (latest + 1), and the enforced one it runs next to. */
export function acceptBody(name: string, p: Pick<Policy, 'latest_version' | 'active_version'>): string {
  const next = p.latest_version + 1;
  return p.active_version ? fill(c.accept.body, { name, next, live: p.active_version }) : fill(c.accept.bodyNoLive, { name, next });
}

/** acceptToast: "acme-sim v3 is running in shadow · deed_01m4…a1c3 sealed". */
export function acceptToast(s: Pick<Suggestion, 'policy' | 'resulting_version' | 'decision_deed' | 'deeds'>, shortDeed: (id: string) => string): { text: string; deed: string | null } {
  const deed = s.decision_deed ?? s.deeds?.[s.deeds.length - 1] ?? null;
  return { text: fill(c.accept.toast, { name: s.policy.name, v: s.resulting_version?.version ?? '?', deed: deed ? shortDeed(deed) : 'deed' }), deed };
}

/** agreementLine: "Agrees on 4,004 of 4,179 actions". */
export function agreementLine(r: Pick<ShadowReport, 'summary'>): string {
  return fill(c.shadow.agreement, { agree: number(r.summary.agree), n: number(r.summary.evaluated) });
}
export function agreementPct(r: Pick<ShadowReport, 'summary'>): string {
  return fill(c.shadow.agreementPct, { p: pct(r.summary.agree, r.summary.evaluated) });
}

/**
 * earlyNotice: the neutral notice in the Promote sheet under 100 decided actions or 24 hours in
 * shadow ('' once both thresholds pass).
 */
export function earlyNotice(r: Pick<ShadowReport, 'summary' | 'shadow'>, now: Date = new Date()): string {
  const n = r.summary.evaluated;
  const v = r.shadow.version;
  if (n < 100) return n === 1 ? fill(c.shadow.stillEarlyOne, { v }) : fill(c.shadow.stillEarly, { n: number(n), v });
  if (r.shadow.since && now.getTime() - Date.parse(r.shadow.since) < 86_400_000) return fill(c.shadow.stillEarlyHours, { v });
  return '';
}

export function shadowTransitions(r: Pick<ShadowReport, 'transitions'>): ShadowTransition[] {
  return [...(r.transitions ?? [])].sort((a, b) => b.count - a.count || a.enforce.localeCompare(b.enforce) || a.shadow.localeCompare(b.shadow));
}

/** isSimulated: the org was made by deedglass dev simulate (GET /v1/org settings, or the insights settings). */
export function isSimulated(org: unknown, settings?: Pick<InsightsSettings, 'simulated'> | null): boolean {
  const o = org as { settings?: { simulated?: unknown }; simulated?: unknown } | null | undefined;
  return o?.settings?.simulated === true || o?.simulated === true || settings?.simulated === true;
}

// ---------- trends ----------

export interface Day { date: string; actions: number; denied: number; verified: number; mismatch: number; unverifiable: number; none: number }

/** days30 fills the series to one row per day of the window (the server omits days with no actions). */
export function fillDays(s: Pick<Stats, 'window' | 'series' | 'timezone'>): Day[] {
  const byDate = new Map((s.series ?? []).map((r) => [r.date, r]));
  const fmt = (d: Date) => {
    try {
      return new Intl.DateTimeFormat('en-CA', { timeZone: s.timezone || 'UTC', year: 'numeric', month: '2-digit', day: '2-digit' }).format(d);
    } catch {
      return d.toISOString().slice(0, 10);
    }
  };
  const out: Day[] = [];
  const end = Date.parse(s.window.to);
  const n = Math.max(1, Math.round(s.window.days || 30));
  const seen = new Set<string>();
  for (let i = n - 1; i >= 0; i--) {
    const key = fmt(new Date(end - i * 86_400_000));
    if (seen.has(key)) continue;
    seen.add(key);
    const r = byDate.get(key);
    const actions = r?.actions ?? 0;
    const verified = r?.verified ?? 0, mismatch = r?.mismatch ?? 0, unverifiable = r?.unverifiable ?? 0;
    out.push({ date: key, actions, denied: r?.denied ?? 0, verified, mismatch, unverifiable, none: Math.max(0, actions - verified - mismatch - unverifiable) });
  }
  return out;
}

/** mixLabel: "Verification mix, last 30 days: 97.8% verified, 0.1% mismatch, 0.7% unverifiable". */
export function mixLabel(t: Stats['totals']): string {
  const total = t.actions;
  return fill(c.trends.mixLabel, { v: pct(t.verdicts.VERIFIED, total), m: pct(t.verdicts.MISMATCH, total), u: pct(t.verdicts.UNVERIFIABLE, total) });
}

export function denialsLabel(days: Day[]): string {
  const n = days.reduce((a, d) => a + d.denied, 0);
  const total = days.reduce((a, d) => a + d.actions, 0);
  const max = days.reduce((a, d) => Math.max(a, d.denied), 0);
  return fill(c.trends.denialsLabel, { n: number(n), total: number(total), max: number(max) });
}

export function waitLabel(a: Stats['totals']['approval']): string {
  const decided = a.approved + a.denied + a.expired;
  if (a.median_seconds === null) return c.trends.waitNone;
  return fill(c.trends.waitLabel, { median: waitText(a.median_seconds), p95: waitText(a.p95_seconds), n: number(decided) });
}

/** dayLabel: "2026-09-25" → "25 Sept" (the org's dates are already local). */
export function dayLabel(d: string): string {
  const [y, m, dd] = d.split('-').map(Number);
  if (!y || !m || !dd) return d;
  return date(new Date(Date.UTC(y, m - 1, dd, 12)), 'UTC').replace(/\s\d{4}$/, '');
}
