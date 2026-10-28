// simplerecords.ts: Locked records (DL-143). Every action's deed in plain words, newest first. Open
// one to see who did it, who approved it and whether it was read back; "Check this record" runs
// S05's verifier on the exact bytes in this browser (no internet needed) and answers in one plain
// sentence; "Download for an auditor" saves those bytes and the lock keys, which the CLI checks too.
// Brand terms (deed, seal key, level) stay under Technical details.

import { api, ApiError } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { simpleCopy as sc } from '../lib/copy-simple.ts';
import { h, icon } from '../lib/dom.ts';
import { dateTime, number, shortId, when } from '../lib/format.ts';
import { plainRecord, resultOf, ICON, TONE, type Result } from '../lib/models/simple.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { DeedEnvelope, LedgerCheck, ListResponse } from '../lib/types.ts';
import { kv } from './components.ts';
import { checkDeed, rawDeed, saveText } from './deedpanel.ts';
import { announce } from './shell.ts';
import { emptyBlock, errorBlock } from './simplefeed.ts';
import { toast } from './toast.ts';

const c = sc.records;
const tz = () => get()?.org.timezone ?? 'UTC';

function resultOfDeed(d: DeedEnvelope): Result {
  const b = d.body;
  if (b.kind !== 'action') return 'done';
  return resultOf({
    state: 'sealed',
    status: b.policy?.decision === 'deny' ? 'denied' : b.execution?.status ?? 'executed',
    witness: b.witness ? { object: 'witness', level: b.witness.level, verdict: b.witness.verdict, method: b.witness.method, required_level: null, detail_digest: b.witness.detail_digest } : null,
    decision: null,
    approval: (b.approval as { status?: string } | null)?.status === 'denied' ? { id: '', status: 'denied', expires_at: '' } : null,
  });
}

function lockedPill(r: Result): HTMLElement {
  // The record is locked whatever happened; the second word says what the check found.
  const word = r === 'checked' ? c.checkResult.VERIFIED : r === 'problem' ? c.checkResult.MISMATCH : r === 'couldnt' ? c.checkResult.UNVERIFIABLE : c.locked;
  const tone = r === 'checked' || r === 'problem' || r === 'couldnt' ? TONE[r] : 'neutral';
  return h('span', { class: `chip sx-pill chip-${tone}`, 'data-tone': tone }, icon(tone === 'neutral' ? 'lock' : ICON[r], 16), word);
}

async function keysText(): Promise<string> {
  const res = await fetch('/.well-known/deedglass/keys.json', { headers: { Accept: 'application/json' }, credentials: 'omit', redirect: 'error' });
  if (!res.ok) throw new Error(`The server answered ${res.status}.`);
  return res.text();
}

export interface Records {
  open: (deed: string) => void;
}

