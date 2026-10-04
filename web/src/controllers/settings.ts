// settings.ts: Settings (S26 §3.4). Eight sections, each its own URL (/app/settings/<section>,
// pushState and popstate): a left sub-nav from 1024 px, a list screen under 768 px with a back link
// on each section. Every change goes through the API, which enforces the role (§3.5); the Console
// only hides or disables what a role can't do, with the reason. Server messages (last_owner,
// destination_blocked, validation) are shown verbatim under their field or in a toast.

import { api, ApiError, list } from '../lib/api.ts';
import { hashChip, idChip, relTime } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { settingsCopy } from '../lib/copy-settings.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { date } from '../lib/format.ts';
import { SETTINGS_SECTIONS } from '../lib/nav.ts';
import {
  approvalSettings, approvalsPatch, DEFAULT_RETENTION, eventFamilies, filterZones, gates, hostOf, inviteDays, isSection,
  memberActions, parseRetention, parseSection, retentionHelp, roleChangeNeedsConfirm, savedToast, sectionNeeds,
  sectionPath, testLine, WEBHOOK_EVENTS, zoneList, type ApprovalSettings, type SectionKey, type WebhookTestResult,
} from '../lib/settings.ts';
import { atLeast, get, onChange, refreshOrg } from '../lib/state.ts';
import type { ApiKey, ListResponse, Org, Role } from '../lib/types.ts';
import { showKeySheet } from './keysheet.ts';
import { confirmTyped, menuButton, openSheet, type MenuItem, type Overlay } from './overlay.ts';
import { emptyState, errorState, permissionState, skeleton } from './screens.ts';
import { startAll, stopAll } from './shell.ts';
import { apiErrorToast, toast } from './toast.ts';

const c = settingsCopy;

// ---------- shapes (S07, S08, S22; GET /v1/org's S26 additions are optional) ----------

interface OrgFull extends Org {
  retention_days?: number | null;
  settings?: { approvals?: Partial<ApprovalSettings> } | null;
  deed?: string | null;
}

interface Member {
  id: string;
  user: { id: string; email: string; name: string };
  role: Role;
  status: 'active' | 'invited';
  invited_at: string | null;
  joined_at: string | null;
  created_at: string;
  expires_at?: string | null;
  deed?: string;
}

export interface Webhook {
  id: string;
  url: string;
  events: string[];
  description: string | null;
  status: 'active' | 'disabled';
  disabled_reason: string | null;
  secret_hint: string | null;
  secret?: string | null;
  failing_since: string | null;
  last_success_at: string | null;
  created_at: string;
  deed?: string;
}

interface Delivery {
  id: string;
  type: string;
  status: 'pending' | 'succeeded' | 'failed' | 'dead';
  attempt: number;
  response_status: number | null;
  response_ms: number | null;
  last_error: string | null;
  created_at: string;
}

interface SealKey {
  id: string;
  status: 'active' | 'retired' | 'revoked' | string;
  thumbprint: string;
  activated_at: string | null;
  retired_at: string | null;
  revoked_at: string | null;
}

// ---------- small pieces ----------

const tzOf = () => get()?.org.timezone ?? 'UTC';
const myRole = (): Role | undefined => get()?.me.membership.role;
const orgName = () => get()?.org.name ?? 'this org';
const roleLabel = (r: Role) => c.members.roles[r] ?? r;
const display = (m: Member) => m.user.name || m.user.email;

/** row: one Linear-style settings row (label and one sentence left, the control right). */
function row(o: { id?: string; label: string; help?: Child; control: Child; wide?: boolean }): HTMLElement {
  const err = o.id ? h('p', { class: 'set-error', id: `${o.id}-e`, role: 'alert', hidden: true }) : null;
  return h('div', { class: `set-row${o.wide ? ' set-row-wide' : ''}`, ...(o.id ? { 'data-field': o.id } : {}) },
    h('div', { class: 'set-row-text' },
      o.id ? h('label', { class: 'set-label', for: o.id }, o.label) : h('span', { class: 'set-label' }, o.label),
      o.help ? h('p', { class: 'set-help', id: o.id ? `${o.id}-h` : null }, o.help) : null),
    h('div', { class: 'set-row-control' }, o.control, err));
}

function group(title: string | null, ...rows: Child[]): HTMLElement {
  return h('section', { class: 'set-group card' }, title ? h('h3', { class: 'set-group-title' }, title) : null, ...rows);
}

/** showError puts a message (the server's, verbatim) under a field, or clears it. */
function showError(root: ParentNode, id: string, msg: string | null): void {
  const e = root.querySelector<HTMLElement>(`#${id}-e`);
  const i = root.querySelector<HTMLElement>(`#${id}`);
  if (e) {
    e.hidden = !msg;
    replace(e, ...(msg ? [icon('circle-alert', 16), h('span', null, msg)] : []));
  }
  if (i) {
    i.setAttribute('aria-invalid', msg ? 'true' : 'false');
    if (msg) {
      const d = new Set((i.getAttribute('aria-describedby') ?? '').split(' ').filter(Boolean));
      d.add(`${id}-e`);
      i.setAttribute('aria-describedby', [...d].join(' '));
    }
  }
}

/** field: a labelled control for sheets (kit layout), with its error line. */
function field(id: string, label: string, input: HTMLElement, help?: string): HTMLElement {
  if (help) input.setAttribute('aria-describedby', `${id}-h`);
  return h('div', { class: 'field', 'data-field': id }, h('label', { class: 'label', for: id }, label), input,
    help ? h('p', { class: 'help', id: `${id}-h` }, help) : null, h('p', { class: 'error', id: `${id}-e`, role: 'alert', hidden: true }));
}

function note(text: string, ic = 'lock'): HTMLElement {
  return h('p', { class: 'set-note' }, icon(ic, 16), h('span', null, text));
}

function disabledWhy(btn: HTMLButtonElement, why: string, id: string): HTMLElement {
  btn.disabled = true;
  btn.setAttribute('aria-describedby', id);
  return h('span', { class: 'set-why', id }, why);
}

function setSkeleton(rows: number): HTMLElement {
  return h('div', { class: 'set-group card', 'aria-hidden': 'true' }, ...Array.from({ length: rows }, () =>
    h('div', { class: 'set-row' },
      h('div', { class: 'set-row-text' }, h('span', { class: 'skel skel-line set-skel-label' }), h('span', { class: 'skel skel-line set-skel-help' })),
      h('div', { class: 'set-row-control' }, h('span', { class: 'skel set-skel-control' })))));
}

function head(lede: Child, ...actions: Child[]): HTMLElement {
  return h('div', { class: 'set-head' }, h('p', { class: 'set-lede' }, lede), actions.length ? h('div', { class: 'set-head-actions' }, ...actions) : null);
}

function ellipsisMenu(label: string, build: () => MenuItem[]): HTMLButtonElement {
  const b = h('button', { type: 'button', class: 'icon-btn set-more', 'aria-label': label }, icon('ellipsis', 20));
  menuButton(b, label, build);
  return b;
}

