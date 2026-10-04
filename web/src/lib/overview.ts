// overview.ts: the Overview's numbers and words, pure (S25 §3.3). The server sends counts and
// attention kinds with data (GET /v1/overview); this file turns them into the tiles' text, the
// attention sentences and the live counter adjustments between refetches. Every number states its
// window. A delta is neutral: it isn't a verdict.

import { copy, fill } from './copy.ts';
import { number, plural } from './format.ts';
import { minutesUntil } from './sentences.ts';
import type { AttentionItem, DeedglassEvent, Overview } from './types.ts';

export type Window = '24h' | '7d';

export function windowWords(w: Window): string {
  return copy.overview.window[w] ?? w;
}

/** delta: "+8% vs previous 24 hours" (neutral words for flat or nothing before). */
export function delta(total: number, previous: number, w: Window): string {
  const win = windowWords(w);
  if (previous === 0) return total === 0 ? fill(copy.overview.deltaFlat, { window: win }) : fill(copy.overview.deltaNew, { window: win });
  const pct = Math.round(((total - previous) / previous) * 100);
  if (pct === 0) return fill(copy.overview.deltaFlat, { window: win });
  return fill(copy.overview.delta, { sign: pct > 0 ? '+' : '−', pct: number(Math.abs(pct)), window: win });
}

/** percent with one decimal under 10: 3.1, 12, 0.4. */
export function pct(part: number, whole: number): string {
  if (whole <= 0) return '0';
  const p = (part / whole) * 100;
  return p < 10 ? (Math.round(p * 10) / 10).toString() : Math.round(p).toString();
}

export function blockedLine(o: Overview): string {
  if (o.blocked.total === 0) return copy.overview.blockedNone;
  return fill(copy.overview.blockedSub, { pct: pct(o.blocked.total, o.actions.total) });
}

export function waitingLine(o: Overview, now: Date): string {
  const a = o.approvals;
  if (a.pending === 0) return copy.overview.waitingNone;
  if (!a.next_expires_at) return copy.overview.waitingNone;
  const mins = minutesUntil(a.next_expires_at, now);
  if (a.expiring_soon === 1) return fill(copy.overview.waitingSub, { n: 1, mins });
  if (a.expiring_soon > 1) return fill(copy.overview.waitingSubMany, { n: a.expiring_soon, mins });
  return fill(copy.overview.waitingNext, { mins });
}

export function sparkLabel(what: string, series: number[], w: Window): string {
  const min = series.length ? Math.min(...series) : 0;
  const max = series.length ? Math.max(...series) : 0;
  return fill(copy.overview.spark, { what, bucket: w === '24h' ? 'hour' : '6 hours', window: windowWords(w), min: number(min), max: number(max) });
}

export interface Segment {
  key: 'VERIFIED' | 'MISMATCH' | 'UNVERIFIABLE' | 'not_witnessed';
  count: number;
  share: number; // 0..1 of the total
  label: string;
  href: string;
}

/** segments: the Verification bar, in a fixed order; each links to the feed, pre-filtered. */
export function segments(o: Overview): Segment[] {
  const v = o.verification;
  const total = v.verified + v.mismatch + v.unverifiable + v.not_witnessed;
  const span = o.window === '24h' ? 'time=24h' : 'time=7d';
  const seg = (key: Segment['key'], count: number, label: string, q: string): Segment => ({
    key, count, share: total > 0 ? count / total : 0, label, href: `/app/actions?${q}${q ? '&' : ''}${span}`,
  });
  return [
    seg('VERIFIED', v.verified, copy.chips.verified, 'verdict=VERIFIED'),
    seg('MISMATCH', v.mismatch, copy.chips.mismatch, 'verdict=MISMATCH'),
    seg('UNVERIFIABLE', v.unverifiable, copy.chips.unverifiable, 'verdict=UNVERIFIABLE'),
    seg('not_witnessed', v.not_witnessed, copy.overview.notWitnessed, ''),
  ];
}

function str(d: Record<string, unknown>, k: string): string {
  const v = d[k];
  return typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '';
}

