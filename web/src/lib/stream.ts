// stream.ts: one EventSource per tab, shared by every component (S24 §3.9, §3.11).
//
// The native EventSource retries inside a connection and sends Last-Event-ID itself. When the
// server ends the stream for good (a 401, the 30-minute lifetime, too many connections) the
// browser closes it; we reopen with backoff (1, 2, 4, 8, 16, 30 s, ±20%) and pass ?last_event_id=,
// because a new EventSource can't set that header. `resync` tells callers to refetch their first
// page: the server couldn't promise nothing was missed. A tab hidden for 10 minutes closes its
// stream and resumes when it is visible again. Handlers run in one animation-frame batch.

import type { DeedglassEvent, StreamHello } from './types.ts';

export type StreamState = 'connecting' | 'live' | 'reconnecting' | 'offline' | 'ended';
export type EventFn = (e: DeedglassEvent) => void;

/** Every event type the server sends (contract §8); EventSource needs a listener per name. */
export const EVENT_TYPES = [
  'action.created', 'action.decided', 'action.executed', 'action.witnessed', 'deed.sealed',
  'approval.requested', 'approval.decided',
  'agent.stopped', 'agent.resumed', 'agent.suspended', 'org.stopped', 'org.started',
  'anomaly.detected', 'suggestion.created', 'policy.activated',
  'decoder.degraded', 'decoder.recovered', 'decoder.alert',
] as const;

export const BACKOFF_S = [1, 2, 4, 8, 16, 30] as const;
export const HIDDEN_CLOSE_MS = 10 * 60 * 1000;

/** nextDelay is the reconnect delay for an attempt (0-based) with ±20% jitter; r is in [0, 1). */
export function nextDelay(attempt: number, r: number): number {
  const base = BACKOFF_S[Math.min(attempt, BACKOFF_S.length - 1)]! * 1000;
  return Math.round(base * (0.8 + 0.4 * r));
}

/** matches reports whether a subscription pattern (a type, "prefix.*" or "*") covers a type. */
export function matches(pattern: string, type: string): boolean {
  if (pattern === '*') return true;
  if (pattern.endsWith('.*')) return type.startsWith(pattern.slice(0, -1));
  return pattern === type;
}

interface ESLike {
  readonly readyState: number;
  addEventListener(type: string, fn: (e: MessageEvent) => void): void;
  close(): void;
  onerror: ((e: Event) => void) | null;
  onopen: ((e: Event) => void) | null;
}

export interface StreamDeps {
  EventSource: new (url: string, init?: EventSourceInit) => ESLike;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (h: unknown) => void;
  random: () => number;
  now: () => number;
  raf: (fn: () => void) => void;
  /** probe answers 'unauthorized' when the session is gone (a 401 on /v1/me). */
  probe: () => Promise<'ok' | 'unauthorized' | 'offline'>;
  online: () => boolean;
  base: string;
}

export class Stream {
  private es: ESLike | null = null;
  private st: StreamState = 'connecting';
  private subs = new Map<string, Set<EventFn>>();
  private stateFns = new Set<(s: StreamState) => void>();
  private resyncFns = new Set<(reason: string) => void>();
  private queue: DeedglassEvent[] = [];
  private flushing = false;
  private attempt = 0;
  private timer: unknown = null;
  private lastId = '';
  private retryAtMs = 0;
  private hiddenTimer: unknown = null;
  private stopped = false;
  hello: StreamHello | null = null;

  constructor(private readonly d: StreamDeps) {}

  start(): void {
    this.stopped = false;
    if (!this.d.online()) {
      this.set('offline');
      return;
    }
    this.open();
  }

  stop(): void {
    this.stopped = true;
    this.close();
    if (this.timer !== null) this.d.clearTimeout(this.timer);
    this.timer = null;
  }

  /** retry reconnects now (a person pressed Retry); a live or stopped stream is left alone. */
  retry(): void {
    if (this.stopped || this.st === 'live') return;
    this.attempt = 0;
    this.reopen(0, true);
  }

  state(): StreamState {
    return this.st;
  }

  /** retryIn is the whole seconds until the next reconnect (0 when none is planned). */
  retryIn(): number {
    return this.retryAtMs ? Math.max(0, Math.ceil((this.retryAtMs - this.d.now()) / 1000)) : 0;
  }

  lastEventId(): string {
    return this.lastId;
  }

  on(type: string, fn: EventFn): () => void {
    let set = this.subs.get(type);
    if (!set) this.subs.set(type, (set = new Set()));
    set.add(fn);
    return () => set!.delete(fn);
  }

  onResync(fn: (reason: string) => void): () => void {
    this.resyncFns.add(fn);
    return () => this.resyncFns.delete(fn);
  }

  onState(fn: (s: StreamState) => void): () => void {
    this.stateFns.add(fn);
    return () => this.stateFns.delete(fn);
  }

