// Tests for the docs gates (S33 §6): each gate fails on the thing it exists to catch.
// node --test scripts/test/   (part of npm run check)
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..');
const repo = join(site, '..');
const node = (args, env = {}) => spawnSync('node', args, { cwd: site, encoding: 'utf8', env: { ...process.env, ...env } });

test('errors: a deleted prose section fails check-errors, naming the code', () => {
  const prose = JSON.parse(readFileSync(join(site, 'src/data/errors-prose.json'), 'utf8'));
  delete prose.budget_exceeded;
  const dir = mkdtempSync(join(tmpdir(), 'dg-t-'));
  writeFileSync(join(dir, 'prose.json'), JSON.stringify(prose));
  const r = node(['scripts/check-errors.mjs'], { ERRORS_PROSE: join(dir, 'prose.json') });
  rmSync(dir, { recursive: true });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /budget_exceeded: no "## budget_exceeded" section/);
});

test('errors: the prose parser needs every part of a section', async () => {
  const { parseErrorsProse } = await import('../gen-data.mjs');
  const ok = parseErrorsProse('# E\n\n## x_code\n\n**400 · invalid_request_error**\n\n**When it happens.** Now.\n\n**What to do.** Fix it.\n\n```json\n{"code":"x_code"}\n```\n');
  assert.equal(ok.x_code.status, 400);
  assert.deepEqual(ok.x_code.example, { code: 'x_code' });
  assert.throws(() => parseErrorsProse('## y_code\n\n**400 · invalid_request_error**\n\n**When it happens.** Now.\n'), /y_code section needs/);
});

test('generated data: an edited token, catalog or formula is stale (Run npm run gen)', async () => {
  const { stale } = await import('../gen-data.mjs');
  const dir = mkdtempSync(join(tmpdir(), 'dg-t-'));
  mkdirSync(join(dir, 'src/styles'), { recursive: true });
  mkdirSync(join(dir, 'src/data'), { recursive: true });
  const files = { 'src/styles/tokens.gen.css': ':root { --ink-950: #07080c; }\n', 'src/data/score-formula.txt': 'points = 100\n' };
  for (const [k, v] of Object.entries(files)) writeFileSync(join(dir, k), v);
  assert.deepEqual(stale(files, dir), []);
  writeFileSync(join(dir, 'src/styles/tokens.gen.css'), ':root { --ink-950: #000000; }\n');
  writeFileSync(join(dir, 'src/data/score-formula.txt'), 'points = 99\n');
  assert.deepEqual(stale(files, dir).sort(), ['src/data/score-formula.txt', 'src/styles/tokens.gen.css']);
  rmSync(dir, { recursive: true });
  const r = node(['scripts/gen-data.mjs', '--check']);
  assert.equal(r.status, 0, r.stderr);
});

test('snippets: a wrong expected line, an unknown placeholder and a skip without a reason fail', async () => {
  const { match, placeholders, kind, steps, blocks, expectedExit } = await import('../test-snippets.mjs');
  assert.equal(match(['✓ Chain intact · … deeds'], '✓ Chain intact · 26 deeds\n'), null);
  assert.equal(match(['✓ Chain broken'], '✓ Chain intact · 26 deeds\n'), '✓ Chain broken');
  assert.throws(() => placeholders('deedglass act <your-tool>'), /unknown placeholder <your-tool>/);
  assert.throws(() => kind({ lang: 'sh', meta: 'test=skip' }), /needs reason/);
  assert.deepEqual(kind({ lang: 'sh', meta: 'test=skip reason="needs Slack"' }), { k: 'skip', reason: 'needs Slack' });
  const s = steps("$ cat > a.yaml <<'YAML'\nk: v\nYAML\n$ echo hi\nhi");
  assert.equal(s.length, 2);
  assert.match(s[0].cmd, /k: v\nYAML$/);
  assert.deepEqual(s[1].expect, ['hi']);
  assert.equal(blocks('x\n```sh test\n$ echo\n```\n')[0].line, 2);
  assert.equal(expectedExit('deedglass act x # exit 1'), 1);
});

test('links: a broken anchor and a missing page fail; a redirect with an anchor resolves', () => {
  const dist = mkdtempSync(join(tmpdir(), 'dg-dist-'));
  mkdirSync(join(dist, 'a'));
  mkdirSync(join(dist, 'doctor-checks'));
  writeFileSync(join(dist, 'index.html'), '<a href="/a/#verified">x</a>');
  writeFileSync(join(dist, 'a/index.html'), '<h2 id="verified">V</h2><a href="/a/#verifed">typo</a>');
  writeFileSync(join(dist, 'doctor-checks/index.html'), '<a id="db-reachable"></a>');
  writeFileSync(join(dist, '_redirects'), '/doctor /doctor-checks/ 301\n');
  const code = `import { resolvePath } from './scripts/check-dist-lib.mjs';
    console.log(JSON.stringify([resolvePath('/a/#verified'), resolvePath('/a/#verifed'), resolvePath('/b/'), resolvePath('/doctor#db-reachable')]));`;
  const r = spawnSync('node', ['--input-type=module', '-e', code], { cwd: site, encoding: 'utf8', env: { ...process.env, DIST: dist } });
  rmSync(dist, { recursive: true });
  assert.deepEqual(JSON.parse(r.stdout), [null, 'no #verifed on /a/', 'no page /b/', null]);
});

