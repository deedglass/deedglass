// The transport (S31 §3.3): headers, timeouts, retries and error envelopes. Retried: network
// errors, 408, 409 idempotency_key_in_use, 429 rate_limited (never budget_exceeded), 502, 503 and
// 504, as the Python SDK does (S35 parity). Never a 500: the server stores a 5xx answer under the
// Idempotency-Key and replays it, so a retry can't change the outcome. A backoff of
// 500 ms × 2^n ± 25%, honouring Retry-After up to 10 s, never beyond the caller's signal or timeout.
// A POST is retried only with the same Idempotency-Key, so the server never runs a tool twice.

import { copy } from './copy.js';
import { APIError, ConnectionError, type DeedglassError, fromEnvelope, isDeedglassError, TimeoutError } from './errors.js';
import { isBrowser, runtime } from './runtime.js';
import type { Action, ResponseMeta } from './types.js';
import { API_VERSION, VERSION } from './version.js';

/** s without trailing slashes. A loop, not /\/+$/, which backtracks quadratically on long runs of "/". */
export function trimSlashes(s: string): string {
  let end = s.length;
  while (end > 0 && s.charCodeAt(end - 1) === 47) end--;
  return s.slice(0, end);
}

export interface TransportOptions {
  baseUrl: string;
  auth: () => string;
  timeoutMs: number;
  maxRetries: number;
  fetch: typeof fetch;
  appInfo?: { name: string; version?: string } | undefined;
  /** Waits between attempts; tests replace it. */
  sleep?: ((ms: number, signal: AbortSignal) => Promise<void>) | undefined;
}

export interface Req {
  method: 'GET' | 'POST';
  path: string;
  query?: Record<string, string | string[] | undefined>;
  body?: unknown;
  headers?: Record<string, string | undefined>;
  idempotencyKey?: string;
  signal?: AbortSignal | undefined;
  timeoutMs?: number | undefined;
  /** The action this request is about, for TimeoutError's message. */
  actionId?: string | undefined;
}

export interface Res<T> {
  data: T;
  meta: ResponseMeta;
  headers: Headers;
}

const RETRY_STATUSES = new Set([408, 409, 429, 502, 503, 504]);
const MAX_RETRY_AFTER_MS = 10_000;

export function retryAfterMs(h: Headers): number {
  const v = h.get('retry-after');
  if (!v) return 0;
  const n = Number(v);
  if (Number.isFinite(n) && n >= 0) return Math.round(n * 1000);
  const at = Date.parse(v);
  return Number.isFinite(at) ? Math.max(0, at - Date.now()) : 0;
}

export function backoffMs(attempt: number, rand: () => number = Math.random): number {
  const base = 500 * 2 ** attempt;
  return Math.round(base * (0.75 + rand() * 0.5));
}

function defaultSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) return reject(signal.reason);
    const t = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(t);
      reject(signal.reason);
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

/** Normalizes an action object the server sent: adds `meta`. */
export function withMeta<T extends object>(obj: T, meta: ResponseMeta): T & { meta: ResponseMeta } {
  Object.defineProperty(obj, 'meta', { value: meta, enumerable: false, writable: true, configurable: true });
  return obj as T & { meta: ResponseMeta };
}

export class Transport {
  readonly #o: TransportOptions;
  readonly #ua: string | null;

  constructor(o: TransportOptions) {
    this.#o = o;
    let ua = `deedglass-sdk-typescript/${VERSION} (${runtime()})`;
    if (o.appInfo) ua += ` ${o.appInfo.name}${o.appInfo.version ? '/' + o.appInfo.version : ''}`;
    this.#ua = isBrowser() ? null : ua;
  }

  get baseUrl(): string {
    return this.#o.baseUrl;
  }

