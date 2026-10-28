// @vitest-environment happy-dom
// S27 §3.4: the Replay tab's model and views on real answers captured from an S27 server with a
// simulated org (test/fixtures/s27; replay-failed.json and replay-unknown-routed.json are built by
// hand from the Go type, the rest are verbatim).
import { describe, expect, it } from 'vitest';
import { policiesCopy } from '../src/lib/copy-policies.ts';
import {
  baselineLabel, cancelledText, clampPercent, driftLine, humanOutcomeLine, isActive, isFinished, POLL_MS, progressText, requestBody, ruleChangesSorted, rulesOf,
  samplesOf, shortDigest, sqlguardDrift, summaryLine, transitionsSorted, unknownLine, windowOf, windowText, type Replay, type Transition,
} from '../src/lib/models/replay.ts';
import { progressView, replayResult, replayView } from '../src/controllers/replay.ts';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import queuedJson from './fixtures/s27/replay-queued.json';
import runningJson from './fixtures/s27/replay-running.json';
import succeededJson from './fixtures/s27/replay-succeeded.json';
import baselineJson from './fixtures/s27/replay-baseline-v1.json';
import cancelledJson from './fixtures/s27/replay-cancelled.json';
import failedJson from './fixtures/s27/replay-failed.json';
import unknownJson from './fixtures/s27/replay-unknown-routed.json';

