// deedview.ts: deed summaries and contract §7 sections from real deed bodies.
import { describe, expect, it } from 'vitest';
import { deedSummary, sections } from '../src/lib/deedview.ts';
import type { DeedBody } from '../src/lib/types.ts';

const action: DeedBody = {
  v: 1, kind: 'action', deed_id: 'deed_01m427zt7h344praz6npt12dbz', org: 'org_01m427ae0hwncfmv51s0s5wmbw', seq: 617, prev_hash: 'sha256:' + 'a'.repeat(64), issued_at: '2026-10-04T01:20:54.001Z',
  agent: { id: 'agt_01m427zpyhtap8504sca4a6ykz', name: 'support-bot' }, on_behalf_of: null, session: null,
  action: { id: 'act_01m427zt6m8q1j435va4dfmyc3', tool: 'postgres.execute', connector: null, mode: 'gateway', request_digest: 'sha256:' + 'b'.repeat(64), intent_hash: null, intent_signed: false },
  policy: { id: 'pol_01m427zszefdzm8a8qf4zxmjwv', version_id: 'pv_01m427zszfyba2y7c0wm00ar1k', version_hash: 'sha256:' + 'c'.repeat(64), mode: 'enforce', decision: 'allow', rule_id: 'allow-order-status-updates' },
  approval: null, execution: { status: 'succeeded', started_at: null, finished_at: null, latency_ms: 3, result_digest: null, error_code: null },
  witness: { level: 'L2', verdict: 'MISMATCH', method: 'postgres.pgoutput', detail_digest: 'sha256:' + 'd'.repeat(64) }, cost: null, admin: null, amends: null, seal_key_id: 'sk_01m427zpbe7k9cwt16pqa5x5f7',
};

describe('deedview', () => {
  it('summaries', () => {
    expect(deedSummary(action)).toBe('postgres.execute · support-bot · L2 · MISMATCH');
    const admin: DeedBody = { ...action, kind: 'admin', action: null, agent: null, policy: null, witness: null, execution: null,
      admin: { action: 'policy.activated', actor: { id: 'usr_x', type: 'user', name: 'Priya Raman' }, target: { id: 'pol_01m427zszefdzm8a8qf4zxmjwv', type: 'policy' }, data_digest: null, event: 'adm_x', reason: null } };
    expect(deedSummary(admin)).toBe('policy.activated · pol_01m4…mjwv · by Priya Raman');
    expect(deedSummary({ ...action, kind: 'amendment', amends: 'deed_01m427zt7h344praz6npt12dbz' })).toBe('Amends deed_01m4…2dbz · postgres.execute · support-bot · L2 · MISMATCH');
  });
  it('sections follow contract §7 and drop empty rows', () => {
    const s = sections(action);
    expect(s.map((x) => x.key)).toEqual(['who', 'action', 'policy', 'execution', 'witness', 'chain']);
    expect(s.find((x) => x.key === 'chain')!.rows.map((r) => r[0])).toEqual(['Seq', 'Previous hash', 'Issued', 'Seal key']);
    expect(s.find((x) => x.key === 'who')!.rows).toEqual([['Agent', 'support-bot (agt_01m427zpyhtap8504sca4a6ykz)', 'mono']]);
  });
});