interface Ctx {
  body: HTMLElement;
  alive: () => boolean;
  rerender: () => void;
  go: (key: SectionKey | null) => void;
}

// ---------- General ----------

async function general(ctx: Ctx): Promise<void> {
  let org = await api<OrgFull>('GET', '/v1/org');
  if (!ctx.alive()) return;
  const g = gates(myRole());
  let days = org.retention_days ?? DEFAULT_RETENTION;
  const zones = zoneList(org.timezone);

  const name = g.editGeneral
    ? h('input', { class: 'input', id: 'set-name', name: 'name', value: org.name, autocomplete: 'off', maxlength: 100, required: true, 'aria-describedby': 'set-name-h' })
    : h('span', { class: 'set-value', id: 'set-name' }, org.name);

  let tzControl: HTMLElement;
  let sel: HTMLSelectElement | null = null;
  if (g.editGeneral) {
    const search = h('input', { class: 'input', type: 'search', id: 'set-tz-q', name: 'timezone_search', 'aria-label': c.general.zoneFilter,
      placeholder: c.general.zoneFilterPlaceholder, autocomplete: 'off', spellcheck: 'false', 'aria-controls': 'set-tz', 'aria-describedby': 'set-tz-n' });
    const s = h('select', { class: 'select', id: 'set-tz', name: 'timezone', 'aria-describedby': 'set-tz-h' });
    const count = h('p', { class: 'set-help set-tz-count', id: 'set-tz-n', 'aria-live': 'polite' });
    sel = s;
    let chosen = org.timezone;
    s.addEventListener('change', () => (chosen = s.value));
    const fillZones = () => {
      const q = search.value.trim();
      const found = filterZones(zones, q);
      const opts = found.includes(chosen) ? found : [chosen, ...found];
      s.replaceChildren(...opts.map((z) => h('option', { value: z, ...(z === chosen ? { selected: true } : {}) }, z)));
      count.textContent = q ? (found.length ? fill(c.general.zoneMatches, { n: found.length }) : c.general.zoneNone) : '';
    };
    search.addEventListener('input', fillZones);
    fillZones();
    tzControl = h('div', { class: 'set-tz' }, h('span', { class: 'set-search' }, icon('search', 16), search), h('span', { class: 'select-wrap' }, s, icon('chevron-down', 16)), count);
  } else {
    tzControl = h('span', { class: 'set-value', id: 'set-tz' }, org.timezone);
  }

  const help = h('span', null, retentionHelp(days));
  let ret: HTMLInputElement | null = null;
  let retControl: HTMLElement;
  if (g.editRetention) {
    ret = h('input', { class: 'input num set-num', id: 'set-ret', name: 'retention_days', type: 'text', inputmode: 'numeric', value: String(days),
      autocomplete: 'off', maxlength: 4, 'aria-describedby': 'set-ret-h' });
    const r = ret;
    r.addEventListener('input', () => {
      const n = parseRetention(r.value);
      replace(help, retentionHelp(n ?? days));
      if (n !== null || !r.value.trim()) showError(ctx.body, 'set-ret', null); // errors wait for blur or Save
    });
    r.addEventListener('blur', () => {
      showError(ctx.body, 'set-ret', r.value.trim() && parseRetention(r.value) === null ? c.general.retentionBad : null);
    });
    retControl = h('span', { class: 'set-unit' }, r, h('span', { class: 't-2' }, c.general.retentionUnit));
  } else {
    retControl = h('div', { class: 'set-readonly' }, h('span', { class: 'set-value num', id: 'set-ret' }, `${days} ${c.general.retentionUnit}`),
      g.editGeneral ? h('span', { class: 'set-why' }, c.ownerOnly) : null);
  }

  const save = h('button', { type: 'button', class: 'btn btn-primary' }, c.save);
  const rows = group(null,
    row({ id: g.editGeneral ? 'set-name' : undefined, label: c.general.name, help: c.general.nameHelp, control: name }),
    row({ label: c.general.slug, help: c.general.slugHelp, control: h('button', { type: 'button', class: 'idchip set-slug', 'data-copy': org.slug, 'aria-label': `Copy ${org.slug}`, translate: 'no' }, org.slug, icon('copy', 16)) }),
    row({ label: c.general.id, control: idChip(org.id) }),
    row({ id: g.editGeneral ? 'set-tz' : undefined, label: c.general.timezone, help: c.general.timezoneHelp, control: tzControl }),
    row({ id: g.editRetention ? 'set-ret' : undefined, label: c.general.retention, help, control: retControl }),
    row({ label: c.general.created, control: h('span', { class: 'set-value' }, date(org.created_at, org.timezone)) }));

  replace(ctx.body, g.editGeneral ? null : note(c.readOnly), rows, g.editGeneral ? h('div', { class: 'set-actions' }, save) : null);

  save.addEventListener('click', async () => {
    const patch: Record<string, unknown> = {};
    if (name instanceof HTMLInputElement) {
      const v = name.value.trim();
      if (!v) return showError(ctx.body, 'set-name', c.general.nameBad);
      if (v !== org.name) patch.name = v;
    }
    if (sel && sel.value && sel.value !== org.timezone) patch.timezone = sel.value;
    if (ret) {
      const n = parseRetention(ret.value);
      if (n === null) return showError(ctx.body, 'set-ret', c.general.retentionBad);
      if (n !== days) patch.retention_days = n;
    }
    if (Object.keys(patch).length === 0) return void toast(c.noChanges);
    for (const id of ['set-name', 'set-tz', 'set-ret']) showError(ctx.body, id, null);
    save.disabled = true;
    save.setAttribute('aria-busy', 'true');
    try {
      const ans = await api<OrgFull>('PATCH', '/v1/org', { body: patch });
      org = { ...org, ...ans };
      days = ans.retention_days ?? (typeof patch.retention_days === 'number' ? patch.retention_days : days);
      replace(help, retentionHelp(days));
      toast(savedToast(ans), { kind: 'success', action: ans.deed ? { label: copy.stop.viewDeed, href: `/app/deeds/${ans.deed}` } : undefined });
      void refreshOrg().catch(() => undefined);
    } catch (e) {
      const map: Record<string, string> = { name: 'set-name', timezone: 'set-tz', retention_days: 'set-ret' };
      const target = e instanceof ApiError && e.param ? map[e.param.split('.')[0] ?? ''] : undefined;
      if (target && ctx.body.querySelector(`#${target}-e`)) showError(ctx.body, target, (e as Error).message);
      else apiErrorToast(e);
    } finally {
      save.disabled = false;
      save.removeAttribute('aria-busy');
    }
  });
}

// ---------- Members ----------

