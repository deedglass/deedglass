// S25 §3.8: the feed at about 200 events a second for 30 s runs with no long task over 50 ms
// (PerformanceObserver), because DOM writes batch per animation frame and the DOM keeps 500 rows.
import { spawn } from 'node:child_process';
import { AUTH, expect, test } from './fixtures.ts';

test.use({ storageState: AUTH('owner') });

test('200 events/s for 30 s: 0 long tasks over 50 ms', async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto('/app/actions');
  await expect(page.locator('#conn')).toHaveAttribute('data-state', 'live');
  await expect(page.locator('table.feed tbody tr').first()).toBeVisible();
  await page.evaluate(() => {
    const w = window as unknown as { __long: number[]; __events: number };
    w.__long = [];
    w.__events = 0;
    new PerformanceObserver((l) => { for (const e of l.getEntries()) if (e.duration > 50) w.__long.push(e.duration); }).observe({ type: 'longtask', buffered: false });
  });
  // fake-action seals each action through its lifecycle: about 6 events per action.
  const started = Date.now();
  const child = spawn(process.env.WEB_E2E_DG ?? '../bin/deedglass', ['dev', 'fake-action', '--org', 'acme', '--n', '1500', '--pace', '20ms', '--quiet'],
    { cwd: process.env.WEB_E2E_WORK, env: process.env, stdio: 'ignore' });
  await new Promise<void>((r) => child.on('exit', () => r()));
  const secs = (Date.now() - started) / 1000;
  await page.waitForTimeout(1500);
  const long = await page.evaluate(() => (window as unknown as { __long: number[] }).__long);
  const rows = await page.locator('table.feed tbody tr').count();
  console.log(`perf: 1,500 actions (≈ 9,000 events) in ${secs.toFixed(1)} s ≈ ${Math.round(9000 / secs)} events/s · long tasks > 50 ms: ${long.length} ${JSON.stringify(long)} · rows in the DOM ${rows}`);
  expect(rows).toBeLessThanOrEqual(500);
  expect(long).toEqual([]);
});
