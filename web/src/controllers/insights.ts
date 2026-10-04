// insights.ts: the Insights screen (S28 §3.8). Four sections in one scroll (Suggestions, Shadow,
// Anomalies, Trends) and a rail (settings, how it works). Each section loads, fails and retries
// on its own. People decide: Accept runs a new version in shadow, Promote is S26's typed enforce
// sheet, Reject and Resolve need a reason, and every one is sealed by the server. Live events
// refetch only the section they touch; j/k, Enter, a, r and ? work without a pointer.

import { ApiError } from '../lib/api.ts';
import { decisionChip, idChip, relTime, type Decision } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { insightsCopy } from '../lib/copy-insights.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { date, dateTime, number, shortId, time } from '../lib/format.ts';
import { policiesCopy } from '../lib/copy-policies.ts';
import {
  accept, acceptBody, acceptToast, acknowledge, agreementLine, agreementPct, diffLines, earlyNotice, evidenceRows, generate, getPolicy, getSettings, getShadow,
  getStats, getVersion, isAdvisory, isSimulated, kindLabel, listAnomalies, listSuggestions, outcomeLine, patchSettings, phrasingNote, reject, replayDays,
  resolve, shadowTransitions, toLineDiff,
  type Anomaly, type AnomalyList, type InsightsSettings, type Recipients, type ShadowReport, type Suggestion,
} from '../lib/models/insights.ts';
import { hasDefaultAllow } from '../lib/models/policy.ts';
import { typing } from '../lib/shortcuts.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream, type EventFn } from '../lib/stream.ts';
import { openActivate } from './activation.ts';
import { trendsView } from './insightscharts.ts';
import { lineDiffView } from './linediffview.ts';
import { openSheet } from './overlay.ts';
import { errorState } from './screens.ts';
import { announce, setPageShortcuts, setSimulated } from './shell.ts';
import { apiErrorToast, toast } from './toast.ts';

const c = insightsCopy;
const tz = () => get()?.org.timezone ?? 'UTC';
const isAdmin = () => atLeast(get()?.me.membership.role, 'admin');
const DECISIONS = new Set(['allow', 'deny', 'require_approval']);

function chipFor(d: string): HTMLElement {
  return DECISIONS.has(d) ? decisionChip(d as Decision) : h('span', { class: 'chip chip-outline mono', translate: 'no' }, d);
}
function arrow(): HTMLElement {
  return h('span', { class: 'ins-arrow' }, h('span', { 'aria-hidden': 'true' }, '→'), h('span', { class: 'sr-only' }, ` ${c.card.arrow} `));
}
function errLine(): HTMLElement {
  return h('p', { class: 'error', role: 'alert', hidden: true });
}
function showErr(el: HTMLElement, e: unknown): void {
  const msg = e instanceof Error ? e.message : String(e);
  const req = e instanceof ApiError && e.requestId ? h('span', { class: 'mono t-2', translate: 'no' }, ` ${e.requestId}`) : null;
  replace(el, icon('circle-alert', 16), h('span', null, msg, req));
  el.hidden = false;
}

// ---------- the suggestion card ----------

export interface CardOptions {
  admin: boolean;
  tz: string;
  expanded?: boolean;
  onAccept: (s: Suggestion) => void;
  onReject: (s: Suggestion) => void;
}