function openInvite(after: () => void): void {
  const g = gates(myRole());
  const email = h('input', { class: 'input', id: 'inv-email', type: 'email', name: 'email', autocomplete: 'off', spellcheck: 'false', inputmode: 'email', maxlength: 254, required: true });
  let role: Role = 'viewer';
  const radios = h('fieldset', { class: 'set-radios set-radios-stack', id: 'inv-role', 'aria-describedby': 'inv-role-e' },
    h('legend', { class: 'label' }, c.members.role),
    ...g.inviteRoles.map((r) => {
      const input = h('input', { type: 'radio', name: 'inv-role', value: r, ...(r === role ? { checked: true } : {}) });
      input.addEventListener('change', () => (role = r));
      return h('label', { class: 'set-choice' }, input, h('span', null, h('span', { class: 'set-choice-label' }, roleLabel(r)), h('span', { class: 'set-choice-help' }, c.members.roleHelp[r])));
    }),
    h('p', { class: 'error', id: 'inv-role-e', role: 'alert', hidden: true }));
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, c.members.send);
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const sheet = openSheet({ title: fill(c.members.inviteTitle, { org: orgName() }), body: [field('inv-email', c.members.email, email), radios], foot: [cancel, go], initialFocus: () => email });
  cancel.addEventListener('click', sheet.close);
  email.addEventListener('input', () => showError(sheet.el, 'inv-email', null));
  go.addEventListener('click', async () => {
    const v = email.value.trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)) return showError(sheet.el, 'inv-email', c.members.emailBad);
    go.disabled = true;
    go.setAttribute('aria-busy', 'true');
    try {
      const m = await api<Member>('POST', '/v1/org/members', { body: { email: v, role } });
      sheet.close();
      toast(fill(c.members.sent, { email: m.user?.email ?? v, days: inviteDays(m) }), { kind: 'success' });
      after();
    } catch (e) {
      if (e instanceof ApiError && e.param === 'email') showError(sheet.el, 'inv-email', e.message);
      else if (e instanceof ApiError && e.param === 'role') showError(sheet.el, 'inv-role', e.message);
      else apiErrorToast(e);
    } finally {
      go.disabled = false;
      go.removeAttribute('aria-busy');
    }
  });
}

async function members(ctx: Ctx): Promise<void> {
  const all: Member[] = [];
  for await (const page of list<Member>('/v1/org/members')) all.push(...page);
  if (!ctx.alive()) return;
  const s = get();
  const g = gates(myRole());
  const me = { role: myRole(), userId: s?.me.user.id ?? '' };
  const tz = tzOf();
  const invite = h('button', { type: 'button', class: 'btn btn-primary btn-sm' }, icon('plus', 16), c.members.invite);
  const why = g.invite ? null : disabledWhy(invite, c.members.inviteNeedsAdmin, 'set-inv-why');
  invite.addEventListener('click', () => openInvite(ctx.rerender));

  const roleCell = (m: Member): Child => {
    if (!g.changeRoles) return h('span', null, roleLabel(m.role));
    const sel = h('select', { class: 'select set-role', name: `role-${m.id}`, 'aria-label': fill(c.members.roleFor, { name: display(m) }) },
      ...(['viewer', 'admin', 'owner'] as Role[]).map((r) => h('option', { value: r, ...(r === m.role ? { selected: true } : {}) }, roleLabel(r))));
    sel.addEventListener('change', () => {
      const to = sel.value as Role;
      const from = m.role;
      sel.value = from; // the row shows the new role once the server has it
      const self = m.user.id === me.userId;
      const run = async () => {
        await api<Member>('PATCH', `/v1/org/members/${encodeURIComponent(m.id)}`, { body: { role: to } });
        toast(fill(c.members.roleChanged, { name: display(m), role: roleLabel(to) }), { kind: 'success' });
        if (self) location.reload();
        else ctx.rerender();
      };
      if (roleChangeNeedsConfirm(from, to)) {
        confirmTyped({
          title: to === 'owner' ? fill(c.members.promoteTitle, { name: display(m) }) : fill(c.members.demoteTitle, { name: display(m), role: roleLabel(to) }),
          body: to === 'owner' ? c.members.promoteBody : fill(c.members.demoteBody, { name: display(m) }),
          confirmLabel: c.members.roleButton,
          onConfirm: run,
        });
      } else {
        run().catch(apiErrorToast);
      }
    });
    return h('span', { class: 'select-wrap set-role-wrap' }, sel, icon('chevron-down', 16));
  };

  const actionsCell = (m: Member): Child => {
    const self = m.user.id === me.userId;
    const acts = memberActions(me, m);
    if (self) {
      const leave = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, icon('log-out', 16), c.members.leave);
      if (!acts.includes('leave')) return h('span', { class: 'set-cell-actions' }, leave, disabledWhy(leave, c.members.leaveViewer, `set-leave-why`));
      leave.addEventListener('click', () => confirmTyped({
        title: fill(c.members.leaveTitle, { org: orgName() }), body: fill(c.members.leaveBody, { org: orgName() }),
        confirmLabel: c.members.leaveButton, danger: true,
        onConfirm: async () => {
          await api('DELETE', `/v1/org/members/${encodeURIComponent(m.id)}`);
          location.assign('/login');
        },
      }));
      return h('span', { class: 'set-cell-actions' }, leave);
    }
    if (acts.length === 0) return null;
    return ellipsisMenu(fill(c.members.actionsFor, { name: display(m) }), () => {
      const items: MenuItem[] = [];
      if (acts.includes('resend')) items.push({ label: c.members.resend, icon: 'refresh-cw', run: () => {
        api<Member>('POST', '/v1/org/members', { body: { email: m.user.email, role: m.role } })
          .then((ans) => toast(fill(c.members.resent, { email: m.user.email, days: inviteDays(ans) }), { kind: 'success' }))
          .catch(apiErrorToast);
      } });
      if (acts.includes('remove')) items.push({ label: c.members.remove, icon: 'x', run: () => confirmTyped({
        title: fill(c.members.removeTitle, { email: m.user.email, org: orgName() }),
        body: fill(c.members.removeBody, { org: orgName() }),
        expected: m.user.email,
        expectedLabel: fill(c.members.removeConfirm, { email: m.user.email }),
        confirmLabel: c.members.removeButton,
        danger: true,
        onConfirm: async () => {
          await api('DELETE', `/v1/org/members/${encodeURIComponent(m.id)}`);
          toast(fill(c.members.removed, { email: m.user.email, org: orgName() }), { kind: 'success' });
          ctx.rerender();
        },
      }) });
      return items;
    });
  };

  const cols = c.members.columns;
  const table = h('table', { class: 'dt cards set-table set-members' },
    h('thead', null, h('tr', null,
      h('th', { scope: 'col' }, cols.name), h('th', { scope: 'col' }, cols.email), h('th', { scope: 'col' }, cols.role),
      h('th', { scope: 'col' }, cols.status), h('th', { scope: 'col' }, cols.joined), h('th', { scope: 'col', class: 'num' }, h('span', { class: 'sr-only' }, c.actions)))),
    h('tbody', null, ...all.map((m) => h('tr', { 'data-row': '', 'data-id': m.id },
      h('td', null, h('span', { class: 'set-person' }, h('span', { class: 'font-medium' }, display(m)),
        m.user.id === me.userId ? h('span', { class: 'chip chip-outline set-you' }, c.members.you) : null),
        m.user.name ? h('span', { class: 'set-sub' }, m.user.email) : null),
      h('td', { 'data-card': 'hide', class: 'set-email' }, m.user.email),
      h('td', null, roleCell(m)),
      h('td', null, m.status === 'invited'
        ? h('span', { class: 'chip chip-outline' }, icon('clock', 16), c.members.status.invited)
        : h('span', { class: 'chip chip-neutral' }, icon('check', 16), c.members.status.active)),
      h('td', { 'data-card': 'hide' }, m.joined_at ? relTime(m.joined_at, tz) : m.invited_at ? h('span', { class: 't-2' }, `${c.members.invitedOn} `, relTime(m.invited_at, tz)) : '·'),
      h('td', { class: 'num' }, actionsCell(m))))));

  replace(ctx.body,
    head(fill(c.sections.members.lede, { org: orgName() }), invite, why),
    all.length <= 1 && g.invite ? h('p', { class: 'set-note' }, icon('info', 16), h('span', null, c.members.empty)) : null,
    h('div', { class: 'table-frame' }, table));
}

