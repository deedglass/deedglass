#!/usr/bin/env node
// check-all.mjs: `npm run check` (S33 §3.6). Every gate over the sources and the built dist/, in
// order, each printing one line. --offline skips the network (external links; the workflow
// linters' first download). Build first: npm run build.
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { site } from './lib-pages.mjs';

const offline = process.argv.includes('--offline');
if (!existsSync(join(site, 'dist', 'index.html'))) {
  console.error('✕ dist/ is missing. Next: npm run build');
  process.exit(1);
}
const node = (f, ...a) => ['node', [join('scripts', f), ...a]];
const gates = [
  ['generated data fresh', ...node('gen-data.mjs', '--check')],
  ['unit tests', 'node', ['--test', 'scripts/test/gates.test.mjs']],
  ['spelling', 'npx', ['cspell', '--no-progress', '--no-summary']],
  ['banned words', 'bash', ['../scripts/banned_words.sh']],
  ['pages', ...node('check-pages.mjs')],
  ['drafts', ...node('check-drafts.mjs')],
  ['pricing', ...node('check-pricing.mjs')],
  ['claims', ...node('check-claims.mjs')],
  ['css', ...node('check-css.mjs')],
  ['install links', ...node('check-install-links.mjs')],
  ['workflows', ...node('check-workflows.mjs', ...(offline ? ['--offline'] : []))],
  ['errors', ...node('check-errors.mjs')],
  ['links and dist', ...node('check-dist.mjs')],
  ['external links', ...node('check-external.mjs', ...(offline ? ['--offline'] : []))],
];
let failed = 0;
for (const [name, cmd, args] of gates) {
  const r = spawnSync(cmd, args, { cwd: site, encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' } });
  const out = ((r.stdout ?? '') + (r.stderr ?? '')).trim().split('\n');
  if (r.status === 0) {
    const line = out.filter((l) => /^[✓○]/.test(l)).pop() ?? (name === 'spelling' ? '✓ spelling · 0 unknown words' : name === 'unit tests' ? `✓ unit tests · ${out.find((l) => /^ℹ pass/.test(l))?.slice(2) ?? 'pass'}` : `✓ ${name} · ${out.pop()}`);
    console.log(line);
  } else {
    failed++;
    console.error(`✕ ${name}`);
    for (const l of out.slice(-30)) console.error('    ' + l);
  }
}
if (failed) {
  console.error(`✕ ${failed} gate${failed > 1 ? 's' : ''} failed`);
  process.exit(1);
}
console.log(`✓ links · ✓ spelling · ✓ banned words · ✓ claims · ✓ errors · ✓ generated data fresh${offline ? ' (offline)' : ''}`);
