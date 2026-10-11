// S26 §6 Vitest: the approval, policy and connector models on the real API answers captured from
// the seeded stack (test/fixtures/s26).
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { decideState, effectLine, emailChips, emailOf, fourEyes, raceSentence, sortPending, sqlOf, validDenyReason, whereOf, type ApprovalContext, type ApprovalFull } from '../src/lib/models/approval.ts';
import { dirty, dryRunInput, hasDefaultAllow, headerLine, shadowEvidence, statusOf, strictJSON, testSummary } from '../src/lib/models/policy.ts';
import { bytes, credentialLine, healthOf, levelsOf, witnessLine, witnessState, type DecoderView } from '../src/lib/models/connector.ts';

const fx = (n: string) => JSON.parse(readFileSync(new URL(`./fixtures/s26/${n}.json`, import.meta.url), 'utf8'));
const approval = fx('approval') as ApprovalFull;
const sp = (x: string | null) => (x ?? '').replace(/\u00a0/g, ' ');

describe('approval model', () => {
  it('sorts pending by expiry, soonest first', () => {
    const a = { ...approval, id: 'apr_a', expires_at: '2026-10-09T09:50:00Z', created_at: '2026-10-09T09:40:00Z' };
    const b = { ...approval, id: 'apr_b', expires_at: '2026-10-09T09:45:00Z', created_at: '2026-10-09T09:41:00Z' };
    const c = { ...approval, id: 'apr_c', expires_at: '2026-10-09T09:45:00Z', created_at: '2026-10-09T09:39:00Z' };
    expect(sortPending([a, b, c]).map((x) => x.id)).toEqual(['apr_c', 'apr_b', 'apr_a']);
  });

  it('builds the effect line per connector type from server context', () => {
    expect(sp(effectLine(approval))).toBe('INSERT ~1 row in public.refunds');
    const stripe = { ...approval, tool: 'stripe.create_refund', context: { ...approval.context!, sql: null, action: { ...approval.context!.action, connector_type: 'stripe' }, sections: { stripe: { amount_display: '$120.00', customer: 'cus_Q3…' } } } };
    expect(effectLine(stripe)).toBe('$120.00 to cus_Q3…');
    const gh = { ...approval, tool: 'github.merge_pr', context: { ...approval.context!, sql: null, sections: {}, args: { pr: 212, repo: 'acme/api' }, action: { ...approval.context!.action, connector_type: 'github', side_effect: 'write' } } };
    expect(effectLine(gh)).toBe('merge PR #212 in acme/api');
    const generic = { ...approval, tool: 'notion.create_page', context: { ...approval.context!, sql: null, sections: {}, args: {}, action: { ...approval.context!.action, connector_type: 'notion', side_effect: 'write' } } };
    expect(effectLine(generic)).toBe('write · notion.create_page');
  });

  it('shows the statement, the estimate as an estimate, and the WHERE clause', () => {
    const s = sqlOf(approval)!;
    expect(s.statement).toMatch(/^INSERT INTO public\.refunds/);
    expect(sp(s.estimate)).toBe('≈ 1 row (EXPLAIN estimate)');
    expect(whereOf("UPDATE public.orders SET status = 'shipped' WHERE id = 42")).toBe('id = 42');
    expect(whereOf('INSERT INTO t VALUES (1)')).toBeNull();
  });

  it('shapes the buttons from you.can_decide and you.why_not', () => {
    const now = new Date('2026-10-04T03:40:00Z');
    expect(decideState(approval, { id: 'usr_me' }, now).kind).toBe('can');
    const self = { ...approval, you: { eligible: true, can_decide: true, why_not: 'This action runs on your behalf, so another approver has to countersign it. You can still deny it.' } };
    expect(decideState(self, { id: 'usr_me' }, now)).toMatchObject({ kind: 'self' });
    const voted = { ...approval, required: 2, countersigned: [{ user: { id: 'usr_me', name: 'Arjun Mehta' }, at: '2026-10-04T03:38:00Z', via: 'console', reason: null, deed: 'deed_x' }] };
    expect(decideState(voted, { id: 'usr_me' }, now, 'UTC')).toEqual({ kind: 'already', text: 'You countersigned at 03:38. Waiting for one more approver.' });
    const notEligible = { ...approval, you: { eligible: false, can_decide: false, why_not: 'Only role:admin can countersign under orders-writes-need-approval.' } };
    expect(decideState(notEligible, { id: 'usr_v' }, now)).toEqual({ kind: 'not_eligible', text: 'Only role:admin can countersign under orders-writes-need-approval.' });
    expect(decideState({ ...approval, expires_at: '2026-10-04T03:39:00Z' }, null, now).kind).toBe('closed');
    expect(decideState({ ...approval, status: 'denied' }, null, now)).toEqual({ kind: 'closed', text: 'Denied · record sealed' });
  });

  it('writes four-eyes progress and the race sentence', () => {
    const v = { ...approval, required: 2, countersigned: [{ user: { id: 'u1', name: 'Priya Raman' }, at: 'x', via: 'console', reason: null, deed: 'd' }] };
    expect(fourEyes(v)).toBe('1 of 2 countersigned · Priya Raman');
    expect(fourEyes(approval)).toBeNull();
    const won = { ...v, status: 'approved' as const, decided_at: '2026-10-04T03:40:00Z' };
    expect(sp(raceSentence(won, new Date('2026-10-04T03:40:20Z')))).toBe('Priya Raman approved this 20 seconds ago.');
  });

  it('a deny reason is 3 to 280 characters', () => {
    expect(validDenyReason('no')).toBe(false);
    expect(validDenyReason('  ok  ')).toBe(false);
    expect(validDenyReason('Refund already issued')).toBe(true);
    expect(validDenyReason('x'.repeat(281))).toBe(false);
  });
});

