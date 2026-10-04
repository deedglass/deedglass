import type { Transport } from '../http.js';
import { withMeta } from '../http.js';
import { newIdempotencyKey } from '../idempotency.js';
import type { Action, ActionStatus, EdgeResult } from '../types.js';

/** Statuses after which nothing more happens without a new call. */
export const SETTLED: ReadonlySet<ActionStatus> = new Set<ActionStatus>(['denied', 'executed', 'failed', 'expired', 'cancelled', 'authorized']);

const ACTION_ID = /^act_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

function path(id: string, suffix = ''): string {
  if (!ACTION_ID.test(id)) throw new TypeError(`${JSON.stringify(id.slice(0, 40))} isn't an action id (act_…).`);
  return `/v1/actions/${id}${suffix}`;
}

export interface GetActionOptions {
  /** Server-side long poll, 0 to 25,000 ms: answers early when the action changes. */
  waitMs?: number;
  expand?: ('request' | 'result')[];
  signal?: AbortSignal;
}

export class Actions {
  readonly #t: Transport;
  constructor(t: Transport) {
    this.#t = t;
  }

  /** GET /v1/actions/{id}, optionally waiting up to 25 s for a change. */
  async get<R = unknown>(id: string, o: GetActionOptions = {}): Promise<Action<R>> {
    const waitMs = Math.max(0, Math.min(o.waitMs ?? 0, 25_000));
    const query: Record<string, string | string[] | undefined> = {};
    if (waitMs > 0) query.wait = String(waitMs / 1000);
    if (o.expand?.length) query['expand[]'] = o.expand;
    const r = await this.#t.request<Action<R>>({ method: 'GET', path: path(id), query, signal: o.signal, timeoutMs: undefined, actionId: id, ...(waitMs ? { timeoutMs: waitMs + 30_000 } : {}) });
    return withMeta(r.data, r.meta);
  }

  /** Long-polls GET ?wait=25 until the action is settled (anything but processing or pending_approval). */
  async waitUntilSettled<R = unknown>(id: string, o: { timeoutMs?: number; signal?: AbortSignal } = {}): Promise<Action<R>> {
    const deadline = Date.now() + (o.timeoutMs ?? 600_000);
    for (;;) {
      const left = deadline - Date.now();
      const a = await this.get<R>(id, { waitMs: Math.max(0, Math.min(25_000, left)), ...(o.signal ? { signal: o.signal } : {}) });
      if (SETTLED.has(a.status) || Date.now() >= deadline) return a;
    }
  }

  /** POST /v1/actions/{id}/cancel: only while pending_approval. */
  async cancel(id: string): Promise<Action> {
    const r = await this.#t.request<Action>({ method: 'POST', path: path(id, '/cancel'), idempotencyKey: newIdempotencyKey(), actionId: id });
    return withMeta(r.data, r.meta);
  }

  /** POST /v1/actions/{id}/result: proxy mode, after your code ran. */
  async reportResult(id: string, r: EdgeResult, o: { idempotencyKey?: string; signal?: AbortSignal } = {}): Promise<Action> {
    const res = await this.#t.request<Action>({
      method: 'POST',
      path: path(id, '/result'),
      body: r,
      idempotencyKey: o.idempotencyKey ?? 'sdkr_' + id,
      signal: o.signal,
      actionId: id,
    });
    return withMeta(res.data, res.meta);
  }
}
