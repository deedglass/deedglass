// types.ts is hand-written: every field the Console reads must exist in what the Go server sends.
// The action and agent shapes come from the CLI goldens; the rest from the Go structs' json tags.
import { describe, expect, it } from 'vitest';
import { read, repo } from './helpers.ts';

function goldenObject(path: string, object: string): Record<string, unknown> {
  const text = read(repo(path));
  const start = text.indexOf('{');
  const body = text.slice(start, text.lastIndexOf('}') + 1);
  const root = JSON.parse(body.split('\n--- stderr')[0]!);
  const found = root.object === object ? root : (root.data ?? []).find((x: { object: string }) => x.object === object);
  if (!found) throw new Error(`${path} has no ${object}`);
  return found;
}

function tags(path: string, typeName: string): Set<string> {
  const src = read(repo(path));
  const start = src.indexOf(`type ${typeName} struct {`);
  if (start < 0) throw new Error(`${path} has no type ${typeName}`);
  const end = src.indexOf('\n}', start);
  return new Set([...src.slice(start, end).matchAll(/json:"([a-z_]+)/g)].map((m) => m[1]!));
}

const READS = {
  action: ['object', 'id', 'created_at', 'livemode', 'agent', 'tool', 'connector', 'mode', 'state', 'status', 'decision', 'policy', 'execution', 'witness', 'deed'],
  actionWitness: ['object', 'level', 'verdict', 'method', 'required_level', 'detail_digest'],
  agent: ['object', 'id', 'name', 'description', 'status', 'active_keys', 'last_used_at', 'created_at'],
  approval: ['object', 'id', 'action', 'agent', 'tool', 'status', 'rule_id', 'required', 'summary', 'created_at', 'expires_at'],
  me: ['object', 'user', 'org', 'membership', 'orgs', 'session', 'csrf_token'],
  org: ['object', 'id', 'name', 'slug', 'timezone', 'status', 'created_at', 'stop'],
  orgStop: ['stopped', 'stopped_at', 'stopped_by', 'reason'],
  orgStopResult: ['object', 'org', 'stopped', 'stopped_at', 'stopped_by', 'reason', 'agents_affected', 'deed'],
  meOrg: ['id', 'name', 'role'],
  // S25: the action detail, the witness detail's L2 evidence, approvals, undo and the ledger check.
  actionDetail: ['on_behalf_of', 'session', 'approval', 'cost', 'request_digest', 'result_digest', 'payload_pruned_at', 'request', 'result', 'events', 'deeds', 'undo'],
  l2: ['transaction', 'expected', 'observed', 'rows', 'rows_shown', 'rows_more', 'columns_more', 'redacted', 'frames'],
  l2Row: ['table', 'op', 'key', 'expected', 'cells'],
  undo: ['undoes', 'undone_by', 'pending', 'eligible'],
  approvalDetail: ['countersigned', 'denied_by', 'decision_reason', 'decided_at', 'deed', 'reason', 'approvers'],
  countersign: ['user', 'at', 'via', 'reason', 'deed'],
  ledgerCheck: ['object', 'status', 'checked', 'first_seq', 'last_seq', 'head', 'seal_keys', 'break', 'next_from_seq', 'checked_at'],
  chainBreak: ['seq', 'deed', 'code', 'message', 'next'],
};

describe('types.ts matches the server', () => {
  it('action (CLI golden)', () => {
    const a = goldenObject('cmd/deedglass/testdata/ledger/actions_list_json.golden', 'action');
    for (const k of READS.action) expect(k in a, `action.${k}`).toBe(true);
    for (const k of READS.actionWitness) expect(k in (a.witness as object), `action.witness.${k}`).toBe(true);
  });
  it('agent (CLI golden)', () => {
    const a = goldenObject('cmd/deedglass/testdata/agent/create_json.golden', 'agent');
    for (const k of READS.agent) expect(k in a, `agent.${k}`).toBe(true);
  });
  it('approval, me, org and the stop objects (Go structs)', () => {
    const check = (keys: string[], path: string, type: string) => {
      const t = tags(path, type);
      for (const k of keys) expect(t.has(k), `${type}.${k}`).toBe(true);
    };
    check(READS.approval, 'internal/approve/approval.go', 'Approval');
    check(READS.me, 'internal/identity/api.go', 'Me');
    check(READS.meOrg, 'internal/identity/api.go', 'MeOrg');
    check(READS.org, 'internal/identity/org.go', 'Org');
    check(READS.orgStop, 'internal/identity/org.go', 'OrgStopState');
    check(READS.orgStopResult, 'internal/identity/agents.go', 'OrgStop');
  });
  it('S25 shapes: the real action detail and the Go structs', () => {
    const d = JSON.parse(read(repo('web/test/fixtures/action-mismatch-l2.json')));
    for (const k of READS.actionDetail) expect(k in d, `action.${k}`).toBe(true);
    const l2 = d.witness.detail.levels.L2;
    for (const k of READS.l2) expect(k in l2, `levels.L2.${k}`).toBe(true);
    for (const k of READS.l2Row) expect(k in l2.rows[0], `levels.L2.rows[].${k}`).toBe(true);
    const check = (keys: string[], path: string, type: string) => {
      const t = tags(path, type);
      for (const k of keys) expect(t.has(k), `${type}.${k}`).toBe(true);
    };
    check(READS.undo, 'internal/ledger/read.go', 'ActionUndo');
    check(READS.approvalDetail, 'internal/approve/approval.go', 'Approval');
    check(READS.countersign, 'internal/approve/approval.go', 'Countersign');
    check(READS.ledgerCheck, 'internal/ledger/check.go', 'ChainCheck');
    check(READS.chainBreak, 'internal/ledger/check.go', 'ChainBreak');
  });
});
