// @vitest-environment happy-dom
// S28 §3.8: the Insights fetchers, the error envelope, the pure helpers and the views, on real
// answers captured from a seed-7 simulated org (test/fixtures/s28; verbatim).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, configure } from '../src/lib/api.ts';
import { copy } from '../src/lib/copy.ts';
import { insightsCopy as c } from '../src/lib/copy-insights.ts';
import { shortId } from '../src/lib/format.ts';
import {
  accept, acceptBody, acceptToast, acknowledge, agreementLine, agreementPct, diffLines, earlyNotice, evidenceRows, fillDays, generate, getSettings, getShadow,
  getStats, isAdvisory, isSimulated, kindLabel, listAnomalies, listSuggestions, mixLabel, outcomeLine, patchSettings, patternText, phrasingNote, reject,
  replayDays, resolve, toLineDiff, waitLabel, waitText,
  type AnomalyList, type InsightsSettings, type ShadowReport, type Stats, type Suggestion,
} from '../src/lib/models/insights.ts';
import { anomalyRow, shadowCard, suggestionCard } from '../src/controllers/insights.ts';
import { trendsView } from '../src/controllers/insightscharts.ts';
import suggestionsJson from './fixtures/s28/suggestions.json';
import shadowJson from './fixtures/s28/shadow.json';
import shadowV3Json from './fixtures/s28/shadow-v3.json';
import anomaliesJson from './fixtures/s28/anomalies.json';
import statsJson from './fixtures/s28/stats.json';
import settingsJson from './fixtures/s28/settings.json';
import acceptJson from './fixtures/s28/accept.json';
import rejectNoReason from './fixtures/s28/reject-no-reason.json';
import slotTaken from './fixtures/s28/accept-slot-taken.json';

const list = suggestionsJson as unknown as { data: Suggestion[] };
const loosen = list.data.find((x) => x.kind === 'loosen')!;
const tighten = list.data.find((x) => x.kind === 'tighten')!;
const budget = list.data.find((x) => x.kind === 'budget')!;
const shadow = shadowJson as unknown as ShadowReport;
const shadowV3 = shadowV3Json as unknown as ShadowReport;
const anomalies = anomaliesJson as unknown as AnomalyList;
const stats = statsJson as unknown as Stats;
const settings = settingsJson as unknown as InsightsSettings;
const accepted = acceptJson as unknown as Suggestion;
const txt = (el: Element | null) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

interface Call { url: string; init: RequestInit }
function wire(answer: (url: string, init: RequestInit) => Response): Call[] {
  const calls: Call[] = [];
  configure({
    fetch: (async (u: RequestInfo | URL, init?: RequestInit) => { calls.push({ url: String(u), init: init! }); return answer(String(u), init!); }) as never,
    csrf: () => 'csrf-1', refreshCsrf: async () => {}, emit: () => {}, sleep: async () => {}, uuid: () => 'idem-1',
  });
  return calls;
}
const hdr = (call: Call) => call.init.headers as Record<string, string>;
const body = (call: Call) => (call.init.body ? JSON.parse(String(call.init.body)) : undefined);

