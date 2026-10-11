// S25 §6 Playwright: the five screens against a real serve, a real Postgres and the seeded L2 hero.
//  1. the feed receives fake actions live, filters by verdict, keeps the filter on reload, j j Enter
//  2. the demo MISMATCH: the sentence, "Showing 20 of 301 rows", the in-browser seal check ✓, and
//     a download whose bytes equal GET /v1/deeds/{id}
//  3. first run: create the demo agent, the key sheet, an action for that agent, ✓
//  4. keys: the secret shows once and is nowhere in the page after a reload
//  5. Stop an agent in tab A; tab B shows Stopped within 2 s
//  6. a viewer sees no Create, Stop or Roll, and the API refuses the page's own POST
//  7. the state matrix (loading, empty, filtered empty, error, 404) and 360 px cards
import { spawn, spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { AUTH, dg, EMAIL, expect, guard, hero, signIn, test } from './fixtures.ts';

const DG = process.env.WEB_E2E_DG ?? '../bin/deedglass';
const fake = (n: number, pace = '150ms', org = 'acme') => spawn(DG, ['dev', 'fake-action', '--org', org, '--n', String(n), '--pace', pace, '--quiet'], { cwd: process.env.WEB_E2E_WORK, env: process.env, stdio: 'ignore' });

test.describe('owner', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · the feed is live, filterable and keyboard-driven', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app/actions');
    await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
    await expect(page.locator('table.feed tbody tr').first()).toBeVisible();
    const child = fake(6);
    await expect(page.locator('table.feed tr[data-live]').first()).toBeVisible({ timeout: 3000 });
    await new Promise<void>((r) => child.on('exit', () => r()));
    // Pause: new rows wait behind the pill.
    await page.keyboard.press('p');
    await expect(page.locator('.feed-pill')).toContainText('Paused');
    const child2 = fake(3);
    await new Promise<void>((r) => child2.on('exit', () => r()));
    await expect(page.locator('.feed-pill')).toContainText(/Paused · \d+ new actions/, { timeout: 5000 });
    await page.locator('.feed-pill').click();
    await expect(page.locator('.feed-pill')).toBeHidden();
    // Filter by MISMATCH; the URL carries it and a reload keeps it.
    await page.locator('[data-filter="verdict"] button', { hasText: "Doesn't match" }).click();
    await expect(page).toHaveURL(/verdict=MISMATCH/);
    await expect(page.locator('table.feed tbody tr').first()).toContainText("Doesn't match");
    await page.reload();
    await expect(page.locator('[data-filter="verdict"] button', { hasText: "Doesn't match" })).toHaveAttribute('aria-pressed', 'true');
    const rows = page.locator('table.feed tbody tr');
    await expect(rows.first()).toBeVisible();
    expect(await rows.locator('.chip-verified').count()).toBe(0);
    await page.locator('main').click({ position: { x: 5, y: 5 } });
    await page.keyboard.press('j');
    await page.keyboard.press('j');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/app\/actions\/act_/);
    await expect(page.locator('[data-sentence]')).toContainText("Doesn't match · ");
    await done();
  });

  test('2 · the demo MISMATCH: sentence, diff, seal check and the exact bytes', async ({ page, context }) => {
    const done = await guard(page);
    const { mismatch } = hero();
    await page.goto(`/app/actions/${mismatch}`);
    await expect(page.locator('[data-sentence]')).toHaveText("Doesn't match · Postgres changed 301 rows in public.orders. The agent reported 1.");
    await expect(page.locator('[data-diff-showing]')).toHaveText(/Showing 20 of 301 rows/);
    await expect(page.locator('table.diff tbody tr')).toHaveCount(20);
    await expect(page.locator('table.diff tr.diff-unexpected').first()).toHaveAttribute('aria-label', 'unexpected change');
    await page.getByRole('button', { name: 'Check in this browser' }).click();
    await expect(page.locator('.deed-check')).toHaveAttribute('data-state', 'valid');
    await expect(page.locator('.deed-check')).toContainText('Seal valid · key sk_');
    const [dl] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download deed' }).click()]);
    const bytes = readFileSync((await dl.path())!);
    const deed = await page.locator('.deed-panel').getAttribute('data-deed');
    const res = await context.request.get(`/v1/deeds/${deed}`, { headers: { 'Deedglass-Version': '2026-10-01' } });
    expect(Buffer.compare(bytes, await res.body())).toBe(0);
    // The CLI checks the downloaded file against this server's keys (S25 §9).
    const out = spawnSync(DG, ['verify', (await dl.path())!, '--keys', `${process.env.WEB_E2E_BASE}/.well-known/deedglass/keys.json`, '--no-color'], { encoding: 'utf8', env: process.env });
    expect(out.status, out.stdout + out.stderr).toBe(0);
    await done();
  });

  test('2b · a deed changed by one byte is refused with the right sentence', async ({ page }) => {
    const { mismatch } = hero();
    await page.goto(`/app/actions/${mismatch}`);
    await expect(page.locator('.deed-panel')).toBeVisible();
    const deed = await page.locator('.deed-panel').getAttribute('data-deed');
    await page.route(`**/v1/deeds/${deed}`, async (route) => {
      const res = await route.fetch();
      const text = (await res.text()).replace('"seq":', '"seq": ').replace('support-bot', 'support-bou');
      await route.fulfill({ response: res, body: text });
    });
    await page.reload();
    await page.getByRole('button', { name: 'Check in this browser' }).click();
    await expect(page.locator('.deed-check')).toHaveAttribute('data-state', 'invalid');
    await expect(page.locator('.deed-check')).not.toContainText('Seal valid');
  });

  test('3 · keys: the secret shows once and is gone after a reload', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app/agents');
    await page.getByRole('link', { name: 'billing-bot' }).click();
    await page.getByRole('tab', { name: 'Keys' }).click();
    await page.getByRole('button', { name: 'Create key' }).click();
    const keyName = `e2e-once-${Date.now()}`;
    await page.locator('#k-name').fill(keyName);
    await page.locator('.sheet').getByRole('button', { name: 'Create key' }).click();
    const secret = await page.locator('[data-secret]').inputValue();
    expect(secret).toMatch(/^dg_test_[A-Za-z0-9]{8}_[A-Za-z0-9]{32}$/);
    await expect(page.getByRole('button', { name: 'Done' })).toBeDisabled();
    await page.getByLabel("I've stored this key in a secret manager").check();
    await page.getByRole('button', { name: 'Done' }).click();
    expect(await page.content()).not.toContain(secret);
    await page.reload();
    await expect(page.getByRole('tab', { name: 'Keys' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByText(keyName)).toBeVisible();
    expect(await page.content()).not.toContain(secret.slice(-32));
    await done();
  });

  test('4 · Stop in tab A turns tab B to Stopped within 2 s', async ({ page, context }) => {
    const b = await context.newPage();
    await page.goto('/app/agents');
    await page.getByRole('link', { name: 'research-bot' }).click();
    const url = page.url();
    if ((await page.locator('[data-agent-status]').textContent())?.includes('Stopped')) {
      await page.getByRole('tab', { name: 'Danger zone' }).click();
      await page.getByRole('button', { name: 'Start agent' }).click();
      await page.getByLabel('Why is it safe to start again?').fill('e2e: put it back');
      await page.locator('.sheet').getByRole('button', { name: 'Start agent' }).click();
      await expect(page.locator('[data-agent-status]')).toContainText('Active');
      await page.getByRole('tab', { name: 'Overview' }).click();
    }
    await b.goto(url);
    await expect(b.locator('[data-agent-status]')).toContainText('Active');
    await page.getByRole('tab', { name: 'Danger zone' }).click();
    await page.getByRole('button', { name: 'Stop agent' }).click();
    await page.getByLabel('Type research-bot to confirm').fill('research-bot');
    const t0 = Date.now();
    await page.locator('.sheet').getByRole('button', { name: 'Stop agent' }).click();
    await expect(b.locator('[data-agent-status]')).toContainText('Stopped', { timeout: 2000 });
    console.log(`stop: tab B showed Stopped ${Date.now() - t0} ms after the click`);
    // Put it back for the other tests.
    await page.getByRole('button', { name: 'Start agent' }).click();
    await page.getByLabel('Why is it safe to start again?').fill('e2e: put it back');
    await page.locator('.sheet').getByRole('button', { name: 'Start agent' }).click();
    await expect(page.locator('[data-agent-status]')).toContainText('Active');
  });

  test('5 · the state matrix: loading, filtered empty, error, 404', async ({ page }) => {
    // Loading: skeletons in the final geometry, no spinner.
    await page.route('**/v1/actions?*', async (r) => { await new Promise((res) => setTimeout(res, 1200)); await r.continue(); });
    await page.goto('/app/actions');
    await expect(page.locator('#screen-body[aria-busy="true"] .skel-feed').first()).toBeVisible();
    await page.unroute('**/v1/actions?*');
    // Filtered empty.
    await page.goto('/app/actions?tool=nothing.matches.*');
    await expect(page.getByText('No actions match these filters.')).toBeVisible();
    await page.getByRole('button', { name: 'Clear filters' }).first().click();
    await expect(page).toHaveURL(/\/app\/actions$/);
    // Error: the server's message and the request id, and Retry.
    await page.route('**/v1/agents?*', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { type: 'api_error', code: 'internal_error', message: 'Something went wrong on our side. Retry, and share the request id if it keeps happening.', param: null, request_id: 'req_01k633rrmh3bcz2p39gg4vp46q', doc_url: '' } }) }));
    await page.goto('/app/agents');
    await expect(page.getByText('req_01k633rrmh3bcz2p39gg4vp46q')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    await page.unroute('**/v1/agents?*');
    // 404: the same answer for a missing id and another org's id.
    await page.goto('/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8');
    await expect(page.getByText("This page doesn't exist.")).toBeVisible();
    await page.goto('/app/deeds/deed_01k6h7mz3c9d8e7f6g5h4jq3m8');
    await expect(page.getByText("This page doesn't exist.")).toBeVisible();
  });

  test('6 · 360 px: the feed is cards, no horizontal scroll', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 780 });
    await page.goto('/app/actions');
    const row = page.locator('table.feed tbody tr').first();
    await expect(row).toBeVisible();
    const box = (await row.boundingBox())!;
    expect(box.height).toBeGreaterThanOrEqual(64);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    for (const p of ['/app', `/app/actions/${hero().mismatch}`, '/app/deeds', '/app/agents']) {
      await page.goto(p);
      await page.waitForLoadState('networkidle');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), p).toBe(true);
    }
  });

  test('7 · reduced motion: no animation runs on a live row', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/app/actions');
    await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
    const child = fake(3);
    await expect(page.locator('table.feed tr[data-live]').first()).toBeVisible({ timeout: 3000 });
    await new Promise<void>((r) => child.on('exit', () => r()));
    const long = await page.evaluate(() => document.getAnimations().filter((a) => (a.effect?.getTiming().duration as number) > 120).length);
    expect(long).toBe(0);
  });
});

