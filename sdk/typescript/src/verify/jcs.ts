// Copied from web/verify/src/jcs.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/jcs.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// RFC 8785 JSON Canonicalization Scheme. Object keys are sorted by UTF-16 code units (the default
// string sort), strings are serialized by JSON.stringify (which escapes exactly what RFC 8785 asks
// for), and numbers by the ECMAScript Number-to-String rules (JSON.stringify writes -0 as 0).

import type { JsonValue } from './strict.js';

export function canonicalize(v: JsonValue): string {
  if (v === null) return 'null';
  switch (typeof v) {
    case 'boolean':
      return v ? 'true' : 'false';
    case 'number':
      if (!Number.isFinite(v)) throw new TypeError('JCS: numbers must be finite');
      return JSON.stringify(v);
    case 'string':
      return JSON.stringify(v);
  }
  if (Array.isArray(v)) return '[' + v.map(canonicalize).join(',') + ']';
  const keys = Object.keys(v).sort();
  let out = '{';
  for (let i = 0; i < keys.length; i++) {
    const k = keys[i]!;
    if (i > 0) out += ',';
    out += JSON.stringify(k) + ':' + canonicalize(v[k]!);
  }
  return out + '}';
}
