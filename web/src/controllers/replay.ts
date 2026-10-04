// replay.ts: the Policies screen's Replay tab (S27 §3.4) on the real API. POST
// /v1/policies/{id}/versions/{v}/replay queues a job, GET /v1/replays/{id} is polled every 500 ms
// while it is queued or running, POST /v1/replays/{id}/cancel stops it. The tab opens on the
// policy's latest replay (GET /v1/replays?policy=…), so viewers read results without running one.
// A 404 on any of these means an older server: S26's sentence stays.

import { api, ApiError } from '../lib/api.ts';
import { relTime } from '../lib/chips.ts';
import { fill } from '../lib/copy.ts';
import { policiesCopy } from '../lib/copy-policies.ts';
import { h, icon, replace, type Child } from '../lib/dom.ts';
import { duration, number, shortId } from '../lib/format.ts';
import {
  baselineLabel, cancelledText, clampPercent, driftLine, humanOutcomeLine, isActive, POLL_MS, progressText, requestBody, ruleChangesSorted, rulesOf,
  samplesOf, shortDigest, sqlguardDrift, summaryLine, toolsOf, transitionsSorted, unknownLine, windowOf, windowText,
  type Replay, type RuleChange, type Transition, type WindowChoice,
} from '../lib/models/replay.ts';
import type { ListResponse } from '../lib/types.ts';
import { segmented } from './components.ts';
import { toast } from './toast.ts';

const c = policiesCopy.replay;
const MAX_WINDOW_MS = 90 * 86_400_000;

export interface ReplayContext {
  policyId: string;
  name: () => string;
  version: () => number; // the version open in the editor: the candidate
  versions: () => number[]; // every version, newest first
  admin: () => boolean;
  tz: () => string;
  announce: (text: string) => void; // shell.ts's live region (injected: shell pulls in the whole chrome)
}

export interface ReplayTab { update: () => void }