// ---------- API keys (org admin keys) ----------

function openCreateOrgKey(after: () => void): void {
  const name = h('input', { class: 'input', id: 'ok-name', name: 'name', autocomplete: 'off', spellcheck: 'false', maxlength: 64, required: true });
  let mode: 'live' | 'test' = 'test';
  const modes = h('fieldset', { class: 'set-radios', id: 'ok-mode' }, h('legend', { class: 'label' }, c.keys.mode),
    ...(['test', 'live'] as const).map((m) => {
      const input = h('input', { type: 'radio', name: 'ok-mode', value: m, ...(m === mode ? { checked: true } : {}) });
      input.addEventListener('change', () => (mode = m));
      return h('label', { class: 'set-choice set-choice-inline' }, input, h('span', { class: 'set-choice-label' }, m === 'live' ? c.keys.live : c.keys.test));
    }));
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, c.keys.create);
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const sheet = openSheet({ title: c.keys.createTitle, body: [field('ok-name', c.keys.name, name, c.keys.nameHelp), modes], foot: [cancel, go], initialFocus: () => name });
  cancel.addEventListener('click', sheet.close);
  go.addEventListener('click', async () => {
    go.disabled = true;
    try {
      const k = await api<ApiKey>('POST', '/v1/org/keys', { body: { name: name.value.trim(), mode } });
      sheet.close();
      showKeySheet(k, after);
    } catch (e) {
      if (e instanceof ApiError && e.param === 'name') showError(sheet.el, 'ok-name', e.message);
      else apiErrorToast(e);
    } finally {
      go.disabled = false;
    }
  });
}

async function keys(ctx: Ctx): Promise<void> {
  const page = await api<ListResponse<ApiKey>>('GET', '/v1/org/keys');
  if (!ctx.alive()) return;
  const tz = tzOf();
  const create = h('button', { type: 'button', class: 'btn btn-primary btn-sm' }, icon('plus', 16), c.keys.create);
  create.addEventListener('click', () => openCreateOrgKey(ctx.rerender));
  const sorted = [...page.data].sort((a, b) => Number(!!a.revoked_at) - Number(!!b.revoked_at) || b.created_at.localeCompare(a.created_at));
  const cols = c.keys.columns;
  const rows = sorted.map((k) => {
    const label = k.name || k.prefix;
    const status = k.revoked_at
      ? h('span', { class: 'status status-stopped' }, icon('x', 16), c.keys.revoked)
      : k.expires_at
        ? h('span', { class: 'status status-suspended' }, icon('clock', 16), c.keys.expiring)
        : h('span', { class: 'status status-active' }, icon('check', 16), c.keys.active);
    const actions = k.revoked_at ? null : ellipsisMenu(fill(c.keys.actionsFor, { name: label }), () => [
      { label: c.keys.roll, icon: 'refresh-cw', run: () => confirmTyped({
        title: fill(c.keys.rollTitle, { name: label }), body: c.keys.rollBody, confirmLabel: c.keys.rollButton,
        onConfirm: async () => {
          const nk = await api<ApiKey>('POST', `/v1/keys/${encodeURIComponent(k.id)}/roll`, { body: {} });
          showKeySheet(nk, ctx.rerender);
        },
      }) },
      { label: c.keys.revoke, icon: 'ban', run: () => confirmTyped({
        title: fill(c.keys.revokeTitle, { name: label }), body: c.keys.revokeBody,
        expected: label, expectedLabel: fill(c.keys.revokeConfirm, { name: label }),
        reasonLabel: copy.stop.reasonOptional, confirmLabel: c.keys.revokeButton, danger: true,
        onConfirm: async (reason) => {
          await api('POST', `/v1/keys/${encodeURIComponent(k.id)}/revoke`, { body: reason ? { reason } : {} });
          toast(fill(c.keys.revokedToast, { name: label }), { kind: 'success' });
          ctx.rerender();
        },
      }) },
    ]);
    return h('tr', { 'data-row': '', 'data-id': k.id },
      h('td', null, h('span', { class: 'font-medium' }, label), h('span', { class: 'set-sub mono' }, `${k.prefix}…`)),
      h('td', { class: 'mono', 'data-card': 'hide', translate: 'no' }, `${k.prefix}…`),
      h('td', { class: 'mono t-2', 'data-card': 'hide' }, k.scopes.join(' ')),
      h('td', { class: 'num', 'data-card': 'hide' }, relTime(k.created_at, tz)),
      h('td', { class: 'num' }, k.last_used_at ? relTime(k.last_used_at, tz) : h('span', { class: 't-2' }, c.never)),
      h('td', { class: 'num', 'data-card': 'hide' }, k.expires_at ? relTime(k.expires_at, tz) : h('span', { class: 't-2' }, c.keys.noExpiry)),
      h('td', null, status),
      h('td', { class: 'num' }, actions));
  });
  replace(ctx.body,
    head(c.sections.keys.lede, create),
    rows.length
      ? h('div', { class: 'table-frame' }, h('table', { class: 'dt cards set-table' },
          h('thead', null, h('tr', null, ...(['name', 'prefix', 'scopes', 'created', 'used', 'expires', 'status'] as const).map((k) =>
            h('th', { scope: 'col', class: k === 'created' || k === 'used' || k === 'expires' ? 'num' : '' }, cols[k])), h('th', { scope: 'col', class: 'num' }, h('span', { class: 'sr-only' }, c.actions)))),
          h('tbody', null, ...rows)))
      : emptyState(c.keys.empty, 'key-round', undefined, 'https://docs.deedglass.com/cli'));
}

// ---------- Approvals ----------

type Choice<K extends keyof ApprovalSettings> = { value: ApprovalSettings[K]; label: string };

