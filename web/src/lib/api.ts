// api.ts: the only way the Console talks to the server (S24 §3.9). Same origin, the session
// cookie, Deedglass-Version on every call, CSRF and an Idempotency-Key on every mutation (reused on
// retry), a 15 s timeout, and no redirects followed. Errors keep the server's message verbatim: it
// already ends with a next step.

import type { ApiErrorBody, ListResponse } from './types.ts';

export const API_VERSION = '2026-10-01';
export type Method = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
type Query = Record<string, string | string[] | undefined>;

export class ApiError extends Error {
  readonly status: number;
  readonly type: string;
  readonly code: string;
  readonly param: string | null;
  readonly requestId: string;
  readonly docUrl: string;
  constructor(status: number, body: ApiErrorBody['error'] | null, fallback: string) {
    super(body?.message ?? fallback);
    this.name = 'ApiError';
    this.status = status;
    this.type = body?.type ?? (status === 0 ? 'network_error' : 'api_error');
    this.code = body?.code ?? (status === 0 ? 'network_error' : 'internal_error');
    this.param = body?.param ?? null;
    this.requestId = body?.request_id ?? '';
    this.docUrl = body?.doc_url ?? '';
  }
}

export interface ApiOptions {
  body?: unknown;
  query?: Query;
  idempotencyKey?: string;
  signal?: AbortSignal;
}

/** The pieces api() needs from the page; tests replace them. */
export interface ApiEnv {
  fetch: typeof fetch;
  csrf: () => string | null;
  refreshCsrf: () => Promise<void>;
  emit: (type: 'dg:session-ended' | 'dg:offline' | 'dg:online', detail?: unknown) => void;
  sleep: (ms: number) => Promise<void>;
  uuid: () => string;
  timeoutMs: number;
}

const defaultEnv = (): ApiEnv => ({
  fetch: (...a) => fetch(...a),
  csrf: () => null,
  refreshCsrf: async () => {},
  emit: (type, detail) => window.dispatchEvent(new CustomEvent(type, { detail })),
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
  uuid: () => crypto.randomUUID(),
  timeoutMs: 15_000,
});

let env: ApiEnv | null = null;
function e(): ApiEnv {
  env ??= defaultEnv();
  return env;
}

/** configure replaces parts of the environment (state.ts wires CSRF; tests wire everything). */
export function configure(over: Partial<ApiEnv>): void {
  env = { ...e(), ...over };
}

export function url(path: string, query?: Query): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v === undefined) continue;
    for (const one of Array.isArray(v) ? v : [v]) q.append(k, one);
  }
  const qs = q.toString();
  return qs ? `${path}?${qs}` : path;
}

const MUTATIONS = new Set<Method>(['POST', 'PATCH', 'PUT', 'DELETE']);

export async function api<T>(method: Method, path: `/v1/${string}`, o: ApiOptions = {}): Promise<T> {
  const env = e();
  const mutation = MUTATIONS.has(method);
  const key = mutation ? (o.idempotencyKey ?? env.uuid()) : undefined;
  let csrfRetried = false;
  let rateRetried = false;
  for (;;) {
    const headers: Record<string, string> = { Accept: 'application/json', 'Deedglass-Version': API_VERSION };
    if (o.body !== undefined) headers['Content-Type'] = 'application/json';
    if (mutation) {
      const csrf = env.csrf();
      if (csrf) headers['Deedglass-CSRF'] = csrf;
      if (method === 'POST' && key) headers['Idempotency-Key'] = key;
    }
    const signals = [AbortSignal.timeout(env.timeoutMs)];
    if (o.signal) signals.push(o.signal);
    let res: Response;
    try {
      res = await env.fetch(url(path, o.query), {
        method,
        headers,
        body: o.body === undefined ? undefined : JSON.stringify(o.body),
        credentials: 'same-origin',
        redirect: 'error',
        cache: 'no-store',
        signal: AbortSignal.any(signals),
      });
    } catch (err) {
      if (o.signal?.aborted) throw err;
      env.emit('dg:offline');
      throw new ApiError(0, null, "Deedglass couldn't reach the server. Check your connection and try again.");
    }
    env.emit('dg:online');
    if (res.status === 204) return undefined as T;
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      body = null;
    }
    if (res.ok) return body as T;
    const err = new ApiError(res.status, (body as ApiErrorBody | null)?.error ?? null, `The server answered ${res.status}.`);
    if (err.code === 'csrf_failed' && !csrfRetried) {
      csrfRetried = true;
      await env.refreshCsrf();
      continue;
    }
    if (res.status === 429 && !rateRetried) {
      const after = Number(res.headers.get('Retry-After') ?? '1');
      if (Number.isFinite(after) && after > 0 && after <= 30) {
        rateRetried = true;
        await env.sleep(after * 1000);
        continue;
      }
    }
    if (res.status === 401) env.emit('dg:session-ended', err);
    throw err;
  }
}

/** list follows next_cursor, yielding one page at a time. */
export async function* list<T>(path: `/v1/${string}`, query: Record<string, string> = {}): AsyncGenerator<T[]> {
  let cursor: string | undefined;
  for (;;) {
    const page = await api<ListResponse<T>>('GET', path, { query: { ...query, cursor } });
    yield page.data;
    if (!page.has_more || !page.next_cursor) return;
    cursor = page.next_cursor;
  }
}
