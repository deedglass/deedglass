// Copied from web/verify/src/index.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/index.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// @deedglass/verify: check deeds in a browser or in Node 24 with the same answer as
// `deedglass verify` on every published vector. The CLI is the reference verifier.

import type { DeedResult } from './deed.js';
import { verifyEnvelope } from './deed.js';
import type { SealKeySet } from './keys.js';
import { type Report, Verifier, type VerifyOptions } from './report.js';

export { canonicalize } from './jcs.js';
export { parseStrict, type JsonValue, type JsonObject } from './strict.js';
export { parseKeySet, SealKeySet, ed25519Supported, unsafeKey } from './keys.js';
export { VerifyError, UnsupportedError, type VerifyCode } from './errors.js';
export { CODES, MESSAGES } from './messages.gen.js';
export {
  Verifier,
  message,
  renderMessage,
  failureFrom,
  cannotDetermine,
  splitLines,
  splitTextLines,
  manyLines,
  type Report,
  type Failure,
  type Warning,
  type KeyUse,
  type VerifyOptions,
} from './report.js';
export type { DeedResult, Envelope, Body } from './deed.js';

/** Checks one deed on its own. Resolves with the deed, or rejects with a VerifyError. */
export function verifyDeed(text: string | Uint8Array, keys: SealKeySet, opts: { strict?: boolean } = {}): Promise<DeedResult> {
  return verifyEnvelope(text, keys, opts);
}

/** Checks deeds as one org's chain (JSONL lines, in order) and returns the verification report. */
export async function verifyDeeds(
  lines: Iterable<string | Uint8Array> | AsyncIterable<string | Uint8Array>,
  keys: SealKeySet | null,
  opts: VerifyOptions = {},
): Promise<Report> {
  const v = new Verifier(keys, opts);
  await v.chain(lines);
  return v.report();
}
