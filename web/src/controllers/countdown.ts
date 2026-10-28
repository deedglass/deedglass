// countdown.ts: Countdown (S26 §3.1). A <time> that reads `in 9 min`, ticking on lib/countdown's
// cadence. No aria-live on the ticking text: one polite announcement at 1 minute left, through the
// caller's announce() or a shared visually hidden live region. Stops itself once expired or once
// the element leaves the page.

import { announcement, crossedAnnounce, label, nextDelay, urgent } from '../lib/countdown.ts';
import { h } from '../lib/dom.ts';

export interface CountdownOptions {
  /** The clock (tests). */
  now?: () => number;
  onExpire?: () => void;
  /** Says a sentence politely; defaults to a shared visually hidden live region. */
  announce?: (text: string) => void;
}

export interface Countdown {
  el: HTMLTimeElement;
  stop(): void;
}

const LIVE_ID = 'dg-countdown-live';

/** liveRegion: one shared polite region for every countdown, created before it's needed. */
function liveRegion(): HTMLElement {
  let r = document.getElementById(LIVE_ID);
  if (!r) {
    r = h('div', { id: LIVE_ID, class: 'sr-only', 'aria-live': 'polite', 'aria-atomic': 'true' });
    document.body.appendChild(r);
  }
  return r;
}

function defaultAnnounce(region: HTMLElement): (text: string) => void {
  return (text) => {
    region.textContent = '';
    // A fresh text node after a clear is what screen readers reliably pick up.
    setTimeout(() => {
      region.textContent = text;
    }, 50);
  };
}

export function Countdown(expiresAt: string | number | Date, o: CountdownOptions = {}): Countdown {
  const now = o.now ?? Date.now;
  const end = expiresAt instanceof Date ? expiresAt.getTime() : typeof expiresAt === 'number' ? expiresAt : Date.parse(expiresAt);
  const announce = o.announce ?? defaultAnnounce(liveRegion());
  const el = h('time', { class: 'cd num', ...(Number.isFinite(end) ? { datetime: new Date(end).toISOString() } : {}) });
  let timer: ReturnType<typeof setTimeout> | null = null;
  let prev = Number.NaN; // the first tick never announces: a countdown that starts under 1 minute is read with the page
  let done = false;
  let seen = false;
  // onExpire fires only when this countdown watched the time run out. One built for a request that
  // is already past its time (the server's sweeper hasn't marked it yet) must not ask its caller to
  // re-fetch: the caller re-renders, builds a new countdown, and would ask again in a tight loop.
  let wasLive = false;

  const onVisible = () => {
    if (document.visibilityState === 'visible') tick();
  };

  function stop(): void {
    if (timer !== null) clearTimeout(timer);
    timer = null;
    document.removeEventListener('visibilitychange', onVisible);
  }

  function tick(): void {
    if (done) return;
    if (el.isConnected) seen = true;
    else if (seen) return stop();
    if (timer !== null) clearTimeout(timer);
    timer = null;
    const left = Number.isFinite(end) ? end - now() : 0;
    if (left > 0) wasLive = true;
    const text = label(left);
    if (el.textContent !== text) el.textContent = text;
    el.classList.toggle('cd-urgent', urgent(left));
    el.classList.toggle('cd-expired', !(left > 0));
    if (crossedAnnounce(prev, left)) announce(announcement);
    prev = left;
    const d = nextDelay(left);
    if (d === null) {
      done = true;
      stop();
      if (wasLive) o.onExpire?.();
      return;
    }
    timer = setTimeout(tick, d);
  }

  document.addEventListener('visibilitychange', onVisible);
  tick();
  return { el, stop: () => { done = true; stop(); } };
}
