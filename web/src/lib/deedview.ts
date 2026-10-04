// deedview.ts: what a deed says, in words, from its parsed body (S25 §3.6). Pure. The summary is
// one line per deed for the list; sections() is contract §7's body as labelled rows for the view.

import { shortId } from './format.ts';
import type { DeedBody } from './types.ts';

export function deedSummary(b: DeedBody): string {
  if (b.kind === 'admin' && b.admin) {
    const who = b.admin.actor?.name ?? b.admin.actor?.id ?? '';
    const target = b.admin.target ? ` · ${shortId(b.admin.target.id)}` : '';
    return `${b.admin.action}${target}${who ? ` · by ${who}` : ''}`;
  }
  const parts: string[] = [];
  if (b.kind === 'amendment' && b.amends) parts.push(`Amends ${shortId(b.amends)}`);
  if (b.action) parts.push(b.action.tool);
  if (b.agent) parts.push(b.agent.name ?? b.agent.id);
  if (b.witness) parts.push(`${b.witness.level} · ${b.witness.verdict}`);
  else if (b.policy?.decision === 'deny') parts.push('Denied');
  else if (b.execution?.status) parts.push(b.execution.status);
  return parts.join(' · ');
}

export type Row = [label: string, value: string | null, kind?: 'mono' | 'id' | 'hash' | 'time' | 'text'];

export interface Section { key: string; rows: Row[] }

const id = (v: string | null | undefined): string | null => (v ? v : null);

/** sections: Who, Action, Policy, Approval, Execution, Witness, Cost, Admin, Chain (contract §7). */
export function sections(b: DeedBody): Section[] {
  const out: Section[] = [];
  const who: Row[] = [];
  if (b.agent) who.push(['Agent', b.agent.name ? `${b.agent.name} (${b.agent.id})` : b.agent.id, 'mono']);
  who.push(['On behalf of', b.on_behalf_of, 'mono'], ['Session', b.session, 'mono']);
  out.push({ key: 'who', rows: who });
  if (b.action) {
    out.push({ key: 'action', rows: [
      ['Action', b.action.id, 'id'], ['Tool', b.action.tool, 'mono'], ['Connector', b.action.connector, 'mono'], ['Mode', b.action.mode],
      ['Request digest', b.action.request_digest, 'hash'], ['Intent', b.action.intent_signed ? `signed · ${b.action.intent_hash ?? ''}` : 'not signed', b.action.intent_signed ? 'mono' : 'text'],
    ] });
  }
  if (b.policy) {
    out.push({ key: 'policy', rows: [
      ['Policy', id(b.policy.id), 'id'], ['Version', id(b.policy.version_id), 'id'], ['Version hash', b.policy.version_hash, 'hash'],
      ['Mode', b.policy.mode], ['Decision', b.policy.decision], ['Rule', b.policy.rule_id, 'mono'],
    ] });
  }
  if (b.approval) {
    const a = b.approval as Record<string, unknown>;
    out.push({ key: 'approval', rows: [
      ['Approval', typeof a.id === 'string' ? a.id : null, 'id'], ['Outcome', typeof a.outcome === 'string' ? a.outcome : null],
      ['Decided at', typeof a.decided_at === 'string' ? a.decided_at : null, 'time'], ['Decision deed', typeof a.deed === 'string' ? a.deed : null, 'id'],
    ] });
  }
  if (b.execution) {
    out.push({ key: 'execution', rows: [
      ['Status', b.execution.status], ['Started', b.execution.started_at, 'time'], ['Finished', b.execution.finished_at, 'time'],
      ['Latency', b.execution.latency_ms === null ? null : `${b.execution.latency_ms} ms`], ['Result digest', b.execution.result_digest, 'hash'], ['Error', b.execution.error_code, 'mono'],
    ] });
  }
  if (b.witness) {
    out.push({ key: 'witness', rows: [['Level', b.witness.level], ['Verdict', b.witness.verdict], ['Method', b.witness.method, 'mono'], ['Detail digest', b.witness.detail_digest, 'hash']] });
  }
  if (b.cost) out.push({ key: 'cost', rows: [['Amount', `${b.cost.amount} ${b.cost.currency} (minor units)`]] });
  if (b.admin) {
    out.push({ key: 'admin', rows: [
      ['Action', b.admin.action, 'mono'], ['Actor', `${b.admin.actor.type} · ${b.admin.actor.name ?? b.admin.actor.id}`],
      ['Target', b.admin.target ? `${b.admin.target.type} · ${b.admin.target.id}` : null, 'mono'], ['Reason', b.admin.reason],
      ['Data digest', b.admin.data_digest, 'hash'], ['Event', b.admin.event, 'mono'],
    ] });
  }
  out.push({ key: 'chain', rows: [
    ['Seq', String(b.seq)], ['Previous hash', b.prev_hash, 'hash'], ['Issued', b.issued_at, 'time'], ['Seal key', b.seal_key_id, 'mono'], ['Amends', b.amends, 'id'],
  ] });
  return out.map((s) => ({ ...s, rows: s.rows.filter((r) => r[1] !== null && r[1] !== '') }));
}