const queued = queuedJson as unknown as Replay;
const running = runningJson as unknown as Replay;
const ok = succeededJson as unknown as Replay;
const base = baselineJson as unknown as Replay;
const cancelled = cancelledJson as unknown as Replay;
const failed = failedJson as unknown as Replay;
const unk = unknownJson as unknown as Replay;
const L = policiesCopy.replay.lines;
const txt = (el: Element | null) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('replay model', () => {
  it('reads the summary line exactly as the CLI prints it', () => {
    expect(summaryLine(ok.summary!)).toBe('453 decisions would change · 15 by a different rule · 5,422 unchanged · 0 unknown');
    expect(summaryLine({ actions: 5612, evaluated: 5612, unchanged: 5200, changed: 408, rule_changed: 4, unknown: 0 })).toBe('408 decisions would change · 4 by a different rule · 5,200 unchanged · 0 unknown');
    expect(summaryLine({ actions: 3, evaluated: 3, unchanged: 2, changed: 1, rule_changed: 0, unknown: 0 })).toBe('1 decision would change · 2 unchanged · 0 unknown');
    expect(summaryLine(unk.summary!)).toBe('453 decisions would change · 15 by a different rule · 5,408 unchanged · 14 unknown');
  });

  it('reads the human outcome for transitions out of approval', () => {
    const t = ok.transitions!.find((x) => x.from === 'require_approval')!;
    expect(humanOutcomeLine(t.human_outcome)).toBe('humans approved 397, expired 5, denied 1, pending 8');
    expect(humanOutcomeLine({ approved: 401, denied: 0, expired: 3, cancelled: 0, pending: 0 })).toBe('humans approved 401, expired 3, denied 0');
    expect(humanOutcomeLine({ approved: 1, denied: 0, expired: 0, cancelled: 2, pending: 0, not_requested: 1204 })).toBe('humans approved 1, expired 0, denied 0, cancelled 2, never asked 1,204');
    expect(humanOutcomeLine(undefined)).toBe('');
    expect(humanOutcomeLine(ok.transitions!.find((x) => x.to === 'deny')!.human_outcome)).toBe('');
  });

  it('explains the unknowns in the CLI words, singular and plural', () => {
    expect(unknownLine(unk.unknown)).toBe('12 inputs pruned by retention · 2 recorded before inputs were stored');
    expect(unknownLine(ok.unknown)).toBe('');
    expect(unknownLine({ input_pruned: 1, input_missing: 1, input_version_unsupported: 1, input_unreadable: 2, evaluation_time_limit: 1 }))
      .toBe("1 input pruned by retention · 1 recorded before inputs were stored · 1 input in a newer format · 2 inputs that don't decode · 1 evaluation that hit its time limit");
  });

  it('uses the same words as internal/learn/copy.go', () => {
    const go = readFileSync(join(process.cwd(), '..', 'internal/learn/copy.go'), 'utf8');
    const words = (tpl: string) => tpl.replace(/\{\w+\}/g, '').replace(/^\s+|\s+$/g, '');
    for (const k of ['changedMany', 'ruleChanged', 'unchanged', 'unknown', 'prunedMany', 'missing', 'unsupportedMany', 'unreadableMany', 'timeLimitMany', 'prunedOne', 'unsupportedOne', 'unreadableOne', 'timeLimitOne'] as const) {
      const w = words(L[k]).replace(/^(\d+ )/, '');
      expect(go.includes(w.replace(/^decisions? /, '')), `${k}: "${w}"`).toBe(true);
    }
    expect(go).toContain('humans approved %s, expired %s, denied %s');
    for (const w of ['", pending "', '", cancelled "', '", never asked "']) expect(go).toContain(w);
  });

  it('shows sqlguard drift only when the recorded versions differ from the current one', () => {
    expect(sqlguardDrift(ok.sqlguard)).toBe(false);
    expect(sqlguardDrift(unk.sqlguard)).toBe(true);
    expect(sqlguardDrift({ recorded: [], current: '1' })).toBe(false);
    expect(sqlguardDrift({ recorded: null, current: '1' })).toBe(false);
    expect(sqlguardDrift(undefined)).toBe(false);
    expect(driftLine(unk.sqlguard!)).toBe('SQL facts were recomputed with sqlguard 2. The recorded decisions used 1, 2.');
  });

  it('shortens the digest to 4 and 4', () => {
    expect(shortDigest(ok.result_digest!)).toBe('sha256:ce0b…5759');
    expect(shortDigest('sha256:786a' + '0'.repeat(56) + 'f22c')).toBe('sha256:786a…f22c');
    expect(shortDigest(unk.result_digest!)).toBe('sha256:786a…f22c');
  });

  it('sorts transitions and rule changes largest first, deterministically', () => {
    const t = (from: string, to: string, count: number): Transition => ({ from, to, count, by_rule: [], by_tool: [], samples: [] });
    expect(transitionsSorted([t('allow', 'deny', 4), t('require_approval', 'allow', 404), t('allow', 'require_approval', 4)]).map((x) => `${x.from}>${x.to}`))
      .toEqual(['require_approval>allow', 'allow>deny', 'allow>require_approval']);
    expect(transitionsSorted(ok.transitions).map((x) => x.count)).toEqual([411, 42]);
    expect(transitionsSorted(null)).toEqual([]);
    expect(ruleChangesSorted(ok.rule_changes).map((x) => x.to_rule)).toEqual(['refund-limit-strict']);
    expect(rulesOf({ ...t('a', 'b', 3), by_rule: [{ rule_id: 'b', count: 1 }, { rule_id: 'a', count: 1 }, { rule_id: 'c', count: 2 }] }).map((x) => x.rule_id)).toEqual(['c', 'a', 'b']);
  });

  it('keeps at most the first 20 samples in server order', () => {
    const s = ok.transitions![0]!.samples!;
    expect(samplesOf(s)).toEqual(s.slice(0, 20));
    expect(samplesOf([...s, ...s]).length).toBe(20);
    expect(samplesOf(null)).toEqual([]);
  });

  it('builds the request: windows and baselines', () => {
    const now = new Date('2026-10-04T07:00:00Z');
    expect(windowOf('24h', now)).toEqual({ from: '2026-10-03T07:00:00.000Z', to: '2026-10-04T07:00:00.000Z' });
    expect(windowOf('7d', now).from).toBe('2026-09-27T07:00:00.000Z');
    expect(windowOf('custom', now, { from: 'a', to: '' })).toEqual({ from: 'a', to: now.toISOString() });
    expect(windowText(ok.window, 'UTC')).toBe('26 Sept 2026 07:03 to 4 Oct 2026 07:03 UTC');
    expect(windowText(ok.window, 'Asia/Kolkata')).toBe('26 Sept 2026 12:33 to 4 Oct 2026 12:33 Asia/Kolkata');
    expect(requestBody({ from: 'f', to: 't' }, 0)).toEqual({ from: 'f', to: 't', baseline: { type: 'recorded' } });
    expect(requestBody({ from: 'f', to: 't' }, 1)).toEqual({ from: 'f', to: 't', baseline: { type: 'version', version: 1 } });
  });

  it('reads status and progress', () => {
    expect(isActive(queued.status) && isActive(running.status)).toBe(true);
    expect([ok, cancelled, failed].every((r) => isFinished(r.status) && !isActive(r.status))).toBe(true);
    expect(POLL_MS).toBe(500);
    expect(progressText(running)).toBe('Replaying 5,890 actions · 0%');
    expect(progressText({ status: 'running', progress: { processed: 2000, total: 5890, percent: 33.9 } })).toBe('Replaying 5,890 actions · 34%');
    expect(progressText(queued)).toBe('Waiting to start · 5,890 actions');
    expect(clampPercent(140)).toBe(100);
    expect(clampPercent(Number.NaN)).toBe(0);
    expect(baselineLabel(ok.baseline)).toBe('recorded decisions');
    expect(baselineLabel(base.baseline)).toBe('v1');
    expect(cancelledText(cancelled)).toBe('Replay cancelled before it started. Run it again to see the result.');
    expect(cancelledText({ progress: { processed: 3000, total: 5890, percent: 50 } })).toBe('Replay cancelled after 3,000 of 5,890 actions. Run it again to see the full result.');
  });
});

