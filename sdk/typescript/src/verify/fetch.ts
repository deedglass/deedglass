// fetchKeys (S31 §3.2): https only (http only for 127.0.0.1 and localhost), no redirects, no
// credentials, no referrer, at most 1 MiB read as a stream, a timeout. Not part of the copied core.

import { parseKeySet, type SealKeySet } from './keys.js';

export const KEYS_MAX_BYTES: number = 1 << 20;
export const KEYS_TIMEOUT_MS: number = 10_000;

export class FetchKeysError extends Error {
  readonly host: string;
  readonly reason: string;
  constructor(host: string, reason: string) {
    super(`Couldn't fetch seal keys from ${host} (${reason}).`);
    this.name = 'FetchKeysError';
    this.host = host;
    this.reason = reason;
  }
}

/** A key URL the verifier may fetch, or null: https anywhere, http only on this machine. */
export function keysURL(raw: string): URL | null {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  if (u.username || u.password) return null;
  if (u.protocol === 'https:') return u;
  if (u.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(u.hostname)) return u;
  return null;
}

export interface FetchKeysResult {
  keys: SealKeySet;
  /** The set came from the URL the caller named, not from the deeds themselves. */
  pinned: true;
  source: string;
}

/** Fetches and parses a seal key set. */
export async function fetchKeys(url: string | URL, o: { fetch?: typeof fetch; timeoutMs?: number; signal?: AbortSignal } = {}): Promise<FetchKeysResult> {
  const u = keysURL(String(url));
  if (!u) throw new FetchKeysError(String(url).slice(0, 80), 'seal keys are fetched over https, or http only from localhost or 127.0.0.1');
  const f = o.fetch ?? globalThis.fetch;
  const timeout = AbortSignal.timeout(o.timeoutMs ?? KEYS_TIMEOUT_MS);
  const signal = o.signal ? AbortSignal.any([o.signal, timeout]) : timeout;
  let res: Response;
  try {
    res = await f(u.toString(), {
      method: 'GET',
      credentials: 'omit',
      redirect: 'error',
      referrerPolicy: 'no-referrer',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
      signal,
    });
  } catch {
    throw new FetchKeysError(u.host, timeout.aborted ? `timed out after ${Math.round((o.timeoutMs ?? KEYS_TIMEOUT_MS) / 1000)} s` : 'the request was refused, redirected or blocked');
  }
  if (!res.ok) throw new FetchKeysError(u.host, `it answered ${res.status}`);
  if (!res.body) throw new FetchKeysError(u.host, 'the answer was empty');
  const reader = res.body.getReader();
  const parts: Uint8Array[] = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    if (n > KEYS_MAX_BYTES) {
      await reader.cancel();
      throw new FetchKeysError(u.host, 'the answer is larger than 1 MiB');
    }
    parts.push(value);
  }
  const out = new Uint8Array(n);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return { keys: await parseKeySet(out), pinned: true, source: u.toString() };
}
