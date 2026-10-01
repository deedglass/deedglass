// The verify page. Everything runs here: nothing pasted is sent or stored. The only network call is
// the key fetch the person starts (fetchkeys.ts). Text goes into the page with textContent only.

import { cannotDetermine, type Failure, manyLines, parseKeySet, type Report, SealKeySet, splitLines, splitTextLines, Verifier, VerifyError } from '../index.js';
import { ed25519Supported } from '../keys.js';
import { SAMPLE_DEEDS, SAMPLE_KEYS } from '../sample.gen.js';
import { decodeUTF8 } from '../strict.js';
import { FetchKeysError, fetchKeys, keysURL } from './fetchkeys.js';
import { Mirror, type MirrorState } from './mirror.js';

// Page copy (S05 §5 and §3.7). Verification copy comes from the generated message catalog.
const COPY = {
  empty: 'Paste a deed to check its seal, or try the sample.',
  emptyNext: 'Deeds stay in this page. The check uses canonical JSON, SHA-256 and the Ed25519 seal.',
  checking: (n: string) => `Checking ${n} deeds…`,
  noKeys: 'Add the seal keys to check this deed.',
  noKeysNext: 'Paste keys.json, or fetch it from the issuer: /.well-known/deedglass/keys.json',
  noDeeds: 'Paste a deed first.',
  unsupported: "This browser can't check Ed25519 seals. Use the CLI: deedglass verify",
  unsupportedNext: 'Safari 17, Firefox 129 and Chrome 137 or later can. The CLI is the reference verifier.',
  pastedKeys: 'Seal keys came from what you pasted. This shows nothing changed since they were sealed, not who sealed them.',
  pastedKeysNext: "Fetch the issuer's published keys by URL to check who sealed them.",
  contacts: (host: string) => `This contacts ${host}.`,
  badURL: 'Use an https URL (http only for localhost).',
  fetched: (host: string, n: number) => `Seal keys from ${host} · ${n} ${n === 1 ? 'key' : 'keys'} · pinned to this URL`,
  keysInvalid: 'The seal keys are not a valid key set.',
  bundle: 'Bundles (.tar.gz) are checked by the CLI for now: deedglass verify bundle.tar.gz',
  copied: 'Report copied.',
  copyFailed: 'Copy did not work here. Select the text instead.',
  sample: 'Sample · test key',
};

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const deedsEl = $<HTMLTextAreaElement>('deeds');
const keysEl = $<HTMLTextAreaElement>('keys');
const urlEl = $<HTMLInputElement>('keys-url');
const fetchBtn = $<HTMLButtonElement>('fetch-keys');
const noticeEl = $<HTMLParagraphElement>('url-notice');
const verifyBtn = $<HTMLButtonElement>('verify');
const sampleBtn = $<HTMLButtonElement>('use-sample');
const fileEl = $<HTMLInputElement>('file');
const fileLine = $<HTMLDivElement>('file-line');
const dropEl = $<HTMLDivElement>('drop');
const sampleChip = $<HTMLSpanElement>('sample-chip');
const resultEl = $<HTMLDivElement>('result');
const markEl = $<HTMLDivElement>('mark');
const headlineEl = $<HTMLParagraphElement>('headline');
const nextEl = $<HTMLParagraphElement>('next');
const noteEl = $<HTMLDivElement>('note');
const progressEl = $<HTMLProgressElement>('progress');
const rowsEl = $<HTMLDListElement>('rows');
const copyBtn = $<HTMLButtonElement>('copy');
const statusEl = $<HTMLSpanElement>('status');
const chooseBtn = $<HTMLButtonElement>('choose-file');

chooseBtn.addEventListener('click', () => fileEl.click());
if (!/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) $<HTMLElement>('mod-key').textContent = 'Ctrl';

const mirror = new Mirror('The Deedglass mark');
markEl.append(mirror.svg);

let loadedFile: { name: string; bytes: Uint8Array } | null = null;
let fetchedKeys: { host: string; text: string } | null = null;
let lastReport: Report | null = null;
let running = false;

function fmt(n: number): string {
  return n.toLocaleString('en-US');
}

