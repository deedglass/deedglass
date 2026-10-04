// Copied from web/verify/src/hex.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/hex.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
/** Lowercase hex of bytes. */
export function toHex(b: Uint8Array): string {
  let s = '';
  for (const x of b) s += x.toString(16).padStart(2, '0');
  return s;
}

/** Bytes of a hex string (even length, 0-9a-fA-F). */
export function fromHex(h: string): Uint8Array {
  if (h.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(h)) throw new TypeError('not hex');
  const out = new Uint8Array(h.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(2 * i, 2 * i + 2), 16);
  return out;
}
