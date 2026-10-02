// The detail stubs (/app/<screen>/<id>): the object's id and its plain fields, read through the
// same REST object S25 renders in full. Strings render as text only.
import { api, ApiError } from '../lib/api.ts';
import { idChip, relTime } from '../lib/chips.ts';
import { h, maybe, type Child } from '../lib/dom.ts';
import { get } from '../lib/state.ts';
import { errorState, permissionState } from './screens.ts';

const PATHS: Record<string, string> = {
  actions: '/v1/actions/', approvals: '/v1/approvals/', agents: '/v1/agents/', policies: '/v1/policies/', connectors: '/v1/connectors/', deeds: '/v1/deeds/',
};
const ID_KEYS = /(^id$|_id$|^deed$|^action$|^agent$)/;
const TIME_KEYS = /_at$/;

function label(k: string): string {
  const t = k.replace(/_/g, ' ');
  return t.charAt(0).toUpperCase() + t.slice(1);
}

export function mountDetail(screen: string): void {
  const id = decodeURIComponent(location.pathname.split('/').pop() ?? '');
  const title = maybe('page-title');
  if (title) title.textContent = id;
  const body = maybe('detail-body');
  if (!body) return;
  const load = async () => {
    try {
      const obj = await api<Record<string, unknown>>('GET', `${PATHS[screen]}${encodeURIComponent(id)}` as `/v1/${string}`);
      const tz = get()?.org.timezone ?? 'UTC';
      const src = (obj.body && typeof obj.body === 'object' ? { ...obj, ...(obj.body as Record<string, unknown>) } : obj) as Record<string, unknown>;
      const rows: Child[] = [];
      for (const [k, v] of Object.entries(src)) {
        if (k === 'object' || k === 'body' || v === null || typeof v === 'object') continue;
        let cell: Child;
        if (typeof v === 'string' && ID_KEYS.test(k) && /^[a-z]{2,4}_/.test(v)) cell = idChip(v);
        else if (typeof v === 'string' && TIME_KEYS.test(k)) cell = relTime(v, tz);
        else cell = String(v);
        rows.push(h('dt', null, label(k)), h('dd', { class: typeof v === 'string' && /^(sha256:|[a-z]{2,4}_)/.test(v) ? 'mono' : '' }, cell));
        if (rows.length >= 24) break;
      }
      body.replaceChildren(h('dl', { class: 'kv' }, ...rows));
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) body.replaceChildren(permissionState('Admin'));
      else if (e instanceof ApiError && e.status === 404) body.replaceChildren(h('p', { class: 't-2 m-0' }, e.message));
      else if (!(e instanceof ApiError && e.status === 401)) body.replaceChildren(errorState(e, () => void load()));
    } finally {
      body.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => void load(), { once: true });
}
