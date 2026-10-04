#!/usr/bin/env node
// test-snippets.mjs (S33 §3.5): the docs' code blocks, run or type-checked.
//
//   ```sh test             run in a per-page temp dir with bash -euo pipefail. Lines starting "$ " are
//                          commands; the lines after one are expected output, matched in order
//                          ("…" is any text; a line that is only "…" is any number of lines).
//                          A command ending "# exit N" is expected to exit N (a denial exits 1).
//   ```sh test=skip reason="…"   listed with its reason, not run (a skip without a reason fails)
//   ```python check        py_compile + mypy --strict with the repo's deedglass package
//   ```ts check            tsc --noEmit against sdk/typescript
//
// The environment is the demo stack (make demo-up DEMO_PORT=8091): DEEDGLASS_SERVER, an agent key
// in .deedglass/quickstart.key, the org admin key in .deedglass/admin.key and in DEEDGLASS_API_KEY,
// read from examples/demo/.demo-keys at run time. A page with `testEnv: init` (the Quickstart) starts
// from an empty directory instead: `deedglass init` runs for real on its own ports (rewritten
// below, and printed), and the stack is removed after. `npx -y deedglass` runs the CLI from a
// throwaway local registry (scripts/npx-registry.sh), the way a newcomer runs it, until it's published.
//
//   node scripts/test-snippets.mjs [--list] [--page <slug>] [--only-parse]
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pages, site } from './lib-pages.mjs';

const repo = join(site, '..');

/** Parses a page's fenced blocks: [{lang, meta, code, line}]. */
export function blocks(body) {
  const out = [];
  const lines = body.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const m = /^```(\w+)(.*)$/.exec(lines[i]);
    if (!m) continue;
    const start = i;
    const code = [];
    for (i++; i < lines.length && !/^```\s*$/.test(lines[i]); i++) code.push(lines[i]);
    out.push({ lang: m[1], meta: m[2].trim(), code: code.join('\n'), line: start + 1 });
  }
  return out;
}

/** The kind of a block: run, skip (with reason), python, ts, or null (not tested). Throws on a bad tag. */
export function kind(b) {
  if (['sh', 'bash'].includes(b.lang)) {
    if (/(^|\s)test=skip\b/.test(b.meta)) {
      const r = /reason="([^"]+)"/.exec(b.meta);
      if (!r) throw new Error('test=skip needs reason="…"');
      return { k: 'skip', reason: r[1] };
    }
    if (/(^|\s)test(\s|$)/.test(b.meta)) return { k: 'run' };
    return null;
  }
  if (b.lang === 'python' && /(^|\s)check(\s|$)/.test(b.meta)) return { k: 'python' };
  if (b.lang === 'ts' && /(^|\s)check(\s|$)/.test(b.meta)) return { k: 'ts' };
  return null;
}

/** Splits a "$ " transcript into [{cmd, expect: [lines]}]; heredoc bodies belong to their command. */
export function steps(code) {
  const lines = code.split('\n');
  const hasPrompt = lines.some((l) => l.startsWith('$ '));
  if (!hasPrompt) return [{ cmd: code, expect: [] }];
  const out = [];
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    if (!l.startsWith('$ ')) {
      if (!out.length) throw new Error(`output before the first "$ " command: ${l}`);
      out[out.length - 1].expect.push(l);
      continue;
    }
    let cmd = l.slice(2);
    const hd = /<<-?'?([A-Z_]+)'?/.exec(cmd);
    if (hd) {
      const body = [];
      for (i++; i < lines.length && lines[i] !== hd[1]; i++) body.push(lines[i]);
      cmd += '\n' + body.join('\n') + '\n' + hd[1];
    }
    out.push({ cmd, expect: [] });
  }
  return out;
}

