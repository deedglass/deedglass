// The strict JSON profile, a port of Go's intent.ParseStrict / ParseStrictNonNegative: valid UTF-8,
// no duplicate keys (compared after unescaping), integers only with magnitude at most 2^53-1, no
// lone surrogates in \u escapes, depth at most 16, nothing after the value. With nonNegative (deed
// bodies and key sets) a minus sign is refused too, so there are no negatives and no -0.
//
// JSON.parse is never used on deed input: it accepts duplicate keys and rounds 2^53+1.

import { malformed, VerifyError } from './errors.js';

export type JsonValue = null | boolean | number | string | JsonValue[] | JsonObject;
export interface JsonObject {
  [key: string]: JsonValue;
}

export const MAX_SAFE_INTEGER = 9007199254740991;
const MAX_DEPTH = 16;

/** A strict-profile failure: the same message and path as Go's intent.Error. */
export class StrictError extends Error {
  constructor(
    readonly msg: string,
    readonly path: string,
  ) {
    super(path ? `${msg} at ${path}` : msg);
    this.name = 'StrictError';
  }
}

const utf8 = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });

/** Decodes bytes as strict UTF-8 (a BOM is kept, as Go keeps it). Throws StrictError. */
export function decodeUTF8(bytes: Uint8Array): string {
  try {
    return utf8.decode(bytes);
  } catch {
    throw new StrictError('input is not valid UTF-8', '');
  }
}

/** True when s has no unpaired surrogate (the string could have come from valid UTF-8). */
export function wellFormed(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      const d = i + 1 < s.length ? s.charCodeAt(i + 1) : 0;
      if (d < 0xdc00 || d > 0xdfff) return false;
      i++;
    } else if (c >= 0xdc00 && c <= 0xdfff) {
      return false;
    }
  }
  return true;
}

/**
 * Parses one JSON value under the strict profile. Objects have a null prototype, so a key such as
 * "__proto__" is an ordinary key. Throws StrictError.
 */
export function parseStrictValue(input: string | Uint8Array, nonNegative = false): JsonValue {
  const text = typeof input === 'string' ? input : decodeUTF8(input);
  if (typeof input === 'string' && !wellFormed(text)) throw new StrictError('input is not valid UTF-8', '');
  const p = new Parser(text, nonNegative);
  p.ws();
  const v = p.value('', 0);
  p.ws();
  if (p.i !== text.length) throw new StrictError('trailing data after the top-level value', '');
  return v;
}

/** The public API: parses or throws VerifyError('malformed'). */
export function parseStrict(text: string | Uint8Array, opts: { nonNegative?: boolean } = {}): JsonValue {
  try {
    return parseStrictValue(text, opts.nonNegative ?? false);
  } catch (e) {
    throw malformedFrom(e);
  }
}

/** Turns a parser error into malformed with Go's detail text. */
export function malformedFrom(e: unknown): VerifyError {
  if (e instanceof StrictError) return malformed(e.path ? `${e.msg} at ${e.path}` : e.msg);
  if (e instanceof VerifyError) return e;
  return malformed(String(e));
}

function joinPath(path: string, key: string): string {
  return path === '' ? key : `${path}.${key}`;
}

class Parser {
  i = 0;
  constructor(
    readonly s: string,
    readonly nonNeg: boolean,
  ) {}

  ws(): void {
    const s = this.s;
    while (this.i < s.length) {
      const c = s.charCodeAt(this.i);
      if (c === 0x20 || c === 0x09 || c === 0x0a || c === 0x0d) this.i++;
      else return;
    }
  }

  value(path: string, depth: number): JsonValue {
    if (this.i >= this.s.length) throw new StrictError('unexpected end of input', path);
    const c = this.s[this.i]!;
    if (c === '{') return this.object(path, depth + 1);
    if (c === '[') return this.array(path, depth + 1);
    if (c === '"') return this.str(path);
    if (c === '-' || (c >= '0' && c <= '9')) return this.number(path);
    if (c === 't') return this.literal(path, 'true', true);
    if (c === 'f') return this.literal(path, 'false', false);
    if (c === 'n') return this.literal(path, 'null', null);
    throw new StrictError(`unexpected character ${goQuoteChar(this.s, this.i)}`, path);
  }

  literal<T extends JsonValue>(path: string, lit: string, v: T): T {
    if (this.s.slice(this.i, this.i + lit.length) !== lit) throw new StrictError('invalid literal', path);
    this.i += lit.length;
    return v;
  }

  object(path: string, depth: number): JsonObject {
    if (depth > MAX_DEPTH) throw new StrictError('nesting too deep', path);
    this.i++; // '{'
    const m: JsonObject = Object.create(null) as JsonObject;
    this.ws();
    if (this.s[this.i] === '}') {
      this.i++;
      return m;
    }
    for (;;) {
      this.ws();
      if (this.s[this.i] !== '"') throw new StrictError('expected object key', path);
      const k = this.str(path);
      const kp = joinPath(path, k);
      if (Object.prototype.hasOwnProperty.call(m, k)) throw new StrictError('duplicate object key', kp);
      this.ws();
      if (this.s[this.i] !== ':') throw new StrictError("expected ':'", kp);
      this.i++;
      this.ws();
      m[k] = this.value(kp, depth);
      this.ws();
      if (this.i >= this.s.length) throw new StrictError('unterminated object', path);
      const c = this.s[this.i];
      if (c === ',') this.i++;
      else if (c === '}') {
        this.i++;
        return m;
      } else throw new StrictError("expected ',' or '}'", path);
    }
  }

