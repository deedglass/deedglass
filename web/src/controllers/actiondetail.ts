// actiondetail.ts: one action (S25 §3.5): the verdict sentence (the API's witness.summary,
// verbatim, with a prefix), the timeline, the decision, the Witness with the DiffTable, the request
// and result (rendered only when opened), amendments, the DeedPanel with the in-browser seal check,
// the approval block and the undo block (S23's strings, verbatim). Live: the page refetches when
// the action moves, and the Sealed moment plays once if it seals while you watch.

import type { ConsoleMirrorState } from '@verify/ui/mirror.ts';
import { api, ApiError } from '../lib/api.ts';
import { decisionChip, idChip, relTime, stateChip, verdictChip } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { diffModel, keyText, type DiffRow } from '../lib/diff.ts';
import { h, icon, maybe, type Child } from '../lib/dom.ts';
import { decisionOf } from '../lib/filters.ts';
import { date, dateTime, duration, number, plural, shortId, time } from '../lib/format.ts';
import { actionSentence, countersignedCount, minutesUntil } from '../lib/sentences.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { ActionDetail, ActionEvent, ApprovalDetail, UndoPreview, WitnessReason } from '../lib/types.ts';
import { cardSection, kv, mono, visibleControls } from './components.ts';
import { deedPanel, type DeedPanel } from './deedpanel.ts';
import { diffRowsTable, diffTable } from './difftable.ts';
import { jsonView } from './jsonview.ts';
import { openSheet } from './overlay.ts';
import { errorState, notFound, permissionState } from './screens.ts';
import { apiErrorToast, toast } from './toast.ts';

const tzOf = () => get()?.org.timezone ?? 'UTC';

/** clock: "14:02:40.118" in the org's time zone. */
export function clock(iso: string, tz: string): string {
  const d = new Date(iso);
  const hms = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZone: tz }).format(d);
  return `${hms}.${String(d.getUTCMilliseconds()).padStart(3, '0')}`;
}

function mirrorStateOf(a: ActionDetail): ConsoleMirrorState {
  const v = a.witness?.verdict;
  if (a.state !== 'sealed') return a.status === 'pending_approval' ? 'pending' : 'open';
  return v === 'VERIFIED' ? 'sealed' : v === 'MISMATCH' ? 'mismatch' : v === 'UNVERIFIABLE' ? 'unverifiable' : 'open';
}

// ---------- sections ----------

let agentNames: Record<string, string> = {};
function actorName(id: string): Child {
  const n = agentNames[id];
  return n ? h('span', null, 'by ', h('a', { href: `/app/agents/${id}` }, visibleControls(n))) : mono(id);
}

export function timeline(evs: ActionEvent[], tz: string): HTMLElement | null {
  if (evs.length === 0) return null;
  const first = new Date(evs[0]!.at).getTime();
  const last = new Date(evs[evs.length - 1]!.at).getTime();
  let prev = first;
  let lastActor = 'system';
  const items = evs.map((e, i) => {
    const actor = e.actor && e.actor !== 'system' && e.actor !== lastActor ? e.actor : '';
    if (e.actor) lastActor = e.actor;
    const t = new Date(e.at).getTime();
    const d = i === 0 ? '' : `+${duration(t - prev)}`;
    prev = t;
    const tone = ['verified'].includes(e.to) ? 'ok' : ['mismatch', 'denied', 'failed'].includes(e.to) ? 'bad' : ['pending_approval', 'witnessing', 'unverifiable'].includes(e.to) ? 'wait' : '';
    return h('li', null,
      h('span', { class: `node ${tone}`, 'aria-hidden': 'true' }, icon(tone === 'ok' ? 'check' : tone === 'bad' ? 'x' : tone === 'wait' ? 'circle-dashed' : 'circle', 16)),
      h('div', { class: 'what' }, h('span', { class: 'mono' }, e.to), actor ? h('p', null, actorName(actor)) : null),
      h('div', { class: 'when num' }, h('time', { datetime: e.at, title: dateTime(e.at, tz) }, clock(e.at, tz)), d ? h('span', { class: 't-2 delta' }, d) : null));
  });
  const total = fill(copy.detail.timelineTotal, { from: evs[0]!.to, to: evs[evs.length - 1]!.to, ms: duration(last - first) });
  return cardSection('d-timeline', copy.detail.timeline, h('span', { class: 't-footnote t-2 num' }, total), h('ol', { class: 'timeline tl-detail' }, ...items));
}

