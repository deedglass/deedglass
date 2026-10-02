// dist → .br and .gz next to every compressible asset (node:zlib, max quality), so deedglass serves
// precompressed bytes by Accept-Encoding (S24 §3.10). Recreates dist/.keep (the committed marker).
import { brotliCompressSync, constants, gzipSync } from 'node:zlib';
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../../internal/console/dist/', import.meta.url));
const ok = /\.(js|css|svg|html|json|webmanifest)$/;
const files = (d) => readdirSync(d).flatMap((f) => (statSync(join(d, f)).isDirectory() ? files(join(d, f)) : [join(d, f)]));
let n = 0;
for (const f of files(dist)) {
  if (!ok.test(f) || f.endsWith('.html')) continue; // pages are small and no-cache; assets are immutable
  const buf = readFileSync(f);
  if (buf.length < 512) continue;
  writeFileSync(f + '.br', brotliCompressSync(buf, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: buf.length } }));
  writeFileSync(f + '.gz', gzipSync(buf, { level: 9 }));
  n++;
}
writeFileSync(join(dist, '.keep'), '');
console.log(`compress: ${n} assets → .br and .gz`);