test('third-party: an injected <script src="https://…"> fails the dist scan', () => {
  const dist = mkdtempSync(join(tmpdir(), 'dg-dist-'));
  writeFileSync(join(dist, 'index.html'), '<script src="https://cdn.example.com/x.js"></script>');
  const r = node(['scripts/check-dist.mjs'], { DIST: dist });
  rmSync(dist, { recursive: true });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /third: \/: <script src="https:\/\/cdn\.example\.com/);
});

test('secrets: a real-looking key in dist fails; the documented example values pass', () => {
  const dist = mkdtempSync(join(tmpdir(), 'dg-dist-'));
  writeFileSync(join(dist, 'index.html'), '<code>dg_test_abcd1234_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef</code>');
  const r = node(['scripts/check-dist.mjs'], { DIST: dist });
  writeFileSync(join(dist, 'index.html'), '<code>dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8</code>');
  const ok = node(['scripts/check-dist.mjs'], { DIST: dist });
  rmSync(dist, { recursive: true });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /secrets: index\.html holds dg_test_abcd/);
  assert.equal(ok.status, 0, ok.stderr);
});

test('claims: a DO NOT USE or unapproved row is not usable', async () => {
  const { usable } = await import('../check-claims.mjs');
  assert.equal(usable({ approved: 'APPROVED (DL-048)', until: 'none' }), true);
  assert.equal(usable({ approved: 'APPROVED (DL-048)', until: 'met (accepted under DL-048)' }), true);
  assert.equal(usable({ approved: 'DO NOT USE', until: 'none' }), false);
  assert.equal(usable({ approved: 'APPROVED', until: 'founder re-opens the URL' }), false);
  assert.equal(usable({ approved: 'PROPOSED · S33', until: 'none' }), false);
});

test('banned words: a page with a banned word fails the repo check', () => {
  const f = join(site, 'src/content/docs/zz-banned-word-probe.md');
  writeFileSync(f, '---\ntitle: probe\ndescription: probe\n---\n\nThis is ' + 'tamper' + '-proof.\n');
  try {
    const r = spawnSync('bash', [join(repo, 'scripts/banned_words.sh')], { cwd: repo, encoding: 'utf8' });
    assert.notEqual(r.status, 0);
    assert.match(r.stdout + r.stderr, /zz-banned-word-probe/);
  } finally {
    rmSync(f);
  }
});

test('workflows: pull_request_target, a checkout in workflow_run and a job without permissions fail', async () => {
  const { structural } = await import('../check-workflows.mjs');
  assert.deepEqual(structural('on: pull_request\npermissions: {}\njobs:\n  a:\n    permissions: {contents: read}\n    steps: []\n'), []);
  assert.match(structural('on: pull_request_target\npermissions: {}\njobs:\n  a:\n    permissions: {}\n    steps: []\n').join(), /pull_request_target/);
  assert.match(structural('on:\n  workflow_run:\n    workflows: [x]\npermissions: {}\njobs:\n  a:\n    permissions: {}\n    steps:\n      - uses: actions/checkout@abc\n').join(), /checks out code/);
  assert.match(structural('on: pull_request\npermissions: {}\njobs:\n  a:\n    steps: []\n').join(), /no permissions block/);
});

test('install links: every generated link decodes; a hand-edited one fails', async () => {
  const { check } = await import('../check-install-links.mjs');
  const data = JSON.parse(readFileSync(join(site, 'src/data/install-links.json'), 'utf8'));
  assert.deepEqual(check(data), []);
  const bad = structuredClone(data);
  bad.clients[0].link = bad.clients[0].link.replace(/config=[^&]+/, 'config=' + Buffer.from('{"command":"npx","args":["-y","evil"]}').toString('base64'));
  assert.match(check(bad).join(), /Cursor: decodes to/);
});

test('drafts: a draft without an owner fails', async () => {
  const { draftProblems } = await import('../check-drafts.mjs');
  const { fails } = draftProblems([{ rel: 'x.mdx', slug: 'zz-not-built', fm: { draft: true }, body: '', generated: false }]);
  assert.match(fails.join(), /draft without owner/);
});

