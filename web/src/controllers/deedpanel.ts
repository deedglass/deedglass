// deedpanel.ts: DeedPanel (S25 §3.5), shared by Action detail and the Deeds screen. It reads the
// deed's exact bytes (GET /v1/deeds/{id} as text) and this server's keys, and checks the seal with
// S05's verifier (parseKeySet, verifyDeed): never JSON.parse on deed input, never re-serialised
// before checking or download. A ✓ appears only after a successful check; a browser without
// WebCrypto Ed25519 gets ○, never ✓. The honest note says whose keys were used.

import { message, parseKeySet, verifyDeed, VerifyError, UnsupportedError } from '@verify/index.ts';
import { parseEnvelope } from '@verify/deed.ts';
import { mountMirror, type ConsoleMirrorState } from '@verify/ui/mirror.ts';
import { API_VERSION } from '../lib/api.ts';
import { copy, fill } from '../lib/copy.ts';
import { copyChip } from '../lib/chips.ts';
import { h, icon } from '../lib/dom.ts';
import { number, shortHash, shortId } from '../lib/format.ts';
import { SealGovernor } from '../lib/seal.ts';
import { toast } from './toast.ts';

const gov = new SealGovernor();

/** saveText downloads text as a file through a Blob URL (no CSP change; the URL never comes from data). */
export function saveText(text: string, name: string, type = 'application/json'): void {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

export type CheckState = 'idle' | 'checking' | 'valid' | 'invalid' | 'unsupported' | 'error';

/** rawDeed fetches the deed's exact bytes as text (the envelope as the server stored it, JCS). */
export async function rawDeed(id: string, signal?: AbortSignal): Promise<string> {
  const res = await fetch(`/v1/deeds/${encodeURIComponent(id)}`, {
    headers: { Accept: 'application/json', 'Deedglass-Version': API_VERSION },
    credentials: 'same-origin', redirect: 'error', signal,
  });
  const text = await res.text();
  if (!res.ok) {
    let msg = `The server answered ${res.status}.`;
    try {
      msg = (JSON.parse(text) as { error?: { message?: string } }).error?.message ?? msg; // an error envelope, not a deed
    } catch {
      /* not JSON */
    }
    throw new Error(msg);
  }
  return text;
}

async function keysText(): Promise<string> {
  const res = await fetch('/.well-known/deedglass/keys.json', { headers: { Accept: 'application/json' }, credentials: 'omit', redirect: 'error' });
  if (!res.ok) throw new Error(`The server answered ${res.status}.`);
  return res.text();
}

export interface CheckResult { state: CheckState; text: string; key?: string }

/** checkDeed runs S05's verifier on the raw text and this server's key set. */
export async function checkDeed(raw: string, deedLabel = 'This deed'): Promise<CheckResult> {
  let keys;
  try {
    keys = await parseKeySet(await keysText());
  } catch (e) {
    if (e instanceof VerifyError) return { state: 'invalid', text: message(e).sentence };
    return { state: 'error', text: fill(copy.deedPanel.keysFailed, { message: (e as Error).message }) };
  }
  try {
    const r = await verifyDeed(raw, keys);
    return { state: 'valid', text: fill(copy.deedPanel.valid, { key: shortId(r.sealKeyId) }), key: r.sealKeyId };
  } catch (e) {
    if (e instanceof UnsupportedError) return { state: 'unsupported', text: copy.deedPanel.unsupported };
    if (e instanceof VerifyError) {
      const m = message(e);
      return { state: 'invalid', text: m.sentence.replace(/^This deed\b/, deedLabel) };
    }
    return { state: 'unsupported', text: copy.deedPanel.unsupported };
  }
}

export interface DeedPanelOptions {
  deed: string | null; // deed id
  seq?: number | null;
  mirror: ConsoleMirrorState; // the deed's final state (sealed, mismatch, unverifiable, open)
  pending?: boolean; // no deed yet
  title?: boolean;
}

export interface DeedPanel {
  el: HTMLElement;
  raw: () => Promise<string>;
  /** sealedLive: the action sealed while the page was open; play Sealed once (seal.ts rules). */
  sealedLive: (state: ConsoleMirrorState) => void;
}

export function deedPanel(o: DeedPanelOptions): DeedPanel {
  const markHost = h('span', { class: 'deed-mark', 'aria-hidden': 'true' });
  const mirror = mountMirror(markHost, { size: 48, state: o.pending || !o.deed ? 'open' : o.mirror });
  const status = h('p', { class: 'deed-check', role: 'status', 'aria-live': 'polite' });
  status.hidden = true;
  const head = h('p', { class: 't-headline deed-sealed' }, o.deed ? fill(copy.deedPanel.sealed, { seq: o.seq ? number(o.seq) : '…' }) : copy.deedPanel.pending);
  const facts = h('dl', { class: 'kv kv-compact deed-facts' });
  let rawText: string | null = null;
  let loading: Promise<string> | null = null;
  const raw = () => {
    if (!o.deed) return Promise.reject(new Error('no deed'));
    if (rawText !== null) return Promise.resolve(rawText);
    loading ??= rawDeed(o.deed).then((t) => {
      rawText = t;
      return t;
    });
    return loading;
  };
  const showFacts = (t: string) => {
    try {
      const p = parseEnvelope(t);
      const b = p.env.body;
      head.textContent = fill(copy.deedPanel.sealed, { seq: number(b.seq) });
      facts.replaceChildren(
        h('dt', null, 'Deed'), h('dd', null, copyChip(p.env.id, shortId(p.env.id))),
        h('dt', null, 'Body hash'), h('dd', null, copyChip(p.env.body_hash, shortHash(p.env.body_hash))),
        h('dt', null, copy.deedPanel.sealKey), h('dd', null, h('span', { class: 'mono', title: p.env.seal_key_id, translate: 'no' }, shortId(p.env.seal_key_id))));
    } catch {
      /* the check below says what is wrong with it */
    }
  };
  const checkBtn = h('button', { type: 'button', class: 'btn btn-secondary', 'data-check': '' }, icon('witness', 16), copy.deedPanel.check);
  const dl = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, icon('download', 16), copy.deedPanel.download);
  const cmdBtn = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, icon('copy', 16), copy.deedPanel.copyCommand);
  checkBtn.addEventListener('click', async () => {
    checkBtn.disabled = true;
    checkBtn.setAttribute('aria-busy', 'true');
    status.hidden = false;
    status.className = 'deed-check';
    status.replaceChildren(icon('circle-dashed', 16), copy.deedPanel.checking);
    try {
      const t = await raw();
      const r = await checkDeed(t, `Deed ${o.seq ?? ''}`.trim());
      const ic = r.state === 'valid' ? 'check' : r.state === 'invalid' ? 'x' : 'circle-dashed';
      status.className = `deed-check is-${r.state}`;
      status.replaceChildren(icon(ic, 16), h('span', null, r.text, r.state === 'valid' ? h('span', { class: 't-2' }, ` · ${copy.deedPanel.validNote}`) : null));
      status.setAttribute('data-state', r.state);
      if (r.state === 'valid' && o.mirror === 'sealed' && gov.mayPlay(`check:${o.deed}`, true)) void mirror.set('sealed', { animate: true });
    } catch (e) {
      status.className = 'deed-check is-error';
      status.replaceChildren(icon('circle-alert', 16), (e as Error).message);
    } finally {
      checkBtn.disabled = false;
      checkBtn.removeAttribute('aria-busy');
    }
  });
  dl.addEventListener('click', async () => {
    try {
      saveText(await raw(), `${o.deed}.json`); // the exact bytes, never re-serialised
    } catch (err) {
      toast((err as Error).message, { kind: 'error' });
    }
  });
  cmdBtn.addEventListener('click', async () => {
    const cmd = fill(copy.deedPanel.command, { file: `${o.deed}.json`, origin: location.origin });
    try {
      await navigator.clipboard.writeText(cmd);
      toast(copy.deedPanel.commandCopied, { kind: 'success' });
    } catch {
      toast("Deedglass couldn't copy to the clipboard. Select the command and copy it by hand.", { kind: 'error' });
    }
  });
  const body = o.deed
    ? [h('div', { class: 'deed-top' }, markHost, h('div', null, head, facts)),
       h('div', { class: 'deed-actions' }, checkBtn), status,
       h('p', { class: 't-footnote t-2 deed-note' }, copy.deedPanel.note),
       h('div', { class: 'deed-links' }, dl, cmdBtn)]
    : [h('div', { class: 'deed-top' }, markHost, h('div', null, head))];
  const el = h('section', { class: 'card card-pad deed-panel', 'aria-labelledby': 'deed-panel-t', 'data-deed': o.deed ?? '' },
    h('h2', { class: 'tile-label', id: 'deed-panel-t' }, copy.deedPanel.title), ...body);
  if (o.deed) void raw().then(showFacts).catch(() => undefined);
  return {
    el,
    raw,
    sealedLive: (state) => {
      void mirror.set(state, { animate: state !== 'sealed' || gov.mayPlay(o.deed ?? '', true) });
    },
  };
}