function radioGroup<K extends keyof ApprovalSettings>(key: K, legend: string, help: string, options: Choice<K>[], value: ApprovalSettings[K], enabled: boolean,
  onChange_: (v: ApprovalSettings[K]) => void): HTMLElement {
  const id = `ap-${key}`;
  return h('div', { class: 'set-row' },
    h('div', { class: 'set-row-text' }, h('span', { class: 'set-label', id: `${id}-l` }, legend), h('p', { class: 'set-help', id: `${id}-h` }, help)),
    h('div', { class: 'set-row-control' },
      h('div', { class: 'set-radios set-seg', role: 'radiogroup', 'aria-labelledby': `${id}-l`, 'aria-describedby': `${id}-h` },
        ...options.map((o) => {
          const input = h('input', { type: 'radio', name: id, value: o.value, ...(o.value === value ? { checked: true } : {}), ...(enabled ? {} : { disabled: true }) });
          input.addEventListener('change', () => onChange_(o.value));
          return h('label', { class: 'set-seg-option' }, input, h('span', null, o.label));
        }))));
}

async function approvals(ctx: Ctx): Promise<void> {
  const org = await api<OrgFull>('GET', '/v1/org');
  if (!ctx.alive()) return;
  const g = gates(myRole());
  let before = approvalSettings(org.settings?.approvals);
  const after: ApprovalSettings = { ...before };
  const a = c.approvals;
  const save = h('button', { type: 'button', class: 'btn btn-primary' }, c.save);
  replace(ctx.body,
    head(fill(c.sections.approvals.lede, { org: org.name })),
    g.editApprovals ? null : note(c.ownerOnly),
    group(null,
      radioGroup('email_links', a.emailLinks, a.emailLinksHelp, [{ value: 'decide', label: a.decide }, { value: 'review', label: a.review }], before.email_links, g.editApprovals, (v) => (after.email_links = v)),
      radioGroup('email_detail', a.emailDetail, a.emailDetailHelp, [{ value: 'summary', label: a.summary }, { value: 'full', label: a.full }], before.email_detail, g.editApprovals, (v) => (after.email_detail = v)),
      radioGroup('self_countersign', a.self, a.selfHelp, [{ value: 'deny', label: a.deny }, { value: 'allow', label: a.allow }], before.self_countersign, g.editApprovals, (v) => (after.self_countersign = v))),
    g.editApprovals ? h('div', { class: 'set-actions' }, save) : null);
  const send = async (patch: Partial<ApprovalSettings>) => {
    const ans = await api<OrgFull>('PATCH', '/v1/org', { body: { settings: { approvals: patch } } });
    before = approvalSettings(ans.settings?.approvals ?? after);
    toast(savedToast(ans), { kind: 'success', action: ans.deed ? { label: copy.stop.viewDeed, href: `/app/deeds/${ans.deed}` } : undefined });
  };
  save.addEventListener('click', async () => {
    const patch = approvalsPatch(before, after);
    if (!patch) return void toast(c.noChanges);
    if (patch.self_countersign === 'allow') {
      confirmTyped({
        title: fill(a.allowTitle, { org: org.name }), body: a.allowBody, expected: org.slug,
        expectedLabel: fill(a.allowConfirm, { slug: org.slug }), confirmLabel: a.allowButton, danger: true,
        onConfirm: () => send(patch),
      });
      return;
    }
    save.disabled = true;
    try {
      await send(patch);
    } catch (e) {
      apiErrorToast(e);
    } finally {
      save.disabled = false;
    }
  });
}

// ---------- Webhooks ----------

/**
 * showWebhookSecretSheet: S22's shown-once sheet for a new endpoint's signing secret. The secret
 * lives only in this sheet's DOM; the moment it starts closing (Done, ×, Escape, the scrim) every
 * input value, attribute and text node holding it is wiped, and the sheet's DOM is dropped.
 */
export function showWebhookSecretSheet(w: Pick<Webhook, 'url'> & { secret?: string | null }, onDone?: () => void): Overlay {
  const secret = w.secret ?? null;
  const w8 = c.webhooks;
  const box = h('label', { class: 'check' }, h('input', { type: 'checkbox', name: 'stored' }), h('span', null, w8.stored));
  const input = box.querySelector('input')!;
  const done = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, w8.done);
  input.addEventListener('change', () => (done.disabled = !input.checked));
  const value = secret
    ? h('div', { class: 'field' },
        h('label', { class: 'label', for: 'wh-secret' }, w8.secretLabel),
        h('div', { class: 'secret' },
          h('input', { class: 'input mono', id: 'wh-secret', name: 'signing_secret', readonly: true, value: secret, autocomplete: 'off', spellcheck: 'false', 'data-secret': '' }),
          h('button', { type: 'button', class: 'btn btn-secondary', 'data-copy': secret, 'aria-label': w8.copySecret }, icon('copy', 16), c.copyButton)))
    : h('p', { class: 'banner banner-warn' }, icon('triangle-alert', 16), w8.secretReplayed);
  let wiped = false;
  const wipe = () => {
    if (wiped) return;
    wiped = true;
    for (const el of sheet.el.querySelectorAll('[data-secret], [data-copy]')) {
      if (el instanceof HTMLInputElement) el.value = '';
      el.removeAttribute('data-copy');
      el.removeAttribute('value');
    }
    sheet.el.querySelector('.secret')?.replaceChildren();
    window.removeEventListener('keydown', onEsc, { capture: true });
  };
  const onEsc = (e: KeyboardEvent) => {
    if (e.key === 'Escape') wipe();
  };
  const sheet = openSheet({
    title: w8.createdTitle,
    body: [h('p', { class: 'mono set-hook-url', translate: 'no' }, w.url), value, h('p', null, w8.secretBody), secret ? box : null],
    foot: [secret ? done : h('button', { type: 'button', class: 'btn btn-primary', 'data-close': '' }, w8.done)],
    initialFocus: () => sheet.el.querySelector<HTMLInputElement>('[data-secret]'),
    onClose: () => {
      wipe();
      sheet.el.replaceChildren();
      onDone?.();
    },
  });
  const close = () => {
    wipe();
    sheet.close();
  };
  done.addEventListener('click', close);
  sheet.el.querySelector('[data-close]')?.addEventListener('click', close);
  sheet.el.querySelector('.sheet-head .icon-btn')?.addEventListener('click', wipe, { capture: true });
  window.addEventListener('keydown', onEsc, { capture: true }); // before the sheet's own Escape handler
  return { el: sheet.el, close };
}