/** suggestionCard: one decision per card (S28 §3.8), exported for Vitest. */
export function suggestionCard(sg: Suggestion, o: CardOptions): HTMLElement {
  const id = sg.id;
  const tid = `sg-${id}-t`;
  const advice = isAdvisory(sg);
  const head = h('header', { class: 'ins-card-head' },
    h('span', { class: 'ins-kind' }, kindLabel(sg.kind)), ' ',
    sg.target_rule_id ? h('span', { class: 'ins-target' }, h('span', { 'aria-hidden': 'true', class: 't-2' }, '·'), ' ', h('span', { class: 'mono', translate: 'no' }, sg.target_rule_id)) : null, ' ',
    h('span', { class: 'ins-card-id' }, idChip(id)));

  // Evidence: key/value, numbers tabular and right-aligned.
  const rows = evidenceRows(sg.evidence ?? {}, o.tz);
  const kv = h('dl', { class: 'ins-kv' }, ...rows.flatMap((r) => [
    h('dt', null, r.label),
    h('dd', { class: `${r.num ? 'num' : ''}${r.mono ? ' mono' : ''}${r.ids ? ' ins-kv-wide' : ''}`.trim() || null, ...(r.mono ? { translate: 'no' } : {}) },
      r.ids ? h('span', { class: 'ins-ids' }, ...r.ids.map((x) => h('a', { href: `/app/actions/${x}`, class: 'mono', translate: 'no', title: x }, shortId(x)))) : r.value),
  ]));
  const evidence = h('section', { class: 'ins-panel', 'aria-labelledby': `${tid}-ev` }, h('h4', { class: 'ins-panel-h', id: `${tid}-ev` }, c.card.evidence), kv);

  // Replay: the transitions as decision chips, counts tabular, human outcomes in footnote size.
  const rp = sg.replay;
  const trans = [...(rp?.transitions ?? [])].sort((a, b) => b.count - a.count);
  const replay = h('section', { class: 'ins-panel', 'aria-labelledby': `${tid}-rp` },
    h('h4', { class: 'ins-panel-h', id: `${tid}-rp` }, c.card.replay,
      rp ? h('span', { class: 'ins-panel-meta num' }, fill(c.card.replayMeta, { days: replayDays(rp.window), n: number(rp.summary.actions) })) : null),
    !rp ? h('p', { class: 't-footnote t-2 m-0' }, c.card.replayAdvice)
      : trans.length === 0 ? h('p', { class: 't-footnote t-2 m-0' }, c.card.replayNone)
      : h('ul', { class: 'ins-trans', role: 'list' }, ...trans.map((t) => {
        const human = outcomeLine(t.human_outcome);
        return h('li', { 'data-transition': `${t.from}>${t.to}` },
          h('div', { class: 'ins-trans-row' }, h('span', { class: 'ins-pair' }, chipFor(t.from), arrow(), chipFor(t.to)), h('span', { class: 'ins-count num' }, number(t.count))),
          human ? h('p', { class: 'ins-human t-footnote num' }, human) : null);
      })));

  // The diff, collapsed: "▸ Policy change +8 lines". Expanded: S26's line-diff on the code surface.
  let diff: HTMLElement | null = null;
  if (sg.diff) {
    const regionId = `${tid}-diff`;
    const region = h('div', { class: 'ins-diff-body', id: regionId });
    region.hidden = !o.expanded;
    const btn = h('button', { type: 'button', class: 'ins-diff-toggle', 'aria-expanded': o.expanded ? 'true' : 'false', 'aria-controls': regionId, 'data-diff-toggle': '' },
      icon('chevron-right', 16, 'ins-chev'), h('span', null, c.card.diff), ' ', h('span', { class: 'num ins-diff-n' }, diffLines(sg.diff)), ' ',
      sg.diff.removed ? h('span', { class: 'num t-2' }, fill(c.card.diffRemoved, { n: sg.diff.removed })) : null);
    const draw = () => {
      if (!region.firstChild) replace(region, lineDiffView(toLineDiff(sg.diff!), { from: `v${sg.policy.base_version}`, to: c.card.diff }));
    };
    if (o.expanded) draw();
    btn.addEventListener('click', () => {
      const open = btn.getAttribute('aria-expanded') !== 'true';
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open) draw();
      region.hidden = !open;
    });
    diff = h('div', { class: 'ins-diff' }, btn, region);
  }

  const warnings = (sg.warnings ?? []).length
    ? h('ul', { class: 'ins-warn t-footnote', role: 'list', 'aria-label': c.card.warnings }, ...(sg.warnings ?? []).map((w) => h('li', null, icon('circle-dashed', 16), w.message)))
    : null;
  const risk = sg.risk_note ? h('p', { class: 'ins-risk t-footnote' }, h('span', { class: 'ins-risk-l' }, c.card.risk, h('span', { class: 'sr-only' }, ':')), ' ', h('span', null, sg.risk_note)) : null;
  const phr = phrasingNote(sg.phrasing);
  const note = phr ? h('p', { class: 'ins-phrasing t-footnote t-2' }, icon('info', 16), phr) : null;

  let actions: HTMLElement;
  if (o.admin && sg.status === 'open') {
    actions = h('footer', { class: 'ins-actions' },
      advice ? h('p', { class: 't-footnote t-2 m-0 ins-actions-note' }, c.card.adviceNote) : null,
      h('button', { type: 'button', class: 'btn btn-quiet', 'data-reject': '', 'aria-keyshortcuts': 'r', onclick: () => o.onReject(sg) }, c.card.reject),
      advice ? null : h('button', { type: 'button', class: 'btn btn-primary', 'data-accept': '', 'aria-keyshortcuts': 'a', onclick: () => o.onAccept(sg) }, c.card.accept));
  } else {
    actions = h('footer', { class: 'ins-actions ins-actions-viewer' }, h('p', { class: 't-footnote t-2 m-0' }, icon('lock', 16), c.card.viewerNote));
  }

  return h('article', { class: 'card ins-card', tabindex: '-1', 'data-suggestion': id, 'data-kind': sg.kind, 'aria-labelledby': tid },
    head,
    h('h3', { class: 't-title-3 ins-title', id: tid }, sg.title),
    h('p', { class: 'ins-rationale t-pretty' }, sg.rationale),
    h('div', { class: 'ins-panels' }, evidence, replay),
    diff, warnings, risk, note, actions);
}

// ---------- the shadow card ----------

export interface ShadowOptions { admin: boolean; tz: string; onPromote: (r: ShadowReport) => void }