/** mountReplay draws the tab into panel once; update() follows the open version and the role. */
export function mountReplay(panel: HTMLElement, ctx: ReplayContext): ReplayTab {
  let win: WindowChoice = '24h';
  let baseline = 0;
  let current: Replay | null = null;
  let pollToken = 0;
  let cancelling = false;
  let posting = false; // a second click while the POST is in flight must not queue a second replay

  const heading = h('h2', { class: 't-headline', id: 'rp-title' });
  const from = h('input', { class: 'input', type: 'datetime-local', id: 'rp-from', name: 'from', autocomplete: 'off', 'aria-describedby': 'rp-custom-help' });
  const to = h('input', { class: 'input', type: 'datetime-local', id: 'rp-to', name: 'to', autocomplete: 'off', 'aria-describedby': 'rp-custom-help' });
  const customErr = h('p', { class: 'error', id: 'rp-custom-e', role: 'alert', hidden: true });
  const custom = h('div', { class: 'rp-custom grid gap-2', hidden: true },
    h('div', { class: 'field-row' },
      h('div', { class: 'field' }, h('label', { class: 'label', for: 'rp-from' }, c.from), from),
      h('div', { class: 'field' }, h('label', { class: 'label', for: 'rp-to' }, c.to), to)),
    h('p', { class: 'help t-footnote t-2 m-0', id: 'rp-custom-help' }, fill(c.customHint, { tz: Intl.DateTimeFormat().resolvedOptions().timeZone })),
    customErr);
  const seg = segmented(c.window, [{ value: '24h' as const, label: c.last24h }, { value: '7d' as const, label: c.last7d }, { value: 'custom' as const, label: c.custom }], win, (v) => {
    win = v;
    custom.hidden = v !== 'custom';
    if (v === 'custom') from.focus();
  }, 'rp-window');
  const baseSel = h('select', { class: 'select', id: 'rp-baseline', name: 'baseline', 'aria-describedby': 'rp-baseline-help' });
  baseSel.addEventListener('change', () => { baseline = Number(baseSel.value) || 0; });
  const run = h('button', { type: 'button', class: 'btn btn-primary', 'data-replay-run': '' }, icon('play', 16), c.run);
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary', hidden: true, 'data-replay-cancel': '' }, icon('x', 16), c.cancel);
  const viewerNote = h('p', { class: 'banner banner-readonly rp-viewer', hidden: true }, icon('eye', 16), c.viewer);
  const controls = h('div', { class: 'rp-controls' },
    h('div', { class: 'field' }, h('span', { class: 'label', id: 'rp-window-l' }, c.window), seg),
    h('div', { class: 'field rp-baseline' }, h('label', { class: 'label', for: 'rp-baseline' }, c.baseline), h('span', { class: 'select-wrap' }, baseSel, icon('chevron-down', 16))));
  const baseHelp = h('p', { class: 'help t-footnote t-2 m-0 t-pretty', id: 'rp-baseline-help' }, c.baselineHelp);
  const form = h('section', { class: 'card card-pad rp-form', 'aria-labelledby': 'rp-title' },
    heading, h('p', { class: 't-2 m-0 t-pretty' }, c.lede), viewerNote, controls, custom, baseHelp,
    h('div', { class: 'rp-actions' }, run, cancel));
  const out = h('div', { class: 'rp-out' });
  replace(panel, h('div', { class: 'grid gap-4' }, form, out));

  const fillBaselines = () => {
    const v = ctx.version();
    const others = ctx.versions().filter((x) => x !== v);
    if (baseline && !others.includes(baseline)) baseline = 0;
    replace(baseSel, h('option', { value: '0' }, c.baselineRecorded), ...others.map((x) => h('option', { value: String(x) }, fill(c.baselineVersion, { v: x }))));
    baseSel.value = String(baseline);
  };

  const busy = () => !!current && isActive(current.status);
  const syncButtons = () => {
    const admin = ctx.admin();
    viewerNote.hidden = admin;
    controls.hidden = !admin;
    baseHelp.hidden = !admin;
    custom.hidden = !admin || win !== 'custom';
    // Viewers see Run, disabled, beside the role sentence; a running replay keeps focus on Run.
    run.disabled = !admin;
    run.setAttribute('aria-disabled', admin && busy() ? 'true' : 'false');
    if (!admin) run.removeAttribute('aria-disabled');
    cancel.hidden = !admin || !busy();
    cancel.setAttribute('aria-disabled', cancelling ? 'true' : 'false');
    replace(cancel, icon('x', 16), cancelling ? c.cancelling : c.cancel);
  };

  const update = () => {
    replace(heading, fill(c.heading, { v: ctx.version() }));
    fillBaselines();
    syncButtons();
  };

  const showError = (e: unknown) => {
    if (e instanceof ApiError && e.status === 404) {
      replace(out, h('p', { class: 'banner banner-readonly' }, icon('info', 16), c.unavailable));
      return;
    }
    if (e instanceof ApiError && (e.param === 'from' || e.param === 'to') && ctx.admin()) {
      if (win !== 'custom') { win = 'custom'; seg.querySelector<HTMLElement>('[data-value="custom"]')?.click(); }
      fieldError(e.message);
      return;
    }
    replace(out, h('div', { class: 'banner rp-alert', role: 'alert' }, icon('circle-alert', 16), h('div', { class: 'body' }, h('p', null, e instanceof Error ? e.message : String(e)))));
  };
  const fieldError = (msg: string | null) => {
    customErr.hidden = !msg;
    replace(customErr, ...(msg ? [icon('circle-alert', 16), msg] : []));
    for (const el of [from, to]) {
      if (msg) { el.setAttribute('aria-invalid', 'true'); el.setAttribute('aria-describedby', 'rp-custom-e rp-custom-help'); }
      else { el.removeAttribute('aria-invalid'); el.setAttribute('aria-describedby', 'rp-custom-help'); }
    }
    if (msg) from.focus();
  };

  const show = (r: Replay, latest = false) => {
    const was = current?.status;
    current = r;
    const live = out.querySelector<HTMLElement>('[data-replay-status="queued"], [data-replay-status="running"]');
    if (live && isActive(r.status) && live.dataset.replayId === r.id) {
      updateProgress(live, r);
      syncButtons();
      return;
    }
    replace(out, replayView(r, { tz: ctx.tz(), latest }));
    syncButtons();
    if (was && isActive(was) && !isActive(r.status)) {
      if (r.status === 'succeeded' && r.summary) ctx.announce(summaryLine(r.summary));
      else if (r.status === 'failed') ctx.announce(c.failedTitle);
      else if (r.status === 'cancelled') ctx.announce(c.cancelledToast);
    }
  };

  const poll = async (id: string) => {
    const token = ++pollToken;
    let misses = 0;
    while (token === pollToken && current && isActive(current.status)) {
      await new Promise((ok) => setTimeout(ok, POLL_MS));
      if (token !== pollToken) return;
      try {
        const r = await api<Replay>('GET', `/v1/replays/${id}`);
        misses = 0;
        if (token === pollToken) show(r);
      } catch (e) {
        if (++misses >= 3 && token === pollToken) { current = null; syncButtons(); showError(e); return; }
      }
    }
  };

  const start = async () => {
    if (!ctx.admin() || busy() || posting) return;
    let w: { from: string; to: string };
    if (win === 'custom') {
      const f = from.value ? new Date(from.value) : null;
      const t = to.value ? new Date(to.value) : new Date();
      if (!f || Number.isNaN(f.getTime()) || Number.isNaN(t.getTime()) || f >= t || t.getTime() - f.getTime() > MAX_WINDOW_MS) { fieldError(c.customInvalid); return; }
      fieldError(null);
      w = { from: f.toISOString(), to: t.toISOString() };
    } else {
      fieldError(null);
      w = windowOf(win, new Date());
    }
    run.setAttribute('aria-disabled', 'true');
    run.setAttribute('aria-busy', 'true');
    posting = true;
    try {
      const r = await api<Replay>('POST', `/v1/policies/${ctx.policyId}/versions/${ctx.version()}/replay`, { body: requestBody(w, baseline) });
      ctx.announce(c.started);
      show(r);
      if (isActive(r.status)) void poll(r.id);
    } catch (e) {
      showError(e);
      syncButtons();
    } finally {
      posting = false;
      run.removeAttribute('aria-busy');
    }
  };

  const stopRun = async () => {
    if (!current || !busy() || cancelling) return;
    cancelling = true;
    syncButtons();
    const id = current.id;
    try {
      const r = await api<Replay>('POST', `/v1/replays/${id}/cancel`);
      pollToken++;
      show(r);
    } catch (e) {
      // 409: it finished first; show what it finished as
      if (e instanceof ApiError && e.status === 409) {
        try { pollToken++; show(await api<Replay>('GET', `/v1/replays/${id}`)); } catch (e2) { showError(e2); }
      } else toast(e instanceof Error ? e.message : String(e), { kind: 'error' });
    } finally {
      cancelling = false;
      syncButtons();
      if (current && isActive(current.status)) void poll(current.id);
      else run.focus();
    }
  };

  run.addEventListener('click', () => void start());
  cancel.addEventListener('click', () => void stopRun());

  update();
  // the latest replay of this policy, for everyone (viewers read results here)
  void api<ListResponse<Replay>>('GET', '/v1/replays', { query: { policy: ctx.policyId, limit: '20' } }).then((l) => {
    const r = l.data.find((x) => x.candidate.version === ctx.version()) ?? l.data[0];
    if (!r || current) return;
    show(r, true);
    if (isActive(r.status)) void poll(r.id);
  }).catch((e) => {
    if (e instanceof ApiError && e.status === 404 && !current) showError(e);
  });

  return { update };
}

