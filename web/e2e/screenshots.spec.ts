// The screenshot pass (00_DESIGN_SKILLS §2.5, S24 §6): /app/_kit, /app and /login at 360, 768 and 1440
// in dark and light, a reduced-motion pass, a keyboard-focus shot, the four states, the palette, the
// Stop all sheet, and the Sealed moment as frames at 0, 120, 240 and 480 ms (animations paused with
// currentTime). Written to SHOTS_DIR (default ~/txv/ops/plan/prompts-v7/reports/screens/S24).
import { spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { AUTH, test } from './fixtures.ts';

// scripts/web_e2e.sh sets SHOTS_DIR ($HOME/txv/ops/plan/prompts-v7/reports/screens/S24).
const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens`;
mkdirSync(DIR, { recursive: true });
const SIZES = [[360, 780], [768, 1024], [1440, 900]] as const;
const THEMES = ['dark', 'light'] as const;

async function prep(page: Page, theme: string, w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
}
async function settle(page: Page) {
  await page.waitForLoadState('networkidle');
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(250);
}

test.use({ storageState: AUTH('owner') });
test.describe.configure({ mode: 'serial' });

test('seed some activity', async () => {
  spawnSync(process.env.WEB_E2E_DG ?? '../bin/deedglass', ['dev', 'fake-action', '--org', 'acme', '--n', '24', '--quiet'], { cwd: process.env.WEB_E2E_WORK, env: process.env });
});

for (const theme of THEMES) {
  for (const [w, h] of SIZES) {
    test(`kit, overview, actions and login · ${w} · ${theme}`, async ({ page, browser }) => {
      await prep(page, theme, w, h);
      await page.goto('/app/_kit');
      await settle(page);
      await page.screenshot({ path: `${DIR}/kit-${w}-${theme}.png` });
      await page.screenshot({ path: `${DIR}/kit-full-${w}-${theme}.png`, fullPage: true });
      await page.goto('/app');
      await settle(page);
      await page.screenshot({ path: `${DIR}/overview-${w}-${theme}.png` });
      await page.goto('/app/actions');
      await settle(page);
      await page.screenshot({ path: `${DIR}/actions-${w}-${theme}.png` });
      const ctx = await browser.newContext({ colorScheme: theme, viewport: { width: w, height: h } });
      const anon = await ctx.newPage();
      await anon.goto('/login');
      await settle(anon);
      await anon.screenshot({ path: `${DIR}/login-${w}-${theme}.png` });
      await ctx.close();
    });
  }
}

test('390 × 844 dark: kit and overview', async ({ page }) => {
  await prep(page, 'dark', 390, 844);
  await page.goto('/app/_kit');
  await settle(page);
  await page.screenshot({ path: `${DIR}/kit-390-dark.png` });
  await page.goto('/app');
  await settle(page);
  await page.screenshot({ path: `${DIR}/overview-390-dark.png` });
});

test('reduced motion and keyboard focus', async ({ page }) => {
  await prep(page, 'dark', 1440, 900);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/app');
  await settle(page);
  await page.screenshot({ path: `${DIR}/overview-1440-dark-reduced-motion.png` });
  for (let i = 0; i < 4; i++) await page.keyboard.press('Tab');
  await page.screenshot({ path: `${DIR}/focus-nav-1440-dark.png` });
  await page.goto('/app/_kit');
  await settle(page);
  await page.locator('#k-buttons').scrollIntoViewIfNeeded();
  await page.getByRole('button', { name: 'Copy deed', exact: true }).focus();
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Tab');
  await page.screenshot({ path: `${DIR}/focus-button-1440-dark.png` });
});

test('the four states: loading, empty, error, no permission', async ({ page }) => {
  await prep(page, 'dark', 1440, 900);
  await page.route('**/v1/agents?*', () => new Promise(() => {}));
  await page.goto('/app/agents');
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${DIR}/state-loading-1440-dark.png` });
  await page.unroute('**/v1/agents?*');
  await page.goto('/app/approvals');
  await settle(page);
  await page.screenshot({ path: `${DIR}/state-empty-1440-dark.png` });
  await page.route('**/v1/policies?*', (r) => r.fulfill({ status: 500, contentType: 'application/json',
    body: JSON.stringify({ error: { type: 'api_error', code: 'internal_error', message: 'Something went wrong on our side. Try again in a moment.', param: null, request_id: 'req_01m3y6a7b8c9d0e1f2g3h4j5k6', doc_url: 'https://docs.deedglass.com/errors/internal_error' } }) }));
  await page.goto('/app/policies');
  await settle(page);
  await page.screenshot({ path: `${DIR}/state-error-1440-dark.png` });
  await page.route('**/v1/connectors?*', (r) => r.fulfill({ status: 403, contentType: 'application/json',
    body: JSON.stringify({ error: { type: 'permission_error', code: 'insufficient_role', message: 'Reading connectors needs the Admin role.', param: null, request_id: 'req_01m3y6a7b8c9d0e1f2g3h4j5k6', doc_url: '' } }) }));
  await page.goto('/app/connectors');
  await settle(page);
  await page.screenshot({ path: `${DIR}/state-permission-1440-dark.png` });
});

