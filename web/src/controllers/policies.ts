// policies.ts: Policies (S26 §3.2): the list with the starter picker, and a policy's page with tabs
// Editor · Dry run · Tests · Versions · Replay (?tab=, ?v=, ?line=). Activation goes only through
// the sheets here: Enforce needs the policy's name typed and a reason, Shadow a reason, and a
// default_allow version a ticked acknowledgement. The server enforces every rule regardless.

import { api, ApiError } from '../lib/api.ts';
import { relTime } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { policiesCopy as c } from '../lib/copy-policies.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { dateTime, number } from '../lib/format.ts';
import { diffLines } from '../lib/linediff.ts';
import {
  authorName, dirty, setPeople, dryRunInput, enforceTitle, hasDefaultAllow, headerLine, shadowEvidence, shortVersionHash, sourceBytes, statusLabel, statusOf,
  MAX_SOURCE_BYTES, type ActiveSet, type Policy, type PolicyDecision, type PolicyStatus, type PolicyVersion, type TestResult,
} from '../lib/models/policy.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Agent, ListResponse, Overview } from '../lib/types.ts';
import type { Connector } from '../lib/models/connector.ts';
import { segmented } from './components.ts';
import { lineDiffView } from './linediffview.ts';
import { mountReplay, type ReplayTab } from './replay.ts';
import { announce } from './shell.ts';
import { openSheet, type Overlay } from './overlay.ts';
import { mountLintedEditor, type LintedEditor } from './policyeditor.ts';
import { emptyState, errorState, notFound, skeleton } from './screens.ts';
import { wireTable } from './table.ts';
import { testResultView, traceView } from './traceview.ts';
import { apiErrorToast, toast } from './toast.ts';

const tz = () => get()?.org.timezone ?? 'UTC';
const isAdmin = () => atLeast(get()?.me.membership.role, 'admin');
const NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

export function statusChipFor(s: PolicyStatus): HTMLElement {
  if (s === 'enforced') return h('span', { class: 'chip chip-neutral' }, icon('gate', 16), statusLabel(s));
  if (s === 'shadow') return h('span', { class: 'chip chip-shadow' }, icon('contrast', 16), statusLabel(s));
  return h('span', { class: 'chip chip-outline' }, icon('circle', 16), statusLabel(s));
}

// ---------- starters ----------

const STARTERS = import.meta.glob(['../../../examples/policies/*.yaml', '!**/*.cases.yaml'], { query: '?raw', import: 'default' }) as Record<string, () => Promise<string>>;

export interface Starter { name: string; description: string; source: string }

/** starterOf reads a starter's name and its header comment (the description) from its YAML. */
export function starterOf(source: string): Starter {
  const name = /^name:\s*([a-z0-9-]+)/m.exec(source)?.[1] ?? 'policy';
  const head: string[] = [];
  for (const line of source.split('\n').slice(1)) {
    if (!line.startsWith('#')) break;
    const t = line.replace(/^#\s?/, '');
    if (!t.trim()) { if (head.length) break; continue; }
    head.push(t.trim());
  }
  return { name, description: head.join(' '), source };
}

async function loadPeople(): Promise<void> {
  try {
    setPeople((await api<ListResponse<{ user: { id: string; name: string; email: string } }>>('GET', '/v1/org/members', { query: { limit: '100' } })).data);
  } catch {
    /* an admin key or a narrow role: authors stay as ids */
  }
}

async function loadStarters(): Promise<Starter[]> {
  const out = await Promise.all(Object.values(STARTERS).map((f) => f().then(starterOf)));
  const order = ['postgres-safe', 'stripe-refunds', 'github-read-mostly', 'slack-post-with-approval', 'gmail-drafts-only', 'default-deny'];
  return out.sort((a, b) => (order.indexOf(a.name) + 99) % 99 - (order.indexOf(b.name) + 99) % 99);
}

function openStarterPicker(): void {
  const list = h('ul', { class: 'starter-list', 'aria-busy': 'true' }, h('li', null, h('span', { class: 'skel skel-line' })));
  const name = h('input', { class: 'input mono', id: 'np-name', name: 'name', autocomplete: 'off', spellcheck: 'false', maxlength: 63, 'aria-describedby': 'np-name-h np-name-e' });
  const err = h('p', { class: 'error', id: 'np-name-e', role: 'alert', hidden: true });
  const blankBox = h('div', { class: 'field', hidden: true }, h('label', { class: 'label', for: 'np-name' }, c.nameLabel), name, h('p', { class: 'help', id: 'np-name-h' }, c.nameHint), err);
  const go = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, c.create);
  let picked: Starter | 'blank' | null = null;
  const sheet = openSheet({ title: c.starterTitle, body: [h('p', { class: 't-2' }, c.starterLede), list, blankBox], foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), go], className: 'starter-sheet' });
  sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
  const choose = (s: Starter | 'blank', btn: HTMLElement) => {
    picked = s;
    for (const b of list.querySelectorAll('[aria-pressed]')) b.setAttribute('aria-pressed', b === btn ? 'true' : 'false');
    blankBox.hidden = s !== 'blank';
    go.disabled = s === 'blank' && !NAME_RE.test(name.value);
    if (s === 'blank') name.focus();
  };
  name.addEventListener('input', () => { if (picked === 'blank') go.disabled = !NAME_RE.test(name.value); });
  void loadStarters().then((ss) => {
    list.setAttribute('aria-busy', 'false');
    const row = (label: string, desc: string, s: Starter | 'blank') => {
      const b = h('button', { type: 'button', class: 'starter', 'aria-pressed': 'false' }, h('span', { class: 'mono starter-name', translate: 'no' }, label), h('span', { class: 'starter-desc t-2' }, desc));
      b.addEventListener('click', () => choose(s, b));
      return h('li', null, b);
    };
    replace(list, ...ss.map((s) => row(s.name, s.description, s)), row(c.blank, c.blankDescription, 'blank'));
  });
  go.addEventListener('click', async () => {
    if (!picked) return;
    const source = picked === 'blank' ? fill(c.blankSource, { name: name.value }) : picked.source;
    go.disabled = true;
    try {
      const p = await api<Policy>('POST', '/v1/policies', { body: { source } });
      sheet.close();
      toast(fill(c.created, { name: p.name }), { kind: 'success' });
      location.assign(`/app/policies/${p.id}?tab=editor`);
    } catch (e) {
      go.disabled = false;
      err.hidden = false;
      replace(err, icon('circle-alert', 16), e instanceof Error ? e.message : String(e));
      if (picked !== 'blank') { blankBox.hidden = false; name.hidden = true; }
    }
  });
}