function mb(bytes: number): string {
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`;
  return `${number(Math.round(bytes / (1 << 20)))} MB`;
}

export interface AttentionLine {
  icon: string;
  tone: 'mismatch' | 'pending' | 'unverifiable' | 'danger' | 'neutral';
  title: string;
  detail: string;
  href: string | null;
}

const REF = /^(act|apr|agt|con|deed)_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

/** refHref links an attention item's ref, after checking its shape (S25 §7). */
export function refHref(ref: string | null): string | null {
  if (!ref || !REF.test(ref)) return null;
  const kind = ref.slice(0, ref.indexOf('_'));
  const base: Record<string, string> = { act: 'actions', apr: 'approvals', agt: 'agents', con: 'connectors', deed: 'deeds' };
  return `/app/${base[kind]}/${ref}`;
}

export function attentionLine(it: AttentionItem, org: string, now: Date, tz: string, time: (iso: string, tz: string) => string): AttentionLine {
  const d = it.data ?? {};
  const a = copy.attention;
  const href = refHref(it.ref);
  switch (it.kind) {
    case 'chain_broken':
      return { icon: 'x', tone: 'mismatch', title: fill(a.chain_broken, { seq: number(Number(d.seq ?? 0)) }), detail: str(d, 'message'), href: href ?? '/app/deeds' };
    case 'org_stopped':
      return { icon: 'stop', tone: 'danger', title: fill(a.org_stopped, { org, since: d.since ? time(str(d, 'since'), tz) : '', by: str(d, 'by') || 'an admin' }), detail: d.reason ? `“${str(d, 'reason')}”` : '', href: '/app/settings' };
    case 'slot_invalidated':
      return { icon: 'circle-alert', tone: 'unverifiable', title: fill(a.slot_invalidated, { connector: str(d, 'connector') }), detail: str(d, 'status_reason'), href };
    case 'slot_lagging':
      return { icon: 'circle-dashed', tone: 'unverifiable', title: fill(a.slot_lagging, { connector: str(d, 'connector'), lag: mb(Number(d.lag_bytes ?? 0)) }), detail: '', href };
    case 'mismatch':
      return { icon: 'x', tone: 'mismatch', title: fill(a.mismatch, { tool: str(d, 'tool'), agent: str(d, 'agent') }), detail: str(d, 'summary'), href };
    case 'approval_expiring': {
      const exp = str(d, 'expires_at');
      return { icon: 'contrast', tone: 'pending', title: fill(a.approval_expiring, { tool: str(d, 'tool'), mins: exp ? minutesUntil(exp, now) : '' }), detail: str(d, 'agent'), href };
    }
    case 'budget_near': {
      const what = a.budgetWhat[str(d, 'budget')] ?? 'calls';
      const money = (v: unknown) => (typeof v === 'object' && v && 'amount' in v ? number(Number((v as { amount: number }).amount) / 100) : number(Number(v ?? 0)));
      return { icon: 'gauge', tone: 'unverifiable', title: fill(a.budget_near, { agent: str(d, 'agent'), used: money(d.used), limit: money(d.limit), what }), detail: '', href };
    }
    default:
      return { icon: 'info', tone: 'neutral', title: String((it as { kind: string }).kind), detail: '', href };
  }
}

/**
 * applyLive adjusts the counters between refetches (S25 §3.3): action.created adds an action, a
 * deny decision adds a block, action.witnessed moves one action from not witnessed to its verdict,
 * approval.requested and approval.decided move Waiting. Returns whether anything changed. The
 * 60-second refetch corrects any drift (an event seen twice, one outside the window).
 */
export function applyLive(o: Overview, e: DeedglassEvent, seen: Set<string>): boolean {
  const obj = e.data.object as Record<string, unknown>;
  const key = `${e.type}:${String(obj.id ?? '')}`;
  if (seen.has(key)) return false;
  seen.add(key);
  const last = o.actions.series.length - 1;
  switch (e.type) {
    case 'action.created':
      o.actions.total++;
      if (last >= 0) o.actions.series[last]!++;
      o.verification.not_witnessed++;
      return true;
    case 'action.decided': {
      const d = (obj.decision as { outcome?: string } | null)?.outcome ?? (obj.status === 'denied' ? 'deny' : '');
      if (d !== 'deny') return false;
      o.blocked.total++;
      if (last >= 0 && o.blocked.series.length) o.blocked.series[o.blocked.series.length - 1]!++;
      return true;
    }
    case 'action.witnessed': {
      const v = (obj.witness as { verdict?: string; level?: string } | null) ?? null;
      const verdict = v?.verdict ?? (obj.verdict as string | undefined);
      const level = (v?.level ?? obj.level) as 'L0' | 'L1' | 'L2' | undefined;
      if (verdict !== 'VERIFIED' && verdict !== 'MISMATCH' && verdict !== 'UNVERIFIABLE') return false;
      if (o.verification.not_witnessed > 0) o.verification.not_witnessed--;
      if (verdict === 'VERIFIED') o.verification.verified++;
      else if (verdict === 'MISMATCH') o.verification.mismatch++;
      else o.verification.unverifiable++;
      if (level && level in o.verification.by_level) o.verification.by_level[level]++;
      return true;
    }
    case 'approval.requested':
      o.approvals.pending++;
      return true;
    case 'approval.decided':
      if (o.approvals.pending > 0) o.approvals.pending--;
      return true;
    default:
      return false;
  }
}

/** usageLine: "212 / 5,000 today" or "212 today". */
export function usageLine(used: number, limit: number | undefined): string {
  return limit ? fill(copy.overview.callsToday, { used: number(used), limit: number(limit) }) : fill(copy.overview.callsTodayNoLimit, { used: number(used) });
}

export { plural };
