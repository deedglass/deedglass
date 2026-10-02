// live.ts: the Overview stub's Live events panel (S24 step 12; S25 replaces it): the last 20 events
// as Postgres committed them. A VERIFIED witness plays Sealed once (the play rules in seal.ts);
// a MISMATCH stops short. Rows loaded before the page opened never animate.

import { mountMirror, type ConsoleMirrorState } from '@verify/ui/mirror.ts';
import { idChip, verdictChip } from '../lib/chips.ts';
import { api } from '../lib/api.ts';
import { copy } from '../lib/copy.ts';
import { h, icon, maybe } from '../lib/dom.ts';
import { time } from '../lib/format.ts';
import { SealGovernor } from '../lib/seal.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, DeedglassEvent, Level, ListResponse, Verdict } from '../lib/types.ts';
import { toast } from './toast.ts';

const MAX = 20;
const ICONS: Record<string, string> = { action: 'activity', deed: 'deed', approval: 'stamp', agent: 'bot', org: 'building-2', policy: 'scroll-text', decoder: 'witness' };
const gov = new SealGovernor();

function summary(ev: DeedglassEvent): (Node | string)[] {
  const o = ev.data.object as Record<string, unknown>;
  const id = typeof o.id === 'string' ? o.id : typeof o.connector === 'string' ? o.connector : '';
  const out: (Node | string)[] = [];
  const agent = (o.agent as { name?: string } | undefined)?.name;
  const tool = typeof o.tool === 'string' ? o.tool : '';
  if (ev.hydrated && (agent || tool)) {
    out.push(h('span', { class: 'mono', translate: 'no' }, tool), agent ? h('span', { class: 't-2' }, ` · ${agent}`) : '');
  } else if (id) {
    out.push(idChip(id));
  }
  return out;
}

function stateFor(ev: DeedglassEvent): ConsoleMirrorState {
  const o = ev.data.object as Record<string, unknown>;
  const w = (o.witness as { verdict?: string } | undefined)?.verdict ?? (o.verdict as string | undefined);
  if (ev.type === 'action.witnessed' || ev.type === 'deed.sealed') {
    if (w === 'VERIFIED') return 'sealed';
    if (w === 'MISMATCH') return 'mismatch';
    if (w === 'UNVERIFIABLE') return 'unverifiable';
  }
  if (ev.type.startsWith('approval.requested')) return 'pending';
  return 'open';
}

export function mountLive(): void {
  const list = maybe('live-list');
  const empty = maybe('live-empty');
  const count = maybe('live-count');
  if (!list) return;
  let n = 0;
  const render = (ev: DeedglassEvent, live: boolean): HTMLElement => {
    const tz = get()?.org.timezone ?? 'UTC';
    const target = stateFor(ev);
    // The Mirror appears only where a verdict was witnessed: VERIFIED seals, MISMATCH stops short.
    // Every other row gets a quiet icon, so aurora stays rare (brand §6.2).
    const markHost = h('span', { class: 'mark', 'aria-hidden': 'true' });
    const witnessed = live && (target === 'sealed' || target === 'mismatch'); // history rows keep aurora out of the list
    const mirror = witnessed ? mountMirror(markHost, { size: 16, state: 'open' }) : null;
    if (!witnessed) markHost.append(icon(ICONS[ev.type.split('.')[0] ?? ''] ?? 'activity', 16));
    const o = ev.data.object as Record<string, unknown>;
    const witness = o.witness as { verdict?: Verdict; level?: Level } | undefined;
    const verdict = witness?.verdict ?? (o.verdict as Verdict | undefined);
    const level = witness?.level ?? (o.level as Level | undefined);
    const li = h('li', { class: live ? 'new' : 'past' },
      markHost,
      h('time', { class: 'when', datetime: ev.created_at }, time(ev.created_at, tz)),
      h('span', { class: 'type', translate: 'no' }, ev.type, ' ', ...summary(ev)),
      ev.type === 'action.witnessed' && verdict ? verdictChip(verdict, level ?? null) : h('span'));
    const id = typeof o.deed === 'string' ? o.deed : typeof o.id === 'string' ? o.id : ev.id;
    // Rows from history never animate; a live seal plays once (seal.ts).
    if (mirror && live) void mirror.set(target, { animate: target !== 'sealed' || gov.mayPlay(id, true) });
    return li;
  };
  const shown = () => {
    if (empty) empty.hidden = list.children.length > 0;
  };
  document.addEventListener('dg:ready', () => {
    // History first: the last actions, as their latest event, without motion.
    void api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: String(MAX) } }).then((page) => {
      if (list.children.length > 0) return;
      for (const a of page.data) {
        const type = a.witness ? 'action.witnessed' : a.state === 'sealed' ? 'deed.sealed' : `action.${a.status === 'executed' ? 'executed' : 'decided'}`;
        list.append(render({ object: 'event', id: a.id, type, api_version: '', created_at: a.created_at, livemode: a.livemode, org: '', hydrated: true, data: { object: a as unknown as Record<string, unknown> } }, false));
      }
      shown();
    }).catch(() => undefined);
    const s = stream();
    s.on('*', (ev) => {
      list.prepend(render(ev, true));
      while (list.children.length > MAX) list.lastElementChild?.remove();
      n++;
      shown();
      if (count) count.textContent = `${n} new ${n === 1 ? 'event' : 'events'}`;
    });
    s.onResync(() => toast(copy.states.resync));
  }, { once: true });
}