function openAddWebhook(after: () => void): void {
  const w8 = c.webhooks;
  const url = h('input', { class: 'input mono', id: 'wh-url', type: 'url', name: 'url', autocomplete: 'off', spellcheck: 'false', inputmode: 'url', placeholder: 'https://hooks.acme.com/deedglass', maxlength: 2048, required: true });
  const desc = h('input', { class: 'input', id: 'wh-desc', name: 'description', autocomplete: 'off', maxlength: 200 });
  const picked = new Set<string>();
  const boxes: HTMLInputElement[] = [];
  const families = eventFamilies(WEBHOOK_EVENTS).map(([fam, evs]) => h('div', { class: 'set-family' },
    h('p', { class: 'set-family-name' }, fam),
    h('div', { class: 'check-grid' }, ...evs.map((ev) => {
      const b = h('input', { type: 'checkbox', name: 'events', value: ev });
      b.addEventListener('change', () => {
        if (b.checked) picked.add(ev);
        else picked.delete(ev);
        showError(sheet.el, 'wh-events', null);
      });
      boxes.push(b);
      return h('label', { class: 'check' }, b, h('span', { class: 'mono' }, ev));
    }))));
  const setAll = (on: boolean) => {
    for (const b of boxes) {
      b.checked = on;
      if (on) picked.add(b.value);
      else picked.delete(b.value);
    }
  };
  const all = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', onclick: () => setAll(true) }, w8.selectAll);
  const none = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', onclick: () => setAll(false) }, w8.clearAll);
  const events = h('fieldset', { class: 'set-events', id: 'wh-events', 'aria-describedby': 'wh-events-h wh-events-e' },
    h('legend', { class: 'label' }, w8.events),
    h('div', { class: 'set-events-head' }, h('p', { class: 'help', id: 'wh-events-h' }, w8.eventsHelp), h('span', { class: 'set-events-tools' }, all, none)),
    ...families,
    h('p', { class: 'error', id: 'wh-events-e', role: 'alert', hidden: true }));
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, w8.add);
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const sheet = openSheet({ title: w8.addTitle, body: [field('wh-url', w8.url, url, w8.urlHelp), field('wh-desc', w8.description, desc), events], foot: [cancel, go], initialFocus: () => url, className: 'sheet-wide' });
  cancel.addEventListener('click', sheet.close);
  url.addEventListener('input', () => showError(sheet.el, 'wh-url', null));
  go.addEventListener('click', async () => {
    const u = url.value.trim();
    let okURL = false;
    try {
      const p = new URL(u);
      okURL = p.protocol === 'https:' || p.protocol === 'http:';
    } catch {
      okURL = false;
    }
    if (!okURL) return showError(sheet.el, 'wh-url', w8.urlBad);
    if (picked.size === 0) return showError(sheet.el, 'wh-events', w8.eventsBad);
    go.disabled = true;
    go.setAttribute('aria-busy', 'true');
    try {
      const body: Record<string, unknown> = { url: u, events: WEBHOOK_EVENTS.filter((e) => picked.has(e)) };
      if (desc.value.trim()) body.description = desc.value.trim();
      const w = await api<Webhook>('POST', '/v1/webhooks', { body });
      sheet.close();
      toast(w8.created, { kind: 'success' });
      showWebhookSecretSheet(w, after);
    } catch (e) {
      const map: Record<string, string> = { url: 'wh-url', events: 'wh-events', description: 'wh-desc' };
      const target = e instanceof ApiError && e.param ? map[e.param.split(/[.[]/)[0] ?? ''] : undefined;
      if (target) showError(sheet.el, target, (e as Error).message);
      else apiErrorToast(e);
    } finally {
      go.disabled = false;
      go.removeAttribute('aria-busy');
    }
  });
}

function hookStatus(w: Webhook): HTMLElement {
  const s = c.webhooks.status;
  if (w.status === 'disabled') return h('span', { class: 'status status-stopped', title: w.disabled_reason ?? '' }, icon('ban', 16), s.disabled);
  if (w.failing_since) return h('span', { class: 'status status-suspended' }, icon('triangle-alert', 16), s.failing);
  return h('span', { class: 'status status-active' }, icon('check', 16), s.active);
}

function deliveriesTable(ds: Delivery[], tz: string): HTMLElement {
  const w8 = c.webhooks;
  if (ds.length === 0) return h('p', { class: 'set-help set-deliveries-empty' }, w8.deliveriesEmpty);
  const tone = (st: Delivery['status']) => st === 'succeeded' ? 'status-active' : st === 'pending' ? 'status-test' : 'status-stopped';
  const ic = (st: Delivery['status']) => st === 'succeeded' ? 'check' : st === 'pending' ? 'clock' : 'x';
  return h('table', { class: 'dt cards set-table set-deliveries' },
    h('thead', null, h('tr', null, h('th', { scope: 'col' }, w8.dcols.type), h('th', { scope: 'col' }, w8.dcols.status),
      h('th', { scope: 'col', class: 'num' }, w8.dcols.attempts), h('th', { scope: 'col', class: 'num' }, w8.dcols.code), h('th', { scope: 'col', class: 'num' }, w8.dcols.time))),
    h('tbody', null, ...ds.map((d) => h('tr', null,
      h('td', { class: 'mono' }, d.type),
      h('td', null, h('span', { class: `status ${tone(d.status)}`, title: d.last_error ?? '' }, icon(ic(d.status), 16), w8.dstatus[d.status] ?? d.status)),
      h('td', { class: 'num', 'data-card': 'label', 'data-label': w8.dcols.attempts }, String(d.attempt)),
      h('td', { class: 'num', 'data-card': 'label', 'data-label': w8.dcols.code }, d.response_status ? `${d.response_status}${d.response_ms !== null ? ` · ${d.response_ms} ms` : ''}` : '·'),
      h('td', { class: 'num' }, relTime(d.created_at, tz))))));
}

function hookItem(w: Webhook, ctx: Ctx, tz: string): HTMLElement {
  const w8 = c.webhooks;
  const host = hostOf(w.url);
  const result = h('p', { class: 'set-test', 'aria-live': 'polite' });
  const region = h('div', { class: 'set-deliveries', id: `wh-d-${w.id}`, role: 'region', 'aria-label': `${w8.deliveries}, ${host}`, hidden: true });
  const toggle = h('button', { type: 'button', class: 'btn btn-quiet btn-sm set-toggle', 'aria-expanded': 'false', 'aria-controls': `wh-d-${w.id}` }, icon('chevron-right', 16), h('span', null, w8.deliveriesShow));
  const loadDeliveries = async () => {
    replace(region, skeleton(2));
    try {
      const page = await api<ListResponse<Delivery>>('GET', `/v1/webhooks/${encodeURIComponent(w.id)}/deliveries`, { query: { limit: '20' } });
      replace(region, h('div', { class: 'table-frame' }, deliveriesTable(page.data, tz)));
    } catch (e) {
      replace(region, errorState(e, () => void loadDeliveries()));
    }
  };
  toggle.addEventListener('click', () => {
    const open = toggle.getAttribute('aria-expanded') !== 'true';
    toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    replace(toggle, icon(open ? 'chevron-down' : 'chevron-right', 16), h('span', null, open ? w8.deliveriesHide : w8.deliveriesShow));
    region.hidden = !open;
    if (open) void loadDeliveries();
  });
  const test = h('button', { type: 'button', class: 'btn btn-secondary btn-sm' }, w8.test);
  test.addEventListener('click', async () => {
    test.disabled = true;
    test.setAttribute('aria-busy', 'true');
    replace(result, h('span', { class: 't-2' }, w8.testing));
    try {
      const r = await api<WebhookTestResult>('POST', `/v1/webhooks/${encodeURIComponent(w.id)}/test`);
      const line = testLine(r);
      result.className = `set-test ${line.ok ? 'set-test-ok' : 'set-test-fail'}`;
      replace(result, line.text);
      if (!region.hidden) void loadDeliveries();
    } catch (e) {
      replace(result);
      apiErrorToast(e);
    } finally {
      test.disabled = false;
      test.removeAttribute('aria-busy');
    }
  });
  const more = ellipsisMenu(fill(w8.actionsFor, { host }), () => [{ label: w8.delete, icon: 'x', run: () => confirmTyped({
    title: fill(w8.deleteTitle, { host }), body: w8.deleteBody, expected: host, expectedLabel: fill(w8.deleteConfirm, { host }),
    confirmLabel: w8.deleteButton, danger: true,
    onConfirm: async () => {
      await api('DELETE', `/v1/webhooks/${encodeURIComponent(w.id)}`);
      toast(fill(w8.deleted, { host }), { kind: 'success' });
      ctx.rerender();
    },
  }) }]);
  const shown = w.events.slice(0, 3);
  return h('li', { class: 'set-hook', 'data-id': w.id },
    h('div', { class: 'set-hook-top' },
      h('span', { class: 'mono set-hook-url', title: w.url, translate: 'no' }, w.url),
      hookStatus(w),
      h('span', { class: 'set-hook-actions' }, test, more)),
    w.description ? h('p', { class: 'set-help' }, w.description) : null,
    h('dl', { class: 'set-hook-meta' },
      h('div', null, h('dt', null, w8.columns.events), h('dd', { class: 'set-events-list' }, ...shown.map((e) => h('span', { class: 'label-chip mono' }, e)),
        w.events.length > shown.length ? h('span', { class: 't-2', title: w.events.join(', ') }, fill(w8.eventsMore, { n: w.events.length - shown.length })) : null)),
      h('div', null, h('dt', null, w8.columns.lastSuccess), h('dd', null, w.last_success_at ? relTime(w.last_success_at, tz) : h('span', { class: 't-2' }, c.never))),
      w.failing_since ? h('div', null, h('dt', null, w8.columns.failingSince), h('dd', null, relTime(w.failing_since, tz))) : null),
    w.status === 'disabled' && w.disabled_reason ? h('p', { class: 'set-note' }, icon('circle-alert', 16), h('span', null, w.disabled_reason)) : null,
    result,
    toggle,
    region);
}

