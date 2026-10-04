import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createLintLoop, MAX_POLICY_BYTES, tooLarge, utf8Bytes, type LintState } from '../src/lib/lintloop.ts';
import type { PolicyLintResult } from '../src/lib/policyfix.ts';

const clean = (name = 'p'): PolicyLintResult => ({ object: 'policy_lint_result', valid: true, name, rules: 2, budgets: 1, errors: [], warnings: [] });

interface Call {
  source: string;
  signal: AbortSignal;
  resolve: (r: PolicyLintResult) => void;
  reject: (e: unknown) => void;
}

function harness() {
  const calls: Call[] = [];
  const states: LintState[] = [];
  let inflight = 0;
  let maxInflight = 0;
  const loop = createLintLoop({
    lint: (source, signal) => new Promise<PolicyLintResult>((resolve, reject) => {
      inflight++;
      maxInflight = Math.max(maxInflight, inflight);
      const done = () => { inflight--; };
      signal.addEventListener('abort', done);
      calls.push({ source, signal, resolve: (r) => { if (!signal.aborted) done(); resolve(r); }, reject: (e) => { if (!signal.aborted) done(); reject(e); } });
    }),
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
    now: () => Date.now(),
  }, (s) => states.push(s));
  return { loop, calls, states, maxInflight: () => maxInflight, last: () => states[states.length - 1] };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('lint loop', () => {
  it('debounces 400 ms after the last change', async () => {
    const t = harness();
    t.loop.change('a');
    await vi.advanceTimersByTimeAsync(300);
    t.loop.change('ab');
    await vi.advanceTimersByTimeAsync(300);
    t.loop.change('abc');
    await vi.advanceTimersByTimeAsync(399);
    expect(t.calls).toHaveLength(0);
    expect(t.last()).toEqual({ kind: 'pending' });
    await vi.advanceTimersByTimeAsync(1);
    expect(t.calls.map((c) => c.source)).toEqual(['abc']);
    expect(t.last()).toEqual({ kind: 'linting' });
    t.calls[0]!.resolve(clean());
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toMatchObject({ kind: 'result', source: 'abc', result: { valid: true } });
  });

  it('aborts the stale call: never more than one in flight, and a stale answer is dropped', async () => {
    const t = harness();
    t.loop.change('one');
    await vi.advanceTimersByTimeAsync(400);
    t.loop.change('two');
    await vi.advanceTimersByTimeAsync(400);
    expect(t.calls).toHaveLength(2);
    expect(t.calls[0]!.signal.aborted).toBe(true);
    expect(t.calls[1]!.signal.aborted).toBe(false);
    t.loop.flush('three');
    expect(t.calls[1]!.signal.aborted).toBe(true);
    expect(t.maxInflight()).toBe(1);
    t.calls[0]!.resolve(clean('one'));
    t.calls[1]!.resolve(clean('two'));
    await vi.advanceTimersByTimeAsync(0);
    expect(t.states.some((s) => s.kind === 'result')).toBe(false);
    t.calls[2]!.resolve(clean('three'));
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toMatchObject({ kind: 'result', source: 'three' });
  });

  it('pauses for 20 seconds on a 429, then lints the latest text', async () => {
    const t = harness();
    t.loop.change('v1');
    await vi.advanceTimersByTimeAsync(400);
    t.calls[0]!.reject(Object.assign(new Error('Too many requests.'), { status: 429 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toMatchObject({ kind: 'paused', message: 'Lint paused for 20 seconds (rate limit).' });
    t.loop.change('v2');
    t.loop.change('v3');
    await vi.advanceTimersByTimeAsync(19_999);
    expect(t.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(t.calls.map((c) => c.source)).toEqual(['v1', 'v3']);
    t.calls[1]!.resolve(clean());
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toMatchObject({ kind: 'result', source: 'v3' });
    // Back to normal: changes debounce again.
    t.loop.change('v4');
    await vi.advanceTimersByTimeAsync(400);
    expect(t.calls).toHaveLength(3);
  });

  it('after a pause, text that was already linted goes idle without a call', async () => {
    const t = harness();
    t.loop.flush('x');
    t.calls[0]!.resolve(clean());
    await vi.advanceTimersByTimeAsync(0);
    t.loop.flush('y');
    t.calls[1]!.reject({ status: 429 });
    await vi.advanceTimersByTimeAsync(0);
    t.loop.change('x'); // back to the linted text during the pause
    await vi.advanceTimersByTimeAsync(20_000);
    expect(t.calls).toHaveLength(2);
    expect(t.last()).toEqual({ kind: 'idle' });
  });

  it('shows the server message for other errors', async () => {
    const t = harness();
    t.loop.flush('x');
    t.calls[0]!.reject(Object.assign(new Error("Deedglass couldn't reach the server. Check your connection and try again."), { status: 0 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(t.last()).toEqual({ kind: 'error', message: "Deedglass couldn't reach the server. Check your connection and try again." });
  });

  it('refuses over 256 KiB of UTF-8 and sends nothing', async () => {
    expect(MAX_POLICY_BYTES).toBe(262_144);
    expect(tooLarge('a'.repeat(262_144))).toBeNull();
    expect(tooLarge('a'.repeat(262_145))).toBe(262_145);
    expect(tooLarge('é'.repeat(131_073))).toBe(262_146); // 2 bytes each, under the length limit
    expect(tooLarge('😀'.repeat(65_536))).toBeNull(); // 4 bytes each: exactly 256 KiB
    expect(utf8Bytes('a·😀€')).toBe(1 + 2 + 4 + 3);
    const t = harness();
    t.loop.change('small');
    t.loop.change('x'.repeat(262_145));
    await vi.advanceTimersByTimeAsync(5_000);
    expect(t.calls).toHaveLength(0);
    expect(t.last()).toMatchObject({ kind: 'too_large', message: 'Policies are limited to 256 KiB. Split this one.' });
    t.loop.flush();
    expect(t.calls).toHaveLength(0);
    // An in-flight call is cancelled when the text grows past the limit.
    t.loop.flush('ok');
    t.loop.change('é'.repeat(131_073));
    expect(t.calls[0]!.signal.aborted).toBe(true);
    t.loop.change('fine again');
    await vi.advanceTimersByTimeAsync(400);
    expect(t.calls.map((c) => c.source)).toEqual(['ok', 'fine again']);
  });

  it('dispose stops timers and cancels the call', async () => {
    const t = harness();
    t.loop.flush('a');
    t.loop.change('b');
    t.loop.dispose();
    expect(t.calls[0]!.signal.aborted).toBe(true);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(t.calls).toHaveLength(1);
  });
});
