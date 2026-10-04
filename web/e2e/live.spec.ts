// Live events (S24 §6, S25 §3.3): fake actions committed in Postgres reach the Overview's Live
// actions card within 2 s, through GET /v1/events, and the connection line reads Live.
import { spawn } from 'node:child_process';
import { AUTH, expect, guard, test } from './fixtures.ts';

test.use({ storageState: AUTH('owner') });

test('the Live actions card receives fake actions within 2 s', async ({ page }) => {
  const done = await guard(page);
  await page.goto('/app');
  await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
  await expect(page.locator('#conn-label')).toHaveText('Live');
  const started = Date.now();
  const child = spawn(process.env.WEB_E2E_DG ?? '../bin/deedglass', ['dev', 'fake-action', '--org', 'acme', '--n', '6', '--pace', '150ms', '--quiet'],
    { cwd: process.env.WEB_E2E_WORK, env: process.env, stdio: 'ignore' });
  await expect(page.locator('#ov-live li[data-live]').first()).toBeVisible({ timeout: 2000 });
  const firstRow = Date.now() - started;
  await new Promise<void>((r) => child.on('exit', () => r()));
  await expect(page.locator('#ov-live li')).toHaveCount(10, { timeout: 5000 });
  await expect(page.locator('#ov-live .chip').first()).toBeVisible();
  console.log(`live: first event in the browser ${firstRow} ms after fake-action started`);
  await done();
});

test('a reconnect resumes and says so honestly', async ({ page }) => {
  await page.goto('/app');
  await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
  await page.context().setOffline(true);
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await expect(page.locator('#conn')).toHaveAttribute('data-state', 'offline');
  await expect(page.getByText('Offline · Deedglass will reconnect when your network is back.')).toBeVisible();
  await page.context().setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live', { timeout: 10_000 });
});