function decisionSection(a: ActionDetail): HTMLElement {
  const d = a.decision;
  const pol = d?.policy ?? a.policy?.id ?? null;
  const rule = d?.rule_id ?? a.policy?.rule_id ?? null;
  const dc = decisionOf(a);
  const pairs: [string, Child][] = [
    [copy.detail.decision, dc ? decisionChip(dc) : stateChip(a.state)],
    [copy.detail.policy, pol ? h('a', { href: `/app/policies/${pol}${a.policy?.version_id ? `?v=${a.policy.version_id}` : ''}`, class: 'mono' }, shortId(pol)) : h('span', { class: 't-2' }, copy.detail.noPolicy)],
    [copy.detail.rule, rule ? mono(rule) : null],
    [copy.detail.reason, d?.reason ? visibleControls(d.reason) : d?.message ? visibleControls(d.message) : null],
    [copy.detail.mode, a.policy?.mode ?? null],
  ];
  const sh = d?.shadow;
  const shadow = sh && sh.outcome !== d?.outcome
    ? h('p', { class: 'shadow-line' }, icon('contrast', 16), fill(copy.detail.shadow, { policy: sh.policy ? shortId(sh.policy) : 'the shadow version', outcome: copy.detail.shadowOutcome[sh.outcome] ?? sh.outcome, rule: sh.rule_id ?? '·' }))
    : null;
  return cardSection('d-decision', copy.detail.decision, null, h('div', { class: 'card-body' }, kv(pairs), shadow));
}

const REASON_ICON: Record<WitnessReason['class'], [string, string]> = { pass: ['check', 'pass'], violation: ['x', 'violation'], gap: ['circle-dashed', 'gap'], note: ['info', 'note'] };

function reasonsList(rs: WitnessReason[]): HTMLElement {
  return h('ul', { class: 'reasons' }, ...rs.map((r) => {
    const [ic, word] = REASON_ICON[r.class] ?? ['info', r.class];
    return h('li', { class: `reason is-${r.class}` },
      h('span', { class: 'reason-icon', 'aria-hidden': 'true' }, icon(ic, 16)),
      h('span', { class: 'reason-level mono t-2' }, r.level),
      h('span', { class: 'reason-text' }, h('span', { class: 'sr-only' }, `${word}: `), visibleControls(r.message)));
  }));
}

function witnessSection(a: ActionDetail): HTMLElement {
  const w = a.witness;
  if (!w) return cardSection('d-witness', copy.detail.witness, null, h('p', { class: 'card-empty t-2' }, copy.detail.witnessNone));
  const det = w.detail;
  const reasons = det?.reasons ?? w.reasons ?? [];
  const model = diffModel(det);
  const l1 = det?.levels?.L1?.readback;
  const right = h('span', { class: 'witness-meta' }, verdictChip(w.verdict, w.level));
  const body: Child[] = [];
  if (w.required_level) body.push(h('p', { class: 't-footnote t-2' }, fill(copy.detail.required, { level: `${w.required_level} ${copy.chips.level[w.required_level] ?? ''}`.trim() })));
  if (l1) body.push(h('p', { class: 't-footnote t-2' }, `L1 read-back · ${l1.tool} · ${l1.target}`));
  body.push(h('h3', { class: 'sub-title' }, copy.detail.reasons), reasonsList(reasons));
  if (model) body.push(diffTable(model, { rawJSON: () => JSON.stringify(det?.levels.L2 ?? {}, null, 2), fileName: `${a.id}-diff.json` }));
  return cardSection('d-witness', h('span', null, copy.detail.witness, h('span', { class: 't-2 title-meta' }, ` · ${w.level} · ${w.method ?? ''}`)), right, h('div', { class: 'card-body' }, ...body));
}

