#!/usr/bin/env node
// capture.mjs: records the README's terminal demo. It runs the real `make demo-hero` against the demo
// stack and keeps every output line with the millisecond it appeared, as a deedglass_cast JSON that
// cast2svg.mjs draws. Nothing is typed or edited. It refuses a dirty tree, so the commit stamped on
// the recording is the code that ran, and it keeps nothing if the run printed a secret-shaped string.
//
//   make demo-up DEMO_PORT=8091                       # the demo stack (examples/demo/README.md)
//   node scripts/demo/capture.mjs [--pace 1.5] [--out docs/assets/demo-hero.cast.json] [--allow-dirty]
//   make demo-svg                                     # docs/assets/demo-hero.svg and demo-hero-light.svg
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));
const arg = (k, d) => { const i = process.argv.indexOf(k); return i > 0 ? process.argv[i + 1] : d; };
const pace = arg('--pace', '1.5');
const out = arg('--out', 'docs/assets/demo-hero.cast.json');
const git = (...a) => execFileSync('git', ['-C', root, ...a], { encoding: 'utf8' }).trim();
const commit = git('rev-parse', '--short', 'HEAD');
if (git('status', '--porcelain', '--untracked-files=no') && !process.argv.includes('--allow-dirty')) {
  console.error("✕ The tree has uncommitted changes, so the commit on the recording wouldn't match what ran. Commit or stash, then run again.");
  process.exit(1);
}
console.log(`◐ Recording make demo-hero · commit ${commit} · HERO_PACE=${pace}`);
const t0 = process.hrtime.bigint();
const ms = () => Number((process.hrtime.bigint() - t0) / 1000000n);
const lines = [];
const child = spawn('make', ['-s', 'demo-hero'], { cwd: root, env: { ...process.env, HERO_PACE: pace, NO_COLOR: '1' } });
let buf = '';
const feed = (chunk) => {
  buf += chunk.toString('utf8');
  let i;
  while ((i = buf.indexOf('\n')) >= 0) {
    lines.push({ t: ms(), text: buf.slice(0, i).replace(/\r$/, '') });
    buf = buf.slice(i + 1);
  }
};
child.stdout.on('data', feed);
child.stderr.on('data', feed);
const code = await new Promise((r) => child.on('close', r));
if (buf) lines.push({ t: ms(), text: buf });
const log = lines.map((l) => l.text).join('\n') + '\n';
// The demo uses demo keys only, but a recording is scanned for secret shapes before anything is kept.
const SECRET = /sk_live_|ghp_[A-Za-z0-9]{20}|xox[abp]-|AKIA[0-9A-Z]{16}|-----BEGIN|dg_(live|test)_[A-Za-z0-9]{20}/;
if (SECRET.test(log)) { console.error('✕ The run printed something shaped like a secret. Nothing was kept.'); process.exit(1); }
if (code !== 0 || !/Hero done/.test(log)) { console.error(`✕ The hero didn't finish (exit ${code}). Run make demo-status.\n${log.slice(-800)}`); process.exit(1); }
const cast = {
  object: 'deedglass_cast', v: 1, slug: 'hero', command: 'make demo-hero', pace: Number(pace), commit,
  recorded_at: new Date().toISOString(), duration_ms: lines.at(-1).t,
  log_sha256: createHash('sha256').update(log).digest('hex'), lines,
};
writeFileSync(out, JSON.stringify(cast, null, 1) + '\n');
console.log(`✓ ${lines.find((l) => /Hero done/.test(l.text)).text} · ${lines.length} lines · ${(cast.duration_ms / 1000).toFixed(1)} s`);
console.log(`  ${out} · log sha256:${cast.log_sha256.slice(0, 12)} · next: make demo-svg`);
