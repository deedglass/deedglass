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
  assert.ok(pricingProblems('Get one at deedglass.com/pro').length > 0); // DL-131
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

test('sitemap (DOC-05): thin per-code pages and /_kit/ leave, each kept URL gets its lastmod', async () => {
  const { sitemap } = await import('../postbuild.mjs');
  const S = 'https://docs.deedglass.com';
  const xml = `<urlset><url><loc>${S}/</loc></url><url><loc>${S}/errors/</loc><lastmod>x</lastmod></url><url><loc>${S}/errors/budget_exceeded/</loc></url>` +
    `<url><loc>${S}/reference/api/operations/getorg/</loc></url><url><loc>${S}/policies/errors/cel_cost/</loc></url><url><loc>${S}/_kit/</loc></url><url><loc>${S}/faq/</loc></url></urlset>`;
  const out = sitemap(xml, { '/': '2026-10-11', '/errors/': '2026-10-12' });
  assert.equal(out, `<urlset><url><loc>${S}/</loc><lastmod>2026-10-11</lastmod></url><url><loc>${S}/errors/</loc><lastmod>2026-10-12</lastmod></url><url><loc>${S}/faq/</loc></url></urlset>`);
  const { sitemapProblems } = await import('../check-seo.mjs');
  assert.deepEqual(sitemapProblems(out).problems, ['sitemap: only 2 of 3 URLs have a lastmod']);
  assert.match(sitemapProblems(xml).problems.join('\n'), /sitemap lists \/errors\/budget_exceeded\//);
  // Every URL on one date: the dates came from one commit (a shallow clone), not from each page's source.
  const urls = (d) => `<urlset>${Array.from({ length: 25 }, (_, i) => `<url><loc>${S}/p${i}/</loc><lastmod>${d(i)}</lastmod></url>`).join('')}</urlset>`;
  assert.match(sitemapProblems(urls(() => '2026-10-17')).problems.join('\n'), /all 25 lastmod dates are 2026-10-17/);
  assert.deepEqual(sitemapProblems(urls((i) => (i ? '2026-10-17' : '2026-10-01'))).problems, []);
});

test('lastmod (DOC-05): a shallow clone gives no dates rather than one commit\'s date for every page', async () => {
  const { gitDates, isShallow } = await import('../seo.mjs');
  const dir = mkdtempSync(join(tmpdir(), 'dg-shallow-'));
  const who = { GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@t', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@t' };
  const git = (cwd, args, at) => execFileSync('git', args, { cwd, encoding: 'utf8', env: { ...process.env, ...who, ...(at && { GIT_AUTHOR_DATE: at, GIT_COMMITTER_DATE: at }) } });
  try {
    const full = join(dir, 'full');
    mkdirSync(join(full, 'docs'), { recursive: true });
    git(full, ['init', '-q', '-b', 'main']);
    for (const [f, at] of [['a.md', '2026-01-02T00:00:00Z'], ['b.md', '2026-03-04T00:00:00Z']]) {
      writeFileSync(join(full, 'docs', f), `${f}\n`);
      git(full, ['add', '.']);
      git(full, ['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', f], at);
    }
    assert.equal(isShallow(full), false);
    assert.deepEqual(Object.fromEntries(gitDates(['docs'], { cwd: full })), { 'docs/b.md': '2026-03-04', 'docs/a.md': '2026-01-02' });
    const shallow = join(dir, 'shallow');
    git(dir, ['clone', '-q', '--depth', '1', `file://${full}`, shallow]);
    assert.equal(isShallow(shallow), true);
    assert.throws(() => gitDates(['docs'], { cwd: shallow, env: {} }), /this checkout is shallow.*fetch-depth: 0/);
    assert.equal(gitDates(['docs'], { cwd: shallow, env: { DOCS_UNDATED: '1' } }).size, 0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('seo pages (DOC-05): a thin page needs noindex; an indexed page needs a short title and one TechArticle', async () => {
  const { pageProblems } = await import('../check-seo.mjs');
  const { jsonLd, ldScript, pageTitle } = await import('../seo.mjs');
  assert.equal(pageTitle('Quickstart'), 'Quickstart | Deedglass Docs');
  assert.equal(pageTitle('A title that is long enough to push the suffix past sixty'), 'A title that is long enough to push the suffix past sixty');
  assert.equal(pageTitle('Deedglass Docs'), 'Deedglass Docs');
  assert.equal(pageTitle('Stop', '/reference/engineering/stop/'), 'Stop (engineering notes) | Deedglass Docs');
  assert.equal(pageTitle('Stop', '/concepts/stop/'), 'Stop | Deedglass Docs');
  const ld = (o) => `<script type="application/ld+json">${ldScript(o)}</script>`;
  const canon = (p) => `<link rel="canonical" href="https://docs.deedglass.com${p}"/>`;
  const good = `<title>Quickstart | Deedglass Docs</title>${canon('/quickstart/')}${ld(jsonLd({ path: '/quickstart/', title: 'Quickstart', description: 'd' }))}`;
  assert.deepEqual(pageProblems('/quickstart/', good), []);
  // seo-aeo-v1: a step-by-step page carries a HowTo beside its TechArticle, steps pointing at its own headings.
  const headings = [{ depth: 2, slug: 'install', text: 'Install' }, { depth: 2, slug: 'run', text: 'Run it' }, { depth: 2, slug: 'next-steps', text: 'Next steps' }];
  const how = jsonLd({ path: '/quickstart/', title: 'Quickstart', description: 'd', headings });
  assert.deepEqual(how['@graph'].map((n) => n['@type']), ['TechArticle', 'HowTo']);
  assert.deepEqual(how['@graph'][1].step.map((st) => st.name), ['Install', 'Run it']);
  const withHow = `<title>Quickstart | Deedglass Docs</title>${canon('/quickstart/')}${ld(how)}<h2 id="install">Install</h2><h2 id="run">Run it</h2>`;
  assert.deepEqual(pageProblems('/quickstart/', withHow), []);
  assert.match(pageProblems('/quickstart/', withHow.replace('id="run"', 'id="x"')).join('\n'), /isn't on the page/);
  assert.equal(jsonLd({ path: '/concepts/deeds/', title: 'Deeds', description: 'd', headings })['@type'], 'TechArticle');
  assert.match(pageProblems('/quickstart/', good.replace(canon('/quickstart/'), canon('/faq/'))).join('\n'), /canonical/);
  const noted = `${good}<div class="sl-markdown-content"><aside><p>This page is <code>docs/X.md</code> in the Deedglass repo, imported at build time.</p></aside><p>Body.</p></div>`;
  assert.match(pageProblems('/quickstart/', noted).join('\n'), /imported-file note/);
  assert.deepEqual(pageProblems('/errors/x/', '<title>x</title>'), ['/errors/x/: a thin page without noindex']);
  assert.match(pageProblems('/faq/', '<title>FAQ</title><meta name="robots" content="noindex, follow">').join('\n'), /noindex on a page that should be indexed/);
  assert.match(pageProblems('/faq/', `<title>${'x'.repeat(61)}</title>`).join('\n'), /61 characters/);
  assert.match(pageProblems('/', good).join('\n'), /no WebSite/);
  assert.ok(!ldScript({ headline: '</script><script>alert(1)</script>' }).includes('<'), 'JSON-LD escapes <');
});

test('llms (seo-aeo-v1): the imported-file note is left out of the llms files, the page text around it stays', async () => {
  const { dropSourceNotes } = await import('../postbuild.mjs');
  const page = 'Intro line.\n\nSource\n\nThis page is `docs/INSTALL.md` in the Deedglass repo, imported at build time.\n\nNext page text.';
  assert.equal(dropSourceNotes(page), 'Intro line.\n\nNext page text.');
  const part = 'Body.\nSource\n\nThis page is part of `docs/LIMITATIONS.md` in the Deedglass repo, imported at build time. [Every limit, by part](/security/limitations/).\n';
  assert.equal(dropSourceNotes(part), 'Body.\n');
  assert.equal(dropSourceNotes('Source\n\nSomething else.\n'), 'Source\n\nSomething else.\n');
});

test('robots.txt (DOC-03): named groups, each repeating Disallow: /_kit/, plus * and the sitemap', async () => {
  const { robotsProblems } = await import('../check-seo.mjs');
  assert.deepEqual(robotsProblems(readFileSync(join(site, 'public/robots.txt'), 'utf8')), []);
  const bad = 'User-agent: Googlebot\nAllow: /\n\nUser-agent: *\nAllow: /\nDisallow: /_kit/\n';
  const p = robotsProblems(bad);
  assert.ok(p.includes("the group for Googlebot doesn't repeat Disallow: /_kit/"), p.join('\n'));
  assert.ok(p.includes('no group names GPTBot') && p.includes('no Sitemap line'), p.join('\n'));
});

test('llms (DOC-02): a page outside the file\'s set is dropped, an unknown page throws, and the gate sees leftovers', async () => {
  const { filterLlms } = await import('../postbuild.mjs');
  const { PAGE_MARK, inFull, inSmall } = await import('../seo.mjs');
  const heads = new Map([
    ['# Quickstart\n\n> Five minutes.', ['quickstart']],
    ['# budget_exceeded\n\n> A budget ran out.', ['errors/budget_exceeded']],
    ['# Engineering notes\n\n> The notes.', ['reference/engineering']],
  ]);
  const text = ['<SYSTEM>x</SYSTEM>', '# Quickstart\n\n> Five minutes.\n\nbody', '# budget_exceeded\n\n> A budget ran out.\n\nbody', '# Engineering notes\n\n> The notes.\n\nbody'].join(PAGE_MARK);
  const r = filterLlms(text, heads, inFull);
  assert.equal(r.text, '<SYSTEM>x</SYSTEM>\n\n# Quickstart\n\n> Five minutes.\n\nbody');
  assert.deepEqual([r.kept, r.dropped], [1, 2]);
  assert.throws(() => filterLlms(text + PAGE_MARK + '# Nobody\n\nbody', heads, inFull), /doesn't start with any page/);
  const { setProblems, indexProblems, ASK } = await import('../check-llms.mjs');
  assert.deepEqual(setProblems('llms-full.txt', r.text, heads, inFull), []);
  assert.match(setProblems('llms-full.txt', text.replaceAll(PAGE_MARK, '\n\n'), heads, inFull).join('\n'), /errors\/budget_exceeded, which is outside its set/);
  assert.equal(inSmall('reference/connectors/postgres'), false);
  assert.equal(inSmall('quickstart'), true);
  const desc = { summary: 'Deedglass sits between AI agents and the systems they change.' };
  const S = 'https://docs.deedglass.com';
  const { LLMS_PAGES } = await import('../seo.mjs');
  const pagesLines = LLMS_PAGES.map((x) => `- [${x.title}](${S}${x.path}): ${x.what}.`).join('\n');
  const ok = `# Deedglass\n\n> ${desc.summary}\n\n- ${ASK} llms-install.md skills/safe-postgres-agents/SKILL.md\n${pagesLines}\n\n${S}/llms-small.txt ${S}/llms-full.txt ${S}/llms-postgres.txt\n`;
  assert.deepEqual(indexProblems(ok, desc), []);
  assert.ok(indexProblems(ok.replace(pagesLines.split('\n')[0], ''), desc).includes(`llms.txt: no link to ${LLMS_PAGES[0].path}`));
  const p = indexProblems(ok.replace(ASK, 'Run deedglass init --yes.') + 'L0 Reported', desc);
  assert.ok(p.some((x) => /missing "If the user asked you/.test(x)) && p.includes('llms.txt: mentions --yes') && p.includes('llms.txt: uses the retired level names'), p.join('\n'));
});

test('llms (DOC-04): a task page llms.txt names must be built with a visible Updated date', async () => {
  const { taskPageProblems } = await import('../check-llms.mjs');
  const p = '/guides/x/';
  assert.deepEqual(taskPageProblems(p, '<h1>X</h1><p class="dg-updated" data-astro-cid-1>Updated <time datetime="2026-10-18">18 October 2026</time></p>'), []);
  assert.match(taskPageProblems(p, '<h1>X</h1><p>Updated recently</p>').join(), /no visible Updated date/);
  assert.match(taskPageProblems(p, null).join(), /isn't built/);
});

test('llms (tidy): heading links, empty anchor links and table padding go; fenced code stays byte for byte', async () => {
  const { tidyLlms } = await import('../postbuild.mjs');
  const code = '```\n| keep    |  x |\n[Section titled “A”](#a)\n```';
  const t = `## A\n\n[Section titled “A”](#a)\n\nText []()[x](/y)\n\n| Aaa    | B  |\n| ------ | -- |\n| 1      | 2  |\n\n${code}\n`;
  assert.equal(tidyLlms(t), `## A\n\nText [x](/y)\n\n| Aaa | B |\n| --- | --- |\n| 1 | 2 |\n\n${code}\n`);
});

test('limitations (DOC-05): every section of docs/LIMITATIONS.md has one page, and old anchors find their part', async () => {
  const { LIMITS_FILE, LIMITS_INDEX, LIMITS_PARTS, headingId, limitsPartFor, rewriteLinks, sectionName, splitSections } = await import('../sync-docs.mjs');
  const md = readFileSync(join(repo, LIMITS_FILE), 'utf8');
  const { sections } = splitSections(md);
  const names = sections.map((x) => sectionName(x.heading));
  const listed = [...LIMITS_INDEX, ...LIMITS_PARTS.flatMap((x) => x.sections)];
  assert.deepEqual([...names].sort(), [...listed].sort(), 'each ## section is listed exactly once');
  assert.equal(sectionName('Undo (S23)'), 'Undo');
  assert.equal(headingId('Undo (S23)'), 'undo-s23');
  const undo = sections.find((x) => sectionName(x.heading) === 'Undo');
  assert.equal(limitsPartFor(headingId(undo.heading), md), 'postgres');
  assert.equal(limitsPartFor('known-limits-in-v1', md), null);
  assert.match(rewriteLinks(`[u](LIMITATIONS.md#${headingId(undo.heading)})`, 'docs/OBSERVE.md'), new RegExp(`\\(/security/limitations/postgres/#${headingId(undo.heading)}\\)`));
});

test('lcp (DOC-LCP): only small CSS and JS inline, never a font; one font preload', async () => {
  const { inlineSmallCode } = await import('../seo.mjs');
  assert.equal(inlineSmallCode('a.css', Buffer.alloc(100)), true);
  assert.equal(inlineSmallCode('a.js', Buffer.alloc(5000)), false);
  assert.equal(inlineSmallCode('inter.woff2', Buffer.alloc(10)), false);
  const { PRELOAD_FONTS } = await import('../postbuild.mjs');
  assert.equal(PRELOAD_FONTS.length, 1);
});
