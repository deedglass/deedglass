// connectors.ts: Connectors (S26 §3.3): the list, the three-step add flow (/app/connectors/new,
// each step its own URL state), and a connector's page: credentials (write-only), test results,
// tools, and for Postgres the readiness panel and the Witness card. Nothing here ever renders a
// credential value: SecretField keeps it only in its input and withSecrets clears it after the PUT.

import { api, ApiError } from '../lib/api.ts';
import { idChip, relTime } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { connectorsCopy as c } from '../lib/copy-connectors.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { dateTime, number } from '../lib/format.ts';
import {
  credentialLine, healthLabel, healthOf, levelsOf, sideEffectClass, typeIcon, witnessLine, witnessState, bytes,
  type Connector, type ConnectorTest, type ConnectorType, type DecoderView,
} from '../lib/models/connector.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { ListResponse } from '../lib/types.ts';
import { checkList, readinessLine, testSummary } from './checklist.ts';
import { codeBlock, kv } from './components.ts';
import { jsonView } from './jsonview.ts';
import { confirmTyped, openSheet } from './overlay.ts';
import { schemaForm } from './schemaform.ts';
import { emptyState, errorState, notFound, permissionState, skeleton } from './screens.ts';
import { secretField, withSecrets, type SecretField } from './secretfield.ts';
import { wireTable } from './table.ts';
import { apiErrorToast, toast } from './toast.ts';

const NAME_RE = /^[a-z][a-z0-9-]{0,62}$/;
const tz = () => get()?.org.timezone ?? 'UTC';
const isAdmin = () => atLeast(get()?.me.membership.role, 'admin');

function levelBadges(levels: string[]): HTMLElement {
  return h('span', { class: 'lvl-badges' }, ...levels.map((l) => h('span', { class: 'lvl-badge', title: l }, copy.chips.level[l as 'L0'] ?? l)));
}

function healthCell(x: Connector, id?: string): HTMLElement {
  const hl = healthLabel(healthOf(x));
  return h('span', { class: `health health-${healthOf(x)}`, ...(id ? { id } : {}) }, h('span', { class: 'health-mark', 'aria-hidden': 'true' }, hl.glyph), hl.text);
}

async function types(): Promise<ConnectorType[]> {
  return (await api<ListResponse<ConnectorType>>('GET', '/v1/connector-types')).data;
}

// ---------- the list ----------

