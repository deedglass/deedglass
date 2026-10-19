// Copied from web/verify/src/alg.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/alg.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// The seal algorithm registry (DEED_SPEC.md §7.4, ADR-109), a port of internal/deed/alg.go. Every
// seal of a v2 deed names its algorithm; every algorithm has its own domain tag, seal prefix and
// accept window. An algorithm that is not in this table is never used to check anything: a deed that
// needs one is unsupported_algorithm (the verifier cannot decide).

export const ALG_ED25519 = 'Ed25519';

/** Ed25519 alone is accepted only for deeds issued before this instant (NIST IR 8547 ipd). */
export const ED25519_ALONE_UNTIL: number = Date.UTC(2035, 0, 1);
export const ED25519_ALONE_UNTIL_TEXT = '2035-01-01T00:00:00.000Z';

export const TAG_DEED_V2_ED25519 = 'deedglass/deed/v2/ed25519\n';
export const MAX_SEALS = 4;

export const reAlg: RegExp = /^[A-Za-z0-9][A-Za-z0-9-]{0,31}$/;
const reUnknownSeal: RegExp = /^[a-z0-9][a-z0-9-]{0,31}:[A-Za-z0-9_-]+$/;
const reB64url: RegExp = /^[A-Za-z0-9_-]*$/;

export interface AlgSpec {
  name: string;
  prefix: string;
  tag: string;
  keySize: number;
  sigSize: number;
  /** null: accepted alone for any issued_at. */
  aloneUntil: number | null;
}

export const ALGS: Record<string, AlgSpec> = {
  [ALG_ED25519]: { name: ALG_ED25519, prefix: 'ed25519:', tag: TAG_DEED_V2_ED25519, keySize: 32, sigSize: 64, aloneUntil: ED25519_ALONE_UNTIL },
};

export function algSupported(alg: string): boolean {
  return Object.prototype.hasOwnProperty.call(ALGS, alg);
}

/** Base64url length without padding of n bytes. */
export function sealChars(n: number): number {
  return Math.floor((n * 8 + 5) / 6);
}

/** Whether seal has the shape alg needs (port of sealShapeOK). */
export function sealShapeOK(alg: string, seal: string): boolean {
  if (algSupported(alg)) {
    const a = ALGS[alg]!;
    return seal.startsWith(a.prefix) && seal.length === a.prefix.length + sealChars(a.sigSize) && reB64url.test(seal.slice(a.prefix.length));
  }
  return reUnknownSeal.test(seal) && seal.startsWith(alg.toLowerCase() + ':');
}

/** Whether a verified seal of alg is enough on its own for a deed issued at t (ms). */
export function acceptedAlone(alg: string, t: number): boolean {
  if (!algSupported(alg)) return false;
  const u = ALGS[alg]!.aloneUntil;
  return u === null || t < u;
}