function shortHash(h: string): string {
  const hex = h.replace(/^sha256:/, '');
  return hex.length < 8 ? h : `sha256:${hex.slice(0, 4)}…${hex.slice(-4)}`;
}

function clear(e: Element): void {
  while (e.firstChild) e.removeChild(e.firstChild);
}

function text(tag: string, t: string, cls?: string): HTMLElement {
  const e = document.createElement(tag);
  e.textContent = t;
  if (cls) e.className = cls;
  return e;
}

function chip(kind: string, symbol: string, label: string): HTMLElement {
  const c = text('span', '', `chip ${kind}`);
  c.append(text('span', symbol), document.createTextNode(label));
  c.firstElementChild?.setAttribute('aria-hidden', 'true');
  return c;
}

/** Sets the headline: a symbol, then a sentence (never colour alone). Parts don't break inside. */
function say(state: MirrorState, symbol: string, sentence: string | string[], next = ''): void {
  resultEl.className = `card result state-${state}`;
  mirror.set(state);
  clear(headlineEl);
  if (symbol) headlineEl.append(text('span', symbol, 'sym'));
  if (typeof sentence === 'string') headlineEl.append(document.createTextNode(sentence));
  else {
    sentence.forEach((p, i) => {
      if (i > 0) headlineEl.append(document.createTextNode(' · '));
      headlineEl.append(text('span', p, 'part'));
    });
  }
  nextEl.textContent = next;
  nextEl.hidden = next === '';
}

function note(sentence: string, next: string): void {
  clear(noteEl);
  const p = text('p', '', 'note');
  p.append(text('span', '○', 'sym'), document.createTextNode(sentence));
  noteEl.append(p, text('p', next, 'next'));
  noteEl.hidden = false;
}

function resetResult(): void {
  clear(noteEl);
  noteEl.hidden = true;
  clear(rowsEl);
  rowsEl.hidden = true;
  progressEl.hidden = true;
  copyBtn.hidden = true;
  statusEl.textContent = '';
}

function row(label: string, value: string | Node): void {
  rowsEl.append(text('dt', label));
  const dd = document.createElement('dd');
  if (typeof value === 'string') dd.textContent = value;
  else dd.append(value);
  rowsEl.append(dd);
  rowsEl.hidden = false;
}

function idle(): void {
  resetResult();
  say('idle', '', COPY.empty, COPY.emptyNext);
}

// --- inputs ---

function useText(): void {
  loadedFile = null;
  fileLine.hidden = true;
  sampleChip.hidden = true;
}

deedsEl.addEventListener('input', useText);
keysEl.addEventListener('input', () => {
  fetchedKeys = null;
  sampleChip.hidden = true;
});

urlEl.addEventListener('input', () => {
  const v = urlEl.value.trim();
  if (v === '') {
    noticeEl.textContent = '';
    return;
  }
  const u = keysURL(v);
  noticeEl.textContent = u ? COPY.contacts(u.host) : COPY.badURL;
});

fetchBtn.addEventListener('click', async () => {
  const u = keysURL(urlEl.value);
  if (!u) {
    noticeEl.textContent = COPY.badURL;
    urlEl.focus();
    return;
  }
  fetchBtn.disabled = true;
  noticeEl.textContent = COPY.contacts(u.host);
  try {
    const bytes = await fetchKeys(u);
    const ks = await parseKeySet(bytes);
    const t = decodeUTF8(bytes);
    keysEl.value = t;
    fetchedKeys = { host: u.host, text: t };
    noticeEl.textContent = COPY.fetched(u.host, ks.keys.length);
  } catch (e) {
    fetchedKeys = null;
    if (e instanceof FetchKeysError) noticeEl.textContent = e.message;
    else if (e instanceof VerifyError) noticeEl.textContent = `${COPY.keysInvalid} (${e.code})`;
    else noticeEl.textContent = String(e);
  } finally {
    fetchBtn.disabled = false;
  }
});