export function shadowCard(r: ShadowReport, o: ShadowOptions): HTMLElement {
  const label = `${r.shadow.name} v${r.shadow.version}`;
  const trans = shadowTransitions(r);
  return h('article', { class: 'card ins-shadow', 'aria-labelledby': 'shadow-t', 'data-shadow-version': String(r.shadow.version) },
    h('header', { class: 'ins-shadow-head' },
      h('div', { class: 'ins-shadow-name' },
        h('h3', { class: 't-headline mono', id: 'shadow-t', translate: 'no' }, label),
        h('span', { class: 'chip chip-outline' }, icon('contrast', 16), policiesCopy.status.shadow),
        r.shadow.since ? h('span', { class: 't-footnote t-2' }, fill(c.shadow.since, { when: '' }), h('time', { datetime: r.shadow.since, title: dateTime(r.shadow.since, o.tz) }, `${date(r.shadow.since, o.tz)} ${time(r.shadow.since, o.tz)}`)) : null),
      o.admin
        ? h('button', { type: 'button', class: 'btn btn-secondary', 'data-promote': '', 'aria-label': fill(c.shadow.promoteLabel, { name: r.shadow.name, v: r.shadow.version }), onclick: () => o.onPromote(r) }, icon('gate', 16), c.shadow.promote)
        : null),
    h('p', { class: 'ins-sentence t-pretty' }, r.sentence),
    r.summary.evaluated === 0 ? null : h('div', { class: 'ins-agree' },
      h('span', { class: 't-metric' }, agreementPct(r).split(' ')[0]),
      h('div', null, h('p', { class: 'm-0 num' }, agreementLine(r)),
        h('p', { class: 'm-0 t-footnote t-2' }, fill(c.shadow.enforcedBy, { list: r.enforce.map((e) => `${e.name} v${e.version}`).join(', ') || '·' })))),
    trans.length ? h('div', { class: 'ins-shadow-trans' },
      h('h4', { class: 'ins-panel-h' }, c.shadow.disagreements),
      h('ul', { class: 'ins-trans', role: 'list' }, ...trans.map((t) => h('li', { 'data-transition': `${t.enforce}>${t.shadow}` },
        h('div', { class: 'ins-trans-row' }, h('span', { class: 'ins-pair' }, chipFor(t.enforce), arrow(), chipFor(t.shadow)), h('span', { class: 'ins-count num' }, number(t.count))),
        h('p', { class: 'ins-human t-footnote t-2' },
          ...[...(t.by_rule ?? []).map((x) => x.rule_id), ...(t.by_agent ?? []).map((x) => x.name)].filter(Boolean).flatMap((x, i) => [i ? ' · ' : '', h('span', { class: 'mono', translate: 'no' }, x)]))))))
      : null,
    h('footer', { class: 'ins-shadow-foot t-footnote' },
      h('a', { href: `/app/policies/${r.shadow.policy}?tab=versions` }, c.shadow.viewPolicy, icon('arrow-right', 16)),
      o.admin ? null : h('span', { class: 't-2' }, c.shadow.viewerNote)));
}

// ---------- anomalies ----------

export interface AnomalyOptions { admin: boolean; tz: string; facts: boolean; onAck: (a: Anomaly) => void; onResolve: (a: Anomaly) => void }

export function anomalyRow(a: Anomaly, o: AnomalyOptions): HTMLElement {
  const kind = c.anomalies.kinds[a.kind] ?? a.kind.replace(/_/g, ' ');
  const meta: Child[] = [];
  if (a.acknowledged) meta.push(fill(c.anomalies.acknowledged, { who: a.acknowledged.by, when: '' }), relTime(a.acknowledged.at, o.tz));
  if (a.occurrences > 1) meta.push(meta.length ? ' · ' : '', fill(c.anomalies.occurrences, { n: number(a.occurrences) }));
  return h('li', { class: 'ins-anom', 'data-anomaly': a.id, 'data-status': a.status },
    h('div', { class: 'ins-anom-head' },
      h('span', { class: 'ins-kind' }, kind),
      h('span', { 'aria-hidden': 'true', class: 't-2' }, '·'),
      h('span', { class: 'ins-anom-agent', translate: 'no' }, a.agent.name),
      a.tool ? h('span', { class: 'mono t-2', translate: 'no' }, a.tool) : null,
      a.active ? h('span', { class: 'chip chip-active' }, icon('activity', 16), o.facts ? c.anomalies.activeFacts : c.anomalies.active) : null,
      h('span', { class: 'ins-anom-when t-footnote t-2' }, relTime(a.window.end, o.tz))),
    h('p', { class: 'ins-anom-title m-0' }, a.title),
    meta.length ? h('p', { class: 't-footnote t-2 m-0' }, ...meta) : null,
    o.admin ? h('div', { class: 'ins-anom-actions' },
      a.acknowledged ? null : h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'data-ack': '', onclick: () => o.onAck(a) }, icon('check', 16), c.anomalies.acknowledge),
      h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'data-resolve': '', onclick: () => o.onResolve(a) }, c.anomalies.resolve)) : null);
}