test.describe('first run (an org with no agents)', () => {
  test('8 · create the demo agent, copy the key, an action seals, ✓', async ({ browser }) => {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    const done = await guard(page);
    await signIn(page, EMAIL.owner, 'acme-staging');
    await expect(page.getByRole('heading', { name: 'Connect your first agent' })).toBeVisible();
    await page.getByRole('button', { name: 'Create demo agent' }).click();
    const secret = await page.locator('[data-secret]').inputValue();
    await page.getByLabel("I've stored this key in a secret manager").check();
    await page.getByRole('button', { name: 'Done' }).click();
    await expect(page.locator('.snippets pre')).toContainText(secret); // the key filled in for step 2
    await expect(page.getByText('Waiting for the first action…')).toBeVisible();
    spawnSync(DG, ['act', 'fake.echo', '--args', '{"hello":"world"}'], { cwd: process.env.WEB_E2E_WORK, env: { ...process.env, DEEDGLASS_API_KEY: secret, DEEDGLASS_SERVER: process.env.WEB_E2E_BASE } });
    await expect(page.getByRole('link', { name: 'First action sealed' })).toBeVisible({ timeout: 5000 });
    await done();
    await ctx.close();
  });
});

test.describe('viewer', () => {
  test.use({ storageState: AUTH('viewer') });
  test('9 · sees everything, changes nothing, and the API agrees', async ({ page }) => {
    await page.goto('/app/agents');
    await expect(page.getByRole('button', { name: 'Create agent' })).toBeDisabled();
    await page.getByRole('link', { name: 'support-bot' }).click();
    await page.getByRole('tab', { name: 'Keys' }).click();
    await expect(page.getByRole('button', { name: 'Create key' })).toBeDisabled();
    for (const b of await page.getByRole('button', { name: 'Roll' }).all()) await expect(b).toBeDisabled();
    await page.getByRole('tab', { name: 'Danger zone' }).click();
    await expect(page.getByRole('button', { name: 'Stop agent' })).toBeDisabled();
    await page.goto(`/app/actions/${hero().mismatch}`);
    await expect(page.getByRole('button', { name: 'Preview undo' })).toBeDisabled();
    const statuses = await page.evaluate(async () => {
      const me = await (await fetch('/v1/me', { headers: { 'Deedglass-Version': '2026-10-01' } })).json();
      const post = (path: string, body: unknown) => fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Deedglass-Version': '2026-10-01', 'Deedglass-CSRF': me.csrf_token, 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(body) }).then((r) => r.status);
      return [await post('/v1/agents', { name: 'viewer-made' }), await post('/v1/org/stop-all', {})];
    });
    expect(statuses).toEqual([403, 403]);
  });
});

test.describe('agent keys never see the Overview', () => {
  test('10 · GET /v1/overview with an agent key is 403 insufficient_scope', async ({ request }) => {
    const admin = JSON.parse(dg('org', 'keys', 'create', '--org', 'acme', '--name', 'e2e-overview-admin', '--mode', 'test', '--json')).secret as string;
    const h = (k: string) => ({ Authorization: `Bearer ${k}`, 'Deedglass-Version': '2026-10-01' });
    expect((await request.get('/v1/overview', { headers: h(admin) })).status()).toBe(200);
    const agents = await (await request.get('/v1/agents', { headers: h(admin) })).json();
    const ops = agents.data.find((a: { name: string }) => a.name === 'ops-bot');
    const k = await (await request.post(`/v1/agents/${ops.id}/keys`, { headers: { ...h(admin), 'Idempotency-Key': `e2e-${Date.now()}` }, data: { name: 'e2e-overview', mode: 'test' } })).json();
    const res = await request.get('/v1/overview', { headers: h(k.secret) });
    expect(res.status()).toBe(403);
    expect((await res.json()).error.code).toBe('insufficient_scope');
  });
});