// ---------- the list ----------

export function mountPolicies(): void {
  const host = maybe('screen-body');
  const actions = maybe('policies-actions');
  if (!host) return;
  const load = async (quiet = false) => {
    if (!quiet) host.replaceChildren(skeleton(4));
    try {
      const [list] = await Promise.all([api<ListResponse<Policy>>('GET', '/v1/policies', { query: { limit: '100' } }), loadPeople()]);
      if (actions) replace(actions, isAdmin() ? h('button', { type: 'button', class: 'btn btn-primary', onclick: openStarterPicker }, icon('plus', 16), c.newPolicy) : null);
      const onlyDefault = list.data.length === 0 || (list.data.length === 1 && list.data[0]!.name === 'default-deny');
      const versions = await Promise.all(list.data.map((p) => api<ListResponse<PolicyVersion>>('GET', `/v1/policies/${p.id}/versions`, { query: { limit: '100' } }).then((r) => r.data).catch(() => [] as PolicyVersion[])));
      const t = h('table', { class: 'dt cards' },
        h('thead', null, h('tr', null, ...[c.columns.name, c.columns.status, c.columns.active, c.columns.latest, c.columns.rules, c.columns.updated].map((l, i) => h('th', { scope: 'col', class: i >= 2 && i <= 4 ? 'num' : '' }, i === 0 || i === 5 ? h('button', { type: 'button' }, l) : l)))),
        h('tbody', null, ...list.data.map((p, i) => {
          const vs = versions[i] ?? [];
          const cur = vs.find((v) => v.version === p.active_version) ?? vs.find((v) => v.version === p.latest_version) ?? vs[0];
          const latest = vs.find((v) => v.version === p.latest_version) ?? cur;
          return h('tr', { 'data-row': '' },
            h('td', { 'data-label': c.columns.name, 'data-sort': p.name }, h('a', { class: 'rowlink mono', href: `/app/policies/${p.id}`, translate: 'no' }, p.name)),
            h('td', { 'data-label': c.columns.status }, statusChipFor(statusOf(p)), p.shadow_version ? h('span', { class: 't-footnote t-2 ml-2' }, fill(c.shadowLive, { v: p.shadow_version })) : null),
            h('td', { class: 'num', 'data-label': c.columns.active, 'data-card': 'label' }, p.active_version ? `v${p.active_version}` : '·'),
            h('td', { class: 'num', 'data-label': c.columns.latest, 'data-card': 'label' }, `v${p.latest_version}`),
            h('td', { class: 'num', 'data-label': c.columns.rules, 'data-card': 'hide' }, cur ? number(cur.rules) : '·'),
            h('td', { 'data-label': c.columns.updated, 'data-sort': p.updated_at }, relTime(p.updated_at, tz()), latest ? h('span', { class: 't-footnote t-2' }, ` · ${authorName(latest)}`) : null));
        })));
      wireTable(t as HTMLTableElement);
      host.replaceChildren(h('div', { class: 'table-frame' }, t));
      if (onlyDefault) host.append(emptyState(c.empty, 'scroll-text', isAdmin() ? h('button', { type: 'button', class: 'btn btn-primary', onclick: openStarterPicker }, icon('plus', 16), c.newPolicy) : undefined, 'https://docs.deedglass.com/policies'));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => {
    void load();
    let tm = 0;
    stream().on('policy.*', () => { clearTimeout(tm); tm = window.setTimeout(() => void load(true), 300); });
  }, { once: true });
}

// ---------- activation sheets ----------

interface ActivateOptions {
  policy: Policy;
  version: PolicyVersion;
  mode: 'enforce' | 'shadow';
  defaultAllow: boolean;
  onDone: () => void;
}

function openActivate(o: ActivateOptions): Overlay {
  const enforce = o.mode === 'enforce';
  const v = o.version.version;
  const org = get()?.org.name ?? 'this org';
  const typed = enforce ? h('input', { class: 'input mono', id: 'act-name', name: 'confirm', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': 'act-name-h' }) : null;
  const reason = h('textarea', { class: 'textarea', id: 'act-reason', name: 'reason', rows: 3, maxlength: 280, required: true, 'aria-required': 'true', autocomplete: 'off' });
  const ack = o.defaultAllow ? h('input', { type: 'checkbox', id: 'act-ack', name: 'acknowledge' }) : null;
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, fill(enforce ? c.enforce.button : c.shadow.button, { v }));
  const ready = () => {
    go.disabled = !((!typed || typed.value === o.policy.name) && reason.value.trim().length >= 3 && (!ack || ack.checked));
  };
  for (const el of [typed, reason, ack]) el?.addEventListener('input', ready);
  ack?.addEventListener('change', ready);
  const body: Child[] = [
    h('p', null, enforce ? fill(c.enforce.body, { org }) : c.shadow.body),
    h('dl', { class: 'kv act-version' }, h('dt', null, 'Version'), h('dd', { class: 'mono', translate: 'no' }, `${o.policy.name} · ${headerLine(o.version)}`)),
    typed ? h('div', { class: 'field' }, h('label', { class: 'label', for: 'act-name' }, fill(c.enforce.confirm, { name: o.policy.name })), typed, h('p', { class: 'help', id: 'act-name-h' }, copy.stop.mismatchHint)) : null,
    h('div', { class: 'field' }, h('label', { class: 'label', for: 'act-reason' }, c.enforce.reason), reason, h('p', { class: 'help' }, c.enforce.reasonHelp)),
    ack ? h('label', { class: 'check act-ack' }, ack, h('span', null, c.enforce.ack)) : null,
    err,
  ];
  const ov = openSheet({ title: enforce ? enforceTitle(o.policy.name, v) : fill(c.shadow.title, { name: o.policy.name, v }), body, foot: [cancel, go], initialFocus: () => cancel, role: enforce ? 'alertdialog' : 'dialog', kind: 'dialog' });
  cancel.addEventListener('click', ov.close);
  go.addEventListener('click', async () => {
    if (go.disabled) return;
    go.disabled = true;
    go.setAttribute('aria-busy', 'true');
    err.hidden = true;
    try {
      const a = await api<{ deeds?: string[] }>('POST', `/v1/policies/${o.policy.id}/versions/${v}/activate`, {
        body: { mode: o.mode, reason: reason.value.trim(), ...(ack?.checked ? { acknowledge_warnings: ['default_allow'] } : {}) },
      });
      ov.close();
      const deed = a.deeds?.[a.deeds.length - 1];
      toast(fill(enforce ? c.enforce.toast : c.shadow.toast, { name: o.policy.name, v }), { kind: 'success', ...(deed ? { action: { label: c.viewDeed, href: `/app/deeds/${deed}` } } : {}) });
      o.onDone();
    } catch (e) {
      go.removeAttribute('aria-busy');
      replace(err, icon('circle-alert', 16), e instanceof Error ? e.message : String(e));
      err.hidden = false;
      ready();
    }
  });
  return ov;
}

function openStopShadow(p: Policy, v: number, onDone: () => void): void {
  const reason = h('textarea', { class: 'textarea', id: 'ss-reason', name: 'reason', rows: 3, maxlength: 280, required: true });
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const go = h('button', { type: 'button', class: 'btn btn-secondary', disabled: true }, c.shadow.stop);
  const cancel = h('button', { type: 'button', class: 'btn btn-quiet' }, copy.stop.cancel);
  reason.addEventListener('input', () => (go.disabled = reason.value.trim().length < 3));
  const ov = openSheet({ kind: 'dialog', title: fill(c.shadow.stopTitle, { name: p.name, v }), body: [h('p', null, c.shadow.stopBody), h('div', { class: 'field' }, h('label', { class: 'label', for: 'ss-reason' }, c.enforce.reason), reason), err], foot: [cancel, go], initialFocus: () => cancel });
  cancel.addEventListener('click', ov.close);
  go.addEventListener('click', async () => {
    go.disabled = true;
    try {
      await api('POST', `/v1/policies/${p.id}/versions/${v}/deactivate`, { body: { reason: reason.value.trim() } });
      ov.close();
      toast(c.shadow.stopToast, { kind: 'success' });
      onDone();
    } catch (e) {
      replace(err, icon('circle-alert', 16), e instanceof Error ? e.message : String(e));
      err.hidden = false;
      go.disabled = false;
    }
  });
}

// ---------- one policy ----------

type Tab = 'editor' | 'dry-run' | 'tests' | 'versions' | 'replay';
const TABS: Tab[] = ['editor', 'dry-run', 'tests', 'versions', 'replay'];

export function mountPolicy(): void {
  const host = maybe('detail-body');
  if (!host) return;
  const id = location.pathname.split('/').filter(Boolean).pop() ?? '';
  const sp = new URLSearchParams(location.search);
  let tab: Tab = (TABS as string[]).includes(sp.get('tab') ?? '') ? (sp.get('tab') as Tab) : 'editor';
  let p: Policy;
  let versions: PolicyVersion[] = [];
  let shown: PolicyVersion; // the version the editor opened
  let base = ''; // its source
  let buffer = '';
  let linted: LintedEditor | null = null;
  const sources = new Map<number, string>();
  const panels: Record<Tab, HTMLElement> = Object.fromEntries(TABS.map((t) => [t, h('section', { class: 'tabpanel', role: 'tabpanel', id: `pp-${t}`, 'aria-labelledby': `pt-${t}`, tabindex: 0 })])) as Record<Tab, HTMLElement>;
  const headHost = h('div');
  const tabsEl = h('div', { class: 'tabs', role: 'tablist', 'aria-label': c.tabsLabel });

  const isDirty = () => dirty(buffer, base);
  const sourceOf = async (v: number): Promise<string> => {
    const have = sources.get(v);
    if (have !== undefined) return have;
    const x = await api<PolicyVersion>('GET', `/v1/policies/${id}/versions/${v}`);
    sources.set(v, x.source ?? '');
    return x.source ?? '';
  };
  const setURL = (extra: Record<string, string | null> = {}) => {
    const q = new URLSearchParams(location.search);
    q.set('tab', tab);
    for (const [k, v] of Object.entries(extra)) if (v === null) q.delete(k); else q.set(k, v);
    history.replaceState(null, '', `${location.pathname}?${q}`);
  };

  // ---------- header ----------
  const drawHead = async () => {
    const admin = isAdmin();
    const latest = versions.find((v) => v.version === p.latest_version) ?? shown;
    const saveBtn = admin && isDirty() ? h('button', { type: 'button', class: 'btn btn-primary btn-sm', 'data-save': '', 'aria-keyshortcuts': 'Meta+S Control+S', onclick: () => void save() }, fill(c.saveAs, { v: p.latest_version + 1 })) : null;
    const canShadow = admin && shown.version !== p.active_version && shown.version !== p.shadow_version;
    const canEnforce = admin && shown.version !== p.active_version;
    const evidence = h('p', { class: 'pp-evidence t-footnote', hidden: true });
    replace(headHost, h('header', { class: 'detail-head pp-head' },
      h('a', { class: 'btn btn-quiet btn-sm -ml-3', href: '/app/policies' }, icon('chevron-left', 16), copy.pages.policies.title),
      h('div', { class: 'pp-title' },
        h('h1', { class: 't-title-2 mono', id: 'page-title', translate: 'no' }, p.name), statusChipFor(statusOf(p)),
        h('span', { class: 'pp-actions' }, saveBtn,
          canShadow ? h('button', { type: 'button', class: 'btn btn-secondary btn-sm', onclick: () => activate('shadow') }, icon('contrast', 16), c.runShadow) : null,
          admin && p.shadow_version ? h('button', { type: 'button', class: 'btn btn-quiet btn-sm', onclick: () => openStopShadow(p, p.shadow_version!, refresh) }, c.shadow.stop) : null,
          canEnforce ? h('button', { type: 'button', class: 'btn btn-secondary btn-sm', onclick: () => activate('enforce') }, icon('gate', 16), c.enforceBtn) : null)),
      h('p', { class: 'pp-meta t-2' }, h('span', { class: 'mono', translate: 'no' }, headerLine(shown)), ` · ${fill(c.by, { who: authorName(shown) })} · `, relTime(shown.created_at, tz()),
        shown.notes ? h('span', null, ` · ${shown.notes}`) : null,
        latest && latest.version !== shown.version ? h('span', null, ' · ', h('a', { href: `/app/policies/${id}?tab=editor&v=${latest.version}` }, `v${latest.version}`)) : null),
      evidence));
    if (p.shadow_version) void loadEvidence(evidence);
  };
  const loadEvidence = async (el: HTMLElement) => {
    try {
      const from = new Date(Date.now() - 86_400_000).toISOString();
      const [diff, ov] = await Promise.all([
        api<ListResponse<unknown>>('GET', '/v1/actions', { query: { shadow_differs: 'true', 'created_at[gte]': from, limit: '100' } }),
        api<Overview>('GET', '/v1/overview', { query: { window: '24h' } }).catch(() => null),
      ]);
      const total = (ov as unknown as { actions?: { total?: number } } | null)?.actions?.total ?? diff.data.length;
      replace(el, icon('contrast', 16), shadowEvidence(diff.data.length, total, diff.has_more), ' · ', h('a', { href: '/app/actions?shadow_differs=true&time=24h' }, c.shadowView));
      el.hidden = false;
    } catch {
      el.hidden = true;
    }
  };
  const activate = (mode: 'enforce' | 'shadow', v: PolicyVersion = shown) => {
    void sourceOf(v.version).then((src) => openActivate({ policy: p, version: v, mode, defaultAllow: hasDefaultAllow(v, src), onDone: refresh }));
  };

  // ---------- editor ----------
  let saving = false;
  const save = async () => {
    if (!isAdmin() || !isDirty() || saving) return;
    saving = true;
    const btn = headHost.querySelector<HTMLButtonElement>('[data-save]');
    if (btn) { btn.setAttribute('aria-busy', 'true'); btn.textContent = c.saving; }
    const before = p.latest_version;
    try {
      const v = await api<PolicyVersion>('POST', `/v1/policies/${id}/versions`, { body: { source: buffer } });
      if (v.version <= before) {
        toast(fill(c.noChanges, { v: v.version }));
      } else {
        toast(fill(c.saved, { v: v.version }), { kind: 'success' });
      }
      sources.set(v.version, buffer);
      await refresh(v.version);
    } catch (e) {
      apiErrorToast(e);
      void drawHead();
    } finally {
      saving = false;
    }
  };
  const drawEditor = () => {
    const panel = panels.editor;
    linted?.destroy();
    panel.replaceChildren();
    linted = mountLintedEditor(panel, {
      value: buffer,
      readOnly: !isAdmin(),
      name: `${p.name}.yaml`,
      label: `${p.name} v${shown.version}`,
      onChange: (v) => {
        const was = isDirty();
        buffer = v;
        if (was !== isDirty()) void drawHead();
      },
      onSave: () => void save(),
    });
    const line = Number(new URLSearchParams(location.search).get('line'));
    if (line > 0) requestAnimationFrame(() => linted?.editor.revealLine(line, true));
  };
  const reveal = (line: number) => {
    selectTab('editor');
    setURL({ line: String(line) });
    requestAnimationFrame(() => linted?.editor.revealLine(line, true));
  };

  // ---------- dry run ----------
  let tools: { name: string; type: string }[] = [];
  let agents: Agent[] = [];
  const drawDryRun = () => {
    const panel = panels['dry-run'];
    const q = new URLSearchParams(location.search);
    const tool = h('input', { class: 'input mono', id: 'dr-tool', name: 'tool', list: 'dr-tools', autocomplete: 'off', spellcheck: 'false', value: q.get('tool') ?? '', 'aria-describedby': 'dr-tool-h dr-tool-e' });
    const dl = h('datalist', { id: 'dr-tools' }, ...tools.map((t) => h('option', { value: t.name })));
    const sql = h('textarea', { class: 'textarea mono', id: 'dr-sql', name: 'sql', rows: 3, spellcheck: 'false', autocomplete: 'off', 'aria-describedby': 'dr-sql-h' });
    const args = h('textarea', { class: 'textarea mono', id: 'dr-args', name: 'args', rows: 5, spellcheck: 'false', autocomplete: 'off', placeholder: '{"charge": "ch_3QxZ8f2L", "amount": 12000}…', 'aria-describedby': 'dr-args-e' });
    const argsErr = h('p', { class: 'error', id: 'dr-args-e', role: 'alert', hidden: true });
    const toolErr = h('p', { class: 'error', id: 'dr-tool-e', role: 'alert', hidden: true });
    const agent = h('select', { class: 'select', id: 'dr-agent', name: 'agent' }, h('option', { value: '' }, c.dryRun.agentAny), ...agents.map((a) => h('option', { value: a.name }, a.name)));
    const obo = h('input', { class: 'input mono', id: 'dr-obo', name: 'on_behalf_of', autocomplete: 'off', spellcheck: 'false' });
    const at = h('input', { class: 'input', type: 'datetime-local', id: 'dr-at', name: 'time', autocomplete: 'off' });
    const sqlBox = h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-sql' }, c.dryRun.sql), sql, h('p', { class: 'help', id: 'dr-sql-h' }, c.dryRun.sqlHint));
    const showSQL = () => { sqlBox.hidden = !/^postgres\./.test(tool.value.trim()); };
    tool.addEventListener('input', showSQL);
    showSQL();
    let against: 'draft' | 'enforced' | 'shown' = 'draft';
    const opts = [{ value: 'draft' as const, label: c.dryRun.draft }, { value: 'enforced' as const, label: c.dryRun.enforced }, ...(shown.version !== p.active_version ? [{ value: 'shown' as const, label: fill(c.dryRun.version, { v: shown.version }) }] : [])];
    const seg = segmented(c.dryRun.against, opts, against, (v) => (against = v), 'dr-against');
    const result = h('div', { class: 'dr-result', 'aria-live': 'polite' }, h('p', { class: 't-2' }, c.dryRun.empty));
    const run = h('button', { type: 'button', class: 'btn btn-primary' }, icon('play', 16), c.dryRun.run);
    run.addEventListener('click', async () => {
      argsErr.hidden = true;
      toolErr.hidden = true;
      tool.removeAttribute('aria-invalid');
      args.removeAttribute('aria-invalid');
      const ty = tools.find((t) => t.name === tool.value.trim())?.type ?? (tool.value.startsWith('postgres.') ? 'postgres' : undefined);
      const parsed = dryRunInput({ tool: tool.value, argsText: args.value, sql: sqlBox.hidden ? '' : sql.value, agent: agent.value, onBehalfOf: obo.value, at: at.value }, ty);
      if ('error' in parsed) {
        const onTool = !tool.value.trim();
        const box = onTool ? toolErr : argsErr;
        replace(box, icon('circle-alert', 16), parsed.error);
        box.hidden = false;
        (onTool ? tool : args).setAttribute('aria-invalid', 'true');
        (onTool ? tool : args).focus();
        return;
      }
      const policy = against === 'draft' ? { source: buffer } : against === 'enforced' ? 'active' : { id, version: shown.version };
      run.disabled = true;
      try {
        const d = await api<PolicyDecision>('POST', '/v1/policies/evaluate', { body: { policy, input: parsed.input, explain: true } });
        replace(result, traceView(d as never, { onReveal: (line: number) => reveal(line) }));
      } catch (e) {
        replace(result, h('p', { class: 'error', role: 'alert' }, icon('circle-alert', 16), e instanceof Error ? e.message : String(e)));
      } finally {
        run.disabled = false;
      }
    });
    replace(panel, h('div', { class: 'pp-split' },
      h('div', { class: 'card card-pad grid gap-4 content-start' },
        h('p', { class: 't-2 m-0' }, c.dryRun.lede),
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-tool' }, c.dryRun.tool), tool, dl, h('p', { class: 'help', id: 'dr-tool-h' }, c.dryRun.toolHint), toolErr),
        sqlBox,
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-args' }, c.dryRun.args), args, argsErr),
        h('div', { class: 'field-row' },
          h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-agent' }, c.dryRun.agent), h('span', { class: 'select-wrap' }, agent, icon('chevron-down', 16))),
          h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-obo' }, c.dryRun.onBehalfOf), obo)),
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'dr-at' }, `${c.dryRun.at} · ${Intl.DateTimeFormat().resolvedOptions().timeZone}`), at),
        h('div', { class: 'field' }, h('span', { class: 'label', id: 'dr-against-l' }, c.dryRun.against), seg),
        h('div', null, run)),
      result));
  };

  // ---------- tests ----------
  const drawTests = () => {
    const panel = panels.tests;
    const cases = h('textarea', { class: 'textarea mono', id: 'ts-cases', name: 'cases', rows: 12, spellcheck: 'false', autocomplete: 'off', 'aria-describedby': 'ts-e' });
    const file = h('input', { type: 'file', id: 'ts-file', name: 'cases_file', accept: '.yaml,.yml,text/yaml', class: 'file-input' });
    const err = h('p', { class: 'error', id: 'ts-e', hidden: true });
    const out = h('div', { 'aria-live': 'polite' }, h('p', { class: 't-2' }, c.tests.empty));
    file.addEventListener('change', () => {
      const f = file.files?.[0];
      if (!f) return;
      if (f.size > 1024 * 1024) { replace(err, icon('circle-alert', 16), c.tests.tooBig); err.hidden = false; file.value = ''; return; }
      err.hidden = true;
      const r = new FileReader();
      r.onload = () => (cases.value = String(r.result ?? ''));
      r.readAsText(f);
    });
    const run = h('button', { type: 'button', class: 'btn btn-primary' }, icon('play', 16), c.tests.run);
    run.addEventListener('click', async () => {
      err.hidden = true;
      if (new TextEncoder().encode(cases.value).length > 1024 * 1024) { replace(err, icon('circle-alert', 16), c.tests.tooBig); err.hidden = false; return; }
      run.disabled = true;
      try {
        const r = await api<TestResult>('POST', '/v1/policies/test', { body: { source: buffer, cases: cases.value } });
        replace(out, testResultView(r as never));
      } catch (e) {
        replace(err, icon('circle-alert', 16), e instanceof Error ? e.message : String(e));
        err.hidden = false;
      } finally {
        run.disabled = false;
      }
    });
    replace(panel, h('div', { class: 'grid gap-4' },
      h('div', { class: 'card card-pad grid gap-3' },
        h('p', { class: 't-2 m-0' }, c.tests.lede),
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'ts-cases' }, c.tests.paste), cases, err),
        h('div', { class: 'flex flex-wrap items-center gap-3' }, h('label', { class: 'btn btn-secondary btn-sm', for: 'ts-file' }, icon('upload', 16), c.tests.choose), file, run)),
      out));
  };

  // ---------- versions ----------
  const drawVersions = () => {
    const panel = panels.versions;
    const picked: number[] = [];
    const diffHost = h('div', null, h('p', { class: 't-2' }, c.versions.pickTwo));
    const compare = h('button', { type: 'button', class: 'btn btn-secondary btn-sm', disabled: true }, icon('git-compare', 16), c.versions.compare);
    const doCompare = async () => {
      if (picked.length !== 2) return;
      const [a, b] = [...picked].sort((x, y) => x - y) as [number, number];
      const [sa, sb] = await Promise.all([sourceOf(a), sourceOf(b)]);
      const d = diffLines(sa, sb);
      replace(diffHost, h('h3', { class: 't-headline' }, fill(c.versions.diffTitle, { a, b })), d.hunks.length ? lineDiffView(d, { from: `v${a}`, to: `v${b}` }) : h('p', { class: 't-2' }, c.versions.same));
    };
    compare.addEventListener('click', () => void doCompare());
    const admin = isAdmin();
    const t = h('table', { class: 'dt cards pv-table' },
      h('thead', null, h('tr', null, h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, c.versions.compare)), ...[c.versions.version, c.versions.status, c.versions.hash, c.versions.author, c.versions.created, c.versions.notes].map((l) => h('th', { scope: 'col' }, l)), admin ? h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, c.versions.actions)) : null)),
      h('tbody', null, ...versions.map((v) => {
        const box = h('input', { type: 'checkbox', name: 'compare', value: String(v.version), 'aria-label': `${c.versions.compare} v${v.version}` });
        box.addEventListener('change', () => {
          if (box.checked) { picked.push(v.version); if (picked.length > 2) { const drop = picked.shift()!; const other = t.querySelector<HTMLInputElement>(`input[value="${drop}"]`); if (other) other.checked = false; } }
          else picked.splice(picked.indexOf(v.version), 1);
          compare.disabled = picked.length !== 2;
        });
        const st = statusOf(v);
        const older = p.active_version !== null && v.version < p.active_version;
        return h('tr', null,
          h('td', { 'data-card': 'hide' }, box),
          h('td', { class: 'num', 'data-label': c.versions.version }, h('a', { href: `/app/policies/${id}?tab=editor&v=${v.version}` }, `v${v.version}`)),
          h('td', { 'data-label': c.versions.status }, statusChipFor(st)),
          h('td', { 'data-label': c.versions.hash, 'data-card': 'label' }, h('span', { class: 'mono', translate: 'no', title: v.version_hash }, shortVersionHash(v.version_hash))),
          h('td', { 'data-label': c.versions.author, 'data-card': 'label' }, authorName(v)),
          h('td', { 'data-label': c.versions.created, 'data-card': 'label' }, h('span', { class: 'num', title: dateTime(v.created_at, tz()) }, relTime(v.created_at, tz()))),
          h('td', { 'data-label': c.versions.notes, 'data-card': 'label' }, v.notes ?? h('span', { class: 't-2' }, '·')),
          admin ? h('td', { class: 'pv-actions' }, st === 'enforced' ? null : h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'aria-label': fill(older ? c.enforce.rollback : c.enforce.button, { v: v.version }), onclick: () => activate('enforce', v) }, older ? fill(c.enforce.rollback, { v: v.version }) : c.enforceBtn),
            st === 'inactive' ? h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'aria-label': fill(c.shadow.button, { v: v.version }), onclick: () => activate('shadow', v) }, c.runShadow) : null) : null);
      })));
    replace(panel, h('div', { class: 'grid gap-4' }, h('div', { class: 'table-frame' }, t), h('div', null, compare), diffHost));
    if (versions.length >= 2) {
      picked.push(versions[1]!.version, versions[0]!.version);
      for (const v of picked) { const b = t.querySelector<HTMLInputElement>(`input[value="${v}"]`); if (b) b.checked = true; }
      compare.disabled = false;
      void doCompare(); // the latest two, compared on open
    }
  };

  // ---------- replay (S27 §3.4: controllers/replay.ts) ----------
  let replayTab: ReplayTab | null = null;
  const drawReplay = () => {
    replayTab = mountReplay(panels.replay, {
      policyId: p.id, name: () => p.name, version: () => shown.version, versions: () => versions.map((v) => v.version), admin: isAdmin, tz, announce,
    });
  };

  // ---------- tabs and loading ----------
  const drawTab = (t: Tab) => {
    if (t === 'editor') { if (!linted) drawEditor(); }
    else if (t === 'dry-run') drawDryRun();
    else if (t === 'tests') drawTests();
    else if (t === 'versions') drawVersions();
    else drawReplay();
  };
  const selectTab = (t: Tab) => {
    tab = t;
    for (const b of tabsEl.querySelectorAll<HTMLElement>('[role=tab]')) {
      const on = b.dataset.tab === t;
      b.setAttribute('aria-selected', on ? 'true' : 'false');
      b.tabIndex = on ? 0 : -1;
    }
    for (const k of TABS) panels[k].hidden = k !== t;
    if (t === 'editor' ? !linted : panels[t].childElementCount === 0) drawTab(t);
    setURL(t === 'editor' ? {} : { line: null });
  };
  const drawTabs = () => {
    replace(tabsEl, ...TABS.map((t) => {
      const b = h('button', { type: 'button', role: 'tab', id: `pt-${t}`, 'aria-controls': `pp-${t}`, 'data-tab': t, 'aria-selected': t === tab ? 'true' : 'false', tabindex: t === tab ? 0 : -1 }, c.tabs[t] ?? t);
      b.addEventListener('click', () => selectTab(t));
      return b;
    }));
    tabsEl.addEventListener('keydown', (e) => {
      const i = TABS.indexOf(tab);
      const n = e.key === 'ArrowRight' ? TABS[(i + 1) % TABS.length] : e.key === 'ArrowLeft' ? TABS[(i - 1 + TABS.length) % TABS.length] : e.key === 'Home' ? TABS[0] : e.key === 'End' ? TABS[TABS.length - 1] : null;
      if (!n) return;
      e.preventDefault();
      selectTab(n);
      tabsEl.querySelector<HTMLElement>(`[data-tab="${n}"]`)?.focus();
    });
  };

  const refresh = async (openVersion?: number) => {
    p = await api<Policy>('GET', `/v1/policies/${id}`);
    versions = (await api<ListResponse<PolicyVersion>>('GET', `/v1/policies/${id}/versions`, { query: { limit: '100' } })).data.sort((a, b) => b.version - a.version);
    const want = openVersion ?? shown?.version;
    shown = versions.find((v) => v.version === want) ?? shown;
    if (openVersion !== undefined) {
      base = await sourceOf(shown.version);
      buffer = base;
      linted?.destroy();
      linted = null;
      if (tab === 'editor') drawEditor();
    }
    await drawHead();
    if (panels.versions.childElementCount) drawVersions(); // data changed; the other tabs keep what you typed
    replayTab?.update();
  };

  // dirty guard: in-app links and the browser's own navigation
  addEventListener('beforeunload', (e) => {
    if (isDirty()) e.preventDefault();
  });
  document.addEventListener('click', (e) => {
    if (!isDirty() || e.defaultPrevented || e.metaKey || e.ctrlKey || e.shiftKey) return;
    const a = (e.target as HTMLElement).closest?.('a[href]') as HTMLAnchorElement | null;
    if (!a || a.target || a.closest('#detail-body .tabpanel .dr-result')) return;
    const url = new URL(a.href, location.href);
    if (url.origin !== location.origin || (url.pathname === location.pathname && url.search === location.search)) return;
    e.preventDefault();
    const discard = h('button', { type: 'button', class: 'btn btn-danger-outline' }, c.discard);
    const keep = h('button', { type: 'button', class: 'btn btn-secondary' }, c.keepEditing);
    const ov = openSheet({ kind: 'dialog', role: 'alertdialog', title: fill(c.discardTitle, { name: p.name }), body: [h('p', null, c.discardBody)], foot: [keep, discard], initialFocus: () => keep });
    keep.addEventListener('click', ov.close);
    discard.addEventListener('click', () => { buffer = base; ov.close(); location.assign(url.href); });
  }, true);

  document.addEventListener('dg:ready', async () => {
    try {
      [p] = await Promise.all([api<Policy>('GET', `/v1/policies/${id}`), loadPeople()]);
      versions = (await api<ListResponse<PolicyVersion>>('GET', `/v1/policies/${id}/versions`, { query: { limit: '100' } })).data.sort((a, b) => b.version - a.version);
      const want = Number(sp.get('v')) || p.latest_version;
      shown = versions.find((v) => v.version === want) ?? versions[0]!;
      base = await sourceOf(shown.version);
      buffer = base;
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) host.replaceChildren(notFound(c.notFound));
      else host.replaceChildren(errorState(e, () => location.reload()));
      host.setAttribute('aria-busy', 'false');
      return;
    }
    drawTabs();
    for (const k of TABS) panels[k].hidden = k !== tab;
    replace(host, headHost, tabsEl, ...TABS.map((k) => panels[k]));
    host.setAttribute('aria-busy', 'false');
    // on a phone the tab strip scrolls: bring a deep-linked tab (?tab=replay) into view
    tabsEl.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    await drawHead();
    drawTab(tab);
    void Promise.all([
      api<ListResponse<Connector>>('GET', '/v1/connectors', { query: { limit: '100' } }).then((r) => {
        tools = r.data.flatMap((x) => (x.tools ?? []).map((t) => ({ name: t.name, type: x.type })));
      }).catch(() => {}),
      api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100' } }).then((r) => { agents = r.data.filter((a) => a.name !== 'console'); }).catch(() => {}),
    ]).then(() => {
      // fill the suggestions in place; never redraw a form someone may be typing in
      const dl = maybe('dr-tools');
      if (dl) replace(dl, ...tools.map((t) => h('option', { value: t.name })));
      const ag = maybe<HTMLSelectElement>('dr-agent');
      if (ag && ag.options.length <= 1) ag.append(...agents.map((a) => h('option', { value: a.name }, a.name)));
    });
    let tm = 0;
    stream().on('policy.*', () => { clearTimeout(tm); tm = window.setTimeout(() => void refresh(), 400); });
  }, { once: true });
}

export { sourceBytes, MAX_SOURCE_BYTES };