// ---------- sheets ----------

function reasonField(id: string, label: string, help: string, required: boolean): { field: HTMLElement; input: HTMLTextAreaElement; err: HTMLElement } {
  const input = h('textarea', { class: 'textarea', id, name: 'reason', rows: 3, maxlength: 280, autocomplete: 'off', 'aria-describedby': `${id}-h ${id}-e`, ...(required ? { required: true, 'aria-required': 'true' } : {}) });
  const err = h('p', { class: 'error', id: `${id}-e`, role: 'alert', hidden: true });
  input.addEventListener('input', () => {
    if (input.getAttribute('aria-invalid') === 'true' && input.value.trim().length >= 3) {
      input.removeAttribute('aria-invalid');
      err.hidden = true;
    }
  });
  return { field: h('div', { class: 'field' }, h('label', { class: 'label', for: id }, label), input, h('p', { class: 'help', id: `${id}-h` }, help), err), input, err };
}

/** submitOnEnter: Enter outside the reason box submits, like a form (the keyboard path j j a Enter). */
function submitOnEnter(panel: HTMLElement, go: () => void): void {
  panel.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || e.isComposing) return;
    const t = e.target as HTMLElement;
    if (t.tagName === 'TEXTAREA' && !(e.metaKey || e.ctrlKey)) return;
    if (t.tagName === 'BUTTON' || t.tagName === 'A' || t.tagName === 'SELECT') return;
    e.preventDefault();
    go();
  });
}

export function openAccept(sg: Suggestion, onDone: () => void): void {
  void getPolicy(sg.policy.id).then((p) => {
    const live = p.shadow_version ?? null;
    const reason = reasonField('acc-reason', c.accept.reason, c.accept.reasonHelp, false);
    const check = live ? h('input', { type: 'checkbox', id: 'acc-replace', name: 'replace_shadow', required: true, 'aria-required': 'true', 'aria-describedby': 'acc-replace-e' }) : null;
    const checkErr = h('p', { class: 'error', id: 'acc-replace-e', role: 'alert', hidden: true });
    const err = errLine();
    const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
    const go = h('button', { type: 'button', class: 'btn btn-primary', 'data-accept-go': '' }, c.accept.button);
    const key = crypto.randomUUID();
    let busy = false;
    check?.addEventListener('change', () => { if (check.checked) checkErr.hidden = true; });
    const body: Child[] = [
      h('p', { class: 't-pretty' }, acceptBody(p.name, p)),
      h('p', { class: 'ins-sheet-title t-footnote' }, h('span', { class: 'ins-kind' }, kindLabel(sg.kind)), ' ', h('span', null, sg.title)),
      live ? h('div', { class: 'ins-replace' },
        h('p', { class: 'm-0' }, fill(c.accept.replaceNote, { name: p.name, v: live })),
        h('label', { class: 'check' }, check!, h('span', null, fill(c.accept.replaceCheck, { v: live }))), checkErr) : null,
      reason.field,
      err,
    ];
    const ov = openSheet({ title: c.accept.title, body, foot: [cancel, go], initialFocus: () => check ?? go, className: 'ins-sheet' });
    cancel.addEventListener('click', ov.close);
    const submit = async () => {
      if (busy) return;
      if (check && !check.checked) {
        replace(checkErr, icon('circle-alert', 16), fill(c.accept.replaceNeeded, { v: live! }));
        checkErr.hidden = false;
        check.setAttribute('aria-invalid', 'true');
        check.focus();
        return;
      }
      busy = true;
      err.hidden = true;
      go.setAttribute('aria-busy', 'true');
      go.disabled = true;
      replace(go, c.accept.busy);
      try {
        const r = await accept(sg.id, { reason: reason.input.value.trim(), replaceShadow: !!check?.checked }, key);
        ov.close();
        const t = acceptToast(r, shortId);
        toast(t.text, { kind: 'success', ...(t.deed ? { action: { label: c.accept.viewDeed, href: `/app/deeds/${t.deed}` } } : {}) });
        if (r.note) toast(r.note);
        onDone();
      } catch (e) {
        busy = false;
        go.removeAttribute('aria-busy');
        go.disabled = false;
        replace(go, c.accept.button);
        showErr(err, e);
        if (e instanceof ApiError && (e.code === 'suggestion_stale' || e.code === 'suggestion_decided')) onDone();
      }
    };
    go.addEventListener('click', () => void submit());
    submitOnEnter(ov.el, () => void submit());
  }).catch(apiErrorToast);
}

