// Copied from web/verify/src/schema.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/schema.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// The closed schema helpers, a port of internal/deed/tree.go and the patterns of body.go. Error
// fields and the order checks run in are the same as Go's, so both verifiers name the same field.

import { schemaField, VerifyError } from './errors.js';
import type { JsonObject, JsonValue } from './strict.js';

export interface Obj {
  m: JsonObject;
  path: string;
}

export function at(o: Obj, k: string): string {
  return o.path === '' ? k : `${o.path}.${k}`;
}

function isObject(v: JsonValue | undefined): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

export function asObj(v: JsonValue | undefined, path: string): Obj {
  if (!isObject(v)) throw schemaField(path === '' ? '(top level)' : path, 'must be an object');
  return { m: v, path };
}

function has(o: Obj, k: string): boolean {
  return Object.prototype.hasOwnProperty.call(o.m, k);
}

/** Compares strings by Unicode code point (Go's byte order of UTF-8), not by UTF-16 unit. */
export function cmpCodePoints(a: string, b: string): number {
  const x = [...a];
  const y = [...b];
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i]!.codePointAt(0)! - y[i]!.codePointAt(0)!;
    if (d !== 0) return d;
  }
  return x.length - y.length;
}

/** Refuses unknown keys (the first in code point order), then missing keys (in spec order). */
export function closed(o: Obj, keys: readonly string[]): void {
  const unknown = Object.keys(o.m).filter((k) => !keys.includes(k));
  if (unknown.length > 0) {
    unknown.sort(cmpCodePoints);
    throw schemaField(at(o, unknown[0]!), 'is not part of the deed schema');
  }
  for (const k of keys) {
    if (!has(o, k)) throw schemaField(at(o, k), 'is missing');
  }
}

/** Accumulates the first error so field reads stay linear (Go's decoder). */
export class Decoder {
  err: VerifyError | null = null;

  check(): void {
    if (this.err) throw this.err;
  }

  s(o: Obj, k: string): string {
    if (this.err) return '';
    const v = o.m[k];
    if (typeof v !== 'string') {
      this.err = schemaField(at(o, k), 'must be a string');
      return '';
    }
    return v;
  }

  ns(o: Obj, k: string): string | null {
    if (this.err) return null;
    const v = o.m[k];
    if (v === null || v === undefined) return null;
    if (typeof v !== 'string') {
      this.err = schemaField(at(o, k), 'must be a string or null');
      return null;
    }
    return v;
  }

  i(o: Obj, k: string): number {
    if (this.err) return 0;
    const v = o.m[k];
    if (typeof v !== 'number') {
      this.err = schemaField(at(o, k), 'must be an integer');
      return 0;
    }
    return v;
  }

  b(o: Obj, k: string): boolean {
    if (this.err) return false;
    const v = o.m[k];
    if (typeof v !== 'boolean') {
      this.err = schemaField(at(o, k), 'must be true or false');
      return false;
    }
    return v;
  }

  /** The nested object at k, closed to keys; null for JSON null (or after an error). */
  obj(o: Obj, k: string, keys: readonly string[]): Obj | null {
    if (this.err) return null;
    const v = o.m[k];
    if (v === null || v === undefined) return null;
    if (!isObject(v)) {
      this.err = schemaField(at(o, k), 'must be an object or null');
      return null;
    }
    const c: Obj = { m: v, path: at(o, k) };
    try {
      closed(c, keys);
    } catch (e) {
      this.err = e as VerifyError;
      return null;
    }
    return c;
  }

  strs(o: Obj, k: string): string[] {
    const v = o.m[k];
    if (!Array.isArray(v)) {
      this.err = schemaField(at(o, k), 'must be an array');
      return [];
    }
    const out: string[] = [];
    for (let i = 0; i < v.length; i++) {
      const s = v[i];
      if (typeof s !== 'string') {
        this.err = schemaField(`${at(o, k)}[${i}]`, 'must be a string');
        return [];
      }
      out.push(s);
    }
    return out;
  }
}

// --- patterns (DEED_SPEC.md §4) ---

export const reDigest: RegExp = /^sha256:[0-9a-f]{64}$/;
export const reSeal: RegExp = /^ed25519:[A-Za-z0-9_-]{86}$/;
export const reTime: RegExp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
export const reHeaderRef: RegExp = /^[A-Za-z0-9._~:@/+=-]{1,256}$/;
export const reMethod: RegExp = /^[a-z0-9][a-z0-9._-]{0,63}$/;
export const reErrorCode: RegExp = /^[a-z][a-z0-9_]{0,63}$/;
export const reRuleID: RegExp = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;
export const reAdminAct: RegExp = /^[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}$/;
export const reRefType: RegExp = /^[a-z][a-z_]{0,31}$/;
export const reRefID: RegExp = /^[a-z][a-z0-9_-]{0,63}$/;
export const reCurrency: RegExp = /^[a-z]{3}$/;

const idPatterns = new Map<string, RegExp>();

/** Contract §3 ids: "<prefix>_" + 26 lowercase Crockford base32 characters, the first 0-7. */
export function isID(prefix: string, s: string): boolean {
  let re = idPatterns.get(prefix);
  if (!re) {
    re = new RegExp(`^${prefix}_[0-7][0-9a-hjkmnp-tv-z]{25}$`);
    idPatterns.set(prefix, re);
  }
  return re.test(s);
}

/** Milliseconds since the epoch for a deed time (exactly YYYY-MM-DDTHH:MM:SS.mmmZ, a real date), or null. */
export function parseTime(s: string): number | null {
  if (!reTime.test(s)) return null;
  const year = Number(s.slice(0, 4));
  const month = Number(s.slice(5, 7));
  const day = Number(s.slice(8, 10));
  const hour = Number(s.slice(11, 13));
  const minute = Number(s.slice(14, 16));
  const second = Number(s.slice(17, 19));
  const ms = Number(s.slice(20, 23));
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return null;
  const leap = (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]!;
  if (day < 1 || day > days) return null;
  const d = new Date(0);
  d.setUTCFullYear(year, month - 1, day);
  d.setUTCHours(hour, minute, second, ms);
  return d.getTime();
}

/** Text of min..max code points with no control characters (a newline only when allowed). */
export function plainText(s: string, min: number, max: number, newline: boolean): boolean {
  let n = 0;
  for (const ch of s) {
    n++;
    const r = ch.codePointAt(0)!;
    if ((r < 0x20 && !(newline && r === 0x0a)) || r === 0x7f || (r >= 0x80 && r < 0xa0)) return false;
  }
  return n >= min && n <= max;
}