function payloadSection(a: ActionDetail): HTMLElement {
  const tz = tzOf();
  if (a.payload_pruned_at) {
    return cardSection('d-payloads', copy.detail.payloads, null, h('p', { class: 'card-empty t-2' }, fill(copy.detail.pruned, { date: date(a.payload_pruned_at, tz) })));
  }
  const panel = (key: 'request' | 'result', label: string) => {
    const v = a[key];
    const host = h('div', { class: 'payload-body', id: `pl-${key}`, hidden: true });
    const btn = h('button', { type: 'button', class: 'payload-toggle', 'aria-expanded': 'false', 'aria-controls': `pl-${key}` }, icon('chevron-right', 16), label, h('span', { class: 't-2 mono' }, key === 'request' ? (a.request_digest ? ` · ${a.request_digest.slice(0, 11)}…` : '') : (a.result_digest ? ` · ${a.result_digest.slice(0, 11)}…` : '')));
    btn.addEventListener('click', () => {
      const open = btn.getAttribute('aria-expanded') !== 'true';
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open && host.childElementCount === 0) host.append(v === undefined || v === null ? h('p', { class: 't-2' }, copy.json.empty) : jsonView(v));
      host.hidden = !open;
    });
    return h('div', { class: 'payload' }, btn, host);
  };
  return cardSection('d-payloads', copy.detail.payloads, null, h('div', { class: 'card-body' }, h('p', { class: 't-footnote t-2' }, copy.detail.payloadsLede), panel('request', copy.detail.request), panel('result', copy.detail.result)));
}

function amendments(a: ActionDetail): HTMLElement | null {
  const hist = a.witness?.history ?? [];
  const am = hist.filter((x) => x.kind === 'amendment');
  if (am.length === 0) return null;
  const tz = tzOf();
  let prev = hist[0]?.verdict ?? '';
  return cardSection('d-amend', copy.detail.amendments, null, h('ul', { class: 'amends' }, ...hist.slice(1).map((x) => {
    const li = h('li', null, fill(copy.detail.amended, { time: clock(x.at, tz).slice(0, 8), from: prev, to: x.verdict }), ' · ', idChip(x.deed, `/app/deeds/${x.deed}`));
    prev = x.verdict;
    return li;
  })));
}

function approvalBlock(a: ActionDetail, ap: ApprovalDetail | null): HTMLElement | null {
  if (!a.approval && !ap) return null;
  const tz = tzOf();
  const status = ap?.status ?? a.approval?.status ?? 'pending';
  const items: Child[] = [];
  const signs = ap && Array.isArray(ap.countersigned) ? ap.countersigned : [];
  if (ap) items.push(h('p', { class: 'num' }, fill(copy.approvalBlock.countersigned, { done: countersignedCount(ap), required: ap.required })));
  for (const c of signs) {
    items.push(h('p', { class: 'sign' }, icon('stamp', 16), h('span', null, fill(copy.approvalBlock.by, { name: c.user.name, time: time(c.at, tz) }), c.reason ? h('span', { class: 't-2' }, ` · “${c.reason}”`) : null, ' · ', idChip(c.deed, `/app/deeds/${c.deed}`))));
  }
  if (ap?.denied_by) items.push(h('p', { class: 'sign is-denied' }, icon('ban', 16), fill(copy.approvalBlock.deniedBy, { name: ap.denied_by.name }), ap.decision_reason ? h('span', { class: 't-2' }, ` · “${ap.decision_reason}”`) : null));
  if (status === 'pending') {
    const exp = ap?.expires_at ?? a.approval?.expires_at;
    items.push(h('p', { class: 'pending-line' }, icon('contrast', 16), fill(copy.approvalBlock.waiting, { mins: exp ? minutesUntil(exp, new Date()) : '' })));
    items.push(h('a', { class: 'btn btn-secondary btn-sm', href: `/app/approvals/${ap?.id ?? a.approval?.id}` }, copy.approvalBlock.open));
  } else if (status === 'expired') items.push(h('p', { class: 't-2' }, copy.approvalBlock.expired));
  else if (status === 'cancelled') items.push(h('p', { class: 't-2' }, copy.approvalBlock.cancelled));
  const chip = status === 'pending' ? h('span', { class: 'chip chip-pending' }, icon('contrast', 16), copy.chips.pending) : stateChip(status === 'approved' ? 'executing' : status);
  if (status === 'approved') chip.replaceChildren(icon('stamp', 16), 'Approved');
  return cardSection('d-approval', copy.approvalBlock.title, chip, h('div', { class: 'card-body block-body' }, ...items));
}

