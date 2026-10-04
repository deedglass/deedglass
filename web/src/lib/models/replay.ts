// models/replay.ts: the Console's view of S27's replay object (internal/learn/replay_jobs.go
// Replay, with the result fields flattened in once it succeeded), as shipped
// (test/fixtures/s27/replay-*.json). The sentences mirror internal/learn/copy.go word for word
// (ReplaySummaryLine, HumanOutcomeLine, UnknownLine), so the CLI, the API and the Console agree.

import { fill } from '../copy.ts';
import { policiesCopy } from '../copy-policies.ts';
import { date, number, shortHash, time } from '../format.ts';

const c = policiesCopy.replay;
const L = c.lines;

export type ReplayStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';
export type Decision = 'allow' | 'deny' | 'require_approval' | string;

export interface ReplaySummary { actions: number; evaluated: number; unchanged: number; changed: number; rule_changed: number; unknown: number }
export interface HumanOutcome { approved: number; denied: number; expired: number; cancelled: number; pending: number; not_requested?: number }
export interface Transition {
  from: Decision;
  to: Decision;
  count: number;
  human_outcome?: HumanOutcome | null;
  by_rule: { rule_id: string; count: number }[] | null;
  by_tool: { tool: string; count: number }[] | null;
  samples: string[] | null;
}
export interface RuleChange { decision: Decision; from_rule: string; to_rule: string; count: number; samples: string[] | null }
export interface UnknownCounts { input_pruned: number; input_missing: number; input_version_unsupported: number; input_unreadable: number; evaluation_time_limit: number }
export interface SQLGuardDrift { recorded: string[] | null; current: string }

export interface Replay {
  object: 'replay';
  id: string;
  status: ReplayStatus | string;
  purpose: string;
  candidate: { policy?: string; name: string; version?: number; version_id?: string; version_hash: string; source_hash?: string };
  baseline: { type: 'recorded' | 'version' | string; version_id?: string; version?: number; version_hash?: string };
  window: { from: string; to: string };
  progress: { processed: number; total: number | null; percent: number };
  requested_by: { type: string; id: string };
  attempts: number;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  error: { code: string; message: string } | null;
  summary?: ReplaySummary;
  transitions?: Transition[] | null;
  rule_changes?: RuleChange[] | null;
  unknown?: UnknownCounts;
  sqlguard?: SQLGuardDrift;
  result_digest?: string;
  elapsed_ms?: number;
}

/** The body of POST /v1/policies/{id}/versions/{v}/replay. */
export interface ReplayRequest { from: string; to: string; baseline: { type: 'recorded' } | { type: 'version'; version: number } }

export type WindowChoice = '24h' | '7d' | 'custom';

export const isActive = (s: string): boolean => s === 'queued' || s === 'running';
export const isFinished = (s: string): boolean => s === 'succeeded' || s === 'failed' || s === 'cancelled';

/** The poll interval while a replay is queued or running (S27: 500 ms). */
export const POLL_MS = 500;

/** windowOf turns the segmented choice into from/to (ISO); custom passes its own values through. */
export function windowOf(choice: WindowChoice, now: Date, custom?: { from: string; to: string }): { from: string; to: string } {
  if (choice === 'custom') return { from: custom?.from ?? '', to: custom?.to || now.toISOString() };
  const span = choice === '24h' ? 86_400_000 : 7 * 86_400_000;
  return { from: new Date(now.getTime() - span).toISOString(), to: now.toISOString() };
}

/** requestBody: baseline 0 means the recorded decisions; any other number is that version. */
export function requestBody(w: { from: string; to: string }, baselineVersion: number): ReplayRequest {
  return { from: w.from, to: w.to, baseline: baselineVersion > 0 ? { type: 'version', version: baselineVersion } : { type: 'recorded' } };
}

/** progressText: "Replaying 5,890 actions · 41%", or the queued sentence before a runner claims it. */
export function progressText(r: Pick<Replay, 'status' | 'progress'>): string {
  const n = number(r.progress.total ?? r.progress.processed);
  if (r.status === 'queued') return fill(c.queued, { n });
  return fill(c.progress, { n, p: clampPercent(r.progress.percent) });
}

export function clampPercent(p: number): number {
  return Math.max(0, Math.min(100, Math.round(Number.isFinite(p) ? p : 0)));
}

/** summaryLine is ReplaySummaryLine: "453 decisions would change · 15 by a different rule · 5,422 unchanged · 0 unknown". */
export function summaryLine(s: ReplaySummary): string {
  const parts = [fill(s.changed === 1 ? L.changedOne : L.changedMany, { n: number(s.changed) })];
  if (s.rule_changed > 0) parts.push(fill(L.ruleChanged, { n: number(s.rule_changed) }));
  parts.push(fill(L.unchanged, { n: number(s.unchanged) }), fill(L.unknown, { n: number(s.unknown) }));
  return parts.join(' · ');
}

