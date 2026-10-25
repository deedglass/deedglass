// weekly.ts: S52's Console pieces. The This week card on Overview (GET /v1/weekly: the same summary
// the Monday email sends, with each caught item's free way back), the person's Monday email toggle
// (POST /v1/weekly/email), and the Clients page (GET /v1/clients, the Agency plan): one dense row per
// client, chips at the end, each row read in that client's own org by the server.

import { api, ApiError } from '../lib/api.ts';
import { relTime } from '../lib/chips.ts';
import { fill } from '../lib/copy.ts';
import { clientsCopy as c, weekCopy as w } from '../lib/copy-clients.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import { get } from '../lib/state.ts';
import { cardSection } from './components.ts';
import { openSheet } from './overlay.ts';
import { emptyState, errorState, skeleton } from './screens.ts';
import { toast } from './toast.ts';

// ---------- the API's shapes (internal/weekly, internal/weeklyapi, ee/agency) ----------

export interface CaughtItem { kind: string; session?: string; actions?: string[]; line: string; undo_possible: boolean; undo_free_path: string }
export interface Summary {
  object: 'weekly_summary'; org: { id: string; name: string; slug: string };
  period: { kind: string; key: string; label: string; timezone: string };
  actions: { total: number; allowed: number; waited_for_a_person: number; blocked_by_rules: number };
  check: { matches: number; doesnt_match: number; cant_check: number; not_checked: number };
  caught: CaughtItem[]; caught_total: number; chain: { status: string; first_broken?: string };
}
export interface EmailState { on: boolean; receives: boolean; owner: boolean; mailer: boolean }
export interface WeeklyView { object: 'weekly_view'; summary: Summary; email: EmailState | null; pro_undo_line: boolean }
export interface ClientRow {
  object: 'client'; org: string; name: string; slug: string; role: string; actions: number; doesnt_match: number; caught: number;
  waiting: number; stopped: boolean; chain: string; last_activity: string | null;
}
export interface ClientList { object: 'list'; data: ClientRow[]; has_more: boolean; next_cursor: string | null; total: number; period: { key: string; label: string } }

// ---------- the lines (equal to internal/weekly's words; test/weekly.test.ts pins them) ----------

const plural = (n: number, one: string, many: string) => `${number(n)} ${n === 1 ? one : many}`;

export function numbersLine(s: Summary): string {
  const a = s.actions;
  if (a.total === 0) return 'No agent actions this week.';
  return `${plural(a.total, 'action', 'actions')} · ${number(a.allowed)} allowed · ${number(a.waited_for_a_person)} waited for a person · ${number(a.blocked_by_rules)} blocked by rules`;
}

export function checkLine(s: Summary): string {
  const k = s.check;
  let l = `Check: ${number(k.matches)} match · ${number(k.doesnt_match)} don't match · ${number(k.cant_check)} can't check`;
  if (k.not_checked > 0) l += ` · ${number(k.not_checked)} not checked`;
  return l;
}

export function chainLine(s: Summary): string {
  switch (s.chain.status) {
    case 'intact': return 'Records chain intact.';
    case 'broken': return s.chain.first_broken ? `Records chain broken at ${s.chain.first_broken}. Run deedglass ledger check.` : 'Records chain broken. Run deedglass ledger check.';
    case 'no_records': return 'No records yet, so nothing to check.';
  }
  return 'Records chain not checked. Run deedglass ledger check.';
}

export function itemLine(it: CaughtItem): string {
  const l = it.line.replace(/^Deedglass caught this: /, '');
  return l.charAt(0).toUpperCase() + l.slice(1);
}

export function freeLine(it: CaughtItem): string {
  if (!it.undo_free_path) return '';
  return `${it.undo_possible ? 'Undo it yourself, free:' : 'Look at it:'} ${it.undo_free_path}`;
}

// ---------- the This week card ----------