  array(path: string, depth: number): JsonValue[] {
    if (depth > MAX_DEPTH) throw new StrictError('nesting too deep', path);
    this.i++; // '['
    const a: JsonValue[] = [];
    this.ws();
    if (this.s[this.i] === ']') {
      this.i++;
      return a;
    }
    for (;;) {
      this.ws();
      a.push(this.value(`${path}[${a.length}]`, depth));
      this.ws();
      if (this.i >= this.s.length) throw new StrictError('unterminated array', path);
      const c = this.s[this.i];
      if (c === ',') this.i++;
      else if (c === ']') {
        this.i++;
        return a;
      } else throw new StrictError("expected ',' or ']'", path);
    }
  }

  str(path: string): string {
    const s = this.s;
    this.i++; // opening quote
    let out = '';
    let run = this.i;
    for (;;) {
      if (this.i >= s.length) throw new StrictError('unterminated string', path);
      const c = s.charCodeAt(this.i);
      if (c === 0x22) {
        out += s.slice(run, this.i);
        this.i++;
        return out;
      }
      if (c < 0x20) throw new StrictError('unescaped control character in string', path);
      if (c === 0x5c) {
        out += s.slice(run, this.i);
        out += this.escape(path);
        run = this.i;
        continue;
      }
      this.i++;
    }
  }

  escape(path: string): string {
    const s = this.s;
    if (this.i + 1 >= s.length) throw new StrictError('truncated escape', path);
    const e = s[this.i + 1]!;
    this.i += 2;
    switch (e) {
      case '"':
        return '"';
      case '\\':
        return '\\';
      case '/':
        return '/';
      case 'b':
        return '\b';
      case 'f':
        return '\f';
      case 'n':
        return '\n';
      case 'r':
        return '\r';
      case 't':
        return '\t';
      case 'u': {
        const r = this.hex4();
        if (r < 0) throw new StrictError('invalid \\u escape', path);
        if (r >= 0xd800 && r < 0xdc00) {
          if (s[this.i] !== '\\' || s[this.i + 1] !== 'u') {
            throw new StrictError('high surrogate not followed by a low surrogate', path);
          }
          this.i += 2;
          const r2 = this.hex4();
          if (r2 < 0) throw new StrictError('invalid \\u escape', path);
          if (r2 < 0xdc00 || r2 > 0xdfff) throw new StrictError('high surrogate not followed by a low surrogate', path);
          return String.fromCharCode(r, r2);
        }
        if (r >= 0xdc00 && r <= 0xdfff) throw new StrictError('lone low surrogate', path);
        return String.fromCharCode(r);
      }
      default:
        throw new StrictError(`invalid escape \\${e}`, path);
    }
  }

  hex4(): number {
    const h = this.s.slice(this.i, this.i + 4);
    if (h.length < 4 || !/^[0-9A-Fa-f]{4}$/.test(h)) return -1;
    this.i += 4;
    return parseInt(h, 16);
  }

  number(path: string): number {
    const s = this.s;
    let neg = false;
    if (s[this.i] === '-') {
      if (this.nonNeg) throw new StrictError('negative numbers and -0 are not allowed', path);
      neg = true;
      this.i++;
    }
    const isDigit = (c: string | undefined) => c !== undefined && c >= '0' && c <= '9';
    if (!isDigit(s[this.i])) throw new StrictError('invalid number', path);
    const digits = this.i;
    if (s[this.i] === '0') {
      this.i++;
      if (isDigit(s[this.i])) throw new StrictError('leading zero in number', path);
    } else {
      while (isDigit(s[this.i])) this.i++;
    }
    const c = s[this.i];
    if (c === '.' || c === 'e' || c === 'E') {
      throw new StrictError('only integers are allowed (no fraction or exponent)', path);
    }
    const d = s.slice(digits, this.i);
    if (d.length > 16) throw new StrictError('integer magnitude exceeds 2^53-1', path);
    const n = Number(d);
    if (n > MAX_SAFE_INTEGER) throw new StrictError('integer magnitude exceeds 2^53-1', path);
    return neg ? -n : n;
  }
}

/** Go's %q of one byte, for "unexpected character" (ASCII is all a parser can stop on here). */
function goQuoteChar(s: string, i: number): string {
  const c = s.charCodeAt(i);
  if (c === 0x27) return "'\\''";
  if (c === 0x5c) return "'\\\\'";
  if (c >= 0x20 && c < 0x7f) return `'${s[i]}'`;
  if (c === 0x7f) return "'\\x7f'";
  if (c < 0x80) {
    const named: Record<number, string> = { 7: 'a', 8: 'b', 12: 'f', 10: 'n', 13: 'r', 9: 't', 11: 'v' };
    if (named[c]) return `'\\${named[c]}'`;
    return `'\\x${c.toString(16).padStart(2, '0')}'`;
  }
  // A non-ASCII lead byte: Go quotes the byte value as a rune (Latin-1 range).
  const b = new TextEncoder().encode(s.slice(i, i + 2))[0]!;
  return `'${String.fromCharCode(b)}'`;
}
