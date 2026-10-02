// @vitest-environment happy-dom
import { describe, expect, it, vi } from 'vitest';
import { BACKOFF_S, matches, nextDelay, Stream, type StreamDeps } from '../src/lib/stream.ts';

class FakeES {
  static all: FakeES[] = [];
  readyState = 0;
  listeners = new Map<string, ((e: MessageEvent) => void)[]>();
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeES.all.push(this);
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) {
    this.listeners.set(t, [...(this.listeners.get(t) ?? []), fn]);
  }
  emit(type: string, data: unknown, id = '') {
    for (const fn of this.listeners.get(type) ?? []) fn({ data: JSON.stringify(data), lastEventId: id } as MessageEvent);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
}

function deps(over: Partial<StreamDeps> = {}): StreamDeps {
  return {
    EventSource: FakeES as never,
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (h) => clearTimeout(h as number),
    random: () => 0.5,
    now: () => Date.now(),
    raf: (fn) => fn(),
    probe: async () => 'ok',
    online: () => true,
    base: '/v1/events',
    ...over,
  };
}

const ev = (type: string) => ({ object: 'event', id: 'evt_x', type, api_version: '2026-10-01', created_at: '', livemode: false, org: 'org_x', hydrated: true, data: { object: { id: 'act_x' } } });

describe('stream', () => {
  it('backs off 1, 2, 4, 8, 16, 30 s within ±20%', () => {
    for (let a = 0; a < 9; a++) {
      const base = BACKOFF_S[Math.min(a, 5)]! * 1000;
      expect(nextDelay(a, 0)).toBe(Math.round(base * 0.8));
      expect(nextDelay(a, 0.999999)).toBeLessThanOrEqual(Math.round(base * 1.2));
    }
  });
  it('matches patterns', () => {
    expect(matches('*', 'deed.sealed')).toBe(true);
    expect(matches('action.*', 'action.witnessed')).toBe(true);
    expect(matches('action.*', 'approval.decided')).toBe(false);
    expect(matches('deed.sealed', 'deed.sealed')).toBe(true);
  });
  it('goes live on hello, dispatches events in one batch, keeps the cursor', () => {
    FakeES.all = [];
    const s = new Stream(deps());
    const states: string[] = [];
    s.onState((x) => states.push(x));
    const got: string[] = [];
    s.on('action.*', (e) => got.push(e.type));
    s.start();
    const es = FakeES.all[0]!;
    expect(es.url).toBe('/v1/events');
    es.emit('hello', { object: 'stream', instance: '7f3k2m', resumed: false, heartbeat_s: 15, server_time: '' });
    expect(s.state()).toBe('live');
    es.emit('action.created', ev('action.created'), '7f3k2m.1');
    es.emit('deed.sealed', ev('deed.sealed'), '7f3k2m.2');
    es.emit('action.witnessed', ev('action.witnessed'), '7f3k2m.3');
    expect(got).toEqual(['action.created', 'action.witnessed']);
    expect(s.lastEventId()).toBe('7f3k2m.3');
    expect(states).toContain('live');
  });
  it('reopens a closed stream with backoff and ?last_event_id=', async () => {
    vi.useFakeTimers();
    FakeES.all = [];
    const s = new Stream(deps({ now: () => Date.now() }));
    s.start();
    const first = FakeES.all[0]!;
    first.emit('hello', {});
    first.emit('action.created', ev('action.created'), '7f3k2m.42');
    first.readyState = 2;
    first.onerror?.(new Event('error'));
    await vi.advanceTimersByTimeAsync(0);
    expect(s.state()).toBe('reconnecting');
    expect(s.retryIn()).toBe(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(FakeES.all.length).toBe(2);
    expect(FakeES.all[1]!.url).toBe('/v1/events?last_event_id=7f3k2m.42');
    // a second close backs off longer
    FakeES.all[1]!.readyState = 2;
    FakeES.all[1]!.onerror?.(new Event('error'));
    await vi.advanceTimersByTimeAsync(1500);
    expect(FakeES.all.length).toBe(2);
    await vi.advanceTimersByTimeAsync(600);
    expect(FakeES.all.length).toBe(3);
    vi.useRealTimers();
  });
  it('a 401 ends the stream and asks for sign-in', async () => {
    FakeES.all = [];
    const ended = vi.fn();
    window.addEventListener('dg:session-ended', ended);
    const s = new Stream(deps({ probe: async () => 'unauthorized' }));
    s.start();
    FakeES.all[0]!.readyState = 2;
    FakeES.all[0]!.onerror?.(new Event('error'));
    await Promise.resolve();
    await Promise.resolve();
    expect(s.state()).toBe('ended');
    expect(ended).toHaveBeenCalled();
  });
  it('passes resync to callers and session_ended closes', () => {
    FakeES.all = [];
    const s = new Stream(deps());
    const reasons: string[] = [];
    s.onResync((r) => reasons.push(r));
    s.start();
    const es = FakeES.all[0]!;
    es.emit('resync', { object: 'resync', reason: 'cursor_expired' });
    expect(reasons).toEqual(['cursor_expired']);
    es.emit('session_ended', { code: 'session_expired' });
    expect(es.closed).toBe(true);
    expect(s.state()).toBe('ended');
  });
  it('closes after 10 hidden minutes and resumes when visible', async () => {
    vi.useFakeTimers();
    FakeES.all = [];
    const s = new Stream(deps());
    s.start();
    FakeES.all[0]!.emit('hello', {});
    s.visibility(true);
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000);
    expect(FakeES.all[0]!.closed).toBe(true);
    expect(s.state()).toBe('ended');
    s.visibility(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeES.all.length).toBe(2);
    vi.useRealTimers();
  });
  it('goes offline and back', async () => {
    vi.useFakeTimers();
    FakeES.all = [];
    const s = new Stream(deps());
    s.start();
    s.network(false);
    expect(s.state()).toBe('offline');
    s.network(true);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeES.all.length).toBe(2);
    vi.useRealTimers();
  });
});
