// The pack-list golden (S31 §6): npm pack --dry-run ships only dist/**, README.md, LICENSE,
// CHANGELOG.md and package.json. `--update` rewrites test/pack.golden.
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const out = JSON.parse(execFileSync('npm', ['pack', '--dry-run', '--json', '--ignore-scripts'], { cwd: root, encoding: 'utf8' }));
const files = out[0].files.map((f) => f.path).sort();
const bad = files.filter((f) => !(f.startsWith('dist/') || ['README.md', 'LICENSE', 'CHANGELOG.md', 'package.json'].includes(f)));
const text = files.join('\n') + '\n';
const golden = root + 'test/pack.golden';
if (process.argv.includes('--update')) writeFileSync(golden, text);
if (bad.length) {
  console.error('✕ the package would ship: ' + bad.join(', '));
  process.exit(1);
}
if (readFileSync(golden, 'utf8') !== text) {
  console.error('✕ the pack list changed; review it, then run node scripts/pack-check.mjs --update');
  process.exit(1);
}
console.log(`✓ pack list matches test/pack.golden · ${files.length} files · ${(out[0].size / 1024).toFixed(1)} KiB packed`);
