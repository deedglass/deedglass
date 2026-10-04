// deedview.ts: one deed (S25 §3.6): contract §7's sections, the exact raw JCS (a formatted view
// only on request, and labelled), the DeedPanel with the in-browser seal check, and ← → by seq.

import { parseEnvelope } from '@verify/deed.ts';
import type { ConsoleMirrorState } from '@verify/ui/mirror.ts';
import { api } from '../lib/api.ts';
import { hashChip, idChip, relTime } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { deedSummary, sections } from '../lib/deedview.ts';
import { h, maybe, type Child } from '../lib/dom.ts';
import { dateTime, number } from '../lib/format.ts';
import { get } from '../lib/state.ts';
import type { DeedEnvelope, ListResponse } from '../lib/types.ts';
import { codeBlock, mono, segmented, visibleControls } from './components.ts';
import { deedPanel, rawDeed } from './deedpanel.ts';
import { kindChip } from './deeds.ts';
import { errorState, notFound } from './screens.ts';

const tzOf = () => get()?.org.timezone ?? 'UTC';

// ---------- the deed view ----------

export function mountDeedView(): void {
  const id = decodeURIComponent(location.pathname.split('/').pop() ?? '');
  const host = maybe('detail-body');
  if (!host) return;
  const neighbour = async (seq: number): Promise<string | null> => {
    if (seq < 1) return null;
    try {
      const p = await api<ListResponse<DeedEnvelope>>('GET', '/v1/deeds', { query: { from_seq: String(seq), to_seq: String(seq), limit: '1' } });
      return p.data[0]?.id ?? null;
    } catch {
      return null;
    }
  };
  const load = async () => {
    let raw: string;
    try {
      raw = await rawDeed(id);
    } catch (e) {
      const msg = (e as Error).message;
      host.replaceChildren(/^No deed/.test(msg) ? notFound(msg) : errorState(e, () => void load()));
      host.setAttribute('aria-busy', 'false');
      return;
    }
    let env;
    try {
      env = parseEnvelope(raw).env;
    } catch (e) {
      host.replaceChildren(errorState(e, () => void load()));
      return;
    }
    const b = env.body as unknown as DeedEnvelope['body'];
    const tz = tzOf();
    const w = b.witness;
    const mirror: ConsoleMirrorState = w ? (w.verdict === 'VERIFIED' ? 'sealed' : w.verdict === 'MISMATCH' ? 'mismatch' : 'unverifiable') : 'sealed';
    const panel = deedPanel({ deed: env.id, seq: b.seq, mirror });
    const secs = sections(b).map((s) => h('section', { class: 'card deed-section', 'aria-labelledby': `ds-${s.key}` },
      h('div', { class: 'card-head' }, h('h2', { class: 't-headline', id: `ds-${s.key}` }, copy.deeds.sections[s.key] ?? s.key)),
      h('dl', { class: 'kv card-body kv-deed' }, ...s.rows.flatMap(([label, value, kindOf]) => {
        let v: Child = value ?? '';
        if (label === 'Previous hash' && value) v = h('span', { class: 'prev-link' }, hashChip(value), b.seq > 1 ? h('a', { href: '#', 'data-prev': '', class: 't-footnote' }, fill(copy.deeds.prev, { seq: number(b.seq - 1) })) : null);
        else if (kindOf === 'hash' && value) v = hashChip(value);
        else if (kindOf === 'id' && value) v = idChip(value, value.startsWith('act_') ? `/app/actions/${value}` : value.startsWith('deed_') ? `/app/deeds/${value}` : value.startsWith('agt_') ? `/app/agents/${value}` : undefined);
        else if (kindOf === 'time' && value) v = h('time', { datetime: value, title: dateTime(value, tz), class: 'mono' }, value);
        else if (kindOf === 'mono' && value) v = mono(value);
        else if (value) v = visibleControls(value);
        return [h('dt', null, label), h('dd', null, v)];
      }))));
    const pre = codeBlock('deed.json · JCS', raw, 'the raw deed');
    pre.classList.add('raw-jcs');
    const rawWrap = h('div', { class: 'raw-wrap' }, pre);
    const toggle = segmented<'raw' | 'pretty'>(copy.deeds.raw, [{ value: 'raw', label: copy.deeds.raw }, { value: 'pretty', label: copy.deeds.pretty }], 'raw', (v) => {
      if (v === 'raw') rawWrap.replaceChildren(pre);
      else {
        const p = codeBlock(`deed.json · ${copy.deeds.prettyNote}`, JSON.stringify(env, null, 2), 'the formatted deed');
        p.classList.add('raw-jcs');
        rawWrap.replaceChildren(p);
      }
    }, 'raw');
    const prev = h('a', { class: 'btn btn-quiet btn-sm', href: '/app/deeds', 'aria-disabled': 'true', 'data-nav': 'prev' }, fill(copy.deeds.prev, { seq: number(Math.max(0, b.seq - 1)) }));
    const next = h('a', { class: 'btn btn-quiet btn-sm', href: '/app/deeds', 'aria-disabled': 'true', 'data-nav': 'next' }, fill(copy.deeds.next, { seq: number(b.seq + 1) }));
    host.replaceChildren(
      h('header', { class: 'detail-head' },
        h('nav', { class: 'crumbs', 'aria-label': 'Breadcrumb' }, h('a', { href: '/app/deeds' }, copy.nav.deeds), h('span', { 'aria-hidden': 'true' }, '/'), idChip(env.id)),
        h('div', { class: 'agent-head' }, h('h1', { class: 't-title-2', id: 'page-title' }, fill(copy.deeds.title, { seq: number(b.seq) })), kindChip(b.kind)),
        h('p', { class: 'detail-meta' }, visibleControls(deedSummary(b)), ' · ', relTime(b.issued_at, tz)),
        h('nav', { class: 'deed-nav', 'aria-label': 'Deeds by seq' }, prev, next)),
      h('div', { class: 'detail-grid' },
        h('aside', { class: 'detail-aside', 'aria-label': copy.deedPanel.title }, panel.el),
        h('div', { class: 'detail-main' },
          h('div', { class: 'deed-sections' }, ...secs),
          h('section', { class: 'card', 'aria-labelledby': 'raw-t' },
            h('div', { class: 'card-head' }, h('h2', { class: 't-headline', id: 'raw-t' }, copy.deeds.raw), toggle),
            h('div', { class: 'card-body' }, h('p', { class: 't-footnote t-2' }, copy.deeds.rawLede), rawWrap)))));
    host.setAttribute('aria-busy', 'false');
    document.title = `Deed ${b.seq} · Deedglass`;
    const [p, n] = await Promise.all([neighbour(b.seq - 1), neighbour(b.seq + 1)]);
    const wire = (a: HTMLAnchorElement, target: string | null) => {
      if (!target) return;
      a.href = `/app/deeds/${target}`;
      a.removeAttribute('aria-disabled');
    };
    wire(prev, p);
    wire(next, n);
    if (!p) prev.hidden = b.seq <= 1;
    const pl = host.querySelector<HTMLAnchorElement>('[data-prev]');
    if (pl && p) pl.href = `/app/deeds/${p}`;
    else pl?.remove();
  };
  document.addEventListener('dg:ready', () => void load(), { once: true });
}

