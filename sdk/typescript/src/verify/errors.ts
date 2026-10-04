// Copied from web/verify/src/errors.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/errors.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
import type { VerifyCode } from './messages.gen.js';

export type { VerifyCode };

/** Every verification failure: the same fields as Go's deed.VerifyError. */
export class VerifyError extends Error {
  code: VerifyCode;
  seq = 0;
  deedId = '';
  field = '';
  detail = '';
  key = '';
  issuedAt = '';
  version = 0;
  prev = 0;
  want = 0;
  org = '';
  wantOrg = '';

  constructor(code: VerifyCode, init: Partial<Omit<VerifyError, 'code'>> = {}) {
    super(code);
    this.name = 'VerifyError';
    this.code = code;
    Object.assign(this, init);
  }
}

/** Thrown when this browser cannot check Ed25519 seals (no WebCrypto Ed25519): never a verdict. */
export class UnsupportedError extends Error {
  constructor(message = 'This browser cannot check Ed25519 seals.') {
    super(message);
    this.name = 'UnsupportedError';
  }
}

export function schemaField(field: string, detail: string): VerifyError {
  return new VerifyError('schema_invalid', { field, detail });
}

export function malformed(detail: string): VerifyError {
  return new VerifyError('malformed', { detail });
}

export function keyRejected(key: string, detail: string): VerifyError {
  return new VerifyError('seal_key_rejected', { key, detail });
}

export function unsupported(v: number): VerifyError {
  return new VerifyError('unsupported_version', { version: v, detail: `v=${v}` });
}