async function webhooks(ctx: Ctx): Promise<void> {
  const page = await api<ListResponse<Webhook>>('GET', '/v1/webhooks');
  if (!ctx.alive()) return;
  const tz = tzOf();
  const add = h('button', { type: 'button', class: 'btn btn-primary btn-sm' }, icon('plus', 16), c.webhooks.add);
  add.addEventListener('click', () => openAddWebhook(ctx.rerender));
  replace(ctx.body,
    head(c.sections.webhooks.lede, page.data.length ? add : null),
    page.data.length
      ? h('ul', { class: 'set-hooks card' }, ...page.data.map((w) => hookItem(w, ctx, tz)))
      : emptyState(c.webhooks.empty, 'link', add, 'https://docs.deedglass.com/webhooks'));
}

// ---------- Seal keys ----------

async function sealKeys(ctx: Ctx): Promise<void> {
  let res: Response;
  try {
    res = await fetch('/.well-known/deedglass/keys.json', { headers: { Accept: 'application/json' }, credentials: 'omit', redirect: 'error', cache: 'no-store' });
  } catch {
    throw new ApiError(0, null, copy.toast.networkError);
  }
  if (!res.ok) throw new ApiError(res.status, null, fill(c.sealKeys.unreachable, { status: res.status }));
  const set = (await res.json()) as { keys?: SealKey[] };
  if (!ctx.alive()) return;
  const tz = tzOf();
  const rank = (s: string) => (s === 'active' ? 0 : s === 'retired' ? 1 : 2);
  const ks = [...(set.keys ?? [])].sort((a, b) => rank(a.status) - rank(b.status) || (b.activated_at ?? '').localeCompare(a.activated_at ?? ''));
  const sk = c.sealKeys;
  const chip = (st: string) => st === 'active'
    ? h('span', { class: 'chip chip-neutral' }, icon('check', 16), sk.status.active)
    : st === 'revoked'
      ? h('span', { class: 'chip chip-outline' }, icon('ban', 16), sk.status.revoked)
      : h('span', { class: 'chip chip-outline' }, icon('clock', 16), sk.status.retired ?? st);
  const until = (k: SealKey) => k.revoked_at ?? k.retired_at;
  replace(ctx.body,
    head(sk.sentence),
    ks.length
      ? h('div', { class: 'table-frame' }, h('table', { class: 'dt cards set-table' },
          h('thead', null, h('tr', null, h('th', { scope: 'col' }, sk.columns.key), h('th', { scope: 'col' }, sk.columns.status),
            h('th', { scope: 'col', class: 'num' }, sk.columns.from), h('th', { scope: 'col', class: 'num' }, sk.columns.to), h('th', { scope: 'col' }, sk.columns.thumbprint))),
          h('tbody', null, ...ks.map((k) => h('tr', null,
            h('td', null, idChip(k.id)),
            h('td', null, chip(k.status)),
            h('td', { class: 'num', 'data-card': 'label', 'data-label': sk.columns.from }, k.activated_at ? date(k.activated_at, tz) : '·'),
            h('td', { class: 'num', 'data-card': 'label', 'data-label': sk.columns.to }, until(k) ? date(until(k)!, tz) : h('span', { class: 't-2' }, sk.now)),
            h('td', { 'data-card': 'hide' }, k.thumbprint ? hashChip(k.thumbprint) : '·'))))))
      : emptyState(sk.empty, 'key-round'));
}

// ---------- Notifications ----------

async function notifications(ctx: Ctx): Promise<void> {
  const org = await api<OrgFull>('GET', '/v1/org');
  if (!ctx.alive()) return;
  const ap = approvalSettings(org.settings?.approvals);
  const n = c.notifications;
  const a = c.approvals;
  const toApprovals = h('a', { href: sectionPath('approvals'), class: 'card-link', 'data-settings-link': 'approvals' }, n.change, icon('chevron-right', 16));
  replace(ctx.body,
    head(fill(c.sections.notifications.lede, { org: org.name })),
    group(n.emailsTitle,
      row({ label: n.who, help: n.whoBody, control: h('a', { href: '/app/policies', class: 'card-link' }, n.whoLink, icon('chevron-right', 16)) }),
      row({ label: n.links, help: a.emailLinksHelp, control: h('span', { class: 'set-value' }, ap.email_links === 'review' ? a.review : a.decide) }),
      row({ label: n.detail, help: a.emailDetailHelp, control: h('span', { class: 'set-value' }, ap.email_detail === 'full' ? a.full : a.summary) }),
      h('div', { class: 'set-row set-row-foot' }, toApprovals)),
    group(n.slackTitle,
      row({ label: n.slackTitle, help: n.slack, control: h('a', { href: '/app/connectors', class: 'card-link' }, n.slackLink, icon('chevron-right', 16)) })));
}

// ---------- Danger zone ----------

