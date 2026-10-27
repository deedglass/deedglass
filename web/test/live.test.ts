// live.ts (console-v1): the live view's words. A plain line per action built from the tool and the
// policy's reason (never the request), the Today counts with and without the server's decisions,
// folded attention, the record link and the offline command.
import { describe, expect, it } from 'vitest';
import { agentActive, fold, outcome, phiValue, plainSummary, recordState, timesWord, todayCounts, toolPhrase, verifyCommand } from '../src/lib/live.ts';
import type { Action, Overview } from '../src/lib/types.ts';
import { read, repo } from './helpers.ts';

const act = (o: Partial<Action> = {}): Action => ({
  object: 'action', id: 'act_1', created_at: '2026-10-27T02:00:00Z', livemode: false, agent: { id: 'agt_1', name: 'support-bot' },
  tool: 'stripe.create_refund', connector: null, mode: 'test', state: 'sealed', status: 'executed',
  decision: { outcome: 'allow', code: null, rule_id: 'r', reason: null }, policy: { id: null, version_id: null, mode: 'enforce', decision: 'allow', rule_id: 'r' },
  execution: { status: 'succeeded', latency_ms: 360, error_code: null }, witness: null, deed: 'deed_1', ...o,
});

describe('toolPhrase', () => {
  it('reads a tool as a short plain phrase', () => {
    expect(toolPhrase('stripe.create_refund')).toBe('Create refund in Stripe');
    expect(toolPhrase('github.merge_pr')).toBe('Merge pr in GitHub');
    expect(toolPhrase('postgres.execute')).toBe('Write to Postgres');
    expect(toolPhrase('acme.do-thing')).toBe('Do thing in acme');
    expect(toolPhrase('bare')).toBe('bare');
    expect(toolPhrase('.x')).toBe('.x');
  });
});

describe('outcome and plainSummary', () => {
  it('says what happened, with the policy reason for a block', () => {
    expect(plainSummary(act())).toBe('Create refund in Stripe · ran in 360\u00a0ms');
    expect(outcome(act({ decision: { outcome: 'deny', code: 'policy_denied', rule_id: 'big', reason: 'Refunds over $100 need a person.' } }))).toEqual({ text: 'blocked: Refunds over $100 need a person.', tone: 'blocked' });
    expect(outcome(act({ decision: { outcome: 'deny', code: 'budget_exceeded', rule_id: null, reason: null } })).text).toBe('blocked: budget_exceeded');
    expect(outcome(act({ state: 'pending_approval', decision: { outcome: 'require_approval', code: null, rule_id: 'r', reason: null }, deed: null })).tone).toBe('held');
    expect(outcome(act({ decision: { outcome: 'require_approval', code: null, rule_id: 'r', reason: null }, approval: { id: 'apr_1', status: 'denied', expires_at: '' } })).text).toBe('a person declined it');
    expect(outcome(act({ decision: { outcome: 'require_approval', code: null, rule_id: 'r', reason: null }, approval: { id: 'apr_1', status: 'approved', expires_at: '' } })).text).toBe('approved, ran in 360\u00a0ms');
    expect(outcome(act({ state: 'failed', execution: { status: 'failed', latency_ms: 5, error_code: 'upstream_500' } }))).toEqual({ text: 'the tool failed (upstream_500)', tone: 'failed' });
    expect(outcome(act({ state: 'executing', execution: null, deed: null })).tone).toBe('running');
  });
});

describe('recordState', () => {
  it('links the sealed record, or says why there is none yet', () => {
    expect(recordState({ deed: 'deed_9', state: 'sealed' })).toEqual({ href: '/app/deeds/deed_9', label: 'record' });
    expect(recordState({ deed: null, state: 'witnessing' }).label).toBe('sealing');
    expect(recordState({ deed: null, state: 'pending_approval' }).label).toBe('later');
    expect(recordState({ deed: null, state: 'sealed' }).label).toBe('none');
  });
});

describe('todayCounts', () => {
  const base = (): Overview => JSON.parse(read(repo('web/test/fixtures/overview.json')));
  it('uses the server decisions when present', () => {
    const o = base();
    o.decisions = { allowed: 500, held: 20 };
    o.approvals.pending = 3;
    expect(todayCounts(o)).toMatchObject({ allowed: 500, held: 20, blocked: o.blocked.total, pending: 3, exact: true });
  });
  it('falls back for an older server', () => {
    const o = base();
    o.approvals.pending = 2;
    const t = todayCounts(o);
    expect(t.exact).toBe(false);
    expect(t.allowed).toBe(o.actions.total - o.blocked.total - 2);
    expect(t.held).toBe(2);
  });
});

describe('fold, agents, Health mode, verify', () => {
  it('folds repeated lines in first-seen order', () => {
    const f = fold([{ title: 'a', detail: 'x' }, { title: 'b', detail: '' }, { title: 'a', detail: 'x' }, { title: 'a', detail: 'y' }]);
    expect(f.map((l) => [l.title, l.detail, l.count])).toEqual([['a', 'x', 2], ['b', '', 1], ['a', 'y', 1]]);
    expect(timesWord(1)).toBe('');
    expect(timesWord(8)).toBe(' · 8 times');
  });
  it('active means an action in the last five minutes', () => {
    const now = new Date('2026-10-27T02:10:00Z');
    expect(agentActive('2026-10-27T02:06:00Z', now)).toBe(true);
    expect(agentActive('2026-10-27T02:04:00Z', now)).toBe(false);
    expect(agentActive(null, now)).toBe(false);
  });
  it('hides values in Health mode and keeps nulls', () => {
    expect(phiValue('shipped', true)).toBe('hidden');
    expect(phiValue(null, true)).toBeNull();
    expect(phiValue('shipped', false)).toBe('shipped');
  });
  it('builds the offline command the deed panel copies', () => {
    expect(verifyCommand('deed_1', 'http://127.0.0.1:8080')).toBe('deedglass verify deed_1.json --keys http://127.0.0.1:8080/.well-known/deedglass/keys.json');
  });
});
