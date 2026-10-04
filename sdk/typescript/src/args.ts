// The strict args profile (docs/INTENTS.md §3, Go's intent.ParseStrictArgs) for JavaScript
// values: finite numbers, integers within ±(2^53 − 1), depth at most 32, at most 256 KiB once
// canonical, and only JSON types (no undefined, functions, BigInt, symbols or cycles).

import { copy } from './copy.js';
import { InvalidRequestError } from './errors.js';
import type { JsonValue } from './types.js';
import { canonicalize } from './verify/jcs.js';
import { digest } from './verify/sha256.js';

export const MAX_ARGS_BYTES: number = 256 * 1024;
export const MAX_ARGS_DEPTH: number = 32;

function refuse(why: string, code = 'validation_failed'): InvalidRequestError {
  return new InvalidRequestError({ type: 'client_error', code, message: why, param: 'args' });
}

/** Checks v against the args profile and returns it as a JsonValue, or throws InvalidRequestError. */
export function checkArgs(v: unknown, depth = 0, seen: Set<object> = new Set()): JsonValue {
  if (depth > MAX_ARGS_DEPTH) throw refuse(copy.argsInvalid('they nest deeper than 32 levels'));
  if (v === null) return null;
  switch (typeof v) {
    case 'string':
    case 'boolean':
      return v;
    case 'number':
      if (!Number.isFinite(v)) throw refuse(copy.argsInvalid('a number is NaN or infinite'));
      // Go's args profile refuses integer TOKENS beyond 2^53 − 1 (an exponent form is a float). JSON
      // writes integers below 1e21 without an exponent, so those are the ones the server refuses.
      if (Number.isInteger(v) && !Number.isSafeInteger(v) && Math.abs(v) < 1e21) throw refuse(copy.bigInt, 'big_int');
      return v;
    case 'object': {
      if (seen.has(v)) throw refuse(copy.argsInvalid('they refer to themselves (a cycle)'));
      seen.add(v);
      try {
        if (Array.isArray(v)) return v.map((x) => checkArgs(x, depth + 1, seen));
        const proto = Object.getPrototypeOf(v);
        if (proto !== Object.prototype && proto !== null) throw refuse(copy.argsInvalid('they hold an object that isn\'t plain JSON (a class instance, Date or Map)'));
        const out: Record<string, JsonValue> = {};
        for (const [k, x] of Object.entries(v)) {
          if (x === undefined) continue; // JSON.stringify drops it too
          out[k] = checkArgs(x, depth + 1, seen);
        }
        return out;
      } finally {
        seen.delete(v);
      }
    }
  }
  throw refuse(copy.argsInvalid(`they hold a ${typeof v}, which JSON can't carry`));
}

/** JCS of the args after the profile, refusing more than 256 KiB. */
export function canonicalArgs(args: unknown): string {
  const c = canonicalize(checkArgs(args));
  if (new TextEncoder().encode(c).length > MAX_ARGS_BYTES) throw refuse(copy.argsInvalid('they are larger than 256 KiB'));
  return c;
}

/** "sha256:" + hex(SHA-256(JCS(args))) after the args profile: the signed intent's args_digest. */
export function argsDigest(args: JsonValue): Promise<string> {
  return digest(canonicalArgs(args));
}
