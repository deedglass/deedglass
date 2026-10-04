// sentences.ts: the one sentence at the top of an action (S25 §3.5). Verdict sentences are the
// API's witness.summary, verbatim: the reason catalog (S19, S21) wrote them, and the Console only
// adds the prefix. Denials use the Gate's own message. Never re-phrase a verdict here.

import { copy, fill } from './copy.ts';
import { decisionOf } from './filters.ts';
import type { ActionDetail, ApprovalDetail, WitnessReason } from './types.ts';

export type Tone = 'verified' | 'mismatch' | 'unverifiable' | 'denied' | 'pending' | 'failed' | 'neutral';

export interface Sentence {
  tone: Tone;
  text: string;
  gaps: string[]; // UNVERIFIABLE: the gap reasons, listed under the sentence
}

/** minutesUntil: "6 min", "1 h 5 min", or "under a minute". */
export function minutesUntil(iso: string, now: Date): string {
  const ms = new Date(iso).getTime() - now.getTime();
  if (ms <= 60_000) return 'under a minute';
  const m = Math.round(ms / 60_000);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
}

function summaryOf(a: ActionDetail): string {
  const w = a.witness;
  if (!w) return '';
  return w.summary ?? w.detail?.summary ?? w.reasons?.find((r) => r.class !== 'note')?.message ?? '';
}

function gapsOf(a: ActionDetail): string[] {
  const rs: WitnessReason[] = a.witness?.reasons ?? a.witness?.detail?.reasons ?? [];
  return rs.filter((r) => r.class === 'gap').map((r) => r.message);
}

/** countersignedCount reads an approval's countersigned (a list for people, a count for keys). */
export function countersignedCount(ap: Pick<ApprovalDetail, 'countersigned'> | null | undefined): number {
  if (!ap) return 0;
  return Array.isArray(ap.countersigned) ? ap.countersigned.length : Number(ap.countersigned ?? 0);
}

export function actionSentence(a: ActionDetail, now: Date, approval?: ApprovalDetail | null): Sentence {
  const s = copy.detail.sentence;
  const w = a.witness;
  if (w && a.state !== 'witnessing') {
    const summary = summaryOf(a);
    if (w.verdict === 'MISMATCH') return { tone: 'mismatch', text: fill(s.mismatch, { summary }), gaps: [] };
    if (w.verdict === 'UNVERIFIABLE') return { tone: 'unverifiable', text: fill(s.unverifiable, { summary }), gaps: gapsOf(a) };
    if (w.level === 'L2') {
      const tpl = (w.method ?? '').startsWith('postgres') ? s.verifiedL2 : s.verifiedL2Other;
      return { tone: 'verified', text: summary ? fill(tpl, { summary }) : tpl.replace(/ · \{summary\}$/, ''), gaps: [] };
    }
    if (w.level === 'L1') return { tone: 'verified', text: summary ? fill(s.verifiedL1, { summary }) : s.verifiedL1.replace(/ · \{summary\}$/, ''), gaps: [] };
    return { tone: 'verified', text: s.verifiedL0, gaps: [] };
  }
  const d = decisionOf(a);
  if (a.status === 'denied' || d === 'deny') {
    if (a.state === 'expired' || a.approval?.status === 'expired') return { tone: 'denied', text: s.expired, gaps: [] };
    if (a.decision?.message) return { tone: 'denied', text: a.decision.message, gaps: [] };
    const rule = a.decision?.rule_id ?? a.policy?.rule_id;
    if (rule) {
      const reason = a.decision?.reason ? `: ${a.decision.reason}` : '.';
      return { tone: 'denied', text: fill(s.deniedRule, { rule, reason }), gaps: [] };
    }
    return { tone: 'denied', text: s.denied, gaps: [] };
  }
  if (a.status === 'pending_approval' || a.state === 'pending_approval') {
    const exp = a.approval?.expires_at ?? approval?.expires_at;
    const mins = exp ? minutesUntil(exp, now) : '';
    if (approval) {
      return { tone: 'pending', text: fill(s.pending, { mins, done: countersignedCount(approval), required: approval.required }), gaps: [] };
    }
    return { tone: 'pending', text: fill(s.pendingShort, { mins }), gaps: [] };
  }
  if (a.state === 'cancelled') return { tone: 'neutral', text: s.cancelled, gaps: [] };
  if (a.state === 'expired') return { tone: 'denied', text: s.expired, gaps: [] };
  if (a.status === 'failed' || a.execution?.status === 'failed') {
    const code = a.execution?.error_code;
    return { tone: 'failed', text: code ? fill(s.failed, { code }) : s.failedNoCode, gaps: [] };
  }
  if (a.state === 'witnessing' || a.state === 'executed') return { tone: 'pending', text: s.witnessing, gaps: [] };
  return { tone: 'neutral', text: fill(s.running, { state: a.state.replace(/_/g, ' ') }), gaps: [] };
}