function undoBlock(a: ActionDetail, onPreview: () => void): HTMLElement | null {
  const u = a.undo;
  if (u.undoes) {
    return cardSection('d-undo', copy.undo.panelTitle, null, h('div', { class: 'card-body' }, h('p', null, fill(copy.undo.undoes, { id: '' }).replace(/ \.$/, ''), ' ', idChip(u.undoes, `/app/actions/${u.undoes}`))));
  }
  if (u.undone_by) {
    return cardSection('d-undo', copy.undo.panelTitle, null, h('div', { class: 'card-body' }, h('a', { class: 'chip chip-neutral undone-chip', href: `/app/actions/${u.undone_by}` }, icon('refresh-cw', 16), `Undone · ${shortId(u.undone_by)}`)));
  }
  if (u.pending) {
    return cardSection('d-undo', copy.undo.panelTitle, null, h('div', { class: 'card-body' }, h('p', null, icon('contrast', 16), ' ', fill(copy.undo.pending, { id: '' }).replace(/ \.$/, ''), ' ', idChip(u.pending, `/app/actions/${u.pending}`))));
  }
  if (!u.eligible) {
    if (a.state !== 'sealed' || !a.witness) return null;
    return cardSection('d-undo', copy.undo.panelTitle, null, h('p', { class: 'card-empty t-2' }, copy.undo.notEligible));
  }
  const admin = atLeast(get()?.me.membership.role, 'admin');
  const btn = h('button', { type: 'button', class: 'btn btn-secondary', ...(admin ? {} : { disabled: true, 'aria-describedby': 'undo-why' }) }, icon('refresh-cw', 16), copy.undo.preview);
  btn.addEventListener('click', onPreview);
  return cardSection('d-undo', copy.undo.panelTitle, null, h('div', { class: 'card-body block-body' }, h('p', null, copy.undo.eligible), btn, admin ? null : h('p', { class: 't-footnote t-2', id: 'undo-why' }, copy.undo.adminOnly)));
}

/** undoRows: the preview's steps as DiffTable rows (Now · After undo). */
export function undoRows(p: UndoPreview): DiffRow[] {
  const rows: DiffRow[] = [];
  for (const st of p.steps.data.slice(0, 20)) {
    const cols = Object.keys(st.set ?? st.expect ?? {});
    const list = cols.length ? cols : [''];
    list.forEach((c, i) => {
      const now = st.expect?.[c] ?? null;
      const after = st.set?.[c] ?? null;
      const masked = now === '•••' || after === '•••';
      rows.push({ table: st.table, key: keyText(st.key), op: st.op, column: c, expected: masked ? '•••' : now, observed: masked ? '•••' : after, before: null, unexpected: true, masked, first: i === 0, cells: list.length });
    });
  }
  return rows;
}