export async function mountThisWeek(host: HTMLElement): Promise<void> {
  const body = h('div', { class: 'wk', 'aria-live': 'polite' }, h('p', { class: 't-2' }, '…'));
  host.replaceChildren(cardSection('ov-week-t', w.lastWeek, null, body));
  try {
    const v = await api<WeeklyView>('GET', '/v1/weekly');
    renderWeek(body, v);
  } catch (e) {
    body.replaceChildren(h('p', { class: 't-2' }, fill(w.error, { message: (e as Error).message })));
  }
}

export function renderWeek(body: HTMLElement, v: WeeklyView): void {
  const s = v.summary;
  const kids: Child[] = [
    h('p', { class: 'wk-period t-footnote' }, s.period.label),
    h('p', { class: 'wk-numbers' }, numbersLine(s)),
    h('p', { class: 'wk-check t-2' }, checkLine(s)),
  ];
  if (s.caught_total > 0) {
    kids.push(h('h3', { class: 'wk-caught-head t-footnote' }, icon('triangle-alert', 16), `Deedglass caught ${plural(s.caught_total, 'thing', 'things')}`));
    kids.push(h('ul', { class: 'wk-caught' }, ...s.caught.slice(0, 3).map((it) => h('li', null,
      h('p', null, itemLine(it)),
      freeLine(it) ? h('p', { class: 'wk-free mono', translate: 'no', 'data-free-path': '' }, freeLine(it)) : null,
      v.pro_undo_line && it.session && it.undo_possible ? h('p', { class: 'wk-pro t-2' }, 'Undo the whole session in one step') : null))));
  } else {
    kids.push(h('p', { class: 't-2' }, w.nothing));
  }
  kids.push(h('p', { class: 'wk-chain t-2' }, chainLine(s)));
  kids.push(h('p', { class: 'wk-report t-2 mono', translate: 'no' }, w.reportLine));
  if (v.email) kids.push(emailLine(v.email));
  replace(body, ...kids);
}

function emailLine(e: EmailState): HTMLElement {
  const line = h('p', { class: 'wk-email t-2' });
  const draw = (st: EmailState) => {
    const text = !st.mailer ? w.emailNoMailer : !st.owner ? w.emailOnNotOwner : st.on ? w.emailOn : w.emailOff;
    const kids: Child[] = [text];
    if (st.mailer && st.owner) {
      const b = h('button', { type: 'button', class: 'btn btn-quiet wk-toggle' }, st.on ? w.turnOff : w.turnOn) as HTMLButtonElement;
      b.addEventListener('click', async () => {
        b.disabled = true;
        try {
          const next = await api<EmailState>('POST', '/v1/weekly/email', { body: { weekly_email: !st.on } });
          draw(next);
        } catch (err) {
          b.disabled = false;
          toast((err as Error).message, { kind: 'error' });
        }
      });
      kids.push(' ', b);
    }
    replace(line, ...kids);
  };
  draw(e);
  return line;
}

// ---------- the Clients page ----------

function chainChip(state: string): HTMLElement {
  const cls = state === 'intact' ? 'chip-verified' : state === 'broken' ? 'chip-mismatch' : 'chip-neutral';
  return h('span', { class: `chip ${cls}` }, c.chain[state] ?? c.chain.not_checked!);
}

async function openClient(org: string, b: HTMLButtonElement): Promise<void> {
  if (org === get()?.org.id) {
    location.assign('/app');
    return;
  }
  b.disabled = true;
  try {
    await api('POST', '/v1/auth/switch-org', { body: { org } });
    location.assign('/app');
  } catch {
    b.disabled = false;
    toast(c.switchFailed, { kind: 'error' });
  }
}

