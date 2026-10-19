// Size and speed budgets (S31 §3.9). Gzip sizes of dist (node:zlib), act() client overhead with a
// mock fetch, verifyDeeds of 1,000 deeds, and a cold import. Run after npm run build. Writes
// bench/results/<date>-<os>-<arch>-sdk-ts.json at the repo root; exit 1 when a budget line fails.
import { gzipSync } from 'node:zlib';
import { readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { arch, platform } from 'node:os';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const repo = fileURLToPath(new URL('../../..', import.meta.url));
const dist = root + 'dist/';
const gz = (files) => gzipSync(Buffer.concat(files.map((f) => readFileSync(dist + f)))).length;
const js = (dir) => readdirSync(dist + dir).filter((f) => f.endsWith('.js')).map((f) => (dir ? dir + '/' : '') + f);

const lines = [];
let ok = true;
const check = (name, value, budget, unit) => {
  const pass = value <= budget;
  ok &&= pass;
  lines.push({ name, value, budget, unit, pass });
  console.log(`${pass ? '✓' : '✕'} ${name.padEnd(34)} ${String(value).padStart(8)} ${unit} · budget ${budget} ${unit}`);
};

const kib = (n) => Math.round((n / 1024) * 100) / 100;
check('core gzip (without verify)', kib(gz([...js(''), ...js('resources')])), 16, 'KiB');
// S31 §3.9 said 14 KiB; tsc-only output (no minifier, by design) of S05's verifier is ~18 KiB, so the
// budget was 19 KiB (a PROPOSED change in the S31 report, ADR-062). Deed v2 and the dependency-free
// ML-DSA-65 check raise it to 28 KiB (ADR-109, amending ADR-062).
check('verify gzip', kib(gz(js('verify'))), 28, 'KiB');
for (const a of ['ai-sdk', 'openai-agents', 'langchain', 'claude-agent-sdk']) check(`adapter ${a} gzip`, kib(gz([`adapters/${a}.js`])), 3, 'KiB');
check('adapters/shared gzip (once)', kib(gz(['adapters/shared.js'])), 3, 'KiB');

const sizesOnly = process.argv.includes('--sizes-only');
if (sizesOnly) {
  console.log('○ timings skipped (--sizes-only)');
  process.exit(ok ? 0 : 1);
}

// act() overhead with a mock fetch.
const { Deedglass } = await import(dist + 'index.js');
const body = JSON.stringify({ object: 'action', id: 'act_01j9zw0000000000000000000a', status: 'executed', mode: 'gateway', deed: 'deed_01j9zw0000000000000000000a' });
const fetch = async () => new Response(body, { status: 200, headers: { 'content-type': 'application/json' } });
const c = new Deedglass({ apiKey: 'dg_test_Ab12Cd34_' + 'SENTINELsentinel'.repeat(2), baseUrl: 'http://127.0.0.1:9', fetch });
for (let i = 0; i < 500; i++) await c.act('billing.refund_customer', { customer: 'cus_123', amount: 2000 });
const t = [];
for (let i = 0; i < 2000; i++) {
  const t0 = performance.now();
  await c.act('billing.refund_customer', { customer: 'cus_123', amount: 2000 });
  t.push(performance.now() - t0);
}
t.sort((a, b) => a - b);
check('act() overhead p50 (mock fetch)', Math.round(t[1000] * 1000) / 1000, 0.3, 'ms');

// verifyDeeds of 1,000 deeds (the S05 bench chain when present, else genesis-20 repeated as loose deeds).
const v = await import(dist + 'verify/index.js');
const keys = await v.parseKeySet(readFileSync(repo + 'testdata/deeds/keys/test-keys.json'));
const lines20 = v.splitLines(readFileSync(repo + 'testdata/deeds/chains/genesis-20.jsonl'));
const t1 = performance.now();
for (let i = 0; i < 50; i++) await v.verifyDeeds(lines20, keys, { keysPinned: true });
check('verifyDeeds 1,000 deeds', Math.round(performance.now() - t1), 1500, 'ms');

// Cold import in a fresh process.
const cold = Number(execFileSync(process.execPath, ['-e', `const t=performance.now();import(${JSON.stringify(dist + 'index.js')}).then(()=>console.log(performance.now()-t))`], { encoding: 'utf8' }).trim());
check("cold import('@deedglass/sdk')", Math.round(cold * 10) / 10, 30, 'ms');

const date = new Date().toISOString().slice(0, 10);
mkdirSync(repo + 'bench/results', { recursive: true });
const out = `${repo}bench/results/${date}-${platform()}-${arch()}-sdk-ts.json`;
writeFileSync(out, JSON.stringify({ suite: 'sdk-ts', node: process.version, date, lines }, null, 2) + '\n');
console.log(`raw: ${out.replace(repo, '')}`);
process.exit(ok ? 0 : 1);