export function mountRecords(host: HTMLElement, deedToOpen: string | null): Records {
  const chain = h('div', { class: 'sv-chain', 'data-state': 'checking', role: 'status' }, icon('refresh-cw'), h('span', null, c.chainChecking));
  const list = h('ul', { class: 'sv-list', 'aria-label': c.title });
  const older = h('div', { class: 'sv-older' }, h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => void load(true) }, c.more));
  let rows: DeedEnvelope[] = [];
  let cursor: string | null = null;
  let openId: string | null = deedToOpen;

  const panel = (d: DeedEnvelope): HTMLElement => {
    const p = plainRecord(d.body);
    const result = h('div', { class: 'sv-result', role: 'status', 'aria-live': 'polite' });
    result.hidden = true;
    const checkBtn = h('button', { type: 'button', class: 'btn btn-primary btn-xl', 'data-check': '' }, icon('shield-check', 20), c.checkThis);
    checkBtn.addEventListener('click', async () => {
      if (checkBtn.getAttribute('aria-busy') === 'true') return;
      checkBtn.setAttribute('aria-busy', 'true');
      checkBtn.replaceChildren(h('span', { class: 'spin-glyph', 'aria-hidden': 'true' }, '◐'), c.checking);
      let state: string;
      let title: string;
      let body = '';
      try {
        const r = await checkDeed(await rawDeed(d.id), c.thisRecord);
        state = r.state;
        if (r.state === 'valid') {
          title = c.valid;
          body = c.validBody;
        } else if (r.state === 'invalid') {
          title = c.invalid;
          body = r.text;
        } else if (r.state === 'unsupported') title = c.unsupported;
        else title = fill(c.error, { message: r.text });
      } catch (e) {
        state = 'error';
        title = fill(c.error, { message: e instanceof Error ? e.message : String(e) });
      }
      const ic = state === 'valid' ? 'check' : state === 'invalid' ? 'circle-alert' : 'circle-dashed';
      result.setAttribute('data-state', state);
      result.replaceChildren(icon(ic), h('div', null, h('strong', null, title), body ? h('p', null, body) : null));
      result.hidden = false;
      announce(title);
      checkBtn.removeAttribute('aria-busy');
      checkBtn.replaceChildren(icon('shield-check', 20), c.checkThis);
    });
    const name = `deedglass-record-${d.body.seq}.jsonl`;
    const dl = h('button', { type: 'button', class: 'btn btn-secondary btn-xl' }, icon('download', 20), c.download);
    dl.addEventListener('click', async () => {
      try {
        const raw = await rawDeed(d.id);
        saveText(raw.endsWith('\n') ? raw : raw + '\n', name, 'application/x-ndjson');
        toast(fill(c.downloaded, { name }), { kind: 'success' });
      } catch (e) {
        toast(fill(c.error, { message: e instanceof Error ? e.message : String(e) }), { kind: 'error' });
      }
    });
    const keys = h('button', { type: 'button', class: 'btn btn-quiet' }, icon('key-round', 16), c.downloadKeys);
    keys.addEventListener('click', async () => {
      try {
        saveText(await keysText(), 'keys.json');
      } catch (e) {
        toast(fill(c.error, { message: e instanceof Error ? e.message : String(e) }), { kind: 'error' });
      }
    });
    const tech = h('details', { class: 'sv-tech' }, h('summary', null, c.technical), kv([
      [c.tech.id, h('span', { class: 'mono', translate: 'no' }, d.id)],
      [c.tech.key, h('span', { class: 'mono', translate: 'no' }, shortId(d.seal_key_id))],
      [c.tech.level, p.level ?? ''],
      [c.tech.chain, number(d.body.seq)],
      [c.tech.offline, h('span', { class: 'mono', translate: 'no' }, c.offlineCommand.replace('record.jsonl', name))],
    ]));
    return h('div', { class: 'sv-record', id: `sv-rec-${d.id}` },
      kv([[c.doneBy, p.who], [c.approval, p.approval], [c.check, p.check], [c.lockedAt, dateTime(d.body.issued_at, tz())]]),
      h('div', { class: 'actions' }, checkBtn, dl, keys), result, tech);
  };

  const row = (d: DeedEnvelope): HTMLElement => {
    const p = plainRecord(d.body);
    const isOpen = openId === d.id;
    const btn = h('button', { type: 'button', 'aria-expanded': isOpen ? 'true' : 'false', 'aria-controls': `sv-rec-${d.id}` },
      h('time', { datetime: d.body.issued_at, title: dateTime(d.body.issued_at, tz()) }, when(d.body.issued_at, tz())),
      h('span', null, h('span', { class: 'what' }, p.title), h('span', { class: 'who' }, p.who)),
      lockedPill(resultOfDeed(d)));
    const li = h('li', { class: 'sv-row', 'data-deed': d.id }, btn);
    if (isOpen) li.append(panel(d));
    btn.addEventListener('click', () => {
      openId = openId === d.id ? null : d.id;
      render();
      list.querySelector<HTMLElement>(`[data-deed="${d.id}"] > button`)?.focus();
    });
    return li;
  };

  const render = () => {
    if (rows.length === 0) {
      host.replaceChildren(chain, emptyBlock(c.empty, 'lock', { label: c.emptyNext, href: '/app/connect' }));
      return;
    }
    list.replaceChildren(...rows.map(row));
    older.hidden = !cursor;
    host.replaceChildren(chain, list, older);
  };

  const loadChain = async () => {
    try {
      const k = await api<LedgerCheck>('GET', '/v1/ledger/check', { query: { 'issued_at[gte]': new Date(Date.now() - 86_400_000).toISOString() } });
      const ok = k.status === 'intact';
      chain.setAttribute('data-state', ok ? 'ok' : k.status === 'broken' ? 'bad' : 'checking');
      chain.replaceChildren(icon(ok ? 'check' : k.status === 'broken' ? 'circle-alert' : 'circle-dashed'), h('span', null, ok ? c.chainOk : k.status === 'broken' ? c.chainBroken : c.chainChecking));
    } catch {
      chain.hidden = true;
    }
  };

  const load = async (more = false) => {
    try {
      const page = await api<ListResponse<DeedEnvelope>>('GET', '/v1/deeds', { query: { order: 'desc', limit: '25', cursor: more && cursor ? cursor : undefined } });
      rows = more ? [...rows, ...page.data] : page.data;
      cursor = page.has_more ? page.next_cursor : null;
      // A record named in the link but not on the first page: read it on its own.
      if (openId && !rows.some((d) => d.id === openId)) {
        try {
          const one = JSON.parse(await rawDeed(openId)) as DeedEnvelope;
          if (one && one.id === openId) rows = [one, ...rows];
        } catch {
          openId = null;
        }
      }
      render();
      if (openId) requestAnimationFrame(() => document.getElementById(`sv-rec-${openId}`)?.scrollIntoView({ block: 'center', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }));
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return;
      host.replaceChildren(errorBlock(fill(c.loadFailed, { message: e instanceof Error ? e.message : String(e) }), c.retry, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };

  void loadChain();
  void load();
  const live = stream();
  let t = 0;
  live.on('action.*', () => {
    clearTimeout(t);
    t = window.setTimeout(() => void load(), 800);
  });
  return {
    open: (id: string) => {
      openId = id;
      void load();
    },
  };
}
