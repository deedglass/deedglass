// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { crossedAnnounce, label, nextDelay, urgent } from '../src/lib/countdown.ts';
import { Countdown } from '../src/controllers/countdown.ts';

const S = 1000;
const M = 60 * S;

describe('lib/countdown', () => {
  it('label: minutes rounded down from 2 min, seconds under it, Expired at 0', () => {
    expect(label(9 * M + 59 * S)).toBe('in 9 min');
    expect(label(2 * M)).toBe('in 2 min');
    expect(label(2 * M - 1)).toBe('in 1 min 59 s');
    expect(label(72 * S)).toBe('in 1 min 12 s');
    expect(label(60 * S)).toBe('in 1 min');
    expect(label(45 * S)).toBe('in 45 s');
    expect(label(44.2 * S)).toBe('in 45 s');
    expect(label(1)).toBe('in 1 s');
    expect(label(0)).toBe('Expired');
    expect(label(-5 * S)).toBe('Expired');
    expect(label(Number.NaN)).toBe('Expired');
  });
  it('urgent under 5 minutes only', () => {
    expect(urgent(5 * M)).toBe(false);
    expect(urgent(5 * M - 1)).toBe(true);
    expect(urgent(90 * S)).toBe(true);
    expect(urgent(0)).toBe(false);
  });
  it('cadence: 30 s ticks, cut to land on 5 and 2 minutes, then every second', () => {
    expect(nextDelay(10 * M)).toBe(30 * S);
    expect(nextDelay(5 * M + 10 * S)).toBe(10 * S);
    expect(nextDelay(4 * M)).toBe(30 * S);
    expect(nextDelay(2 * M + 20 * S)).toBe(20 * S);
    expect(nextDelay(2 * M + 31 * S)).toBe(30 * S);
    expect(nextDelay(2 * M)).toBe(S);
    expect(nextDelay(119_500)).toBe(500);
    expect(nextDelay(60 * S)).toBe(S);
    expect(nextDelay(1)).toBe(1);
    expect(nextDelay(0)).toBeNull();
    expect(nextDelay(-1)).toBeNull();
  });
  it('the announcement fires only when the 1 minute mark is crossed', () => {
    expect(crossedAnnounce(61 * S, 60 * S)).toBe(true);
    expect(crossedAnnounce(60 * S, 59 * S)).toBe(false);
    expect(crossedAnnounce(Number.NaN, 30 * S)).toBe(false);
    expect(crossedAnnounce(61 * S, 0)).toBe(false);
  });
});

describe('Countdown', () => {
  let t = 0;
  beforeEach(() => {
    vi.useFakeTimers();
    t = 1_000_000;
    document.body.replaceChildren();
  });
  afterEach(() => vi.useRealTimers());
  const now = () => t;
  const advance = (ms: number) => {
    // move the injected clock with the fake timers, one millisecond-exact step at a time
    let left = ms;
    while (left > 0) {
      const step = Math.min(left, 250);
      t += step;
      vi.advanceTimersByTime(step);
      left -= step;
    }
  };

  it('ticks every 30 s above 2 minutes and every second below, with no aria-live on the text', () => {
    const spy = vi.spyOn(globalThis, 'setTimeout');
    const cd = Countdown(t + 9 * M, { now, announce: () => {} });
    document.body.append(cd.el);
    expect(cd.el.textContent).toBe('in 9 min');
    expect(cd.el.tagName).toBe('TIME');
    expect(cd.el.hasAttribute('aria-live')).toBe(false);
    expect(cd.el.classList.contains('cd-urgent')).toBe(false);
    expect(spy.mock.calls.at(-1)![1]).toBe(30 * S);
    advance(4 * M + 30 * S); // 4:30 left
    expect(cd.el.textContent).toBe('in 4 min');
    expect(cd.el.classList.contains('cd-urgent')).toBe(true);
    expect(spy.mock.calls.at(-1)![1]).toBe(30 * S);
    advance(2 * M + 30 * S); // 2:00 left: switches to seconds
    expect(spy.mock.calls.at(-1)![1]).toBe(S);
    advance(48 * S); // 1:12
    expect(cd.el.textContent).toBe('in 1 min 12 s');
    cd.stop();
    spy.mockRestore();
  });

  it('announces exactly once at 1 minute, then expires once', () => {
    const said: string[] = [];
    let expired = 0;
    const cd = Countdown(t + 3 * M, { now, announce: (s) => said.push(s), onExpire: () => expired++ });
    document.body.append(cd.el);
    advance(2 * M + 30 * S);
    expect(said).toEqual(['1 minute left to decide.']);
    advance(31 * S);
    expect(cd.el.textContent).toBe('Expired');
    expect(cd.el.classList.contains('cd-urgent')).toBe(false);
    expect(expired).toBe(1);
    advance(5 * M);
    expect(said.length).toBe(1);
    expect(expired).toBe(1);
  });

  it('a countdown that starts under 1 minute does not announce; stop() halts it', () => {
    const said: string[] = [];
    const cd = Countdown(new Date(t + 45 * S), { now, announce: (s) => said.push(s) });
    document.body.append(cd.el);
    expect(cd.el.textContent).toBe('in 45 s');
    advance(5 * S);
    expect(cd.el.textContent).toBe('in 40 s');
    cd.stop();
    advance(10 * S);
    expect(cd.el.textContent).toBe('in 40 s');
    expect(said).toEqual([]);
  });

  it('uses one shared hidden live region when no announce is given', () => {
    Countdown(t + 9 * M, { now }).stop();
    Countdown(t + 4 * M, { now }).stop();
    const regions = document.querySelectorAll('[aria-live]');
    expect(regions.length).toBe(1);
    expect(regions[0]!.getAttribute('aria-live')).toBe('polite');
    expect(regions[0]!.classList.contains('sr-only')).toBe(true);
  });

  it('stops by itself once removed from the page', () => {
    const cd = Countdown(t + 90 * S, { now, announce: () => {} });
    document.body.append(cd.el);
    advance(2 * S);
    cd.el.remove();
    advance(2 * S);
    expect(vi.getTimerCount()).toBe(0);
  });
});