export function mountConnectors(): void {
  const host = maybe('screen-body');
  const actions = maybe('connectors-actions');
  if (!host) return;
  const load = async (quiet = false) => {
    if (!quiet) host.replaceChildren(skeleton(4));
    try {
      const [list, ts] = await Promise.all([api<ListResponse<Connector>>('GET', '/v1/connectors', { query: { limit: '100' } }), types().catch(() => [] as ConnectorType[])]);
      const titles = new Map(ts.map((t) => [t.type, t]));
      if (actions) replace(actions, isAdmin() ? h('a', { class: 'btn btn-primary', href: '/app/connectors/new' }, icon('plus', 16), c.add) : null);
      if (!list.data.length) {
        host.replaceChildren(emptyState(copy.connectors.empty, 'plug', isAdmin() ? h('a', { class: 'btn btn-primary', href: '/app/connectors/new' }, icon('plus', 16), c.add) : undefined, 'https://docs.deedglass.com/connectors'));
        return;
      }
      const t = h('table', { class: 'dt cards con-table' },
        h('thead', null, h('tr', null, ...[c.columns.name, c.columns.type, c.columns.levels, c.columns.health, c.columns.credentials, c.columns.tools].map((l, i) => h('th', { scope: 'col', class: i === 5 ? 'num' : '' }, l)))),
        h('tbody', null, ...list.data.map((x) => {
          const ty = titles.get(x.type);
          return h('tr', { 'data-row': '' },
            h('td', { 'data-label': c.columns.name }, h('a', { class: 'rowlink mono', href: `/app/connectors/${x.id}`, translate: 'no' }, x.name)),
            h('td', { 'data-label': c.columns.type }, h('span', { class: 'con-type' }, icon(typeIcon(x.type), 16), ty?.title ?? x.type)),
            h('td', { 'data-label': c.columns.levels, 'data-card': 'label' }, levelBadges(levelsOf(x, ty?.levels))),
            h('td', { 'data-label': c.columns.health }, healthCell(x)),
            h('td', { 'data-label': c.columns.credentials, 'data-card': 'label' }, h('span', { class: x.credentials?.set ? '' : 't-2' }, credentialLine(x.credentials))),
            h('td', { class: 'num', 'data-label': c.columns.tools, 'data-card': 'hide' }, number(x.tools?.length ?? 0)));
        })));
      wireTable(t as HTMLTableElement);
      host.replaceChildren(h('div', { class: 'table-frame' }, t));
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => {
    void load();
    let tm = 0;
    stream().on('decoder.*', () => { clearTimeout(tm); tm = window.setTimeout(() => void load(true), 400); });
  }, { once: true });
}

// ---------- shared: the credential form ----------

function credentialForm(type: ConnectorType | undefined, current: string | null, idp: string): { el: HTMLElement; kind: () => string; fields: () => Record<string, SecretField> } {
  const kinds = type?.credential_kinds ?? [];
  let kind = current && kinds.some((k) => k.kind === current) ? current : (kinds[0]?.kind ?? '');
  const holder = h('div', { class: 'grid gap-3' });
  let fields: Record<string, SecretField> = {};
  const draw = () => {
    fields = {};
    const k = kinds.find((x) => x.kind === kind);
    for (const f of k?.fields ?? []) fields[f.name] = f.secret ? secretField({ id: `${idp}-${f.name}`, label: f.name, required: f.name !== 'witness_dsn' }) : plainField(`${idp}-${f.name}`, f.name);
    replace(holder, ...Object.values(fields).map((f) => f.el));
  };
  const parts: Child[] = [];
  if (kinds.length > 1) {
    const sel = h('select', { class: 'select', id: `${idp}-kind`, name: 'kind' }, ...kinds.map((k) => h('option', { value: k.kind, ...(k.kind === kind ? { selected: true } : {}) }, k.kind)));
    sel.addEventListener('change', () => { kind = sel.value; draw(); });
    parts.push(h('div', { class: 'field' }, h('label', { class: 'label', for: `${idp}-kind` }, c.credentials.kind), h('span', { class: 'select-wrap' }, sel, icon('chevron-down', 16))));
  }
  draw();
  const el = h('div', { class: 'grid gap-3' }, ...parts, holder, h('p', { class: 't-footnote t-2' }, c.credentials.writeOnly));
  return { el, kind: () => kind, fields: () => fields };
}

async function saveCredentials(id: string, kind: string, fields: Record<string, SecretField>): Promise<Connector> {
  return withSecrets(fields, async (values) => {
    const secret: Record<string, string> = {};
    for (const [k, v] of Object.entries(values)) if (v) secret[k] = v;
    return api<Connector>('PUT', `/v1/connectors/${id}/credentials`, { body: { kind, secret } });
  });
}

/** plainField: a non-secret credential field (a basic-auth username) behind SecretField's shape. */
function plainField(id: string, label: string): SecretField {
  const input = h('input', { class: 'input mono', id, name: label, autocomplete: 'off', spellcheck: 'false', 'aria-describedby': `${id}-e` });
  const err = h('p', { class: 'error', id: `${id}-e`, hidden: true });
  const el = h('div', { class: 'field' }, h('label', { class: 'label', for: id }, label), input, err);
  return {
    el,
    value: () => input.value,
    clear: () => { input.value = ''; },
    hide: () => {},
    focus: () => input.focus(),
    setError: (m) => { err.hidden = !m; replace(err, ...(m ? [icon('circle-alert', 16), m] : [])); input.setAttribute('aria-invalid', m ? 'true' : 'false'); },
  };
}

// ---------- the add flow ----------

type Step = 'type' | 'configure' | 'credentials';

export function mountConnectorNew(): void {
  const host = maybe('new-body');
  const stepsHost = maybe('new-steps');
  if (!host || !stepsHost) return;
  let all: ConnectorType[] = [];
  const params = () => new URLSearchParams(location.search);
  const go = (step: Step, extra: Record<string, string> = {}) => {
    const q = new URLSearchParams({ step, ...extra });
    history.pushState(null, '', `/app/connectors/new?${q}`);
    moved = true;
    void render();
  };
  const drawSteps = (step: Step) => {
    const order: Step[] = ['type', 'configure', 'credentials'];
    const n = order.indexOf(step);
    replace(stepsHost, h('p', { class: 't-footnote t-2' }, fill(c.stepOf, { n: n + 1 })),
      h('ol', { class: 'steps' }, ...order.map((s, i) => h('li', { class: i < n ? 'done' : i === n ? 'current' : '', ...(i === n ? { 'aria-current': 'step' } : {}) },
        h('span', { class: 'step-n', 'aria-hidden': 'true' }, i < n ? '✓' : String(i + 1)), c.steps[s]))));
  };
  const render = async () => {
    const p = params();
    const step = (p.get('step') as Step) || 'type';
    drawSteps(step);
    if (!isAdmin()) {
      host.replaceChildren(permissionState('Admin'));
      return;
    }
    if (step === 'type') renderType();
    else if (step === 'configure') renderConfigure(p.get('type') ?? '');
    else await renderCredentials(p.get('id') ?? '');
    if (moved && !host.contains(document.activeElement)) host.focus({ preventScroll: false }); // the clicked control is gone; start the step
    moved = false;
  };
  let moved = false;
  const renderType = () => {
    if (!all.length) {
      host.replaceChildren(emptyState(c.typesEmpty, 'plug'));
      return;
    }
    host.replaceChildren(h('h2', { class: 't-headline mb-3', id: 'pick-t' }, c.pickType),
      h('ul', { class: 'type-cards', 'aria-labelledby': 'pick-t' }, ...[...all].sort((a, b) => b.levels.length - a.levels.length || a.title.localeCompare(b.title)).map((t) => h('li', null,
        h('a', {
          class: 'type-card', href: `/app/connectors/new?step=configure&type=${encodeURIComponent(t.type)}`, 'data-type': t.type,
          onclick: (ev: Event) => { const e = ev as MouseEvent; if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return; e.preventDefault(); go('configure', { type: t.type }); },
        },
        h('span', { class: 'type-card-h' }, h('span', { class: 'type-icon' }, icon(typeIcon(t.type), 20)), h('span', { class: 't-headline' }, t.title), levelBadges(t.levels)),
        h('span', { class: 'type-card-d t-2' }, t.description))))));
  };
  const renderConfigure = (type: string) => {
    const t = all.find((x) => x.type === type);
    if (!t) return go('type');
    const name = h('input', { class: 'input mono', id: 'cn-name', name: 'name', autocomplete: 'off', spellcheck: 'false', maxlength: 63, required: true, 'aria-describedby': 'cn-name-h cn-name-e' });
    const desc = h('input', { class: 'input', id: 'cn-desc', name: 'description', autocomplete: 'off', maxlength: 280 });
    const nameErr = h('p', { class: 'error', id: 'cn-name-e', hidden: true });
    const form = schemaForm(t.config_schema as never, { idPrefix: 'cfg' });
    const create = h('button', { type: 'button', class: 'btn btn-primary' }, c.create);
    const showName = (m: string | null) => {
      nameErr.hidden = !m;
      replace(nameErr, ...(m ? [icon('circle-alert', 16), m] : []));
      name.setAttribute('aria-invalid', m ? 'true' : 'false');
    };
    create.addEventListener('click', async () => {
      form.clearErrors();
      showName(null);
      if (!NAME_RE.test(name.value)) { showName(c.nameHint); name.focus(); return; }
      if (!form.validate()) return;
      create.disabled = true;
      create.setAttribute('aria-busy', 'true');
      create.textContent = c.creating;
      try {
        const x = await api<Connector & { deed?: string }>('POST', '/v1/connectors', { body: { type, name: name.value, description: desc.value.trim(), config: form.values() } });
        toast(fill(c.created, { name: x.name }), { kind: 'success' });
        go('credentials', { id: x.id });
      } catch (e) {
        create.disabled = false;
        create.removeAttribute('aria-busy');
        create.textContent = c.create;
        if (e instanceof ApiError && e.param) {
          if (e.param === 'name') { showName(e.message); name.focus(); return; }
          form.setErrors([{ param: e.param, message: e.message }]);
          return;
        }
        apiErrorToast(e);
      }
    });
    host.replaceChildren(h('div', { class: 'card card-pad grid gap-4 cn-form' },
      h('p', { class: 'con-type' }, icon(typeIcon(t.type), 16), t.title, t.docs_url ? h('a', { class: 'ml-auto', href: t.docs_url }, c.docs, icon('arrow-up-right', 16)) : null),
      h('div', { class: 'field' }, h('label', { class: 'label', for: 'cn-name' }, c.name), name, h('p', { class: 'help', id: 'cn-name-h' }, c.nameHint), nameErr),
      h('div', { class: 'field' }, h('label', { class: 'label', for: 'cn-desc' }, c.description), desc),
      h('h2', { class: 'apv-sec-t' }, c.config), form.el,
      h('div', { class: 'flex gap-2 justify-end' }, h('a', { class: 'btn btn-secondary', href: '/app/connectors/new?step=type', onclick: (ev: Event) => { const e = ev as MouseEvent; if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return; e.preventDefault(); go('type'); } }, c.back), create)));
    if (matchMedia('(pointer: fine)').matches) name.focus(); // no keyboard popping up on a phone
  };
  const renderCredentials = async (id: string) => {
    host.replaceChildren(skeleton(3));
    let x: Connector;
    try {
      x = await api<Connector>('GET', `/v1/connectors/${id}`);
    } catch (e) {
      host.replaceChildren(errorState(e, () => void render()));
      return;
    }
    const t = all.find((y) => y.type === x.type);
    const cf = credentialForm(t, x.credentials?.kind ?? null, 'cr');
    const save = h('button', { type: 'button', class: 'btn btn-primary' }, c.credentials.save);
    const result = h('div', { class: 'grid gap-3', 'aria-live': 'polite' });
    save.addEventListener('click', async () => {
      save.disabled = true;
      save.textContent = c.saving;
      try {
        await saveCredentials(id, cf.kind(), cf.fields());
        toast(fill(c.credentials.saved, { name: x.name }), { kind: 'success' });
        replace(result, h('p', { class: 't-2' }, c.test.running));
        const test = await api<ConnectorTest>('POST', `/v1/connectors/${id}/test`, { body: {} });
        replace(result, h('p', { class: `cl-summary cl-summary-${test.status}` }, testSummary(test)), checkList(test, { idPrefix: 'new-cl' }),
          h('a', { class: 'btn btn-primary justify-self-start', href: `/app/connectors/${id}` }, fill('Open {name}', { name: x.name })));
      } catch (e) {
        save.disabled = false;
        save.textContent = c.credentials.save;
        if (e instanceof ApiError && e.param) {
          const f = cf.fields()[e.param.replace(/^secret\./, '')];
          if (f) { f.setError(e.message); f.focus(); } else apiErrorToast(e);
          return;
        }
        apiErrorToast(e);
      }
    });
    host.replaceChildren(h('div', { class: 'card card-pad grid gap-4 cn-form' },
      h('p', { class: 'con-type' }, icon(typeIcon(x.type), 16), h('span', { class: 'mono', translate: 'no' }, x.name)),
      cf.el,
      h('div', { class: 'flex gap-2 justify-end flex-wrap' }, h('a', { class: 'btn btn-secondary', href: `/app/connectors/${id}` }, c.skipCredentials), save),
      result));
  };
  addEventListener('popstate', () => void render());
  document.addEventListener('dg:ready', async () => {
    try {
      all = await types();
    } catch (e) {
      host.replaceChildren(errorState(e, () => location.reload()));
      return;
    }
    void render();
  }, { once: true });
}

// ---------- one connector ----------

function witnessCard(d: DecoderView | null, err: ApiError | null): HTMLElement {
  const st = witnessState(d);
  const tone = st === 'healthy' ? 'ok' : st === 'lagging' ? 'warn' : st === 'invalidated' ? 'bad' : 'idle';
  const line = h('p', { class: `wit-line wit-${tone}` }, h('span', { class: 'wit-dot', 'aria-hidden': 'true' }), witnessLine(d));
  const body: Child[] = [line];
  if (st === 'lagging') body.push(h('p', { class: 'wit-note' }, c.witness.lagging));
  if (st === 'invalidated') body.push(h('p', { class: 'wit-note wit-bad-note' }, c.witness.invalidated));
  if (!d && err && err.code === 'witness_not_configured') body.push(h('p', { class: 't-2' }, err.message));
  if (d) {
    body.push(kv([
      [c.witness.slot, h('span', { class: 'mono', translate: 'no' }, d.slot)],
      [c.witness.watching, h('span', { class: 'mono', translate: 'no' }, d.watching.join(', ') || '·')],
      ['Retained WAL', bytes(d.lag.retained_bytes)],
      [c.witness.since, relTime(d.since, tz())],
      ...(d.reason ? [[c.witness.reason, d.reason] as [string, Child]] : []),
    ]));
  }
  return h('section', { class: 'card card-pad grid gap-3', 'aria-labelledby': 'wit-t' }, h('h2', { class: 't-headline', id: 'wit-t' }, c.witness.title), ...body);
}

function toolsTable(x: Connector, enforced: { policy: string } | null): HTMLElement {
  if (!x.tools?.length) return h('p', { class: 't-2' }, c.tools.none);
  const t = h('table', { class: 'dt cards' },
    h('thead', null, h('tr', null, ...[c.tools.name, c.tools.side, c.tools.idempotent, c.tools.levels, c.tools.allowed].map((l) => h('th', { scope: 'col' }, l)))),
    h('tbody', null, ...x.tools.map((tl) => h('tr', null,
      h('td', { 'data-label': c.tools.name }, h('span', { class: 'mono', translate: 'no', title: tl.description ?? '' }, tl.name)),
      h('td', { 'data-label': c.tools.side }, h('span', { class: sideEffectClass(tl.side_effect) }, tl.side_effect === 'write' ? h('span', { class: 'side-dot', 'aria-hidden': 'true' }) : null, tl.side_effect)),
      h('td', { 'data-label': c.tools.idempotent, 'data-card': 'label' }, tl.idempotent ? c.tools.yes : c.tools.no),
      h('td', { 'data-label': c.tools.levels, 'data-card': 'label' }, levelBadges(tl.levels ?? [])),
      h('td', { 'data-label': c.tools.allowed, 'data-card': 'label' }, enforced
        ? h('a', { href: `/app/policies/${enforced.policy}?tab=dry-run&tool=${encodeURIComponent(tl.name)}` }, c.tools.dryRun, icon('arrow-right', 16))
        : h('span', { class: 't-2' }, '·'))))));
  wireTable(t as HTMLTableElement);
  return h('div', { class: 'table-frame' }, t);
}

export function mountConnector(): void {
  const host = maybe('detail-body');
  if (!host) return;
  const id = location.pathname.split('/').filter(Boolean).pop() ?? '';
  let x: Connector | null = null;
  let type: ConnectorType | undefined;
  let lastTest: ConnectorTest | null = null;
  let cooldownUntil = 0;

  const testSection = h('section', { class: 'card card-pad grid gap-3', 'aria-labelledby': 'test-t' });
  const witnessHost = h('div');

  // The test header (title, wait note, button) is built once; a run redraws only the results, and the
  // 6 s rest updates only the note and the button, so an open "Show SQL" or a focused link survives.
  const runBtn = h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'data-run-test': '' }, icon('refresh-cw', 16), h('span', null, c.test.run));
  runBtn.addEventListener('click', () => void runTest());
  const waitNote = h('span', { class: 't-footnote t-2' });
  const testHead = h('div', { class: 'flex items-center gap-3 flex-wrap' }, h('h2', { class: 't-headline', id: 'test-t' }), h('span', { class: 'ml-auto flex items-center gap-2' }, waitNote, runBtn));
  const testBody = h('div', { class: 'grid gap-3', 'aria-live': 'polite' });
  let busyTest = false;
  const drawButton = () => {
    const left = Math.max(0, Math.ceil((cooldownUntil - Date.now()) / 1000));
    runBtn.disabled = busyTest || left > 0;
    runBtn.setAttribute('aria-busy', busyTest ? 'true' : 'false');
    const label = busyTest ? c.test.running : lastTest ? c.test.again : c.test.run;
    if (runBtn.lastChild?.textContent !== label) replace(runBtn.lastChild as HTMLElement, label);
    const wait = left > 0 ? fill(c.test.wait, { s: left }) : '';
    if (waitNote.textContent !== wait) waitNote.textContent = wait;
  };
  const drawTest = () => {
    if (!x) return;
    const pg = x.type === 'postgres';
    testHead.querySelector('h2')!.textContent = pg ? c.readiness.title : c.test.title;
    drawButton();
    const body: Child[] = [];
    if (!lastTest) {
      body.push(h('p', { class: 't-2' }, pg ? copy.postgres.empty : c.test.never));
    } else {
      body.push(pg ? readinessLine(lastTest) : h('p', { class: `cl-summary cl-summary-${lastTest.status}` }, testSummary(lastTest)));
      if (pg && lastTest.checks.some((k) => /amazon rds|\brds\./i.test(`${k.detail ?? ''} ${k.fix ?? ''}`))) body.push(h('p', { class: 'banner banner-readonly' }, icon('info', 16), c.readiness.rds));
      body.push(checkList(lastTest, { db: dbOf(x), idPrefix: 'cl' }));
    }
    replace(testBody, ...body);
    const cli = pg ? h('div', { class: 'grid gap-1' }, h('p', { class: 't-footnote t-2' }, c.readiness.cliLabel), codeBlock('shell', fill(c.readiness.cli, { name: x.name }), c.readiness.cliLabel)) : null;
    replace(testSection, testHead, testBody, cli);
  };
  const runTest = async () => {
    if (!x || Date.now() < cooldownUntil || busyTest) return;
    busyTest = true;
    drawButton();
    try {
      lastTest = await api<ConnectorTest>('POST', `/v1/connectors/${x.id}/test`, { body: {} });
      x.health = { status: lastTest.status, tested_at: new Date().toISOString() };
    } catch (e) {
      apiErrorToast(e);
    }
    busyTest = false;
    cooldownUntil = Date.now() + 6000;
    drawTest();
    const hc = maybe('con-health');
    if (hc && x) hc.replaceWith(healthCell(x, 'con-health'));
    const tick = window.setInterval(() => {
      drawButton();
      if (Date.now() >= cooldownUntil) clearInterval(tick);
    }, 1000);
  };
  const loadWitness = async () => {
    if (!x || x.type !== 'postgres') return;
    try {
      const d = await api<DecoderView>('GET', `/v1/connectors/${x.id}/decoder`, { query: { recent: '0' } });
      witnessHost.replaceChildren(witnessCard(d, null));
    } catch (e) {
      witnessHost.replaceChildren(witnessCard(null, e instanceof ApiError ? e : null));
    }
  };

  const credRow = (): HTMLElement => {
    const cr = x!.credentials;
    const admin = isAdmin();
    return h('section', { class: 'card card-pad grid gap-3', 'aria-labelledby': 'cred-t' },
      h('h2', { class: 't-headline', id: 'cred-t' }, c.credentials.title),
      h('div', { class: 'cred-row' },
        h('span', { class: cr?.set ? 'cred-set' : 'cred-unset t-2' }, cr?.set ? icon('key-round', 16) : icon('circle-dashed', 16), credentialLine(cr)),
        admin ? h('span', { class: 'flex gap-2 ml-auto' },
          h('button', { type: 'button', class: 'btn btn-secondary btn-sm', onclick: openReplace }, cr?.set ? c.credentials.replace : c.credentials.save),
          cr?.set ? h('button', { type: 'button', class: 'btn btn-quiet btn-sm', onclick: openRemove }, c.credentials.remove) : null) : null),
      admin ? null : h('p', { class: 't-footnote t-2' }, c.credentials.viewer));
  };
  const openReplace = () => {
    const cf = credentialForm(type, x!.credentials?.kind ?? null, 'rp');
    const save = h('button', { type: 'button', class: 'btn btn-primary' }, c.credentials.save);
    const sheet = openSheet({
      title: `${c.credentials.title} · ${x!.name}`,
      body: [cf.el],
      foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), save],
      onClose: () => { for (const f of Object.values(cf.fields())) f.clear(); },
    });
    sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
    save.addEventListener('click', async () => {
      save.disabled = true;
      try {
        x = await saveCredentials(x!.id, cf.kind(), cf.fields());
        sheet.close();
        toast(fill(c.credentials.saved, { name: x.name }), { kind: 'success' });
        draw();
        host.querySelector<HTMLElement>('.cred-row button')?.focus();
        void runTest();
      } catch (e) {
        save.disabled = false;
        if (e instanceof ApiError && e.param) {
          const f = cf.fields()[e.param.replace(/^secret\./, '')];
          if (f) { f.setError(e.message); f.focus(); return; }
        }
        apiErrorToast(e);
      }
    });
  };
  const openRemove = () => {
    confirmTyped({
      title: fill(c.credentials.removeTitle, { name: x!.name }),
      body: fill(c.credentials.removeBody, { name: x!.name }),
      confirmLabel: c.credentials.removeButton,
      danger: true,
      onConfirm: async () => {
        await api('DELETE', `/v1/connectors/${x!.id}/credentials`);
        x = await api<Connector>('GET', `/v1/connectors/${x!.id}`);
        toast(fill(c.credentials.removed, { name: x.name }), { kind: 'success' });
        draw();
        requestAnimationFrame(() => host.querySelector<HTMLElement>('.cred-row button')?.focus());
      },
    });
  };
  const openEdit = () => {
    const form = schemaForm((type?.config_schema ?? null) as never, { idPrefix: 'ed', initial: x!.config });
    const save = h('button', { type: 'button', class: 'btn btn-primary' }, c.save);
    const err = h('p', { class: 'error', role: 'alert', hidden: true });
    const sheet = openSheet({ title: `${c.edit} · ${x!.name}`, body: [form.el, err], foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), save] });
    sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
    save.addEventListener('click', async () => {
      form.clearErrors();
      if (!form.validate()) return;
      save.disabled = true;
      try {
        x = await api<Connector>('PATCH', `/v1/connectors/${x!.id}`, { body: { config: form.values(), expected_config_hash: x!.config_hash } });
        sheet.close();
        toast(fill(c.saved, { name: x.name }), { kind: 'success' });
        draw();
      } catch (e) {
        save.disabled = false;
        if (e instanceof ApiError && e.param === 'expected_config_hash') {
          replace(err, icon('circle-alert', 16), c.stale, ' ', h('button', { type: 'button', class: 'btn btn-secondary btn-sm', onclick: () => location.reload() }, c.reload));
          err.hidden = false;
          return;
        }
        if (e instanceof ApiError && e.param) return form.setErrors([{ param: e.param, message: e.message }]);
        apiErrorToast(e);
      }
    });
  };

  const draw = () => {
    if (!x) return;
    const levels = levelsOf(x, type?.levels);
    const head = h('header', { class: 'detail-head' },
      h('a', { class: 'btn btn-quiet btn-sm -ml-3', href: '/app/connectors' }, icon('chevron-left', 16), copy.pages.connectors.title),
      h('div', { class: 'flex items-center gap-3 flex-wrap' },
        h('span', { class: 'type-icon' }, icon(typeIcon(x.type), 20)),
        h('h1', { class: 't-title-2 mono', id: 'page-title', translate: 'no' }, x.name),
        levelBadges(levels), healthCell(x, 'con-health'),
        isAdmin() ? h('button', { type: 'button', class: 'btn btn-secondary btn-sm ml-auto', onclick: openEdit }, icon('pencil', 16), c.edit) : null),
      x.description ? h('p', { class: 'lede t-2' }, x.description) : null);
    const overview = h('section', { class: 'card card-pad grid gap-3', 'aria-labelledby': 'ov-t' },
      h('h2', { class: 't-headline', id: 'ov-t' }, c.overview),
      kv([
        ['Type', h('span', { class: 'con-type' }, icon(typeIcon(x.type), 16), type?.title ?? x.type)],
        ['Connector', idChip(x.id)],
        [c.hash, h('span', { class: 'mono', translate: 'no', title: x.config_hash }, `${x.config_hash.slice(0, 11)}…${x.config_hash.slice(-4)}`)],
        ['Updated', h('span', { class: 'num' }, dateTime(x.updated_at, tz()))],
      ]),
      h('details', { class: 'con-config' }, h('summary', null, c.config2), h('div', { class: 'apv-args', tabindex: 0, role: 'region', 'aria-label': c.config2 }, jsonView(x.config))));
    const tools = h('section', { class: 'card card-pad grid gap-3', 'aria-labelledby': 'tools-t' }, h('h2', { class: 't-headline', id: 'tools-t' }, c.tools.title), h('div', { id: 'tools-host' }, toolsTable(x, null)));
    drawTest();
    replace(host, head,
      isAdmin() ? null : h('p', { class: 'banner banner-readonly' }, icon('eye', 16), c.viewer),
      h('div', { class: 'con-grid' },
        h('div', { class: 'grid gap-4 min-w-0' }, x.type === 'postgres' ? testSection : null, credRow(), x.type !== 'postgres' ? testSection : null, tools),
        h('div', { class: 'grid gap-4 min-w-0 content-start' }, x.type === 'postgres' ? witnessHost : null, overview)));
    host.setAttribute('aria-busy', 'false');
    void api<{ enforce: { policy: string } | null }>('GET', '/v1/policies/active').then((a) => {
      const th = maybe('tools-host');
      if (th && x) th.replaceChildren(toolsTable(x, a.enforce));
    }).catch(() => {});
  };

  document.addEventListener('dg:ready', async () => {
    try {
      const [cx, ts] = await Promise.all([api<Connector>('GET', `/v1/connectors/${id}`), types().catch(() => [] as ConnectorType[])]);
      x = cx;
      type = ts.find((t) => t.type === cx.type);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) host.replaceChildren(notFound(c.notFound));
      else host.replaceChildren(errorState(e, () => location.reload()));
      host.setAttribute('aria-busy', 'false');
      return;
    }
    draw();
    if (x.type === 'postgres') {
      void loadWitness();
      let tm = 0;
      stream().on('decoder.*', () => { clearTimeout(tm); tm = window.setTimeout(() => void loadWitness(), 400); });
      window.setInterval(() => { if (document.visibilityState === 'visible') void loadWitness(); }, 15_000);
    }
  }, { once: true });
}

function dbOf(x: Connector): string | undefined {
  const hint = x.credentials?.hint ?? '';
  const m = /\/([A-Za-z0-9_-]+)$/.exec(hint);
  return m?.[1];
}