describe('insights fetchers', () => {
  beforeEach(() => wire(() => json(200, {})));

  it('lists the open suggestions by impact and follows the cursor', async () => {
    let n = 0;
    const calls = wire(() => (++n === 1
      ? json(200, { object: 'list', data: [budget, loosen], has_more: true, next_cursor: 'cur_2' })
      : json(200, { object: 'list', data: [tighten], has_more: false, next_cursor: null })));
    const out = await listSuggestions();
    expect(out.map((x) => x.kind)).toEqual(['loosen', 'tighten', 'budget']);
    expect(calls[0]!.url).toBe('/v1/suggestions?status=open&limit=50');
    expect(calls[1]!.url).toBe('/v1/suggestions?status=open&limit=50&cursor=cur_2');
    expect(hdr(calls[0]!)['Deedglass-Version']).toBe('2026-10-01');
  });

  it('reads no live shadow (404 not_found) as null, and keeps any other error', async () => {
    wire(() => json(404, { error: { type: 'invalid_request_error', code: 'not_found', message: 'No policy is running in shadow.', param: null, request_id: 'req_1', doc_url: '' } }));
    expect(await getShadow()).toBeNull();
    wire(() => json(200, shadow));
    expect((await getShadow())?.shadow.version).toBe(2);
    wire(() => json(500, { error: { type: 'api_error', code: 'internal_error', message: 'Something failed on our side.', param: null, request_id: 'req_9', doc_url: '' } }));
    const e = (await getShadow().catch((x: unknown) => x)) as ApiError;
    expect(e).toBeInstanceOf(ApiError);
    expect(e.requestId).toBe('req_9');
  });

  it('accepts with replace_shadow, the reason, CSRF and one Idempotency-Key', async () => {
    const calls = wire(() => json(200, accepted));
    const r = await accept(loosen.id, { reason: 'Ops approves these every time', replaceShadow: true }, 'key-7');
    expect(r.resulting_version?.version).toBe(3);
    expect(calls[0]!.url).toBe(`/v1/suggestions/${loosen.id}/accept`);
    expect(calls[0]!.init.method).toBe('POST');
    expect(body(calls[0]!)).toEqual({ replace_shadow: true, reason: 'Ops approves these every time' });
    expect(hdr(calls[0]!)['Idempotency-Key']).toBe('key-7');
    expect(hdr(calls[0]!)['Deedglass-CSRF']).toBe('csrf-1');
    // an empty reason is left out, replace_shadow is always sent
    await accept(loosen.id, { reason: '', replaceShadow: false });
    expect(body(calls[1]!)).toEqual({ replace_shadow: false });
  });

  it('rejects, generates, acknowledges and resolves on the documented routes', async () => {
    const calls = wire(() => json(200, {}));
    await reject(tighten.id, 'Inventory writes are fine');
    await generate();
    await acknowledge('anm_1');
    await resolve('anm_1', 'Expected burst from the import');
    await patchSettings({ anomaly_facts: true, reason: 'Policies should see it' });
    await getStats();
    await getSettings();
    await listAnomalies().catch(() => undefined);
    expect(calls.map((x) => `${x.init.method} ${x.url}`)).toEqual([
      `POST /v1/suggestions/${tighten.id}/reject`, 'POST /v1/suggestions/generate', 'POST /v1/insights/anomalies/anm_1/acknowledge',
      'POST /v1/insights/anomalies/anm_1/resolve', 'PATCH /v1/insights/settings', 'GET /v1/insights/stats?window=30d&by=none', 'GET /v1/insights/settings',
      'GET /v1/insights/anomalies?limit=50',
    ]);
    expect(body(calls[0]!)).toEqual({ reason: 'Inventory writes are fine' });
    expect(body(calls[1]!)).toBeUndefined();
    expect(hdr(calls[1]!)['Idempotency-Key']).toBe('idem-1');
    expect(body(calls[3]!)).toEqual({ reason: 'Expected burst from the import' });
    expect(body(calls[4]!)).toEqual({ anomaly_facts: true, reason: 'Policies should see it' });
  });

  it('keeps the error envelope verbatim: message, code, param and request id', async () => {
    wire(() => json(400, rejectNoReason));
    const e = (await reject(loosen.id, '').catch((x: unknown) => x)) as ApiError;
    expect(e.code).toBe('validation_failed');
    expect(e.param).toBe('reason');
    expect(e.message).toBe(c.reject.required);
    expect(e.requestId).toMatch(/^req_/);
    wire(() => json(409, slotTaken));
    const s = (await accept(loosen.id, { replaceShadow: false }).catch((x: unknown) => x)) as ApiError;
    expect(s.status).toBe(409);
    expect(s.code).toBe('shadow_slot_taken');
    expect(s.param).toBe('replace_shadow');
    expect(s.message).toBe('acme-sim v2 is already running in shadow. Promote or clear it first, or accept with replace_shadow: true to replace it.');
  });

  it('lists open anomalies newest first and leaves resolved ones out', async () => {
    const shuffled = { ...anomalies, data: [...anomalies.data].reverse().concat([{ ...anomalies.data[0]!, id: 'anm_resolved', status: 'resolved' }]) };
    wire(() => json(200, shuffled));
    const r = await listAnomalies();
    expect(r.data.map((a) => a.kind)).toEqual(['user_ref_burst', 'off_hours', 'volume_spike', 'mismatch_spike', 'new_tool']);
  });
});