export function openReject(sg: Suggestion, onDone: () => void): void {
  const reason = reasonField('rej-reason', c.reject.reason, c.reject.reasonHelp, true);
  const err = errLine();
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: 'btn btn-primary', 'data-reject-go': '' }, c.reject.button);
  let busy = false;
  const ov = openSheet({
    title: c.reject.title, className: 'ins-sheet', foot: [cancel, go], initialFocus: () => reason.input,
    body: [h('p', { class: 't-pretty' }, c.reject.body), h('p', { class: 'ins-sheet-title t-footnote' }, h('span', { class: 'ins-kind' }, kindLabel(sg.kind)), ' ', h('span', null, sg.title)), reason.field, err],
  });
  cancel.addEventListener('click', ov.close);
  const submit = async () => {
    if (busy) return;
    const text = reason.input.value.trim();
    if (text.length < 3) {
      reason.input.setAttribute('aria-invalid', 'true');
      replace(reason.err, icon('circle-alert', 16), c.reject.required);
      reason.err.hidden = false;
      reason.input.focus();
      return;
    }
    busy = true;
    go.setAttribute('aria-busy', 'true');
    go.disabled = true;
    try {
      await reject(sg.id, text);
      ov.close();
      toast(c.reject.toast, { kind: 'success' });
      onDone();
    } catch (e) {
      busy = false;
      go.removeAttribute('aria-busy');
      go.disabled = false;
      if (e instanceof ApiError && e.param === 'reason') {
        reason.input.setAttribute('aria-invalid', 'true');
        showErr(reason.err, e);
      } else showErr(err, e);
    }
  };
  go.addEventListener('click', () => void submit());
  submitOnEnter(ov.el, () => void submit());
}

function openPromote(r: ShadowReport, onDone: () => void): void {
  void Promise.all([getPolicy(r.shadow.policy), getVersion(r.shadow.policy, r.shadow.version)]).then(([p, v]) => {
    const notice = earlyNotice(r);
    const trans = shadowTransitions(r);
    const extra: Child[] = [
      h('div', { class: 'ins-promote' },
        h('p', { class: 'm-0 t-pretty' }, r.sentence),
        r.summary.evaluated ? h('p', { class: 'm-0 num ins-promote-agree' }, h('strong', null, agreementPct(r)), ' · ', agreementLine(r)) : null,
        trans.length ? h('ul', { class: 'ins-trans ins-trans-compact', role: 'list' }, ...trans.map((t) => h('li', null,
          h('div', { class: 'ins-trans-row' }, h('span', { class: 'ins-pair' }, chipFor(t.enforce), arrow(), chipFor(t.shadow)), h('span', { class: 'ins-count num' }, number(t.count)))))) : null),
      notice ? h('p', { class: 'banner banner-readonly ins-early', role: 'note' }, icon('info', 16), h('span', null, notice)) : null,
    ];
    openActivate({ policy: p, version: v, mode: 'enforce', defaultAllow: hasDefaultAllow(v, v.source), onDone, extra });
  }).catch(apiErrorToast);
}

function openResolve(a: Anomaly, onDone: () => void): void {
  const reason = reasonField('res-reason', c.anomalies.resolveReason, c.reject.reasonHelp, true);
  const err = errLine();
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: 'btn btn-primary', 'data-resolve-go': '' }, c.anomalies.resolveButton);
  const ov = openSheet({ title: c.anomalies.resolveTitle, className: 'ins-sheet', foot: [cancel, go], initialFocus: () => reason.input,
    body: [h('p', { class: 't-pretty' }, c.anomalies.resolveBody), h('p', { class: 'ins-sheet-title t-footnote' }, a.title), reason.field, err] });
  cancel.addEventListener('click', ov.close);
  let busy = false;
  const submit = async () => {
    if (busy) return;
    const text = reason.input.value.trim();
    if (text.length < 3) {
      reason.input.setAttribute('aria-invalid', 'true');
      replace(reason.err, icon('circle-alert', 16), c.anomalies.resolveReason);
      reason.err.hidden = false;
      reason.input.focus();
      return;
    }
    busy = true;
    go.disabled = true;
    try {
      await resolve(a.id, text);
      ov.close();
      toast(c.anomalies.resolved, { kind: 'success' });
      onDone();
    } catch (e) {
      busy = false;
      go.disabled = false;
      showErr(err, e);
    }
  };
  go.addEventListener('click', () => void submit());
  submitOnEnter(ov.el, () => void submit());
}