  url(path: string, query?: Req['query']): string {
    const u = new URL(trimSlashes(this.#o.baseUrl) + path);
    for (const [k, v] of Object.entries(query ?? {})) {
      if (v === undefined) continue;
      for (const x of Array.isArray(v) ? v : [v]) u.searchParams.append(k, x);
    }
    return u.toString();
  }

  async request<T>(r: Req): Promise<Res<T>> {
    const timeoutMs = r.timeoutMs ?? this.#o.timeoutMs;
    const timeout = AbortSignal.timeout(timeoutMs);
    const signal = r.signal ? AbortSignal.any([r.signal, timeout]) : timeout;
    const sleep = this.#o.sleep ?? defaultSleep;
    const headers: Record<string, string> = {
      Authorization: 'Bearer ' + this.#o.auth(),
      'Deedglass-Version': API_VERSION,
      Accept: 'application/json',
    };
    if (this.#ua) headers['User-Agent'] = this.#ua;
    if (r.method === 'POST') headers['Idempotency-Key'] = r.idempotencyKey ?? '';
    for (const [k, v] of Object.entries(r.headers ?? {})) if (v !== undefined) headers[k] = v;
    let body: string | undefined;
    if (r.body !== undefined) {
      body = JSON.stringify(r.body);
      headers['Content-Type'] = 'application/json';
    }
    const url = this.url(r.path, r.query);
    const timedOut = (): TimeoutError =>
      new TimeoutError({
        type: 'client_error',
        code: 'timeout',
        message: copy.timeout(Math.round(timeoutMs / 1000), r.actionId),
        ...(r.actionId ? { actionId: r.actionId } : {}),
      });
    for (let attempt = 0; ; attempt++) {
      let resp: Response;
      try {
        resp = await this.#o.fetch(url, { method: r.method, headers, body: body ?? null, signal, redirect: 'error' });
      } catch (err) {
        if (r.signal?.aborted) throw r.signal.reason ?? err;
        if (timeout.aborted) throw timedOut();
        if (attempt < this.#o.maxRetries) {
          if (!(await this.#wait(sleep, backoffMs(attempt), signal))) throw r.signal?.aborted ? r.signal.reason : timedOut();
          continue;
        }
        throw new ConnectionError({ type: 'client_error', code: 'connection_error', message: copy.connection(new URL(url).host), cause: err });
      }
      const requestId = resp.headers.get('deedglass-request-id');
      const meta: ResponseMeta = { requestId, replayed: resp.headers.get('deedglass-idempotent-replayed') === 'true', status: resp.status };
      let parsed: unknown;
      let text = '';
      try {
        text = await resp.text();
      } catch (err) {
        if (r.signal?.aborted) throw r.signal.reason ?? err;
        if (timeout.aborted) throw timedOut();
        throw new ConnectionError({ type: 'client_error', code: 'connection_error', message: copy.connection(new URL(url).host), cause: err });
      }
      try {
        parsed = text === '' ? null : JSON.parse(text);
      } catch {
        parsed = undefined;
      }
      if (resp.status >= 200 && resp.status < 300) {
        if (parsed === undefined || parsed === null || typeof parsed !== 'object') {
          throw new APIError({ type: 'api_error', code: 'unreadable_response', message: copy.unreadable(resp.status), status: resp.status, requestId });
        }
        return { data: parsed as T, meta, headers: resp.headers };
      }
      const err = this.#error(resp, parsed, requestId);
      const retryable =
        RETRY_STATUSES.has(resp.status) &&
        !(resp.status === 429 && err.code !== 'rate_limited') &&
        !(resp.status === 409 && err.code !== 'idempotency_key_in_use') &&
        attempt < this.#o.maxRetries;
      if (!retryable) throw err;
      let wait = backoffMs(attempt);
      const ra = retryAfterMs(resp.headers);
      if (ra > 0) wait = Math.min(ra, MAX_RETRY_AFTER_MS);
      if (!(await this.#wait(sleep, wait, signal))) throw r.signal?.aborted ? r.signal.reason : timedOut();
    }
  }

  async #wait(sleep: NonNullable<TransportOptions['sleep']>, ms: number, signal: AbortSignal): Promise<boolean> {
    try {
      await sleep(ms, signal);
      return !signal.aborted;
    } catch {
      return false;
    }
  }

  #error(resp: Response, parsed: unknown, requestId: string | null): DeedglassError {
    const ra = retryAfterMs(resp.headers);
    if (parsed === undefined || parsed === null || typeof parsed !== 'object' || !('error' in parsed)) {
      return fromEnvelope(resp.status, { error: { message: copy.unreadable(resp.status) } }, requestId, ra);
    }
    const raw = (parsed as { error: { action?: unknown } }).error.action;
    let action: Action | undefined;
    if (raw && typeof raw === 'object') action = withMeta(raw as Action, { requestId, replayed: false, status: resp.status });
    return fromEnvelope(resp.status, parsed, requestId, ra, action);
  }
}

export { isDeedglassError };
