// live.ts: the live view's words, pure (console-v1). One action becomes a plain line ("Create
// refund in Stripe · blocked: Refunds over $100 need a person"); the Overview's Today row gets its
// four counts; repeated attention items fold into one line with a count. No values from a request
// are ever read here, so the same lines are safe in Health mode (PHI mode on).

import { decisionOf } from './filters.ts';
import { duration, number } from './format.ts';
import type { Action, Overview } from './types.ts';
import { liveCopy } from './copy-live.ts';

export { liveCopy };


const APP: Record<string, string> = {
  postgres: 'Postgres', github: 'GitHub', stripe: 'Stripe', slack: 'Slack', gmail: 'Gmail', notion: 'Notion',
  fhir: 'FHIR', orders: 'Orders', fake: 'the test connector', rest: 'the REST API', email: 'email', shell: 'the shell',
};

const SPECIAL: Record<string, string> = {
  'postgres.execute': 'Write to Postgres',
  'postgres.query': 'Read from Postgres',
  'fake.echo': 'Echo through the test connector',
};

/** toolPhrase: "stripe.create_refund" → "Create refund in Stripe"; unknown shapes come back as written. */
export function toolPhrase(tool: string): string {
  if (SPECIAL[tool]) return SPECIAL[tool]!;
  const dot = tool.indexOf('.');
  if (dot <= 0 || dot === tool.length - 1) return tool;
  const app = tool.slice(0, dot);
  const verb = tool.slice(dot + 1).replace(/[_.-]+/g, ' ').trim();
  if (!verb) return tool;
  const words = verb.charAt(0).toUpperCase() + verb.slice(1);
  return `${words} in ${APP[app] ?? app}`;
}

export type Tone = 'neutral' | 'blocked' | 'held' | 'failed' | 'running';

/** outcome: what happened, in a few words, and its tone. Reasons come from the policy (never args). */
export function outcome(a: Action): { text: string; tone: Tone } {
  const d = decisionOf(a);
  if (d === 'deny') {
    const why = a.decision?.reason ?? a.decision?.message ?? a.decision?.code ?? '';
    return { text: why ? `blocked: ${why}` : 'blocked', tone: 'blocked' };
  }
  if (a.state === 'pending_approval' || (d === 'require_approval' && a.approval?.status === 'pending')) return { text: 'waiting for a person', tone: 'held' };
  if (a.approval?.status === 'denied') return { text: 'a person declined it', tone: 'blocked' };
  if (a.approval?.status === 'expired') return { text: 'nobody approved it in time', tone: 'blocked' };
  if (a.state === 'failed' || a.execution?.status === 'failed') {
    return { text: a.execution?.error_code ? `the tool failed (${a.execution.error_code})` : 'the tool failed', tone: 'failed' };
  }
  if (['received', 'authenticated', 'evaluated', 'executing'].includes(a.state)) return { text: 'running', tone: 'running' };
  const ms = a.execution?.latency_ms;
  const who = d === 'require_approval' ? 'approved, ran' : 'ran';
  return { text: typeof ms === 'number' ? `${who} in ${duration(ms)}` : who, tone: 'neutral' };
}

/** plainSummary: "Create refund in Stripe · ran in 360 ms". */
export function plainSummary(a: Action): string {
  return `${toolPhrase(a.tool)} · ${outcome(a).text}`;
}

/** recordState: the sealed record's link target, or why there isn't one yet. */
export function recordState(a: Pick<Action, 'deed' | 'state'>): { href: string | null; label: 'record' | 'sealing' | 'later' | 'none' } {
  if (a.deed) return { href: `/app/deeds/${a.deed}`, label: 'record' };
  if (a.state === 'pending_approval') return { href: null, label: 'later' };
  if (a.state === 'sealed' || a.state === 'denied') return { href: null, label: 'none' };
  return { href: null, label: 'sealing' };
}

export interface TodayCounts { allowed: number; held: number; blocked: number; mismatch: number; total: number; pending: number; exact: boolean }

/**
 * todayCounts: the four Today numbers. A server that predates `decisions` gives no allowed or held:
 * allowed is then the actions that weren't blocked or held now, and `exact` is false.
 */
export function todayCounts(o: Overview): TodayCounts {
  const blocked = o.blocked.total;
  const pending = o.approvals.pending;
  if (o.decisions) {
    return { allowed: o.decisions.allowed, held: o.decisions.held, blocked, mismatch: o.verification.mismatch, total: o.actions.total, pending, exact: true };
  }
  return { allowed: Math.max(0, o.actions.total - blocked - pending), held: pending, blocked, mismatch: o.verification.mismatch, total: o.actions.total, pending, exact: false };
}

/** fold: repeated lines (the same title and detail) become one with a count, first seen first. */
export function fold<T extends { title: string; detail: string }>(lines: T[]): (T & { count: number })[] {
  const out: (T & { count: number })[] = [];
  const at = new Map<string, number>();
  for (const l of lines) {
    const k = `${l.title}\u0000${l.detail}`;
    const i = at.get(k);
    if (i === undefined) {
      at.set(k, out.length);
      out.push({ ...l, count: 1 });
    } else out[i]!.count++;
  }
  return out;
}

/** timesWord: " · 8 times" for a folded line (empty for one). */
export function timesWord(n: number): string {
  return n > 1 ? ` · ${number(n)} times` : '';
}

/** agentActive: an agent counts as active when its last action is under five minutes old. */
export function agentActive(last: string | null, now: Date): boolean {
  return !!last && now.getTime() - new Date(last).getTime() < 5 * 60_000;
}

/** phiValue: in Health mode every value is replaced; the field name stays (PHI_MODE.md). */
export function phiValue(v: string | null, phi: boolean): string | null {
  if (!phi || v === null) return v;
  return liveCopy.phi.hidden;
}

/** verifyCommand: the offline check for one record, the same command the deed panel copies. */
export function verifyCommand(deed: string, origin: string): string {
  return `deedglass verify ${deed}.json --keys ${origin}/.well-known/deedglass/keys.json`;
}
