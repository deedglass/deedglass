// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest';
import { outcomeText, sqlFactsText, stepGlyph, stepText, testResultView, testSummary, traceView, type PolicyDecision, type PolicyTestResult } from '../src/controllers/traceview.ts';
import denyJson from './fixtures/s26/evaluate-deny.json';
import allowJson from './fixtures/s26/evaluate-allow.json';
import testJson from './fixtures/s26/policy-test.json';
import testFailJson from './fixtures/s26/policy-test-fail.json';

const deny = denyJson as unknown as PolicyDecision;
const allow = allowJson as unknown as PolicyDecision;
const rows = (el: HTMLElement) => [...el.querySelectorAll('.tv-step')].map((r) => `${r.querySelector('.tv-glyph')!.textContent} ${r.querySelector('.tv-text')!.textContent}`);

describe('TraceView', () => {
  it('renders the deny: chip, verbatim message, rule and line, trace, facts, time', () => {
    const onReveal = vi.fn();
    const el = traceView(deny, { onReveal });
    expect(el.getAttribute('aria-label')).toBe('Dry run result');
    expect(el.querySelector('.chip')!.textContent).toBe('Denied');
    expect(el.querySelector('.tv-msg')!.textContent).toBe('Blocked by policy "block-unscoped-writes": UPDATE and DELETE need a WHERE clause.');
    expect(el.querySelector('.tv-rule')!.textContent).toBe('Rule block-unscoped-writes · line 47');
    expect(rows(el)).toEqual([
      '✓ stop · agent active · org running',
      '✓ budget agent-rate · 0 of 30 calls this minute',
      '· rule block-destructive-sql · when → false',
      '· rule block-schema-changes · when → false',
      '✕ rule block-unscoped-writes · when → true',
      '· 13 more rules not evaluated',
    ]);
    expect(el.querySelectorAll('.tv-step .sr-only')[4]!.textContent).toBe('matched: ');
    expect(el.querySelector('.tv-facts')!.textContent).toBe('sql: delete · write · unbounded · public.orders');
    expect(el.querySelector('.tv-time')!.textContent).toBe('evaluated in 48 µs');
    (el.querySelector('button.tv-line') as HTMLButtonElement).click();
    expect(onReveal).toHaveBeenCalledWith(47);
    const ids = [...el.querySelectorAll<HTMLButtonElement>('button.tv-id')];
    expect(ids.map((b) => b.textContent)).toEqual(['block-destructive-sql', 'block-schema-changes', 'block-unscoped-writes']);
    ids[0]!.click();
    expect(onReveal).toHaveBeenLastCalledWith(37);
  });

  it('renders the allow: ✓ on the matching rule, no message, verify level', () => {
    const el = traceView(allow);
    expect(el.querySelector('.chip')!.textContent).toBe('Allowed');
    expect(el.querySelector('.tv-msg')).toBeNull();
    expect(el.querySelector('.tv-rule')!.textContent).toBe('Rule allow-order-status-updates · line 64 · verify L2');
    expect(el.querySelector('button')).toBeNull(); // no onReveal, no buttons
    expect(rows(el)).toContain('✓ rule allow-order-status-updates · when → true');
    expect(rows(el)).toContain("· rule undo-needs-approval · match doesn't cover this action");
    expect(el.querySelector('.tv-facts')!.textContent).toBe('sql: update · write · public.orders');
  });

  it('glyphs: approval ◐, failures ✕, the default decision', () => {
    const d = { decision: 'require_approval' as const, rule_id: 'needs-countersign' };
    expect(stepGlyph({ step: 'rule', id: 'needs-countersign', result: 'match' }, d).g).toBe('◐');
    expect(stepGlyph({ step: 'budget', id: 'b', result: 'fail' }, d).g).toBe('✕');
    expect(stepGlyph({ step: 'rule', id: 'x', result: 'no_match' }, d).g).toBe('·');
    expect(stepGlyph({ step: 'rule', id: 'x', result: 'weird' }, d).g).toBe('○');
    expect(stepText({ step: 'budget', id: 'agent-rate', result: 'fail', detail: '30 of 30 calls this minute' })).toBe('budget agent-rate · 30 of 30 calls this minute');
    const def = traceView({ object: 'policy_decision', decision: 'deny', source: 'default', message: 'No rule allows this action.', trace: [] });
    expect(def.querySelector('.tv-rule')!.textContent).toBe('No rule matched, so the default decision applies.');
    expect(def.querySelector('.tv-trace')).toBeNull();
    expect(sqlFactsText(null)).toBeNull();
    expect(sqlFactsText({ present: false })).toBeNull();
    expect(sqlFactsText({ present: true, kind: 'select', category: 'read', multi: true, tables: ['a', 'b'] })).toBe('sql: select · read · multi-statement · a · b');
  });
});

describe('policy test results', () => {
  const ok = testJson as unknown as PolicyTestResult;
  const fail = testFailJson as unknown as PolicyTestResult;

  it('summaries use S12 copy', () => {
    expect(testSummary(ok)).toEqual({ ok: true, text: '57 cases passed · every rule covered' });
    expect(testSummary(fail)).toEqual({ ok: false, text: '4 failed' });
    expect(testSummary({ ...fail, failed: 1, coverage: { uncovered: ['allow-describe'] } })).toEqual({ ok: false, text: '1 failed · allow-describe has no case' });
    expect(testSummary({ ...ok, passed: 24, coverage: { uncovered: ['allow-describe', 'a', 'b'] } }).text).toBe('24 cases passed · allow-describe and 2 more rules have no case');
    expect(testSummary({ ...ok, passed: 1 }).text).toBe('1 case passed · every rule covered');
    expect(outcomeText({ decision: 'deny', rule: 'block-destructive-sql' })).toBe('deny · block-destructive-sql');
    expect(outcomeText({ decision: 'allow', rule: 'r', verify: 'L0' })).toBe('allow · r · verify L0');
  });

  it('the table lists failed cases first with expected, got, ✓/✕ and the problems', () => {
    const el = testResultView(fail);
    expect(el.querySelector('.pt-sum')!.textContent).toBe('✕4 failed');
    expect(el.querySelector('.pt-sum')!.getAttribute('role')).toBe('status');
    const frame = el.querySelector('.pt-frame')!;
    expect(frame.getAttribute('role')).toBe('region');
    expect(frame.getAttribute('tabindex')).toBe('0');
    expect([...el.querySelectorAll('th')].map((t) => t.textContent)).toEqual(['Case', 'Expected', 'Got', 'Result', 'Detail']);
    const trs = [...el.querySelectorAll('tbody tr')];
    expect(trs).toHaveLength(fail.cases.length);
    expect(trs.slice(0, 4).every((r) => r.classList.contains('pt-fail'))).toBe(true);
    const first = trs[0]!.querySelectorAll('td');
    expect(first[0]!.textContent).toBe("a DROP through the native connector gets the brand's sentenceline 24");
    expect(first[1]!.textContent).toBe('deny · block-destructive-sql');
    expect(first[2]!.textContent).toBe('allow · block-destructive-sql · verify L0');
    expect(first[3]!.textContent).toBe('✕Failed');
    expect([...first[4]!.querySelectorAll('.pt-problem')].map((p) => p.textContent)).toEqual(fail.cases.find((c) => c.line === 24)!.problems);
    expect(testResultView(ok).querySelector('.pt-sum')!.textContent).toBe('✓57 cases passed · every rule covered');
  });
});