test('the palette and the Stop all sheet', async ({ page }) => {
  for (const [w, h] of [[1440, 900], [360, 780]] as const) {
    await prep(page, 'dark', w, h);
    await page.goto('/app');
    await settle(page);
    await page.keyboard.press('ControlOrMeta+k');
    await page.getByRole('combobox').fill('a');
    await page.waitForTimeout(250);
    await page.screenshot({ path: `${DIR}/palette-${w}-dark.png` });
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Stop all agents' }).click();
    await page.getByLabel('Type acme to confirm').fill('acme');
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${DIR}/stop-all-${w}-dark.png` });
    await page.keyboard.press('Escape');
  }
});

test('Sealed and Mismatch frames at 0, 120, 240 and 480 ms', async ({ browser }) => {
  // 4× device pixels, so the 48 px mark's ring and hairline are visible in the frames.
  const ctx = await browser.newContext({ storageState: AUTH('owner'), deviceScaleFactor: 4, viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  const page = await ctx.newPage();
  await page.goto('/app/_kit');
  await settle(page);
  const player = page.locator('#kit-player figure').last();
  await player.scrollIntoViewIfNeeded();
  for (const kind of ['Sealed', 'Mismatch'] as const) {
    await page.getByRole('button', { name: 'Reset' }).click();
    await page.waitForTimeout(100);
    await page.getByRole('button', { name: `Play ${kind}` }).click();
    // Only the Mirror's animations (the kit's skeleton pulses forever while it waits for nothing).
    await page.waitForFunction(() => document.getAnimations().some((a) => ((a.effect as KeyframeEffect | null)?.target as Element | null)?.closest('.dg-mirror')));
    await page.evaluate(() => document.getAnimations().filter((a) => ((a.effect as KeyframeEffect | null)?.target as Element | null)?.closest('.dg-mirror')).forEach((a) => a.pause()));
    for (const t of [0, 120, 240, 480]) {
      await page.evaluate((ms) => document.getAnimations().filter((a) => ((a.effect as KeyframeEffect | null)?.target as Element | null)?.closest('.dg-mirror')).forEach((a) => { a.currentTime = ms; }), t);
      await page.waitForTimeout(60);
      await player.screenshot({ path: `${DIR}/${kind.toLowerCase()}-frame-${String(t).padStart(3, '0')}ms.png` });
    }
    await page.evaluate(() => document.getAnimations().filter((a) => ((a.effect as KeyframeEffect | null)?.target as Element | null)?.closest('.dg-mirror')).forEach((a) => a.finish()));
  }
  await ctx.close();
});
