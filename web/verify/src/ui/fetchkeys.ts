// The page's only network call: GET the seal keys the person named, after they press Fetch keys.
// No cookies, no referrer, no redirects, at most 1 MiB, at most 10 s. Nothing pasted is ever sent.

export const KEYS_MAX = 1 << 20;
export const KEYS_TIMEOUT_MS = 10_000;

export class FetchKeysError extends Error {
  constructor(
    readonly host: string,
    readonly reason: string,
  ) {
    super(`Could not fetch seal keys from ${host} (${reason}).`);
    this.name = 'FetchKeysError';
  }
}

/** Parses a key URL: https anywhere, http only for this machine. Returns null when not allowed. */
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

export async function fetchKeys(u: URL): Promise<Uint8Array> {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), KEYS_TIMEOUT_MS);
  try {
    let res: Response;
    try {
      res = await fetch(u, {
        method: 'GET',
        credentials: 'omit',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        cache: 'no-store',
        mode: 'cors',
        headers: { Accept: 'application/json' },
        signal: ctl.signal,
      });
    } catch {
      throw new FetchKeysError(u.host, ctl.signal.aborted ? 'timed out after 10 s' : 'the request was refused, redirected or blocked');
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
      if (n > KEYS_MAX) {
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
    return out;
  } finally {
    clearTimeout(timer);
  }
}