function openUndo(a: ActionDetail): void {
  const busy = h('p', { class: 't-2', role: 'status' }, copy.states.loading);
  const sheet = openSheet({ title: fill(copy.undo.sheetTitle, { id: shortId(a.id) }), body: [busy], className: 'sheet-wide' });
  void (async () => {
    let p: UndoPreview;
    try {
      p = await api<UndoPreview>('POST', `/v1/actions/${a.id}/undo/preview`);
    } catch (e) {
      const err = e as ApiError & { checks?: unknown };
      busy.replaceWith(h('p', { class: 'error' }, icon('circle-alert', 16), err.message));
      return;
    }
    const checks = h('ul', { class: 'reasons' }, ...p.checks.map((c) => {
      const cls = c.status === 'pass' ? 'pass' : c.status === 'fail' ? 'violation' : 'gap';
      const [ic] = REASON_ICON[cls as WitnessReason['class']];
      return h('li', { class: `reason is-${cls}` }, h('span', { class: 'reason-icon', 'aria-hidden': 'true' }, icon(ic, 16)),
        h('span', { class: 'reason-text' }, h('b', null, c.title), ' · ', c.detail ?? c.message ?? '',
          c.fix ? h('span', { class: 'fix' }, ` ${c.fix}`) : null,
          c.fix_sql ? h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'data-copy': c.fix_sql }, icon('copy', 16), copy.undo.copySql) : null));
    }));
    const body: Child[] = [h('p', { class: 'sheet-sub' }, p.headline)];
    if (!p.available) {
      body.push(h('p', { class: 'banner banner-warn' }, icon('triangle-alert', 16), copy.undo.unavailable), checks);
      sheet.el.querySelector('.sheet-body')?.replaceChildren(...body.filter(Boolean) as Node[]);
      return;
    }
    body.push(diffRowsTable(undoRows(p), { heads: [copy.undo.colNow, copy.undo.colAfter], tone: 'pending', pairs: true }));
    if (p.steps.total > 20) body.push(h('p', { class: 't-footnote t-2', title: p.plan_digest ?? '' }, fill(copy.undo.more, { n: number(p.steps.total - 20) })));
    body.push(h('h3', { class: 'sub-title' }, copy.undo.checks), checks);
    if (p.notes.length) body.push(h('h3', { class: 'sub-title' }, copy.undo.notes), h('ul', { class: 'notes' }, ...p.notes.map((n) => h('li', null, n))));
    if (p.policy) body.push(h('p', { class: 'policy-line' }, icon('stamp', 16), mono(p.policy.rule_id ?? ''), p.policy.decision === 'require_approval' ? ' · a person countersigns first' : ''));
    const reason = h('textarea', { class: 'textarea', id: 'undo-reason', name: 'reason', maxlength: 280, rows: 3, required: true, 'aria-required': 'true' });
    body.push(h('div', { class: 'field' }, h('label', { class: 'label', for: 'undo-reason' }, copy.undo.reason), reason));
    const go = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, copy.undo.request);
    reason.addEventListener('input', () => (go.disabled = reason.value.trim().length < 3));
    go.addEventListener('click', async () => {
      go.disabled = true;
      go.setAttribute('aria-busy', 'true');
      try {
        const r = await api<{ id: string; status: string }>('POST', `/v1/actions/${a.id}/undo`, { body: { plan: p.plan_digest, reason: reason.value.trim() } });
        sheet.close();
        toast(r.status === 'pending_approval' ? copy.undo.requested : copy.undo.requestedRan, { kind: 'success' });
        location.assign(`/app/actions/${r.id}`);
      } catch (e) {
        go.disabled = false;
        apiErrorToast(e);
      } finally {
        go.removeAttribute('aria-busy');
      }
    });
    const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
    cancel.addEventListener('click', () => sheet.close());
    sheet.el.querySelector('.sheet-body')?.replaceChildren(...body.filter(Boolean) as Node[]);
    const foot = h('div', { class: 'sheet-foot' }, cancel, go);
    sheet.el.append(foot);
  })();
}

// ---------- the page ----------

