// console-v1 Playwright: the live view against a real serve, a real Postgres and the seeded L2 hero.
//  1. Live activity: fake actions arrive live with a summary, the decision, the read-back result
//     and a link to the sealed record; the row opens the action
//  2. Needs you at 390 px: a four-eyes approval arrives live, big buttons, 0 → 1 of 2 signed, the
//     second admin approves and the card leaves
//  3. Needs you: Decline asks for a reason and the approval is denied
//  4. the action page: claimed beside observed, the diff rows, the policy that decided, the chain
//     position and the offline command (copied, then run by the CLI)
//  5. the empty state: one next step, then the live view when the first action arrives
//  6. Health mode: field names, never values
//  7. axe on the live view and the empty state
import { spawn, spawnSync } from 'node:child_process';
import AxeBuilder from '@axe-core/playwright';
import { AUTH, apiAs, dg, EMAIL, expect, guard, hero, pendingRefund, seeds, signIn, test } from './fixtures.ts';

const DG = process.env.WEB_E2E_DG ?? '../bin/deedglass';
const fake = (n: number, org = 'acme') => spawn(DG, ['dev', 'fake-action', '--org', org, '--n', String(n), '--pace', '150ms', '--quiet'], { cwd: process.env.WEB_E2E_WORK, env: process.env, stdio: 'ignore' });
const axe = async (page: import('@playwright/test').Page) => {
  const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  return r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`);
};

test.describe.configure({ mode: 'serial' });

/** clearPending declines every pending approval as the signed-in person, so the inbox starts empty. */
async function clearPending(page: import('@playwright/test').Page): Promise<void> {
  await page.goto('/app');
  await page.evaluate(async () => {
    const H = { 'Deedglass-Version': '2026-10-01' };
    const me = await (await fetch('/v1/me', { headers: H })).json();
    const list = await (await fetch('/v1/approvals?status=pending&limit=100', { headers: H })).json();
    for (const a of list.data) {
      await fetch(`/v1/approvals/${a.id}/deny`, {
        method: 'POST',
        headers: { ...H, 'Content-Type': 'application/json', 'Deedglass-CSRF': me.csrf_token, 'Idempotency-Key': crypto.randomUUID() },
        body: JSON.stringify({ reason: 'e2e: start the inbox empty', context_digest: a.context_digest ?? '' }),
      });
    }
  });
}

test.describe('owner', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · Live activity: summary, decision, read-back and the sealed record, live', async ({ page }) => {
    const done = await guard(page);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto('/app');
    await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
    await expect(page.locator('#live-feed li').first()).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Needs you' })).toBeVisible();
    for (const t of ['Allowed', 'Held for a person', 'Blocked', "Doesn't match"]) await expect(page.locator('.today-tiles .tile-label', { hasText: t })).toBeVisible();
    const child = fake(6);
    const live = page.locator('#live-feed li[data-live]');
    await expect(live.first()).toBeVisible({ timeout: 3000 });
    await new Promise<void>((r) => child.on('exit', () => r()));
    const row = live.first();
    await expect(row.locator('[data-summary]')).toHaveText(/ · /);
    await expect(row.locator('[data-decision] .chip')).toBeVisible();
    // Within a few seconds every live row is sealed: a record link, and a read-back chip on the ones that ran.
    await expect(page.locator('#live-feed li[data-live] a[data-record]').first()).toBeVisible({ timeout: 8000 });
    await expect(page.locator('#live-feed li[data-live] [data-readback] .chip').first()).toBeVisible({ timeout: 8000 });
    const href = await page.locator('#live-feed li[data-live] a[data-record]').first().getAttribute('href');
    expect(href).toMatch(/^\/app\/deeds\/deed_/);
    await row.locator('.feed-tool').click();
    await expect(page).toHaveURL(/\/app\/actions\/act_/);
    await done();
  });

  test('2 · Needs you at 390 px: four-eyes, 0 → 1 of 2, then the second admin', async ({ page, browser }) => {
    const done = await guard(page);
    await page.setViewportSize({ width: 390, height: 844 });
    await clearPending(page);
    await page.goto('/app');
    await expect(page.locator('.inbox-empty')).toBeVisible();
    await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
    const ap = await pendingRefund({ fourEyes: true });
    const card = page.locator(`.inbox-card[data-approval="${ap.approval}"]`);
    await expect(card).toBeVisible({ timeout: 5000 }); // arrived live, no reload
    await expect(card.locator('[data-four-eyes]')).toContainText('0 of 2 signed');
    const approve = card.getByRole('button', { name: 'Approve' });
    const decline = card.getByRole('button', { name: 'Decline' });
    const [ab, db] = [(await approve.boundingBox())!, (await decline.boundingBox())!];
    expect(ab.height).toBeGreaterThanOrEqual(44);
    expect(Math.abs(ab.y - db.y)).toBeLessThan(2); // side by side
    expect(ab.x + ab.width).toBeLessThanOrEqual(390);
    await approve.click();
    const sheet = page.getByRole('dialog', { name: 'Approve postgres.execute?' });
    await expect(sheet).toBeVisible();
    await sheet.getByRole('button', { name: 'Approve' }).click();
    await expect(card.locator('[data-four-eyes]')).toContainText('1 of 2 signed', { timeout: 5000 });
    await expect(card.locator('[data-you-signed]')).toBeVisible();
    await expect(card.getByRole('button')).toHaveCount(0);
    const b = await browser.newContext({ storageState: AUTH('admin'), viewport: { width: 390, height: 844 } });
    const pb = await b.newPage();
    await pb.goto('/app');
    const card2 = pb.locator(`.inbox-card[data-approval="${ap.approval}"]`);
    await card2.getByRole('button', { name: 'Approve' }).click();
    await pb.getByRole('dialog', { name: /Approve/ }).getByRole('button', { name: 'Approve' }).click();
    await expect(card2).toHaveCount(0, { timeout: 5000 });
    await expect(card).toHaveCount(0, { timeout: 5000 }); // the first tab hears it live
    await b.close();
    const a = await apiAs<{ approval: { status: string } }>(seeds().adminKey, 'GET', `/v1/actions/${ap.action}`);
    expect(a.body.approval.status).toBe('approved');
    await done();
  });

  test('3 · Needs you: Decline needs a reason, then the approval is denied', async ({ page }) => {
    const done = await guard(page);
    await clearPending(page);
    const ap = await pendingRefund({ cents: 900 });
    await page.goto('/app');
    const card = page.locator(`.inbox-card[data-approval="${ap.approval}"]`);
    await card.getByRole('button', { name: 'Decline' }).click();
    const sheet = page.getByRole('dialog', { name: /Deny/ });
    const go = sheet.getByRole('button', { name: 'Deny' });
    await expect(go).toBeDisabled();
    await sheet.getByRole('textbox').fill('Not for this order');
    await go.click();
    await expect(card).toHaveCount(0, { timeout: 5000 });
    const a = await apiAs<{ approval: { status: string } }>(seeds().adminKey, 'GET', `/v1/actions/${ap.action}`);
    expect(a.body.approval.status).toBe('denied');
    await done();
  });

  test('4 · the action page: claimed beside observed, the diff, the policy, the chain, verify offline', async ({ page, context }) => {
    const done = await guard(page);
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/app/actions/${hero().mismatch}`);
    const co = page.locator('section[aria-labelledby="d-co"]');
    await expect(co.locator('.co-claimed')).toContainText('The agent claimed');
    await expect(co.locator('.co-claimed .co-text')).toContainText('UPDATE 1 row in public.orders');
    await expect(co.locator('.co-observed .co-text')).toContainText('UPDATE 301 rows in public.orders');
    await expect(co.locator('.co-observed .chip-mismatch')).toBeVisible();
    const [cb, ob] = [(await co.locator('.co-claimed').boundingBox())!, (await co.locator('.co-observed').boundingBox())!];
    expect(Math.abs(cb.y - ob.y)).toBeLessThan(2); // side by side at 1440
    await expect(co.locator('table.diff tbody tr')).toHaveCount(20);
    await expect(page.locator('[data-diff-showing]')).toHaveText(/Showing\s20\sof\s301\srows/);
    const pol = page.locator('section[aria-labelledby="d-decision"]');
    await expect(pol.getByRole('heading', { name: 'The policy that decided' })).toBeVisible();
    await expect(pol).toContainText('allow-order-status-updates');
    const chain = page.locator('section[aria-labelledby="d-chain"]');
    await expect(chain.locator('.chain-pos')).toContainText(/Record \d+/);
    const cmd = (await chain.locator('[data-verify-cmd]').textContent())!;
    expect(cmd).toMatch(/^deedglass verify deed_[0-9a-z]+\.json --keys http:\/\/127\.0\.0\.1:\d+\/\.well-known\/deedglass\/keys\.json$/);
    await chain.getByRole('button', { name: /^Copy/ }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(cmd);
    // The command works: download the record, then run exactly what was copied.
    const [dl] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download deed' }).click()]);
    const file = (await dl.path())!;
    const args = cmd.split(' ').slice(1);
    args[1] = file;
    const out = spawnSync(DG, [...args, '--no-color'], { encoding: 'utf8', env: process.env });
    expect(out.status, out.stdout + out.stderr).toBe(0);
    // On a phone the comparison comes before the record card.
    await page.setViewportSize({ width: 390, height: 844 });
    const [coy, deedy] = [(await co.boundingBox())!.y, (await page.locator('.deed-panel').boundingBox())!.y];
    expect(coy).toBeLessThan(deedy);
    await done();
  });

  test('6 · Health mode shows field names, never values', async ({ page }) => {
    const done = await guard(page);
    await page.route('**/v1/org', async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      body.settings = { ...(body.settings ?? {}), phi_mode: true };
      await route.fulfill({ response: res, json: body });
    });
    await page.goto(`/app/actions/${hero().mismatch}`);
    await expect(page.locator('.banner-phi')).toContainText('Health mode is on');
    const cells = page.locator('section[aria-labelledby="d-co"] table.diff tbody td.diff-obs');
    await expect(cells.first()).toContainText('•••');
    await expect(page.locator('section[aria-labelledby="d-co"] table.diff')).not.toContainText('shipped');
    await expect(page.locator('section[aria-labelledby="d-co"] table.diff')).toContainText('status'); // the field name stays
    await expect(page.locator('[data-phi]')).toContainText('request and the result stay hidden');
    await page.goto('/app');
    await expect(page.locator('.banner-phi')).toBeVisible();
    await done();
  });

  test('7a · axe: the live view in both themes', async ({ page }) => {
    for (const scheme of ['dark', 'light'] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, scheme);
      await page.goto('/app');
      await expect(page.locator('#live-feed li').first()).toBeVisible();
      await page.waitForLoadState('networkidle');
      expect(await axe(page)).toEqual([]);
    }
  });
});

test.describe('an org with no actions', () => {
  test('5 · one next step, then the live view when the first action arrives (and 7b · axe)', async ({ browser }) => {
    const slug = `empty-${Date.now().toString(36)}`;
    dg('org', 'create', '--name', 'Empty Co', '--slug', slug, '--owner-email', EMAIL.owner, '--no-color');
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const page = await ctx.newPage();
    const done = await guard(page);
    await signIn(page, EMAIL.owner, slug);
    const hero = page.locator('[data-empty]');
    await expect(hero.getByRole('heading', { name: 'Waiting for the first action' })).toBeVisible();
    await expect(hero.locator('code')).toHaveCount(1);
    await expect(hero.locator('code')).toContainText('deedglass init --smoke');
    await expect(hero.getByRole('button', { name: /^Copy/ })).toBeVisible();
    expect(await axe(page)).toEqual([]);
    await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
    const child = fake(2, slug);
    await expect(page.locator('#live-feed li').first()).toBeVisible({ timeout: 8000 });
    await new Promise<void>((r) => child.on('exit', () => r()));
    await expect(page.locator('[data-empty]')).toHaveCount(0);
    await expect(page.locator('.today-tiles')).toBeVisible();
    await done();
    await ctx.close();
  });
});