function openSettings(cur: InsightsSettings, onDone: () => void): void {
  const facts = h('input', { type: 'checkbox', id: 'set-facts', name: 'anomaly_facts', 'aria-describedby': 'set-facts-h' });
  facts.checked = cur.anomaly_facts;
  const phr = h('input', { type: 'checkbox', id: 'set-phrasing', name: 'llm_phrasing', 'aria-describedby': 'set-phrasing-h' });
  phr.checked = cur.llm_phrasing;
  if (!cur.phrasing_available && !cur.llm_phrasing) phr.disabled = true;
  const sel = h('select', { class: 'select', id: 'set-digest', name: 'digest' }, ...(['owners', 'admins', 'off'] as Recipients[]).map((k) => h('option', { value: k }, c.rail.digestTo[k] ?? k)));
  sel.value = String(cur.digest?.recipients ?? 'owners');
  const reason = reasonField('set-reason', c.settings.reason, c.reject.reasonHelp, true);
  const err = errLine();
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, c.settings.save);
  const ov = openSheet({
    title: c.settings.title, className: 'ins-sheet', foot: [cancel, go], initialFocus: () => facts,
    body: [
      h('p', { class: 't-2 t-pretty' }, c.settings.lede),
      h('div', { class: 'field' }, h('label', { class: 'check' }, facts, h('span', null, c.settings.facts)), h('p', { class: 'help', id: 'set-facts-h' }, c.settings.factsHelp)),
      h('div', { class: 'field' }, h('label', { class: 'check' }, phr, h('span', null, c.settings.phrasing)),
        h('p', { class: 'help', id: 'set-phrasing-h' }, cur.phrasing_available ? c.settings.phrasingHelp : c.settings.phrasingUnavailable)),
      h('div', { class: 'field' }, h('label', { class: 'label', for: 'set-digest' }, c.settings.digest), h('span', { class: 'select-wrap' }, sel, icon('chevron-down', 16))),
      reason.field, err,
    ],
  });
  cancel.addEventListener('click', ov.close);
  go.addEventListener('click', async () => {
    const text = reason.input.value.trim();
    const patch: Parameters<typeof patchSettings>[0] = { reason: text };
    if (facts.checked !== cur.anomaly_facts) patch.anomaly_facts = facts.checked;
    if (phr.checked !== cur.llm_phrasing) patch.llm_phrasing = phr.checked;
    if (sel.value !== cur.digest?.recipients) patch.digest = { recipients: sel.value as Recipients };
    if (Object.keys(patch).length === 1) {
      showErr(err, new Error(c.settings.noChange));
      return;
    }
    if (text.length < 3) {
      reason.input.setAttribute('aria-invalid', 'true');
      replace(reason.err, icon('circle-alert', 16), c.settings.reason);
      reason.err.hidden = false;
      reason.input.focus();
      return;
    }
    go.disabled = true;
    try {
      await patchSettings(patch);
      ov.close();
      toast(c.settings.saved, { kind: 'success' });
      onDone();
    } catch (e) {
      go.disabled = false;
      showErr(err, e);
    }
  });
}

// ---------- the screen ----------

type Key = 'suggestions' | 'shadow' | 'anomalies' | 'trends' | 'settings';