describe('insights helpers', () => {
  it('reads the loosen evidence as labelled rows with tabular numbers', () => {
    const rows = evidenceRows(loosen.evidence, 'Asia/Kolkata');
    const kv = Object.fromEntries(rows.map((r) => [r.label, r.value]));
    expect(kv.Window).toBe('20 Sept 2026 to 4 Oct 2026');
    expect(kv['Approvals asked']).toBe('411');
    expect(kv.Approved).toBe('398');
    expect(kv.Denied).toBe('0');
    expect(kv.Expired).toBe('5');
    expect(kv.Pending).toBe('8');
    expect(kv['Median wait']).toBe('19 min');
    expect(kv['People who approved']).toBe('2');
    expect(kv.Tool).toBe('postgres.execute');
    expect(kv.Pattern).toBe('sql.kind update · sql.statements 1 · sql.tables public.orders');
    expect(rows.find((r) => r.key === 'samples')?.ids).toHaveLength(5);
    expect(rows.filter((r) => r.num).every((r) => /^[\d,]+( min)?$/.test(r.value))).toBe(true);
  });

  it('reads the tighten and budget evidence too', () => {
    const t = Object.fromEntries(evidenceRows(tighten.evidence).map((r) => [r.label, r.value]));
    expect(t["Doesn't match verdicts"]).toBe('3');
    expect(t.Rule).toBe('allow-db-writes');
    const b = Object.fromEntries(evidenceRows(budget.evidence).map((r) => [r.label, r.value]));
    expect(b.Budget).toBe('refunds-per-day');
    expect(b.Limit).toBe('$4,000.00 a day');
    expect(b['Highest day']).toBe('95%');
    expect(b['Days at 80% or more']).toBe('3');
    expect(patternText({ tool: 'x', 'sql.kind': 'delete' })).toBe('sql.kind delete');
  });

  it('turns the server hunks into numbered line-diff rows, with no red', () => {
    const d = toLineDiff(loosen.diff!);
    expect(d.added).toBe(8);
    expect(d.removed).toBe(0);
    const lines = d.hunks[0]!.lines;
    expect(lines[0]).toMatchObject({ kind: 'ctx', old: 52, new: 52 });
    expect(lines[3]).toMatchObject({ kind: 'add', old: null, new: 55 });
    expect(lines.at(-1)).toMatchObject({ kind: 'ctx', old: 57, new: 65 });
    const b = toLineDiff(budget.diff!);
    expect(b.hunks[0]!.lines.filter((l) => l.kind === 'del')).toHaveLength(1);
    expect(diffLines(loosen.diff)).toBe('+8 lines');
    expect(diffLines(budget.diff)).toBe('+1 line');
    expect(diffLines(null)).toBe('');
  });

  it('writes the human outcome, the wait and the replay window', () => {
    expect(outcomeLine(loosen.replay!.transitions![0]!.human_outcome)).toBe('398 approved · 0 denied · 5 expired · 8 pending');
    expect(outcomeLine(null)).toBe('');
    expect(waitText(1123)).toBe('19 min');
    expect(waitText(6578)).toBe('1 h 50 min');
    expect(waitText(42)).toBe('42 s');
    expect(waitText(7200)).toBe('2 h');
    expect(waitText(null)).toBe('·');
    expect(replayDays(loosen.replay!.window)).toBe(14);
  });

  it('labels kinds and knows advice from a change', () => {
    expect(kindLabel('loosen')).toBe('Loosen');
    expect(kindLabel('explicit_rule')).toBe('Explicit rule');
    expect(kindLabel('something_new')).toBe('something new');
    expect(isAdvisory(loosen)).toBe(false);
    expect(isAdvisory({ kind: 'advisory', diff: null })).toBe(true);
  });

  it('writes the accept sentence and the toast with the short deed id', () => {
    expect(acceptBody('acme-sim', { latest_version: 2, active_version: 1 })).toBe('Deedglass creates acme-sim v3 from it and runs it in shadow next to v1. Nothing is enforced until someone promotes it.');
    const t = acceptToast(accepted, shortId);
    expect(t.deed).toBe(accepted.decision_deed);
    expect(t.text).toBe(`acme-sim v3 is running in shadow · ${shortId(accepted.decision_deed!)} sealed`);
    expect(t.text).toMatch(/^acme-sim v3 is running in shadow · deed_\w{4}…\w{4} sealed$/);
  });

  it('reads the shadow agreement and the early notice', () => {
    expect(agreementLine(shadow)).toBe('Agrees on 4,004 of 4,179 actions');
    expect(agreementPct(shadow)).toBe('95.8% agreement');
    expect(earlyNotice(shadowV3)).toBe('Only 0 actions have been decided since v3 entered shadow. You can promote now, or wait for more traffic.');
    expect(earlyNotice({ ...shadowV3, summary: { ...shadowV3.summary, evaluated: 12 } })).toBe('Only 12 actions have been decided since v3 entered shadow. You can promote now, or wait for more traffic.');
    const since = shadow.shadow.since!;
    expect(earlyNotice(shadow, new Date(Date.parse(since) + 3_600_000))).toBe('v2 has been in shadow for less than 24 hours. You can promote now, or wait for more traffic.');
    expect(earlyNotice(shadow, new Date(Date.parse(since) + 2 * 86_400_000))).toBe('');
  });

  it('only words a claude note when Claude wrote it', () => {
    expect(phrasingNote(loosen.phrasing)).toBe('');
    expect(phrasingNote({ source: 'claude', model: 'claude-sonnet-5' })).toBe('Worded by claude-sonnet-5 from the numbers above. Deedglass checked that every number matches the evidence.');
  });

  it('fills the 30-day series and summarises it', () => {
    const days = fillDays(stats);
    expect(days.length).toBeGreaterThanOrEqual(30);
    expect(days.length).toBeLessThanOrEqual(31);
    expect(days.reduce((a, d) => a + d.actions, 0)).toBe(stats.totals.actions);
    expect(days.every((d) => d.none >= 0)).toBe(true);
    expect(mixLabel(stats.totals)).toBe('Verification mix, last 30 days: 97.8% verified, 0.1% mismatch, 0.7% unverifiable');
    expect(waitLabel(stats.totals.approval)).toBe('Approval wait, last 30 days: median 19 min, 95th percentile 1 h 50 min, across 411 decided approvals');
  });

  it('knows a simulated org from GET /v1/org or the insights settings', () => {
    expect(isSimulated({ settings: { simulated: true } })).toBe(true);
    expect(isSimulated({ settings: { approvals: {} } }, settings)).toBe(false);
    expect(isSimulated(null, { simulated: true })).toBe(true);
  });

  it("shows internal/learn/copy.go's SimulatedBanner word for word", () => {
    const go = readFileSync(join(process.cwd(), '..', 'internal/learn/copy.go'), 'utf8');
    const m = /const SimulatedBanner = "([^"]+)"/.exec(go);
    expect(m?.[1]).toBe(copy.states.simulatedBanner);
  });
});