describe('policy model', () => {
  it('reads the status and the header line', () => {
    const p = fx('policy');
    expect(statusOf(p)).toBe('enforced');
    const v = fx('policy-versions').data[0];
    expect(headerLine(v)).toBe('v1 · sha256:23e1…5284');
  });
  it('tracks dirty buffers with CRLF as LF', () => {
    expect(dirty('a\r\nb', 'a\nb')).toBe(false);
    expect(dirty('a\nb\n', 'a\nb')).toBe(true);
  });
  it('builds the dry-run input and reports JSON errors by line', () => {
    const ok = dryRunInput({ tool: 'postgres.execute', argsText: '', sql: 'DELETE FROM public.orders', agent: 'support-bot', onBehalfOf: '', at: '' }, 'postgres');
    expect(ok).toEqual({ input: { tool: { name: 'postgres.execute', connector_type: 'postgres' }, sql: 'DELETE FROM public.orders', agent: { name: 'support-bot' } } });
    expect(dryRunInput({ tool: '', argsText: '', sql: '', agent: '', onBehalfOf: '', at: '' })).toHaveProperty('error');
    const bad = strictJSON('{\n  "a": 1,\n  "b": \n}');
    expect(bad).toHaveProperty('error');
    expect(strictJSON('{"a": 1, "a": 2}')).toEqual({ error: 'The arguments repeat the key “a”. Each key may appear once.' });
    expect(strictJSON('{"a": {"a": 1}}')).toEqual({ value: { a: { a: 1 } } });
    expect(dryRunInput({ tool: 'x', argsText: '[1]', sql: '', agent: '', onBehalfOf: '', at: '' })).toHaveProperty('error');
  });
  it('summarises tests with S12 copy', () => {
    expect(testSummary(fx('policy-test'))).toBe('57 cases passed · every rule covered');
    expect(testSummary(fx('policy-test-fail'))).toMatch(/^4 failed · /);
    expect(testSummary({ passed: 0, failed: 1, coverage: { uncovered: ['allow-describe'], covered: 0, total: 1, rules: [] } })).toBe('1 failed · allow-describe has no case');
  });
  it('finds default_allow and writes the shadow evidence', () => {
    expect(hasDefaultAllow({ warnings: [] }, 'defaults:\n  decision: allow\n')).toBe(true);
    expect(hasDefaultAllow({ warnings: [] }, 'defaults:\n  decision: deny\n')).toBe(false);
    expect(shadowEvidence(3, 212, false)).toBe('Shadow disagreed on 3 of 212 actions in the last 24 h');
    expect(shadowEvidence(100, 4000, true)).toBe('Shadow disagreed on 100+ of 4000 actions in the last 24 h');
  });
});

