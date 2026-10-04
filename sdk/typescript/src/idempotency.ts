// Idempotency keys: one per logical POST, reused by every retry of it (S31 §3.3), so a retry never
// runs a tool twice (S13 dedupes on the key).

/** A new idempotency key: "sdk_" + a random UUID. */
export function newIdempotencyKey(): string {
  return 'sdk_' + globalThis.crypto.randomUUID();
}

/** S03's Idempotency-Key grammar: 1 to 255 printable ASCII characters. */
export function validIdempotencyKey(k: string): boolean {
  return k.length > 0 && k.length <= 255 && /^[\x20-\x7e]+$/.test(k);
}