async function takeFile(f: File): Promise<void> {
  if (/\.(tar\.gz|tgz)$/i.test(f.name)) {
    resetResult();
    say('cannot', '○', COPY.bundle);
    return;
  }
  const bytes = new Uint8Array(await f.arrayBuffer());
  loadedFile = { name: f.name, bytes };
  sampleChip.hidden = true;
  deedsEl.value = '';
  clear(fileLine);
  fileLine.append(text('span', `${f.name} · ${fmt(bytes.length)} bytes · loaded`), document.createTextNode(' '));
  const remove = text('button', 'Clear', 'link') as HTMLButtonElement;
  remove.type = 'button';
  remove.addEventListener('click', () => {
    useText();
    deedsEl.focus();
  });
  fileLine.append(remove);
  fileLine.hidden = false;
}

fileEl.addEventListener('change', () => {
  const f = fileEl.files?.[0];
  if (f) void takeFile(f);
  fileEl.value = '';
});

dropEl.addEventListener('dragover', (e) => {
  e.preventDefault();
  dropEl.classList.add('over');
});
dropEl.addEventListener('dragleave', () => dropEl.classList.remove('over'));
dropEl.addEventListener('drop', (e) => {
  e.preventDefault();
  dropEl.classList.remove('over');
  const f = e.dataTransfer?.files?.[0];
  if (f) void takeFile(f);
});

sampleBtn.addEventListener('click', () => {
  useText();
  deedsEl.value = SAMPLE_DEEDS;
  keysEl.value = SAMPLE_KEYS;
  fetchedKeys = null;
  sampleChip.hidden = false;
  void run();
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
    e.preventDefault();
    void run();
  }
});

verifyBtn.addEventListener('click', () => void run());

copyBtn.addEventListener('click', async () => {
  if (!lastReport) return;
  try {
    await navigator.clipboard.writeText(JSON.stringify(lastReport, null, 2) + '\n');
    statusEl.textContent = COPY.copied;
  } catch {
    statusEl.textContent = COPY.copyFailed;
  }
});

// --- verify ---

async function run(): Promise<void> {
  if (running) return;
  const hasDeeds = loadedFile !== null || deedsEl.value.trim() !== '';
  resetResult();
  if (!hasDeeds) {
    say('idle', '', COPY.noDeeds, COPY.empty);
    deedsEl.focus();
    return;
  }
  if (!(await ed25519Supported())) {
    say('unsupported', '○', COPY.unsupported, COPY.unsupportedNext);
    return;
  }
  running = true;
  verifyBtn.disabled = true;
  try {
    let keys: SealKeySet | null = null;
    const keyText = keysEl.value.trim();
    if (keyText !== '') {
      try {
        keys = await parseKeySet(keyText);
      } catch (e) {
        const ve = e as VerifyError;
        say('invalid', '✕', `${COPY.keysInvalid} (${ve.code}${ve.field ? ': ' + ve.field : ''})`, 'Paste the issuer’s keys.json exactly as published.');
        return;
      }
    }
    const pinned = fetchedKeys !== null && fetchedKeys.text.trim() === keyText;
    const v = new Verifier(keys, {
      keysPinned: pinned,
      keySource: pinned ? `https://${fetchedKeys!.host}` : 'pasted',
      onProgress: (n) => {
        progressEl.value = n;
        headlineEl.lastChild!.textContent = COPY.checking(fmt(n));
      },
    });
    let chain: boolean;
    let total: number;
    if (loadedFile) {
      const lines = splitLines(loadedFile.bytes);
      const head = new TextDecoder().decode(loadedFile.bytes.subarray(0, Math.min(loadedFile.bytes.length, 200_000))); // a sniff only
      chain = loadedFile.name.endsWith('.jsonl') || manyLines(head);
      total = chain ? lines.length : 1;
      begin(total);
      if (chain) await v.chain(lines, loadedFile.name);
      else await v.loose(trimEnd(loadedFile.bytes), loadedFile.name);
    } else {
      const t = deedsEl.value;
      chain = manyLines(t);
      const lines = chain ? splitTextLines(t.replace(/\s+$/, '') + '\n') : [];
      total = chain ? lines.length : 1;
      begin(total);
      if (chain) await v.chain(lines, 'pasted');
      else await v.loose(t.replace(/[\r\n]+$/, '').replace(/^\s+|\s+$/g, ''), 'pasted');
    }
    show(await v.report(), chain, pinned);
  } catch (e) {
    if (e instanceof Error && e.name === 'UnsupportedError') say('unsupported', '○', COPY.unsupported, COPY.unsupportedNext);
    else say('cannot', '○', 'The check stopped: ' + String(e), 'Try the CLI: deedglass verify');
  } finally {
    running = false;
    verifyBtn.disabled = false;
  }
}