test('pricing: a retired offer or the licence-category phrase fails', async () => {
  const { pricingProblems, FAQ_FREE } = await import('../check-pricing.mjs');
  assert.equal(pricingProblems('Pro is $29 a month.').length, 0);
  assert.ok(pricingProblems('The $199 Check').length > 0);
  assert.ok(pricingProblems('see deedglass.com/check').length > 0);
  assert.equal(pricingProblems('an ' + 'open' + ' source core').length, 0); // public since the switch
  assert.notEqual(FAQ_FREE.replace('.', ''), FAQ_FREE);
});

test('postbuild: copy buttons copy the commands only', async () => {
  const { commandsOnly } = await import('../postbuild.mjs');
  assert.equal(commandsOnly('$ a\x7fout\x7f$ b'), 'a\x7fb');
  assert.equal(commandsOnly('plain\x7ftext'), 'plain\x7ftext');
});

test('DeedCard: the sample verifies; one changed digit is a MISMATCH with the CLI sentence', async () => {
  const samples = JSON.parse(readFileSync(join(site, 'src/data/samples.json'), 'utf8'));
  const v = await import(join(repo, 'web/verify/dist/index.js'));
  const keys = await v.parseKeySet(samples.keys);
  const r = await v.verifyDeed(samples.deed, keys);
  assert.match(r.deedId, /^deed_/);
  let msg;
  try {
    await v.verifyDeed(samples.tampered, keys);
  } catch (e) {
    msg = v.message(e).sentence;
  }
  assert.ok(msg, 'the tampered deed must fail');
  const dir = mkdtempSync(join(tmpdir(), 'dg-t-'));
  writeFileSync(join(dir, 'deed.json'), samples.tampered);
  writeFileSync(join(dir, 'keys.json'), samples.keys);
  const cli = spawnSync(join(repo, 'bin/deedglass'), ['verify', join(dir, 'deed.json'), '--keys', join(dir, 'keys.json')], { encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' } });
  rmSync(dir, { recursive: true });
  assert.equal(cli.status, 1);
  assert.ok((cli.stdout + cli.stderr).includes(msg), `CLI said: ${cli.stdout}${cli.stderr}\npage says: ${msg}`);
});

test('inventory: every docs/*.md is imported, moved, a generator source or repo-only', async () => {
  const { IMPORTS, MOVED, SOURCES, REPO_ONLY } = await import('../sync-docs.mjs');
  const { walk } = await import('../postbuild.mjs');
  const known = new Set([...Object.keys(IMPORTS), ...Object.keys(MOVED), ...Object.keys(SOURCES), ...REPO_ONLY]);
  const orphans = [...walk(join(repo, 'docs'))].map((f) => f.slice(repo.length + 1)).filter((f) => f.endsWith('.md') && !known.has(f));
  assert.deepEqual(orphans, []);
});

test('security.txt (S34, RFC 9116): the five fields, a mailto contact, an https policy and an Expires within a year', () => {
  const txt = readFileSync(join(site, 'public/.well-known/security.txt'), 'utf8');
  const fields = {};
  for (const line of txt.split('\n').filter(Boolean)) {
    const [k, ...v] = line.split(': ');
    (fields[k] ??= []).push(v.join(': '));
  }
  assert.deepEqual(Object.keys(fields).sort(), ['Canonical', 'Contact', 'Expires', 'Policy', 'Preferred-Languages']);
  assert.match(fields.Contact[0], /^mailto:security@deedglass\.com$/);
  assert.ok(fields.Canonical.includes('https://docs.deedglass.com/.well-known/security.txt'));
  assert.match(fields.Policy[0], /^https:\/\/github\.com\/deedglass\/deedglass\/blob\/main\/SECURITY\.md$/);
  const expires = Date.parse(fields.Expires[0]);
  assert.ok(expires > Date.now(), 'Expires is in the past: renew security.txt');
  assert.ok(expires - Date.now() < 366 * 24 * 3600 * 1000, 'Expires is more than a year away (RFC 9116 §2.5.5 recommends less)');
});

test('disclosure page (S34): it carries SECURITY.md\'s commitments word for word', () => {
  const page = readFileSync(join(site, 'src/content/docs/security/disclosure.mdx'), 'utf8').replace(/\s+/g, ' ');
  const policy = readFileSync(join(repo, 'SECURITY.md'), 'utf8').replace(/\s+/g, ' ');
  for (const s of ['within 3 business days', 'within 7 business days', 'within 30 days', 'There is **no bug bounty** at this stage.',
    'The address is being set up.', 'We won\'t pursue or support legal action against research done in good faith within this policy.',
    'The latest minor release gets security fixes.']) {
    assert.ok(policy.includes(s), `SECURITY.md lacks "${s}"`);
    assert.ok(page.includes(s), `the disclosure page lacks "${s}" (keep it equal to SECURITY.md)`);
  }
});
