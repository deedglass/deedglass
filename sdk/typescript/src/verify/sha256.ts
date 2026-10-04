// Copied from web/verify/src/sha256.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/sha256.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
import { toHex } from './hex.js';
import { UnsupportedError } from './errors.js';

const enc = new TextEncoder();

export function subtle(): SubtleCrypto {
  const s = globalThis.crypto?.subtle;
  if (!s) throw new UnsupportedError('This page needs WebCrypto (a secure context: https or localhost).');
  return s;
}

/** SHA-256 of bytes or of a string's UTF-8 bytes, as lowercase hex. */
export async function sha256Hex(data: Uint8Array | string): Promise<string> {
  const b = typeof data === 'string' ? enc.encode(data) : data;
  const d = await subtle().digest('SHA-256', b as BufferSource);
  return toHex(new Uint8Array(d));
}

/** "sha256:" + lowercase hex: the deed digest format. */
export async function digest(data: Uint8Array | string): Promise<string> {
  return 'sha256:' + (await sha256Hex(data));
}

export function utf8Bytes(s: string): Uint8Array {
  return enc.encode(s);
}

export function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}