export function mountActionDetail(): void {
  const id = decodeURIComponent(location.pathname.split('/').pop() ?? '');
  const host = maybe('detail-body');
  if (!host) return;
  let panel: DeedPanel | null = null;
  let last: ActionDetail | null = null;
  let reloadTimer = 0;
  const render = (a: ActionDetail, ap: ApprovalDetail | null) => {
    const tz = tzOf();
    agentNames = { [a.agent.id]: a.agent.name };
    const s = actionSentence(a, new Date(), ap);
    const dc = decisionOf(a);
    const head = h('header', { class: 'detail-head' },
      h('nav', { class: 'crumbs', 'aria-label': 'Breadcrumb' }, h('a', { href: '/app/actions' }, copy.detail.back), h('span', { 'aria-hidden': 'true' }, '/'), idChip(a.id)),
      h('h1', { class: 't-title-2 mono detail-title', id: 'page-title', translate: 'no' }, visibleControls(a.tool)),
      h('p', { class: 'detail-meta' },
        h('a', { href: `/app/agents/${a.agent.id}` }, visibleControls(a.agent.name)),
        a.on_behalf_of ? h('span', null, ' · ', fill(copy.detail.onBehalfOf, { who: '' }).trim(), ' ', mono(a.on_behalf_of)) : null,
        a.session ? h('span', null, ' · session ', mono(a.session)) : null,
        ' · ', relTime(a.created_at, tz)),
      h('div', { class: 'detail-chips' }, stateChip(a.state), dc ? decisionChip(dc) : null, a.witness && a.state !== 'witnessing' ? verdictChip(a.witness.verdict, a.witness.level) : null),
      h('div', { class: `verdict-sentence vs-${s.tone}`, role: s.tone === 'mismatch' ? 'alert' : 'status' },
        h('p', { 'data-sentence': '' }, s.text),
        s.gaps.length ? h('ul', { class: 'gaps' }, ...s.gaps.map((g) => h('li', null, icon('circle-dashed', 16), g))) : null));
    const firstDeed = a.deeds?.find((d) => d.kind === 'action') ?? a.deeds?.[0];
    panel = deedPanel({ deed: a.deed, seq: firstDeed?.seq ?? null, mirror: mirrorStateOf(a), pending: !a.deed });
    const aside = h('aside', { class: 'detail-aside', 'aria-label': copy.deedPanel.title }, panel.el, approvalBlock(a, ap), undoBlock(a, () => openUndo(a)));
    const main = h('div', { class: 'detail-main' }, witnessSection(a), timeline(a.events?.data ?? [], tz), decisionSection(a), payloadSection(a), amendments(a));
    host.replaceChildren(head, h('div', { class: 'detail-grid' }, aside, main));
    document.title = `${a.tool} · ${shortId(a.id)} · Deedglass`;
  };
  const load = async (quiet = false) => {
    try {
      const a = await api<ActionDetail>('GET', `/v1/actions/${encodeURIComponent(id)}`, { query: { 'expand[]': 'witness.detail' } });
      let ap: ApprovalDetail | null = null;
      if (a.approval?.id) {
        try {
          ap = await api<ApprovalDetail>('GET', `/v1/approvals/${a.approval.id}`);
        } catch {
          ap = null;
        }
      }
      const sealedNow = quiet && last && last.state !== 'sealed' && a.state === 'sealed';
      last = a;
      render(a, ap);
      if (sealedNow && panel) panel.sealedLive(mirrorStateOf(a));
    } catch (e) {
      if (quiet) return;
      if (e instanceof ApiError && e.status === 404) host.replaceChildren(notFound(e.message));
      else if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => {
    void load();
    const s = stream();
    s.on('action.*', (ev) => {
      const o = ev.data.object as { id?: string; action?: string };
      if (o.id !== id && o.action !== id) return;
      clearTimeout(reloadTimer);
      reloadTimer = window.setTimeout(() => void load(true), 250);
    });
    s.on('approval.*', (ev) => {
      const o = ev.data.object as { action?: string };
      if (o.action === id) void load(true);
    });
  }, { once: true });
}

export { plural };