function begin(total: number): void {
  say('checking', '◐', COPY.checking(fmt(total)));
  progressEl.max = total;
  progressEl.value = 0;
  progressEl.hidden = total < 2;
}

function trimEnd(b: Uint8Array): Uint8Array {
  let n = b.length;
  while (n > 0 && (b[n - 1] === 0x0a || b[n - 1] === 0x0d)) n--;
  return b.subarray(0, n);
}

function show(rep: Report, chain: boolean, pinned: boolean): void {
  lastReport = rep;
  progressEl.hidden = true;
  const f = rep.failures[0];
  if (f) {
    showFailure(rep, f);
  } else {
    const parts = [`${fmt(rep.intact)} ${rep.intact === 1 ? 'deed' : 'deeds'}`];
    if (chain) parts.push('chain intact');
    else parts.push('seals intact', 'chain not checked');
    parts.push(`${rep.seal_keys.length} ${rep.seal_keys.length === 1 ? 'seal key' : 'seal keys'}`);
    if (chain && rep.from && rep.to) parts.push(`${rep.from.slice(0, 10)} → ${rep.to.slice(0, 10)}`);
    say('valid', '✓', parts);
    if (!pinned) note(COPY.pastedKeys, COPY.pastedKeysNext);
    for (const w of rep.warnings) {
      if (w.code === 'test_keys') {
        const p = text('p', '', 'note');
        p.append(text('span', '○', 'sym'), document.createTextNode(w.message));
        noteEl.append(p);
        noteEl.hidden = false;
      }
    }
    if (rep.org) row('org', rep.org);
    if (rep.first_seq !== null && rep.last_seq !== null) {
      row('seq', `${fmt(rep.first_seq)} → ${fmt(rep.last_seq)}${chain ? ' (from genesis)' : ''}`);
    }
    const rec = document.createElement('div');
    rec.className = 'chips';
    const kinds: [string, string, string][] = [
      ['VERIFIED', 'verified', '✓'],
      ['MISMATCH', 'mismatch', '✕'],
      ['UNVERIFIABLE', 'unverifiable', '○'],
      ['none', 'neutral', '·'],
    ];
    for (const [k, cls, sym] of kinds) {
      const n = rep.verdicts_recorded[k];
      if (n) rec.append(chip(cls, sym, `${fmt(n)} ${k === 'none' ? 'no witness' : k}`));
    }
    row('recorded', rec);
    row('seal keys', rep.seal_keys.map((k) => `${k.id} (${fmt(k.first_seq)} → ${fmt(k.last_seq)})`).join(' · '));
    row('took', `${(rep.duration_ms / 1000).toFixed(2)} s`);
  }
  copyBtn.hidden = false;
}

function showFailure(rep: Report, f: Failure): void {
  if (f.code === 'keys_missing') {
    say('cannot', '○', COPY.noKeys, COPY.noKeysNext);
    return;
  }
  const cannot = cannotDetermine(f.code);
  say(cannot ? 'cannot' : 'invalid', cannot ? '○' : '✕', f.message, f.next);
  if (f.deed) row('deed', `${f.deed}${f.line !== null && rep.deeds > 1 ? ` (line ${fmt(f.line)})` : ''}`);
  if (f.expected && f.computed) {
    row('sealed', `body_hash ${shortHash(f.expected)}`);
    row('computed', shortHash(f.computed));
  }
  if (rep.intact > 0 && f.code !== 'chain_seq_gap') row('before it', `${fmt(rep.intact)} ${rep.intact === 1 ? 'deed' : 'deeds'} intact · stopped at the first break`);
  row('code', f.code);
}

idle();