describe('insights views', () => {
  const noop = () => {};
  it('draws a suggestion card for an admin: header, panels, a collapsed diff, risk and two actions', () => {
    const accepts: string[] = [];
    const el = suggestionCard(loosen, { admin: true, tz: 'UTC', onAccept: (s) => accepts.push(s.id), onReject: noop });
    expect(txt(el.querySelector('.ins-card-head'))).toBe('Loosen · approve-order-updates sug_01m4…wjah');
    expect(el.querySelector('h3')?.textContent).toBe(loosen.title);
    const row = el.querySelector('.ins-trans > li')!;
    expect(row.getAttribute('data-transition')).toBe('require_approval>allow');
    expect([...row.querySelectorAll('.chip')].map((x) => txt(x))).toEqual(['Approval', 'Allowed']);
    expect(txt(row.querySelector('.ins-count'))).toBe('411');
    expect(txt(row.querySelector('.ins-human'))).toBe('398 approved · 0 denied · 5 expired · 8 pending');
    const toggle = el.querySelector<HTMLButtonElement>('[data-diff-toggle]')!;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(txt(toggle)).toBe('Policy change +8 lines');
    expect(el.querySelector('.ld')).toBeNull();
    toggle.click();
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(el.querySelectorAll('.ld-add')).toHaveLength(8);
    expect(el.querySelector('.ld-del')).toBeNull();
    expect(txt(el.querySelector('.ins-risk'))).toBe(`Risk: ${loosen.risk_note}`);
    expect(el.querySelector('.ins-phrasing')).toBeNull();
    el.querySelector<HTMLButtonElement>('[data-accept]')!.click();
    expect(accepts).toEqual([loosen.id]);
    expect(txt(el.querySelector('[data-accept]'))).toBe('Accept and run in shadow');
    expect(txt(el.querySelector('[data-reject]'))).toBe('Reject');
  });

  it('shows a viewer neither button, only the footnote', () => {
    const el = suggestionCard(loosen, { admin: false, tz: 'UTC', onAccept: noop, onReject: noop });
    expect(el.querySelector('[data-accept], [data-reject]')).toBeNull();
    expect(txt(el.querySelector('.ins-actions'))).toBe(c.card.viewerNote);
  });

  it('offers advice only a Reject, and words a Claude card', () => {
    const adv = { ...tighten, kind: 'advisory', diff: null, replay: null, phrasing: { source: 'claude', model: 'claude-sonnet-5' } } as Suggestion;
    const el = suggestionCard(adv, { admin: true, tz: 'UTC', onAccept: noop, onReject: noop });
    expect(el.querySelector('[data-accept]')).toBeNull();
    expect(el.querySelector('[data-reject]')).not.toBeNull();
    expect(el.querySelector('[data-diff-toggle]')).toBeNull();
    expect(txt(el.querySelector('.ins-phrasing'))).toContain('claude-sonnet-5');
  });

  it('draws the shadow with its sentence, agreement and Promote for admins only', () => {
    const el = shadowCard(shadow, { admin: true, tz: 'UTC', onPromote: noop });
    expect(txt(el.querySelector('.ins-sentence'))).toBe(shadow.sentence);
    expect(txt(el.querySelector('.ins-agree'))).toContain('Agrees on 4,004 of 4,179 actions');
    expect(el.querySelectorAll('.ins-trans > li')).toHaveLength(2);
    expect(el.querySelector('.chip-denied')).not.toBeNull();
    expect(el.querySelector('[data-promote]')).not.toBeNull();
    expect(shadowCard(shadow, { admin: false, tz: 'UTC', onPromote: noop }).querySelector('[data-promote]')).toBeNull();
  });

  it('marks an active anomaly with the amber outline chip, worded by the facts setting', () => {
    const a = { ...anomalies.data[0]!, active: true };
    const on = anomalyRow(a, { admin: true, tz: 'UTC', facts: true, onAck: noop, onResolve: noop });
    expect(txt(on.querySelector('.chip-active'))).toBe('Active now · policies see it');
    const off = anomalyRow(a, { admin: false, tz: 'UTC', facts: false, onAck: noop, onResolve: noop });
    expect(txt(off.querySelector('.chip-active'))).toBe('Active now');
    expect(off.querySelector('[data-resolve]')).toBeNull();
    expect(anomalyRow(anomalies.data[1]!, { admin: true, tz: 'UTC', facts: false, onAck: noop, onResolve: noop }).querySelector('.chip-active')).toBeNull();
  });

  it('draws the trends as labelled images with table fallbacks and no aurora', () => {
    const el = trendsView(stats);
    const imgs = [...el.querySelectorAll('svg[role="img"]')];
    expect(imgs).toHaveLength(3);
    expect(imgs[0]!.getAttribute('aria-label')).toBe(mixLabel(stats.totals));
    expect(el.querySelectorAll('details > summary')).toHaveLength(3);
    expect(el.querySelectorAll('rect.bar-mismatch').length).toBeGreaterThan(0);
    expect(el.querySelector('[class*="aurora"]')).toBeNull();
    expect([...el.querySelectorAll('.ins-legend li')].map((x) => [...x.querySelectorAll('span')].map((y) => y.textContent).join(' '))[0]).toBe('Matches 5,763');
  });
});

vi.restoreAllMocks();