// ---------- the views (pure DOM from a replay object) ----------

export interface ReplayViewOptions { tz: string; latest?: boolean; now?: Date }

export function replayView(r: Replay, o: ReplayViewOptions): HTMLElement {
  if (r.status === 'queued' || r.status === 'running') return progressView(r, o);
  if (r.status === 'failed') return failedView(r, o);
  if (r.status === 'cancelled') return h('section', { class: 'card card-pad rp-state', 'data-replay-status': 'cancelled' }, h('p', { class: 'rp-line m-0' }, icon('circle-slash', 16), cancelledText(r)), metaLine(r, o));
  return replayResult(r, o);
}

export function progressView(r: Replay, o?: ReplayViewOptions): HTMLElement {
  const el = h('section', { class: 'card card-pad rp-state', 'data-replay-status': r.status, 'data-replay-id': r.id, 'aria-busy': 'true' },
    h('p', { class: 'rp-line m-0 num' }, h('span', { class: 'rp-glyph', 'aria-hidden': 'true' }, '◐'), ' ', h('span', { class: 'rp-progress-text' })),
    h('progress', { class: 'rp-progress', max: 100, 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-label': c.progressLabel }),
    o ? metaLine(r, o) : null);
  updateProgress(el, r);
  return el;
}

/** updateProgress moves the bar and the sentence in place (no rebuild every 500 ms). */
export function updateProgress(el: HTMLElement, r: Replay): void {
  const p = r.status === 'queued' ? 0 : clampPercent(r.progress.percent);
  const text = progressText(r);
  el.dataset.replayStatus = r.status;
  const t = el.querySelector('.rp-progress-text');
  if (t && t.textContent !== text) t.textContent = text;
  const bar = el.querySelector('progress');
  if (bar) {
    bar.value = p;
    bar.setAttribute('value', String(p));
    bar.setAttribute('aria-valuenow', String(p));
    bar.setAttribute('aria-valuetext', text);
  }
}

function failedView(r: Replay, o: ReplayViewOptions): HTMLElement {
  return h('section', { class: 'banner rp-alert', role: 'alert', 'data-replay-status': 'failed' }, icon('circle-alert', 16),
    h('div', { class: 'body' }, h('p', null, h('strong', null, c.failedTitle), ' ', r.error?.message ?? ''), metaLine(r, o)));
}

function metaLine(r: Replay, o: ReplayViewOptions): HTMLElement {
  const v = r.candidate.version ?? 0;
  const head = fill(c.meta, { name: r.candidate.name, v, baseline: baselineLabel(r.baseline), window: windowText(r.window, o.tz) });
  const n = r.summary?.actions ?? r.progress.total ?? r.progress.processed;
  const parts = [head, fill(c.metaCount, { n: number(n) })];
  if (r.status === 'succeeded' && r.elapsed_ms !== undefined) parts.push(duration(r.elapsed_ms));
  return h('p', { class: 'rp-meta t-footnote t-2 m-0 num' }, o.latest ? `${c.latest} · ` : null, parts.join(' · '), ' · ', relTime(r.finished_at ?? r.created_at, o.tz, o.now));
}

/** replayResult: a succeeded replay, read top to bottom like the CLI. */
export function replayResult(r: Replay, o: ReplayViewOptions): HTMLElement {
  const s = r.summary ?? { actions: 0, evaluated: 0, unchanged: 0, changed: 0, rule_changed: 0, unknown: 0 };
  const tr = transitionsSorted(r.transitions);
  const rc = ruleChangesSorted(r.rule_changes);
  const v = r.candidate.version ?? 0;
  const unk = s.unknown > 0 ? unknownLine(r.unknown) : '';
  const digest = r.result_digest ?? '';
  return h('section', { class: 'card rp-result', 'data-replay-status': 'succeeded', 'aria-labelledby': 'rp-summary' },
    h('header', { class: 'rp-head' },
      h('h2', { class: 'rp-summary num', id: 'rp-summary' }, summaryLine(s)),
      metaLine(r, o),
      s.unknown > 0 ? h('p', { class: 'rp-unknown t-footnote m-0 num', 'data-replay-unknown': '' }, h('span', { class: 'rp-glyph', 'aria-hidden': 'true' }, '○'), ' ',
        h('span', { class: 'rp-unknown-text' }, h('span', { class: 'rp-unknown-n' }, fill(c.unknownTitle, { n: number(s.unknown) })), unk ? h('span', { class: 'rp-unknown-why t-2' }, h('span', { class: 'rp-sep' }, ' · '), unk) : null)) : null,
      r.sqlguard && sqlguardDrift(r.sqlguard) ? h('p', { class: 'rp-drift t-footnote t-2 m-0', 'data-replay-drift': '' }, icon('info', 16), driftLine(r.sqlguard)) : null),
    h('div', { class: 'rp-section' },
      h('h3', { class: 'rp-h' }, c.transitions),
      tr.length ? h('ul', { class: 'rp-rows', role: 'list' }, ...tr.map((t) => transitionRow(t, v))) : h('p', { class: 't-2 m-0' }, c.noChange)),
    rc.length ? h('div', { class: 'rp-section' },
      h('h3', { class: 'rp-h' }, c.ruleChanges),
      h('ul', { class: 'rp-rows', role: 'list' }, ...rc.map(ruleChangeRow))) : null,
    digest ? h('footer', { class: 'rp-foot t-footnote' },
      h('span', { class: 'rp-foot-text num' }, fill(c.digest, { digest: '' }), h('span', { class: 'mono', translate: 'no', title: digest }, shortDigest(digest)), ` · ${c.digestNote}`),
      h('button', { type: 'button', class: 'idchip idchip-icon rp-copy', 'data-copy': digest, title: c.copyDigest, 'aria-label': c.copyDigest }, icon('copy', 16))) : null);
}

function arrow(): HTMLElement {
  return h('span', { class: 'rp-arrow' }, h('span', { 'aria-hidden': 'true' }, ' → '), h('span', { class: 'sr-only' }, ` ${c.arrowWord} `));
}

function decisionWord(d: string, side: 'from' | 'to'): HTMLElement {
  return h('span', { class: 'rp-dec', 'data-decision': d, 'data-side': side }, d);
}

function transitionRow(t: Transition, v: number): HTMLElement {
  const rules = rulesOf(t);
  const tools = toolsOf(t);
  const human = humanOutcomeLine(t.human_outcome);
  return h('li', { class: 'rp-row', 'data-transition': `${t.from}>${t.to}` },
    h('div', { class: 'rp-row-head' },
      h('span', { class: 'rp-pair mono', translate: 'no' }, decisionWord(t.from, 'from'), arrow(), decisionWord(t.to, 'to')),
      h('span', { class: 'rp-count num' }, number(t.count))),
    h('div', { class: 'rp-row-body' },
      rules.length ? h('p', { class: 'rp-chips m-0' }, h('span', { class: 'sr-only' }, `${fill(c.rules, { v })}: `),
        ...rules.map((x) => h('span', { class: 'label-chip', translate: 'no', title: `${x.rule_id || c.defaultRule} · ${number(x.count)}` }, x.rule_id || c.defaultRule, rules.length > 1 ? h('span', { class: 'rp-chip-n num' }, number(x.count)) : null))) : null,
      human ? h('p', { class: 'rp-human t-footnote m-0 num' }, human) : null,
      tools.length ? h('p', { class: 'rp-tools t-footnote t-2 m-0' }, h('span', { class: 'sr-only' }, `${c.tools}: `),
        ...tools.flatMap((x, i) => [i ? ' · ' : '', h('span', { class: 'mono', translate: 'no' }, x.tool), tools.length > 1 ? h('span', { class: 'num' }, ` ${number(x.count)}`) : null])) : null,
      samplesBlock(t.samples)));
}

function ruleChangeRow(x: RuleChange): HTMLElement {
  return h('li', { class: 'rp-row', 'data-rule-change': `${x.from_rule}>${x.to_rule}` },
    h('div', { class: 'rp-row-head' },
      h('span', { class: 'rp-pair mono', translate: 'no' }, h('span', { class: 'rp-dec' }, x.decision)),
      h('span', { class: 'rp-count num' }, number(x.count))),
    h('div', { class: 'rp-row-body' },
      h('p', { class: 'rp-rules mono m-0', translate: 'no' }, x.from_rule || c.defaultRule, arrow(), x.to_rule || c.defaultRule),
      samplesBlock(x.samples)));
}

function samplesBlock(samples: string[] | null | undefined): Child {
  const ids = samplesOf(samples);
  if (!ids.length) return null;
  return h('details', { class: 'rp-samples' },
    h('summary', { class: 't-footnote t-2' }, fill(c.samples, { n: ids.length })),
    h('ul', { class: 'rp-ids', role: 'list' }, ...ids.map((id) => h('li', null, h('a', { href: `/app/actions/${id}`, class: 'mono', translate: 'no', title: id, 'aria-label': fill(c.sampleLabel, { id }) }, shortId(id))))));
}