export function mountInsights(): void {
  const root = maybe('insights');
  if (!root) return;
  let suggestions: Suggestion[] = [];
  let shadow: ShadowReport | null = null;
  let settings: InsightsSettings | null = null;
  let anomalies: AnomalyList | null = null;
  let sigSuggestions = '';
  const expanded = new Set<string>();

  const body = (k: Exclude<Key, 'settings'>) => maybe(`body-${k}`)!;
  const meta = (k: Exclude<Key, 'settings'>, text: string) => { const m = maybe(`meta-${k}`); if (m) m.textContent = text; };
  const failed = (k: Exclude<Key, 'settings'>, e: unknown, retry: () => void) => {
    const host = body(k);
    host.setAttribute('aria-busy', 'false');
    replace(host, h('div', { class: 'card ins-error' }, errorState(e, retry)));
  };
  const done = (k: Exclude<Key, 'settings'>, ...children: Child[]) => {
    const host = body(k);
    replace(host, ...children);
    host.setAttribute('aria-busy', 'false');
  };
  const empty = (text: string, ic: string, action?: Child) => {
    const [first, ...rest] = text.split(/(?<=\.)\s+/);
    return h('div', { class: 'card ins-empty', role: 'status' }, h('span', { class: 'well' }, icon(ic, 20)),
      h('div', null, h('p', { class: 't-headline m-0' }, first ?? text), rest.length ? h('p', { class: 'm-0 t-2 t-pretty' }, rest.join(' ')) : null), action ?? null);
  };

  // ----- suggestions -----
  const renderSuggestions = () => {
    const focusedId = (document.activeElement as HTMLElement | null)?.closest?.('.ins-card')?.getAttribute('data-suggestion') ?? null;
    meta('suggestions', suggestions.length ? fill(c.sectionCount, { n: number(suggestions.length) }) : '');
    if (!suggestions.length) {
      done('suggestions', empty(c.empty.suggestions, 'file-diff'));
      return;
    }
    const admin = isAdmin();
    done('suggestions', h('div', { class: 'ins-cards' }, ...suggestions.map((sg) => {
      const card = suggestionCard(sg, { admin, tz: tz(), expanded: expanded.has(sg.id), onAccept: (x) => openAccept(x, refreshAfterDecision), onReject: (x) => openReject(x, refreshAfterDecision) });
      card.querySelector('[data-diff-toggle]')?.addEventListener('click', (e) => {
        const open = (e.currentTarget as HTMLElement).getAttribute('aria-expanded') === 'true';
        if (open) expanded.add(sg.id); else expanded.delete(sg.id);
      });
      return card;
    })));
    if (focusedId) (root.querySelector(`.ins-card[data-suggestion="${CSS.escape(focusedId)}"]`) as HTMLElement | null)?.focus();
  };
  const loadSuggestions = async (o: { announceNew?: boolean } = {}) => {
    try {
      const before = new Set(suggestions.map((x) => x.id));
      const next = await listSuggestions('open');
      const sig = next.map((x) => `${x.id}:${x.status}`).join(',');
      const added = next.filter((x) => !before.has(x.id)).length;
      suggestions = next;
      if (sig !== sigSuggestions || body('suggestions').getAttribute('aria-busy') !== 'false') {
        sigSuggestions = sig;
        renderSuggestions();
      }
      if (o.announceNew && added > 0) announce(added === 1 ? c.newOne : fill(c.newMany, { n: added }));
    } catch (e) {
      failed('suggestions', e, () => void loadSuggestions());
    }
  };

  // ----- shadow -----
  const loadShadow = async () => {
    try {
      shadow = await getShadow();
      if (!shadow) {
        meta('shadow', '');
        done('shadow', empty(c.empty.shadow, 'git-compare', h('a', { class: 'btn btn-quiet btn-sm', href: '/app/policies' }, copy.pages.policies.title, icon('arrow-right', 16))));
        return;
      }
      meta('shadow', '');
      done('shadow', shadowCard(shadow, { admin: isAdmin(), tz: tz(), onPromote: (r) => openPromote(r, refreshAll) }));
    } catch (e) {
      failed('shadow', e, () => void loadShadow());
    }
  };

  // ----- anomalies -----
  const renderAnomalies = () => {
    if (!anomalies) return;
    const list = anomalies.data;
    meta('anomalies', list.length ? fill(c.sectionCount, { n: number(list.length) }) : '');
    const notes: Child[] = [];
    if (anomalies.notice) notes.push(h('p', { class: 't-footnote t-2 m-0' }, anomalies.notice));
    for (const b of anomalies.baseline_building ?? []) notes.push(h('p', { class: 't-footnote t-2 m-0' }, b.sentence));
    if (!list.length) {
      done('anomalies', empty(c.empty.anomalies, 'activity'), ...notes);
      return;
    }
    const admin = isAdmin();
    const facts = settings?.anomaly_facts ?? anomalies.anomaly_facts;
    done('anomalies', h('ul', { class: 'card ins-anoms', role: 'list' }, ...list.map((a) => anomalyRow(a, {
      admin, tz: tz(), facts,
      onAck: (x) => void acknowledge(x.id).then(() => { toast(c.anomalies.acked, { kind: 'success' }); void loadAnomalies(); }).catch(apiErrorToast),
      onResolve: (x) => openResolve(x, () => void loadAnomalies()),
    }))), admin ? null : h('p', { class: 't-footnote t-2 m-0' }, c.anomalies.viewerNote), ...notes);
  };
  const loadAnomalies = async () => {
    try {
      anomalies = await listAnomalies();
      renderAnomalies();
    } catch (e) {
      failed('anomalies', e, () => void loadAnomalies());
    }
  };

  // ----- trends -----
  const loadTrends = async () => {
    try {
      const st = await getStats();
      meta('trends', fill(c.trends.window, { tz: st.timezone }));
      if (!st.totals.actions) {
        done('trends', empty(st.empty ?? c.empty.trends, 'chart-column'));
        return;
      }
      done('trends', trendsView(st));
    } catch (e) {
      failed('trends', e, () => void loadTrends());
    }
  };

  // ----- settings (the rail) -----
  const renderRail = () => {
    const host = maybe('rail-settings');
    const change = maybe('rail-change');
    if (!host || !settings) return;
    const s = settings;
    replace(host,
      h('dt', null, c.rail.facts), h('dd', null, s.anomaly_facts ? c.rail.factsOn : c.rail.factsOff),
      h('dt', null, c.rail.phrasing), h('dd', null, s.llm_phrasing ? c.rail.phrasingOn : c.rail.phrasingOff),
      h('dt', null, c.rail.digest), h('dd', null, c.rail.digestTo[String(s.digest?.recipients)] ?? String(s.digest?.recipients ?? '')));
    host.setAttribute('aria-busy', 'false');
    if (s.notice) host.after(h('p', { class: 't-footnote t-2 m-0 ins-rail-notice' }, s.notice));
    if (change) replace(change, isAdmin() ? h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'data-settings': '', 'aria-label': `${c.rail.change}: ${c.settings.title}`, onclick: () => openSettings(s, () => { void loadSettings(); void loadAnomalies(); }) }, c.rail.change) : null);
  };
  const loadSettings = async () => {
    try {
      settings = await getSettings();
      setSimulated(isSimulated(get()?.org, settings));
      renderRail();
    } catch (e) {
      const host = maybe('rail-settings');
      if (host) {
        host.setAttribute('aria-busy', 'false');
        replace(host, h('dt', null, c.rail.settings), h('dd', { class: 't-2' }, e instanceof Error ? e.message : String(e)));
      }
    }
  };

  const refreshAfterDecision = () => { void loadSuggestions(); void loadShadow(); };
  const refreshAll = () => { void loadSuggestions(); void loadShadow(); void loadAnomalies(); void loadTrends(); void loadSettings(); };

  // ----- header action: generate (admins) -----
  const drawActions = () => {
    const slot = maybe('insights-actions');
    if (!slot) return;
    if (!isAdmin()) { replace(slot); return; }
    const label = (t: string) => h('span', { class: 'ins-gen-label' }, t);
    const btn = h('button', { type: 'button', class: 'btn btn-secondary btn-sm ins-gen', 'data-generate': '' }, icon('refresh-cw', 16), label(c.generate));
    btn.addEventListener('click', async () => {
      if (btn.getAttribute('aria-busy') === 'true') return;
      btn.setAttribute('aria-busy', 'true');
      replace(btn, icon('refresh-cw', 16), label(c.generating));
      try {
        const r = await generate();
        const n = r.created?.length ?? 0;
        toast(r.notice ?? (n === 0 ? fill(c.generatedNone, { unchanged: number(r.unchanged) }) : n === 1 ? c.generatedOne : fill(c.generated, { n })), { kind: n ? 'success' : 'neutral' });
        await loadSuggestions();
      } catch (e) {
        apiErrorToast(e);
      } finally {
        btn.removeAttribute('aria-busy');
        replace(btn, icon('refresh-cw', 16), label(c.generate));
      }
    });
    replace(slot, btn);
  };

  // ----- keyboard: j/k between cards, Enter toggles the diff, a accepts, r rejects -----
  setPageShortcuts(c.shortcuts.title, [['j k', c.shortcuts.move], ['Enter', c.shortcuts.diff], ['a', c.shortcuts.accept], ['r', c.shortcuts.reject]]);
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
    if (document.querySelector('.palette, .sheet, .dialog, .menu')) return;
    const cards = [...root.querySelectorAll<HTMLElement>('.ins-card')];
    if (!cards.length) return;
    const active = document.activeElement as HTMLElement | null;
    const cur = active ? cards.findIndex((x) => x === active || x.contains(active)) : -1;
    const k = e.key;
    if (k === 'j' || k === 'k') {
      e.preventDefault();
      const next = k === 'j' ? Math.min(cards.length - 1, cur + 1) : Math.max(0, cur < 0 ? 0 : cur - 1);
      cards[next]!.focus();
      cards[next]!.scrollIntoView({ block: 'nearest', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
      return;
    }
    if (cur < 0) return;
    const card = cards[cur]!;
    if (k === 'Enter' && active === card) {
      e.preventDefault();
      card.querySelector<HTMLButtonElement>('[data-diff-toggle]')?.click();
    } else if (k === 'a' || k === 'r') {
      const btn = card.querySelector<HTMLButtonElement>(k === 'a' ? '[data-accept]' : '[data-reject]');
      if (!btn) return;
      e.preventDefault();
      btn.click();
    }
  });

  const start = () => {
    drawActions();
    void loadSettings().then(() => renderAnomalies());
    void loadSuggestions();
    void loadShadow();
    void loadAnomalies();
    void loadTrends();
    const live = stream();
    const timers: Partial<Record<Key, number>> = {};
    const soon = (k: Key, fn: () => void, ms = 400) => {
      clearTimeout(timers[k]);
      timers[k] = window.setTimeout(fn, ms);
    };
    live.on('suggestion.created', () => soon('suggestions', () => void loadSuggestions({ announceNew: true })));
    live.on('anomaly.detected', () => soon('anomalies', () => void loadAnomalies()));
    live.on('policy.activated', () => { soon('shadow', () => void loadShadow()); soon('suggestions', () => void loadSuggestions()); });
    const onDeed: EventFn = (ev) => {
      const k = (ev.data?.object as { kind?: string } | undefined)?.kind ?? (ev as unknown as { kind?: string }).kind;
      if (k && k !== 'admin') return; // action deeds: Trends catch up on the next load
      soon('suggestions', () => void loadSuggestions(), 800);
      soon('shadow', () => void loadShadow(), 800);
      soon('anomalies', () => void loadAnomalies(), 800);
    };
    live.on('deed.sealed', onDeed);
    live.onResync(refreshAll);
  };
  if (get()) start();
  else {
    document.addEventListener('dg:ready', start, { once: true });
    document.addEventListener('dg:load-failed', (ev) => {
      const e = (ev as CustomEvent).detail;
      for (const k of ['suggestions', 'shadow', 'anomalies', 'trends'] as const) failed(k, e, () => location.reload());
    }, { once: true });
  }
}