describe('connector model', () => {
  const pg = fx('connector-postgres');
  const dec = fx('decoder-postgres') as DecoderView;
  it('reads health, levels and the credential row', () => {
    expect(healthOf(pg)).toBe('unknown');
    expect(levelsOf(pg)).toEqual(['L0', 'L1', 'L2']);
    const now = new Date(new Date(pg.credentials.updated_at).getTime() + 2 * 3600_000);
    expect(credentialLine(pg.credentials, now)).toBe('Set · dsn · deedglass_agent@127.0.0.1:55469/acme · updated 2h ago');
    expect(credentialLine(null)).toBe('Not set');
  });
  it('maps the decoder to the Witness card', () => {
    expect(witnessState(dec)).toBe('healthy');
    expect(witnessState({ ...dec, health: 'degraded' })).toBe('lagging');
    expect(witnessState({ ...dec, wal_status: 'lost' })).toBe('invalidated');
    expect(witnessState({ ...dec, health: 'pending' })).toBe('inactive');
    expect(witnessState(null)).toBe('not_created');
    const now = new Date(new Date(dec.last_commit!.at!).getTime() + 4000);
    expect(witnessLine({ ...dec, lag: { ...dec.lag, bytes: 12 * 1024 * 1024 } }, now)).toBe('Check · healthy · 12 MB behind · last commit 4s ago');
    expect(bytes(432013312)).toBe('412 MB');
  });
});

describe('default_allow detection', () => {
  it('reads only the top-level defaults block', () => {
    const deny = 'name: x\ndefaults:\n  decision: deny\n  reason: No.\n\nrules:\n  - id: a\n    decision: allow\n';
    expect(hasDefaultAllow({ warnings: [] }, deny)).toBe(false);
    expect(hasDefaultAllow({ warnings: [] }, 'defaults:\n  reason: Yes.\n  decision: allow\nrules: []\n')).toBe(true);
    expect(hasDefaultAllow({ warnings: [] }, 'defaults: { decision: allow }\n')).toBe(true);
  });
});

describe('S40 email_preview', () => {
  const ctx = {
    v: 1, action: { id: 'act_1', tool: 'mail.send', connector: 'mail', connector_type: 'outbound_mail', side_effect: 'write', mode: 'gateway', request_digest: 'sha256:x' },
    agent: { id: 'agt_1', name: 'sender-m1' }, on_behalf_of: null, policy: { name: 'growth-outreach', version: 2, rule_id: 'send-needs-countersign', required: 1 },
    args: null, args_note: null, sql: null,
    sections: { email_preview: { from: 'arjun@trydeedglass.example', to: 'maya@acme-robotics.example', subject: 'your agent and Postgres',
      body_text: 'Hi Maya,\n\nOne question?\n', words: 96, links: 0, questions: 1, step: 1, threaded: false,
      window: { local: 'Wed 21 Oct, 08:30 to 10:30 PDT', ist: '21:00 to 23:00 IST' },
      review: { company: 'Acme Robotics', lint_summary: '14 of 14', judge_summary: 'flag only · 6 of 6' } } },
  } as unknown as ApprovalContext;
  it('reads the section and words the effect line', () => {
    const a = { tool: 'mail.send', context: ctx };
    expect(emailOf(a)?.subject).toBe('your agent and Postgres');
    expect(effectLine(a)).toBe('Email to Acme Robotics · your agent and Postgres');
    expect(emailChips(emailOf(a)!)).toBe('96 words · 0 links · 1 question · lints 14 of 14 · judge flag only · 6 of 6');
  });
  it('ignores actions without the section', () => {
    expect(emailOf({ context: { ...ctx, sections: {} } })).toBeNull();
  });
});

describe('S40 one tap per email', () => {
  it('the Approvals screen and /approve have no bulk-approve control', () => {
    for (const f of ['../src/controllers/approvals.ts', '../../internal/approve/pages/approve.html', '../../internal/approve/pages/approve.js']) {
      const src = readFileSync(new URL(f, import.meta.url), 'utf8').toLowerCase();
      for (const bad of ['approve all', 'approve selected', 'select all', "type: 'checkbox'", 'type="checkbox"', 'bulk']) {
        expect(src.includes(bad), `${f} holds "${bad}"`).toBe(false);
      }
    }
  });
});