export function clientRow(r: ClientRow, tz = 'UTC'): HTMLElement {
  const open = h('button', { type: 'button', class: 'btn btn-quiet', 'aria-label': `${c.open} ${r.name}` }, c.open, icon('chevron-right', 16)) as HTMLButtonElement;
  open.addEventListener('click', () => void openClient(r.org, open));
  const quiet = r.actions === 0;
  return h('tr', { 'data-row': '', 'data-org': r.org },
    h('td', { 'data-label': c.columns.client }, h('span', { class: 'cl-name' }, r.name), ' ', h('span', { class: 'mono t-2 cl-slug', translate: 'no' }, r.slug)),
    h('td', { class: 'num', 'data-label': c.columns.actions, 'data-card': quiet ? undefined : 'label' }, quiet ? h('span', { class: 't-2' }, c.noActivity) : number(r.actions)),
    h('td', { class: 'num', 'data-label': c.columns.doesntMatch, 'data-card': quiet ? 'hide' : 'label' }, quiet ? '' : number(r.doesnt_match)),
    h('td', { class: 'num', 'data-label': c.columns.waiting, 'data-card': quiet && !r.waiting ? 'hide' : 'label' }, quiet && !r.waiting ? '' : number(r.waiting)),
    h('td', { 'data-label': c.columns.state }, h('span', { class: 'cl-chips' },
      r.caught ? h('span', { class: 'chip chip-caught' }, icon('triangle-alert', 16), fill(c.caughtChip, { n: number(r.caught) })) : null,
      r.stopped ? h('span', { class: 'chip chip-denied' }, c.stopped) : null,
      chainChip(r.chain),
      r.last_activity ? relTime(r.last_activity, tz) : null)),
    h('td', { class: 'cl-open' }, open));
}

export function proState(): HTMLElement {
  return h('section', { class: 'card cl-pro', 'aria-labelledby': 'cl-pro-t' },
    h('h2', { class: 't-headline', id: 'cl-pro-t' }, c.proTitle),
    h('p', null, c.proBody),
    h('p', { class: 'mono t-2', translate: 'no' }, c.proFree),
    h('a', { class: 'btn btn-primary', href: c.proURL }, c.proLink, icon('arrow-up-right', 16)));
}

export function mountClients(): void {
  const host = maybe('screen-body');
  if (!host) return;
  const load = async () => {
    host.setAttribute('aria-busy', 'true');
    host.replaceChildren(skeleton());
    try {
      const l = await api<ClientList>('GET', '/v1/clients', { query: { limit: '200' } });
      const tz = get()?.org.timezone ?? 'UTC';
      if (l.data.length <= 1) {
        host.replaceChildren(emptyState(c.empty, 'building-2'), ...(l.data.length ? [table(l, tz)] : []));
      } else {
        host.replaceChildren(table(l, tz));
      }
      const month = monthKey();
      host.append(h('section', { class: 'cl-foot' },
        h('p', { class: 't-2' }, c.separation),
        h('h2', { class: 't-footnote' }, c.reportsTitle),
        h('p', { class: 'mono t-2', translate: 'no' }, fill(c.reportsLine, { month })),
        l.has_more ? h('p', { class: 't-2' }, fill(c.more, { n: number(l.data.length) })) : null));
    } catch (e) {
      if (e instanceof ApiError && e.code === 'pro_required') host.replaceChildren(proState());
      else host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => void load(), { once: true });
}

function table(l: ClientList, tz: string): HTMLElement {
  const cols = c.columns;
  return h('div', { class: 'table-frame' }, h('table', { class: 'dt cards cl-list' },
    h('caption', { class: 'sr-only' }, c.lede),
    h('thead', null, h('tr', null,
      h('th', { scope: 'col' }, cols.client), h('th', { scope: 'col', class: 'num' }, cols.actions), h('th', { scope: 'col', class: 'num' }, cols.doesntMatch),
      h('th', { scope: 'col', class: 'num' }, cols.waiting), h('th', { scope: 'col' }, cols.state), h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, cols.client)))),
    h('tbody', null, ...l.data.map((r) => clientRow(r, tz)))));
}

/** The last complete month, YYYY-MM (the reports' default). */
export function monthKey(now = new Date()): string {
  const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1, 1));
  return d.toISOString().slice(0, 7);
}

/** proSheet: the same explanation as a sheet (the palette opens it). */
export function clientsProSheet(): void {
  openSheet({ title: c.proTitle, body: [h('p', null, c.proBody), h('p', { class: 'mono t-2' }, c.proFree)],
    foot: [h('a', { class: 'btn btn-primary', href: c.proURL }, c.proLink, icon('arrow-up-right', 16))] });
}