const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
/** Matches expected lines in order against output; returns null or the first expected line not found. */
export function match(expect, output) {
  const out = output.replace(/\x1b\[[0-9;]*m/g, '').split('\n').map((l) => l.replace(/\s+$/, ''));
  let j = 0;
  for (const e of expect) {
    const t = e.replace(/\s+$/, '');
    if (t === '…' || t === '') continue;
    const re = new RegExp('^' + t.split('…').map(esc).join('.*') + '$');
    while (j < out.length && !re.test(out[j])) j++;
    if (j >= out.length) return e;
    j++;
  }
  return null;
}

export const PLACEHOLDERS = {};
/** Fails on a placeholder no one can fill: <…> in a command that isn't in PLACEHOLDERS. */
export function placeholders(cmd) {
  const found = [...cmd.matchAll(/<([a-z][a-z0-9-]*)>/g)].map((m) => m[0]).filter((p) => !(p in PLACEHOLDERS));
  if (found.length) throw new Error(`unknown placeholder ${found.join(', ')}`);
  return cmd.replace(/<([a-z][a-z0-9-]*)>/g, (p) => PLACEHOLDERS[p]);
}

function demoKeys() {
  const f = join(repo, 'examples/demo/.demo-keys');
  if (!existsSync(f)) throw new Error('examples/demo/.demo-keys is missing. Run make demo-up DEMO_PORT=8091 first.');
  return Object.fromEntries(readFileSync(f, 'utf8').split('\n').filter((l) => /^[a-z-]+=/.test(l)).map((l) => l.split(/=(.*)/s).slice(0, 2)));
}

function demoEnv() {
  return Object.fromEntries(readFileSync(join(repo, 'examples/demo/.env'), 'utf8').split('\n').filter((l) => /^[A-Z_]+=/.test(l)).map((l) => l.split(/=(.*)/s).slice(0, 2)));
}

/** A "$ " command may end with "# exit N": the exit code it is expected to end with (a denial is 1). */
export function expectedExit(cmd) {
  const m = /#\s*exit (\d+)\s*$/.exec(cmd.split('\n')[0]);
  return m ? Number(m[1]) : 0;
}

function run(cmd, env, cwd, timeout) {
  const r = spawnSync('bash', ['-euo', 'pipefail', '-c', cmd], { cwd, env, encoding: 'utf8', timeout, maxBuffer: 16 << 20 });
  return { code: r.status, out: (r.stdout ?? '') + (r.stderr ?? ''), timedOut: r.error?.code === 'ETIMEDOUT' };
}

function main() {
  const args = process.argv.slice(2);
  const onlyPage = args.includes('--page') ? args[args.indexOf('--page') + 1] : null;
  const parseOnly = args.includes('--only-parse') || args.includes('--list');
  const all = pages().filter((p) => !p.generated);
  const fails = [];
  const skipped = [];
  let total = 0, ran = 0, checked = 0;
  const work = [];
  for (const p of all) {
    for (const b of blocks(p.body)) {
      let k;
      try {
        k = kind(b);
      } catch (e) {
        fails.push(`${p.rel}:${b.line}: ${e.message}`);
        continue;
      }
      if (!k) continue;
      total++;
      if (k.k === 'skip') {
        skipped.push(`${p.rel}:${b.line} · ${k.reason}${p.fm.owner ? ` (${p.fm.owner})` : ''}`);
        continue;
      }
      if (k.k === 'run') {
        try {
          steps(b.code).forEach((s) => placeholders(s.cmd));
        } catch (e) {
          fails.push(`${p.rel}:${b.line}: ${e.message}`);
          continue;
        }
      }
      work.push({ p, b, k });
    }
  }
  if (args.includes('--list')) {
    for (const w of work) console.log(`${w.k.k.padEnd(6)} ${w.p.rel}:${w.b.line}`);
    for (const s of skipped) console.log(`skip   ${s}`);
  }
  if (parseOnly) {
    report(total, 0, 0, skipped, fails, true);
    return;
  }

  const server = process.env.DOCS_SERVER ?? 'http://127.0.0.1:8091';
  const keys = demoKeys();
  const bin = join(repo, 'bin');
  const npxReg = join(site, 'scripts', 'npx-registry.sh');
  let registry = null;
  const usesNpx = work.some((w) => w.k.k === 'run' && /npx -y deedglass/.test(w.b.code) && (!onlyPage || w.p.slug === onlyPage));
  if (usesNpx) registry = execFileSync('bash', [npxReg, 'start'], { encoding: 'utf8' }).trim();
  const byPage = new Map();
  for (const w of work) {
    if (onlyPage && w.p.slug !== onlyPage) continue;
    if (!byPage.has(w.p)) byPage.set(w.p, []);
    byPage.get(w.p).push(w);
  }
  try {
    for (const [p, ws] of byPage) {
      const dir = mkdtempSync(join(tmpdir(), 'dg-snip-'));
      const init = p.fm.testEnv === 'init';
      const env = {
        ...process.env, NO_COLOR: '1', CI: 'true', DEEDGLASS_NO_BANNER: '1', HOME: process.env.HOME,
        PATH: `${bin}:${process.env.PATH}`, DEEDGLASS_SERVER: server, DEEDGLASS_API_KEY: keys.admin,
        npm_config_update_notifier: 'false', npm_config_fund: 'false', npm_config_audit: 'false',
      };
      if (registry) Object.assign(env, { npm_config_registry: `http://127.0.0.1:${registry}/`, npm_config_cache: join(dir, '.npm') });
      let ports = null;
      if (init) {
        delete env.DEEDGLASS_API_KEY;
        delete env.DEEDGLASS_SERVER;
        ports = { listen: 18300 + Math.floor(Math.random() * 400), pg: 56100 + Math.floor(Math.random() * 400) };
      } else {
        // The demo stack, as if deedglass init had set up this directory: the Ledger commands find
        // its database and seal key through .deedglass/stack.json, as they do in a Quickstart directory.
        mkdirSync(join(dir, '.deedglass'), { mode: 0o700 });
        const denv = demoEnv();
        writeFileSync(join(dir, '.deedglass', 'stack.json'), JSON.stringify({ object: 'local_stack', version: 1, pg_port: 55433, app_password: denv.DEEDGLASS_DEV_PG_APP_PASSWORD,
          listen: new URL(server).host, seal_key_file: join(repo, 'examples/demo/.demo/keys', denv.DEMO_SEAL_KEY) }), { mode: 0o600 });
        writeFileSync(join(dir, '.deedglass', 'quickstart.key'), keys['claude-code'] + '\n', { mode: 0o600 });
        writeFileSync(join(dir, '.deedglass', 'admin.key'), keys.admin + '\n', { mode: 0o600 });
      }
      let cwd = dir;
      for (const { b, k } of ws) {
        if (k.k === 'python' || k.k === 'ts') {
          const r = typecheck(k.k, b.code, dir);
          checked++;
          if (r) fails.push(`${p.rel}:${b.line}: ${r}`);
          continue;
        }
        ran++;
        for (const s of steps(b.code)) {
          let cmd = placeholders(s.cmd);
          if (init && /deedglass init\b/.test(cmd)) {
            cmd = cmd.replace(/deedglass init( --yes)?/, (m) => `${m} --listen 127.0.0.1:${ports.listen} --pg-port ${ports.pg} --no-open`);
            console.log(`  ${p.rel}:${b.line}: test rewrite: ${cmd.split('\n')[0]}`);
          }
          if (init) cmd = cmd.replace(/127\.0\.0\.1:8080/g, `127.0.0.1:${ports.listen}`);
          const cd = /^cd (\S+)$/.exec(cmd.trim()) ?? /&& cd (\S+)$/.exec(cmd.trim());
          const r = run(cmd, env, cwd, init ? 300_000 : 60_000);
          if (cd && r.code === 0) cwd = join(cwd, cd[1]);
          if (init && /deedglass init\b/.test(cmd)) env.DEEDGLASS_SERVER = `http://127.0.0.1:${ports.listen}`;
          if (r.timedOut || r.code !== expectedExit(cmd)) {
            fails.push(`${p.rel}:${b.line}: \`${s.cmd.split('\n')[0]}\` exited ${r.timedOut ? 'by timeout' : r.code} (expected ${expectedExit(cmd)})\n${indent(r.out)}`);
            break;
          }
          const miss = match(s.expect, r.out);
          if (miss) {
            fails.push(`${p.rel}:${b.line}: \`${s.cmd.split('\n')[0]}\` printed no line matching "${miss}"\n${indent(r.out)}`);
            break;
          }
          if (/^export /.test(cmd.trim())) {
            const ex = /^export ([A-Z_]+)="?\$\(cat ([^)]+)\)"?$/.exec(cmd.trim());
            if (ex) env[ex[1]] = readFileSync(join(cwd, ex[2]), 'utf8').trim();
          }
        }
      }
      if (init) teardown(cwd, env);
      rmSync(dir, { recursive: true, force: true });
    }
  } finally {
    if (registry) execFileSync('bash', [npxReg, 'stop'], { stdio: 'ignore' });
  }
  report(total, ran, checked, skipped, fails, false);
}

function teardown(cwd, env) {
  const stackFile = join(cwd, '.deedglass', 'stack.json');
  run('deedglass init --stop || true', env, cwd, 60_000);
  if (!existsSync(stackFile)) return;
  const s = JSON.parse(readFileSync(stackFile, 'utf8'));
  const names = [s.container, s.demo_container].filter(Boolean).join(' ');
  const vols = [s.volume, s.demo_volume].filter(Boolean).join(' ');
  run(`docker rm -f ${names} >/dev/null 2>&1 || true; docker volume rm ${vols} >/dev/null 2>&1 || true`, env, cwd, 60_000);
}

function typecheck(k, code, dir) {
  if (k === 'python') {
    const py = join(repo, 'sdk/python/.venv/bin');
    if (!existsSync(join(py, 'mypy'))) return 'sdk/python/.venv is missing: run make py-ci once';
    const f = join(dir, `snippet_${Math.random().toString(36).slice(2)}.py`);
    writeFileSync(f, code + '\n');
    const r = spawnSync(join(py, 'python'), ['-m', 'py_compile', f], { encoding: 'utf8' });
    if (r.status !== 0) return 'py_compile: ' + r.stderr;
    const m = spawnSync(join(py, 'mypy'), ['--strict', '--no-error-summary', '--ignore-missing-imports', f], { encoding: 'utf8', cwd: dir });
    return m.status === 0 ? null : 'mypy --strict:\n' + indent(m.stdout + m.stderr);
  }
  const ts = join(repo, 'sdk/typescript');
  const tsc = join(ts, 'node_modules/.bin/tsc');
  if (!existsSync(tsc)) return 'sdk/typescript/node_modules is missing: run make sdk-ts once';
  const d = mkdtempSync(join(dir, 'ts-'));
  writeFileSync(join(d, 'snippet.ts'), code + '\n');
  writeFileSync(join(d, 'tsconfig.json'), JSON.stringify({
    compilerOptions: { strict: true, noEmit: true, target: 'ES2022', module: 'ESNext', moduleResolution: 'Bundler', skipLibCheck: true,
      types: ['node'], typeRoots: [join(ts, 'node_modules/@types')], paths: { '@deedglass/sdk': [join(ts, 'src/index.ts')] }, allowImportingTsExtensions: true },
    files: ['snippet.ts'],
  }));
  const r = spawnSync(tsc, ['-p', d], { encoding: 'utf8' });
  return r.status === 0 ? null : 'tsc:\n' + indent(r.stdout + r.stderr);
}

const indent = (s) => s.trim().split('\n').slice(-25).map((l) => '    ' + l).join('\n');

function report(total, ran, checked, skipped, fails, parseOnly) {
  for (const s of skipped) console.log(`  ○ skipped ${s}`);
  if (fails.length) {
    for (const f of fails) console.error('✕ ' + f);
    process.exit(1);
  }
  console.log(parseOnly
    ? `✓ ${total} snippets parsed · ${skipped.length} skipped with reasons · 0 problems`
    : `✓ ${total} snippets · ${ran} run · ${checked} type-checked · ${skipped.length} skipped (reasons listed) · 0 failed`);
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) main();