function danger(ctx: Ctx): void {
  const s = get();
  if (!s) return;
  const g = gates(s.me.membership.role);
  const d = c.danger;
  const stopped = !!s.org.stop?.stopped;
  const stop = h('button', { type: 'button', class: 'btn btn-danger-outline' }, icon('stop', 16), copy.stop.allButton);
  const start = h('button', { type: 'button', class: 'btn btn-secondary' }, icon('play', 16), copy.stop.startButton);
  const exp = h('button', { type: 'button', class: 'btn btn-secondary' }, icon('download', 16), d.exportButton);
  stop.addEventListener('click', () => stopAll());
  start.addEventListener('click', () => startAll());
  const from = s.org.created_at.slice(0, 10);
  // deeds.ts loads on demand: the export sheet is the only piece of it Settings needs.
  exp.addEventListener('click', () => {
    import('./deeds.ts')
      .then(({ exportSheet }) => exportSheet({ from, to: new Date().toISOString().slice(0, 10) }))
      .catch(apiErrorToast);
  });
  const stopWhy = !g.stopAll ? disabledWhy(stop, fill(copy.stop.viewer, { org: s.org.name }), 'dz-stop-why') : stopped ? disabledWhy(stop, fill(d.stopped, { org: s.org.name }), 'dz-stop-why') : null;
  const startWhy = !g.startAll ? disabledWhy(start, d.startOwner, 'dz-start-why') : !stopped ? disabledWhy(start, d.startNotStopped, 'dz-start-why') : null;
  const line = (title: string, lede: string, btn: HTMLElement, why: HTMLElement | null) =>
    h('div', { class: 'danger-row' }, h('div', null, h('p', { class: 'font-medium m-0' }, title), h('p', null, lede), why ? h('p', { class: 'set-why-line' }, why) : null), btn);
  replace(ctx.body,
    head(c.sections.danger.lede),
    h('section', { class: 'card set-danger', 'aria-label': c.sections.danger.title },
      line(copy.stop.allButton, fill(d.stopLede, { org: s.org.name }), stop, stopWhy),
      line(copy.stop.startButton, d.startLede, start, startWhy),
      line(d.exportTitle, fill(d.exportLede, { date: date(s.org.created_at, s.org.timezone) }), exp, null)));
}

// ---------- the page ----------

const RENDER: Record<SectionKey, (ctx: Ctx) => Promise<void> | void> = {
  general, members, keys, approvals, webhooks, 'seal-keys': sealKeys, notifications, danger,
};

function loadingFor(k: SectionKey): HTMLElement {
  if (k === 'members' || k === 'keys' || k === 'seal-keys') return h('div', null, h('div', { class: 'set-head', 'aria-hidden': 'true' }, h('span', { class: 'skel skel-line set-skel-help' })), skeleton(4));
  if (k === 'webhooks') return h('div', null, h('div', { class: 'set-head', 'aria-hidden': 'true' }, h('span', { class: 'skel skel-line set-skel-help' })), skeleton(3));
  return setSkeleton(k === 'general' ? 6 : k === 'danger' ? 3 : 4);
}

const TITLES: Record<string, string> = Object.fromEntries(SETTINGS_SECTIONS.map((s) => [s.key, s.label]));

export function mountSettings(): void {
  const root = maybe('settings');
  const nav = maybe('settings-nav');
  const body = maybe('settings-body');
  const title = maybe('settings-h');
  if (!root || !nav || !body || !title) return;
  let current: SectionKey = 'general';
  let token = 0;
  let ready = false;
  const narrow = () => matchMedia('(max-width: 767.98px)').matches;

  const render = async (k: SectionKey) => {
    const mine = ++token;
    const alive = () => mine === token;
    body.setAttribute('aria-busy', 'true');
    const need = sectionNeeds(k);
    if (need && !atLeast(get()?.me.membership.role, need)) {
      body.replaceChildren(permissionState(copy.topbar.role[need] ?? 'Admin'));
      body.setAttribute('aria-busy', 'false');
      return;
    }
    body.replaceChildren(loadingFor(k));
    const ctx: Ctx = { body, alive, rerender: () => void render(k), go };
    try {
      await RENDER[k](ctx);
    } catch (e) {
      if (!alive()) return;
      if (e instanceof ApiError && e.status === 401) return;
      body.replaceChildren(e instanceof ApiError && e.status === 403 ? permissionState(copy.topbar.role[sectionNeeds(k) ?? 'admin'] ?? 'Admin') : errorState(e, () => void render(k)));
    } finally {
      if (alive()) body.setAttribute('aria-busy', 'false');
    }
  };

  const show = (key: SectionKey | null, focus: boolean) => {
    root.dataset.view = key ? 'section' : 'list';
    current = key ?? 'general';
    for (const a of nav.querySelectorAll<HTMLAnchorElement>('a[data-section]')) {
      if (a.dataset.section === current) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    }
    title.textContent = TITLES[current] ?? current;
    document.title = key ? `${TITLES[current]} · Settings · Deedglass` : 'Settings · Deedglass';
    if (ready) void render(current);
    if (focus) title.focus();
  };

  function go(key: SectionKey | null): void {
    const url = sectionPath(key);
    if (location.pathname !== url) history.pushState(null, '', url);
    // On a phone the list and the section are separate screens: move focus with the screen.
    show(key, key !== null && narrow());
    if (key === null && narrow()) nav?.querySelector<HTMLElement>(`a[data-section="${current}"]`)?.focus();
  }

  const onLink = (e: MouseEvent) => {
    const a = (e.target as HTMLElement).closest?.('a[data-section], a[data-settings-link]') as HTMLAnchorElement | null;
    if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const raw = a.dataset.section ?? a.dataset.settingsLink ?? '';
    const key = raw === 'list' ? null : isSection(raw) ? raw : undefined;
    if (key === undefined) return;
    e.preventDefault();
    go(key);
  };
  root.addEventListener('click', onLink);
  window.addEventListener('popstate', () => show(parseSection(location.pathname).key, false));

  const first = parseSection(location.pathname);
  if (first.canonical !== location.pathname) history.replaceState(null, '', first.canonical + location.search);
  show(first.key, false);

  document.addEventListener('dg:ready', () => {
    ready = true;
    const role = get()?.me.membership.role;
    for (const a of nav.querySelectorAll<HTMLAnchorElement>('a[data-section]')) {
      const need = isSection(a.dataset.section ?? '') ? sectionNeeds(a.dataset.section as SectionKey) : null;
      a.querySelector('.set-lock')?.remove();
      if (need && !atLeast(role, need)) a.insertBefore(h('span', { class: 'set-lock', title: c.needsAdmin }, icon('lock', 16), h('span', { class: 'sr-only' }, ` (${c.needsAdmin})`)), a.querySelector('.settings-nav-chev'));
    }
    void render(current);
    // The danger zone follows Stop all and Start all (org.stopped / org.started refresh the state).
    let seen = get()?.org.stop?.stopped;
    onChange((s) => {
      if (s.org.stop?.stopped !== seen && current === 'danger') void render('danger');
      seen = s.org.stop?.stopped;
    });
  }, { once: true });
  document.addEventListener('dg:load-failed', (e) => body.replaceChildren(errorState((e as CustomEvent).detail, () => location.reload())), { once: true });
}