describe('replay views', () => {
  const o = { tz: 'UTC', now: new Date('2026-10-04T07:10:00Z') };

  it('renders a determinate progress element while running', () => {
    const el = progressView({ ...running, progress: { processed: 2400, total: 5890, percent: 41 } });
    const p = el.querySelector('progress')!;
    expect(p.getAttribute('aria-valuenow')).toBe('41');
    expect(p.getAttribute('value')).toBe('41');
    expect(txt(el.querySelector('.rp-line'))).toBe('◐ Replaying 5,890 actions · 41%');
    expect(progressView(queued).querySelector('progress')!.getAttribute('aria-valuenow')).toBe('0');
    expect(txt(progressView(running, o).querySelector('.rp-meta'))).toContain('acme-sim v3 against recorded decisions');
  });

  it('renders the result: summary, transitions, human outcome, rule changes, digest', () => {
    const el = replayResult(ok, o);
    expect(txt(el.querySelector('.rp-summary'))).toBe(summaryLine(ok.summary!));
    const rows = [...el.querySelectorAll('[data-transition]')];
    expect(rows.map((r) => r.getAttribute('data-transition'))).toEqual(['require_approval>allow', 'allow>deny']);
    expect(txt(rows[0]!.querySelector('.rp-pair'))).toBe('require_approval → to allow');
    expect(txt(rows[0]!.querySelector('.rp-count'))).toBe('411');
    expect(txt(rows[0]!.querySelector('.rp-human'))).toBe('humans approved 397, expired 5, denied 1, pending 8');
    expect(rows[1]!.querySelector('.rp-human')).toBeNull();
    expect(txt(rows[0]!.querySelector('.rp-chips'))).toContain('allow-order-status-updates');
    const links = [...rows[0]!.querySelectorAll<HTMLAnchorElement>('.rp-ids a')];
    expect(links.length).toBe(20);
    expect(links[0]!.getAttribute('href')).toBe(`/app/actions/${ok.transitions![0]!.samples![0]}`);
    // axe label-content-name-mismatch: the name holds the visible short id; the full id stays in title
    expect(links[0]!.getAttribute('aria-label')).toContain(txt(links[0]!));
    expect(links[0]!.getAttribute('title')).toBe(ok.transitions![0]!.samples![0]);
    // red only as the outline on the candidate's new deny
    expect(rows[1]!.querySelector('[data-decision="deny"][data-side="to"]')).not.toBeNull();
    expect(el.querySelector('[data-rule-change="refund-limit>refund-limit-strict"]')).not.toBeNull();
    expect(el.querySelector('[data-replay-unknown]')).toBeNull();
    expect(el.querySelector('[data-replay-drift]')).toBeNull();
    const foot = el.querySelector('.rp-foot')!;
    expect(txt(foot)).toBe('Result sha256:ce0b…5759 · same inputs and version always give this digest');
    expect(foot.querySelector('[data-copy]')!.getAttribute('data-copy')).toBe(ok.result_digest);
    expect(txt(el.querySelector('.rp-meta'))).toContain('acme-sim v3 against recorded decisions');
  });

  it('shows the unknown line (amber ○, never red) and sqlguard drift only when they apply', () => {
    const el = replayResult(unk, o);
    expect(txt(el.querySelector('[data-replay-unknown]'))).toBe('○ 14 unknown · 12 inputs pruned by retention · 2 recorded before inputs were stored');
    expect(txt(el.querySelector('[data-replay-drift]'))).toBe(driftLine(unk.sqlguard!));
    expect(el.querySelector('[data-replay-unknown] .icon-x, .chip-mismatch')).toBeNull();
  });

  it('names a version baseline', () => {
    expect(txt(replayResult(base, o).querySelector('.rp-meta'))).toContain('acme-sim v3 against v1');
  });

  it('renders failed as an alert with the next step and cancelled as a neutral line', () => {
    const f = replayView(failed, o);
    expect(f.getAttribute('role')).toBe('alert');
    expect(txt(f)).toContain(`The replay failed. ${failed.error!.message}`);
    expect(failed.error!.message).toMatch(/Start it again/);
    expect(f.querySelector('button')).toBeNull(); // one Run per page: the form's
    const cx = replayView(cancelled, o);
    expect(cx.querySelector('[role=alert]')).toBeNull();
    expect(cx.getAttribute('role')).toBeNull();
    expect(txt(cx.querySelector('.rp-line'))).toBe(cancelledText(cancelled));
  });
});