  /** Browser hooks: wire them to visibilitychange, online and offline. */
  visibility(hidden: boolean): void {
    if (hidden) {
      if (this.hiddenTimer === null) this.hiddenTimer = this.d.setTimeout(() => { this.hiddenTimer = null; this.close(); this.set('ended'); }, HIDDEN_CLOSE_MS);
      return;
    }
    if (this.hiddenTimer !== null) {
      this.d.clearTimeout(this.hiddenTimer);
      this.hiddenTimer = null;
    }
    if (!this.es && !this.stopped && this.st === 'ended') this.reopen(0, true);
  }

  network(online: boolean): void {
    if (!online) {
      this.close();
      if (this.timer !== null) this.d.clearTimeout(this.timer);
      this.timer = null;
      this.set('offline');
    } else if (this.st === 'offline' && !this.stopped) {
      this.reopen(0, true);
    }
  }

  private url(): string {
    return this.lastId ? `${this.d.base}?last_event_id=${encodeURIComponent(this.lastId)}` : this.d.base;
  }

  private open(): void {
    this.close();
    this.set(this.attempt === 0 && !this.lastId ? 'connecting' : 'reconnecting');
    const es = new this.d.EventSource(this.url(), { withCredentials: false });
    this.es = es;
    es.addEventListener('hello', (m) => {
      try {
        this.hello = JSON.parse(m.data) as StreamHello;
      } catch {
        this.hello = null;
      }
      this.attempt = 0;
      this.retryAtMs = 0;
      this.set('live');
    });
    es.addEventListener('resync', (m) => {
      let reason = 'unknown';
      try {
        reason = (JSON.parse(m.data) as { reason?: string }).reason ?? reason;
      } catch { /* keep unknown */ }
      if (m.lastEventId) this.lastId = m.lastEventId;
      for (const fn of this.resyncFns) fn(reason);
    });
    es.addEventListener('session_ended', () => {
      this.close();
      this.set('ended');
      window.dispatchEvent(new CustomEvent('dg:session-ended'));
    });
    const onEvent = (m: MessageEvent) => {
      if (m.lastEventId) this.lastId = m.lastEventId;
      let ev: DeedglassEvent;
      try {
        ev = JSON.parse(m.data) as DeedglassEvent;
      } catch {
        return;
      }
      this.enqueue(ev);
    };
    for (const t of EVENT_TYPES) es.addEventListener(t, onEvent);
    es.onerror = () => {
      if (this.es !== es) return;
      if (es.readyState === 2) {
        // Closed for good: find out why, then back off.
        this.es = null;
        void this.d.probe().then((p) => {
          if (this.stopped) return;
          if (p === 'unauthorized') {
            this.set('ended');
            window.dispatchEvent(new CustomEvent('dg:session-ended'));
          } else if (p === 'offline' || !this.d.online()) {
            this.set('offline');
          } else {
            this.reopen(this.attempt++);
          }
        });
      } else {
        this.set('reconnecting');
      }
    };
  }

  private reopen(attempt: number, immediate = false): void {
    if (this.timer !== null) this.d.clearTimeout(this.timer);
    const delay = immediate ? 0 : nextDelay(attempt, this.d.random());
    this.retryAtMs = this.d.now() + delay;
    this.set('reconnecting');
    this.timer = this.d.setTimeout(() => {
      this.timer = null;
      this.open();
    }, delay);
  }

  private close(): void {
    if (this.es) {
      this.es.onerror = null;
      this.es.close();
      this.es = null;
    }
  }

  private set(s: StreamState): void {
    if (this.st === s) return;
    this.st = s;
    for (const fn of this.stateFns) fn(s);
  }

  private enqueue(ev: DeedglassEvent): void {
    this.queue.push(ev);
    if (this.flushing) return;
    this.flushing = true;
    this.d.raf(() => {
      this.flushing = false;
      const batch = this.queue;
      this.queue = [];
      for (const ev of batch) {
        for (const [pattern, fns] of this.subs) {
          if (!matches(pattern, ev.type)) continue;
          for (const fn of fns) {
            try {
              fn(ev);
            } catch (err) {
              console.error('stream handler failed', err);
            }
          }
        }
      }
    });
  }
}

let shared: Stream | null = null;

/** stream returns the tab's one stream, starting it on first use. */
export function stream(): Stream {
  if (shared) return shared;
  shared = new Stream({
    EventSource: EventSource as unknown as StreamDeps['EventSource'],
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (h) => clearTimeout(h as number),
    random: Math.random,
    now: () => Date.now(),
    raf: (fn) => requestAnimationFrame(() => fn()),
    probe: async () => {
      try {
        const r = await fetch('/v1/me', { credentials: 'same-origin', headers: { 'Deedglass-Version': '2026-10-01' }, cache: 'no-store' });
        return r.status === 401 ? 'unauthorized' : 'ok';
      } catch {
        return 'offline';
      }
    },
    online: () => navigator.onLine !== false,
    base: '/v1/events',
  });
  document.addEventListener('visibilitychange', () => shared?.visibility(document.visibilityState === 'hidden'));
  window.addEventListener('online', () => shared?.network(true));
  window.addEventListener('offline', () => shared?.network(false));
  shared.start();
  return shared;
}
