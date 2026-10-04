// Zero runtime dependencies (S31 §3.9 invariant 1): package.json has no dependencies, core dist
// imports only relative paths, and each adapter imports only its own peer.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const pkg = JSON.parse(readFileSync(root + 'package.json', 'utf8'));
const fail = [];
if (Object.keys(pkg.dependencies ?? {}).length) fail.push('package.json has dependencies: ' + Object.keys(pkg.dependencies).join(', '));
const PEERS = { 'ai-sdk.js': ['ai'], 'openai-agents.js': ['@openai/agents'], 'langchain.js': ['@langchain/core/tools'], 'claude-agent-sdk.js': [] };
const walk = (d) => readdirSync(d).flatMap((f) => (statSync(d + f).isDirectory() ? walk(d + f + '/') : [d + f]));
let n = 0;
for (const f of walk(root + 'dist/').filter((f) => f.endsWith('.js'))) {
  const src = readFileSync(f, 'utf8');
  const specs = [...src.matchAll(/(?:^|[\s;])(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]|import\(\s*['"]([^'"]+)['"]\s*\)|^import\s*['"]([^'"]+)['"]/gm)].map((m) => m[1] ?? m[2] ?? m[3]);
  const name = f.slice(f.lastIndexOf('/') + 1);
  const allowed = f.includes('/adapters/') ? (PEERS[name] ?? []) : [];
  for (const s of specs) {
    n++;
    if (s.startsWith('./') || s.startsWith('../')) continue;
    if (!allowed.includes(s)) fail.push(`${f.replace(root, '')} imports ${s}`);
  }
}
if (fail.length) {
  for (const l of fail) console.error('✕ ' + l);
  process.exit(1);
}
console.log(`✓ 0 runtime dependencies · ${n} import specifiers checked · adapters import only their own peer`);
