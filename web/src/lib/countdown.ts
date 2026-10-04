// countdown.ts: the pure cadence and wording behind Countdown (S26 §3.1). `in 9 min` from 2 minutes
// up (minutes rounded down), `in 1 min 12 s` / `in 45 s` under 2 minutes, `Expired` at 0. Ticks every
// 30 s, every second under 2 minutes; urgent (weight 600, never red) under 5 minutes; one polite
// announcement as the 1 minute mark is crossed.

import { formsCopy } from './copy-forms.ts';
import { fill } from './copy.ts';

const C = formsCopy.countdown;
export const SECOND = 1000;
export const MINUTE = 60 * SECOND;
/** Below this, the label shows seconds and ticks every second. */
export const FINE = 2 * MINUTE;
/** Below this, the label is urgent. */
export const URGENT = 5 * MINUTE;
/** Crossing this announces once. */
export const ANNOUNCE_AT = MINUTE;
export const COARSE_TICK = 30 * SECOND;

/** label: the words for the time left. */
export function label(msLeft: number): string {
  if (!(msLeft > 0)) return C.expired;
  if (msLeft >= FINE) return fill(C.min, { m: Math.floor(msLeft / MINUTE) });
  const s = Math.min(Math.ceil(msLeft / SECOND), FINE / SECOND - 1);
  const m = Math.floor(s / 60);
  const r = s % 60;
  if (m === 0) return fill(C.sec, { s: r });
  return r === 0 ? fill(C.min, { m }) : fill(C.minSec, { m, s: r });
}

/** urgent: under 5 minutes and not yet expired. */
export function urgent(msLeft: number): boolean {
  return msLeft > 0 && msLeft < URGENT;
}

/**
 * nextDelay: ms until the next tick, or null once expired. 30 s ticks, cut short so a tick lands
 * exactly on the 5 minute and 2 minute marks; under 2 minutes, on each whole second.
 */
export function nextDelay(msLeft: number): number | null {
  if (!(msLeft > 0)) return null;
  if (msLeft <= FINE) return msLeft % SECOND || SECOND;
  if (msLeft > URGENT) return Math.min(COARSE_TICK, msLeft - URGENT);
  return Math.min(COARSE_TICK, msLeft - FINE);
}

/** crossedAnnounce: true for the one tick that crosses the 1 minute mark (prev above, now at or below, not expired). */
export function crossedAnnounce(prevMs: number, msLeft: number): boolean {
  return prevMs > ANNOUNCE_AT && msLeft <= ANNOUNCE_AT && msLeft > 0;
}

export const announcement = C.oneMinute;
