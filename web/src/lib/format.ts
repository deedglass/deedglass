// format.ts: numbers, money, time, ids and hashes, one way everywhere (brand §12, S24 §3.9).
// en-US grouping matches the CLI ("1,204"). Times are in the org's time zone.

const nf = new Intl.NumberFormat('en-US');

/** number: 1204 → "1,204". */
export function number(n: number): string {
  return nf.format(n);
}

/** plural: (1, "deed") → "1 deed"; (2, "deed") → "2 deeds". */
export function plural(n: number, one: string, many = one + 's'): string {
  return `${number(n)} ${n === 1 ? one : many}`;
}

const zeroDecimal = new Set(['jpy', 'krw', 'vnd', 'clp', 'xof', 'xaf', 'ugx', 'rwf', 'pyg', 'kmf', 'gnf', 'djf', 'bif', 'mga', 'vuv', 'xpf']);

/** money from minor units: (12000, "usd") → "$120.00". */
export function money(amount: number, currency: string): string {
  const cur = currency.toLowerCase();
  const digits = zeroDecimal.has(cur) ? 0 : 2;
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: cur.toUpperCase(), minimumFractionDigits: digits, maximumFractionDigits: digits })
    .format(amount / 10 ** digits);
}

function zoned(d: Date, tz: string, o: Intl.DateTimeFormatOptions): string {
  try {
    return new Intl.DateTimeFormat('en-GB', { ...o, timeZone: tz }).format(d);
  } catch {
    return new Intl.DateTimeFormat('en-GB', { ...o, timeZone: 'UTC' }).format(d);
  }
}

/** time: "14:05" in the org's time zone. */
export function time(iso: string | Date, tz = 'UTC'): string {
  return zoned(new Date(iso), tz, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
}

/** date: "2 Oct 2026" in the org's time zone. */
export function date(iso: string | Date, tz = 'UTC'): string {
  return zoned(new Date(iso), tz, { day: 'numeric', month: 'short', year: 'numeric' });
}

/** dateTime: "2 Oct 2026, 14:05:09 UTC" (tooltips: the absolute time, with the zone). */
export function dateTime(iso: string | Date, tz = 'UTC'): string {
  const d = new Date(iso);
  const t = zoned(d, tz, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
  return `${date(d, tz)}, ${t} ${tz === 'UTC' ? 'UTC' : tz}`;
}

/** relative: "just now", "45s ago", "2m ago", "3h ago", "5d ago", then the date. */
export function relative(iso: string | Date, now: Date = new Date(), tz = 'UTC'): string {
  const d = new Date(iso);
  const s = Math.round((now.getTime() - d.getTime()) / 1000);
  if (s < -60) return `in ${relativeSpan(-s)}`;
  if (s < 10) return 'just now';
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 86400 * 7) return `${Math.floor(s / 86400)}d ago`;
  return date(d, tz);
}

function relativeSpan(s: number): string {
  if (s < 3600) return `${Math.max(1, Math.floor(s / 60))}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

const ID_RE = /^([a-z]{2,4})_([0-9a-z]{26})$/;

/** shortId: "agt_01k6h7mz3c9d8e7f6g5h4jvqvp" → "agt_01k6…vqvp" (brand §12). Anything else is clipped. */
export function shortId(id: string): string {
  const m = ID_RE.exec(id);
  if (m) return `${m[1]}_${m[2]!.slice(0, 4)}…${m[2]!.slice(-4)}`;
  return id.length > 16 ? `${id.slice(0, 12)}…` : id;
}

/** shortHash: "sha256:3f9a…c21e" (first 4 and last 4 hex). */
export function shortHash(h: string): string {
  const m = /^(sha256):([0-9a-f]{64})$/.exec(h);
  if (m) return `${m[1]}:${m[2]!.slice(0, 4)}…${m[2]!.slice(-4)}`;
  return h.length > 20 ? `${h.slice(0, 12)}…${h.slice(-4)}` : h;
}

/** duration in ms: 41 → "41 ms", 1500 → "1.5 s". */
export function duration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
  return `${Math.round(ms / 60_000)} min`;
}

/** cap: badges show 99+ (S24 §3.6). */
export function cap(n: number): string {
  return n > 99 ? '99+' : number(n);
}