/** humanOutcomeLine is HumanOutcomeLine: "humans approved 397, expired 5, denied 1, pending 8". */
export function humanOutcomeLine(h: HumanOutcome | null | undefined): string {
  if (!h) return '';
  const parts = [fill(L.human, { approved: number(h.approved), expired: number(h.expired), denied: number(h.denied) })];
  if (h.pending > 0) parts.push(fill(L.humanPending, { n: number(h.pending) }));
  if (h.cancelled > 0) parts.push(fill(L.humanCancelled, { n: number(h.cancelled) }));
  if ((h.not_requested ?? 0) > 0) parts.push(fill(L.humanNotRequested, { n: number(h.not_requested!) }));
  return parts.join(', ');
}

/** unknownLine is UnknownLine: "12 inputs pruned by retention · 2 recorded before inputs were stored". */
export function unknownLine(u: UnknownCounts | undefined): string {
  if (!u) return '';
  const parts: string[] = [];
  const add = (n: number, one: string, many: string) => { if (n > 0) parts.push(fill(n === 1 ? one : many, { n: number(n) })); };
  add(u.input_pruned, L.prunedOne, L.prunedMany);
  add(u.input_missing, L.missing, L.missing);
  add(u.input_version_unsupported, L.unsupportedOne, L.unsupportedMany);
  add(u.input_unreadable, L.unreadableOne, L.unreadableMany);
  add(u.evaluation_time_limit, L.timeLimitOne, L.timeLimitMany);
  return parts.join(' · ');
}

/** sqlguardDrift: the classifier versions recorded with the decisions differ from today's. */
export function sqlguardDrift(g: SQLGuardDrift | undefined): boolean {
  if (!g || !g.current) return false;
  return (g.recorded ?? []).some((v) => v !== g.current);
}

/** driftLine: "SQL facts were recomputed with sqlguard 2. The recorded decisions used 1, 2." */
export function driftLine(g: SQLGuardDrift): string {
  return fill(c.drift, { current: g.current, recorded: [...(g.recorded ?? [])].sort().join(', ') });
}

/** shortDigest: "sha256:786a…f22c". */
export const shortDigest = (d: string): string => shortHash(d);

/** transitionsSorted: largest first, then by from and to, so the order never depends on the server's. */
export function transitionsSorted(t: Transition[] | null | undefined): Transition[] {
  return [...(t ?? [])].sort((a, b) => b.count - a.count || a.from.localeCompare(b.from) || a.to.localeCompare(b.to));
}

export function ruleChangesSorted(r: RuleChange[] | null | undefined): RuleChange[] {
  return [...(r ?? [])].sort((a, b) => b.count - a.count || a.decision.localeCompare(b.decision) || a.from_rule.localeCompare(b.from_rule) || a.to_rule.localeCompare(b.to_rule));
}

/** rulesOf: the candidate's deciding rules, largest first ("" is the policy's default). */
export function rulesOf(t: Transition): { rule_id: string; count: number }[] {
  return [...(t.by_rule ?? [])].sort((a, b) => b.count - a.count || a.rule_id.localeCompare(b.rule_id));
}

export function toolsOf(t: Transition): { tool: string; count: number }[] {
  return [...(t.by_tool ?? [])].sort((a, b) => b.count - a.count || a.tool.localeCompare(b.tool));
}

/** samplesOf: the first 20 by decision time, as the server ordered them. */
export const samplesOf = (s: string[] | null | undefined): string[] => (s ?? []).slice(0, 20);

/** baselineLabel: "recorded decisions" or "v1". */
export function baselineLabel(b: Replay['baseline']): string {
  return b.type === 'version' && b.version ? fill(c.againstVersion, { v: b.version }) : c.againstRecorded;
}

/** windowText: "27 Sept 2026 07:16 to 4 Oct 2026 07:16 UTC" (the org's zone, named once). */
export function windowText(w: { from: string; to: string }, tz: string): string {
  const at = (iso: string) => `${date(iso, tz)} ${time(iso, tz)}`;
  return fill(c.windowRange, { from: at(w.from), to: at(w.to), tz });
}

/** cancelledText: how far a cancelled replay got. */
export function cancelledText(r: Pick<Replay, 'progress'>): string {
  if (!r.progress.processed) return c.cancelledBefore;
  return fill(c.cancelled, { n: number(r.progress.processed), total: number(r.progress.total ?? r.progress.processed) });
}
