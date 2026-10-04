// @deedglass/sdk/verify: check deeds offline with the same answer as `deedglass verify` on every
// published vector. No API key, no browser guard. The core is S05's verifier (core.ts and its
// modules are copies of web/verify/src; see scripts/sync-verify.mjs).

export * from './core.js';
export { fetchKeys, keysURL, FetchKeysError, KEYS_MAX_BYTES, type FetchKeysResult } from './fetch.js';
