// The SDK's offline verifier is S05's browser verifier, byte for byte (S31 §3.2, the fallback):
// web/verify/src holds the one source; this script copies its non-UI modules into src/verify/ with
// a header. `node scripts/sync-verify.mjs --check` (make sdk-sync-verify, and a Vitest case) fails
// when a copy has drifted. Edit web/verify/src, never src/verify/<copied file>.
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('..', import.meta.url));
const from = here + '../../web/verify/src/';
const to = here + 'src/verify/';
// index.ts is copied as core.ts: src/verify/index.ts adds fetchKeys around it.
export const FILES = {
  'alg.ts': 'alg.ts', 'b64url.ts': 'b64url.ts', 'chain.ts': 'chain.ts', 'deed.ts': 'deed.ts', 'errors.ts': 'errors.ts', 'hex.ts': 'hex.ts',
  'jcs.ts': 'jcs.ts', 'keys.ts': 'keys.ts', 'keys.gen.ts': 'keys.gen.ts', 'messages.gen.ts': 'messages.gen.ts', 'mldsa.ts': 'mldsa.ts',
  'report.ts': 'report.ts', 'schema.ts': 'schema.ts', 'sha256.ts': 'sha256.ts', 'strict.ts': 'strict.ts', 'index.ts': 'core.ts',
};

export function header(src) {
  return `// Copied from web/verify/src/${src} by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:\n// edit web/verify/src/${src}, then run npm run sync-verify. make sdk-sync-verify fails on drift.\n`;
}

const check = process.argv.includes('--check');
let stale = [];
for (const [src, dst] of Object.entries(FILES)) {
  const want = header(src) + readFileSync(from + src, 'utf8');
  const path = to + dst;
  const have = existsSync(path) ? readFileSync(path, 'utf8') : null;
  if (have === want) continue;
  if (check) stale.push(dst);
  else writeFileSync(path, want);
}
if (check && stale.length) {
  console.error(`✕ src/verify is stale: ${stale.join(', ')}. Run npm run sync-verify in sdk/typescript.`);
  process.exit(1);
}
console.log(check ? '✓ src/verify matches web/verify/src' : `✓ src/verify copied from web/verify/src (${Object.keys(FILES).length} files)`);
