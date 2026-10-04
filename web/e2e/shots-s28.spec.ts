// The S28 screenshot pass (00_DESIGN_SKILLS §2.5, S28 §5.D): the Insights screen on the seeded
// simulated org at 360, 768 and 1440 in dark and light; the diff open; the accept sheet with the
// replace-shadow box; the reject sheet with its reason error; the settings sheet with Claude phrasing
// off; the viewer; the accept → shadow → promote flow; and the loading, empty, error and
// simulated-banner states. Those four come from routed answers (named in the report as such): the
// seeded org has data and no failure. Written to SHOTS_DIR (scripts/web_e2e.sh shots28).
import { mkdirSync, readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { appendQuietDays, EMAIL, expect, signIn, test } from './fixtures.ts';

const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens`;
mkdirSync(DIR, { recursive: true });
const SIZES = [[360, 780], [768, 1024], [1440, 900]] as const;
const THEMES = ['dark', 'light'] as const;
const slug = () => (JSON.parse(readFileSync(`${process.env.WEB_E2E_WORK}/sim.json`, 'utf8')) as { slug: string }).slug;

test.describe.configure({ mode: 'serial' });
test.use({ storageState: { cookies: [], origins: [] } });

async function prep(page: Page, theme: string, w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.emulateMedia({ colorScheme: theme as 'dark' | 'light' });
  await page.evaluate((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme).catch(() => undefined);
}
async function settle(page: Page, ms = 400) {
  await page.waitForLoadState('networkidle').catch(() => undefined);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}
const shot = (page: Page, name: string, full = false) => page.screenshot({ path: `${DIR}/${name}.png`, fullPage: full });
async function go(page: Page, theme: string, w: number, h: number, wait = true) {
  await prep(page, theme, w, h);
  await page.goto('/app/insights');
  if (!wait) return;
  await expect(page.locator('#body-trends')).toHaveAttribute('aria-busy', 'false');
  await settle(page);
}
const loosen = (page: Page) => page.locator('.ins-card[data-kind="loosen"]');

test('admin views, sheets and states', async ({ page }) => {
  test.setTimeout(600_000);
  await signIn(page, EMAIL.admin, slug());
  for (const theme of THEMES) {
    for (const [w, h] of SIZES) {
      await go(page, theme, w, h);
      await shot(page, `insights-${w}-${theme}`);
      if (w === 1440) await shot(page, `insights-full-${w}-${theme}`, true);
      if (w === 768) continue;
      // the diff open
      await loosen(page).locator('[data-diff-toggle]').click();
      await loosen(page).locator('.ins-diff').scrollIntoViewIfNeeded();
      await page.evaluate(() => window.scrollBy(0, -160));
      await settle(page, 200);
      await shot(page, `diff-open-${w}-${theme}`);
      // the accept sheet (v2 runs in shadow: the replace box)
      await loosen(page).getByRole('button', { name: 'Accept and run in shadow' }).click();
      await expect(page.getByRole('dialog', { name: 'Accept this suggestion?' })).toBeVisible();
      await settle(page, 350);
      await shot(page, `accept-sheet-${w}-${theme}`);
      await page.keyboard.press('Escape');
      // the reject sheet with its reason error
      await page.locator('.ins-card[data-kind="budget"]').getByRole('button', { name: 'Reject' }).click();
      const rej = page.getByRole('dialog', { name: 'Reject this suggestion?' });
      await rej.getByRole('button', { name: 'Reject suggestion' }).click();
      await expect(rej.locator('#rej-reason-e')).toBeVisible();
      await settle(page, 300);
      await shot(page, `reject-error-${w}-${theme}`);
      await page.keyboard.press('Escape');
      await settle(page, 250);
      if (w === 1440) {
        // the settings sheet: Claude phrasing off (no server key)
        await page.locator('[data-settings]').click();
        await expect(page.getByRole('dialog', { name: 'Insights settings' })).toBeVisible();
        await settle(page, 350);
        await shot(page, `settings-phrasing-off-${w}-${theme}`);
        await page.keyboard.press('Escape');
        await settle(page, 250);
      }
    }
  }
  // reduced motion and the keyboard focus ring
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await go(page, 'dark', 1440, 900);
  await loosen(page).locator('[data-diff-toggle]').click();
  await settle(page, 100);
  await shot(page, 'insights-1440-dark-reduced-motion');
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await go(page, 'dark', 1440, 900);
  await page.locator('.page-head .lede').click();
  await page.keyboard.press('j');
  await page.keyboard.press('j');
  await page.locator('.ins-card').nth(1).scrollIntoViewIfNeeded();
  await settle(page, 300);
  await shot(page, 'focus-1440-dark');

  // routed states, labelled as such in the report
  const empty = { object: 'list', data: [], has_more: false, next_cursor: null };
  for (const theme of THEMES) {
    for (const [w, h] of [[1440, 900], [360, 780]] as const) {
      if (w === 360 && theme === 'light') continue;
      await page.route('**/v1/suggestions?**', (r) => r.fulfill({ json: empty }));
      await page.route('**/v1/insights/shadow', (r) => r.fulfill({ status: 404, json: { error: { type: 'invalid_request_error', code: 'not_found', message: 'No policy is running in shadow.', param: null, request_id: 'req_shots', doc_url: '' } } }));
      await page.route('**/v1/insights/anomalies?**', (r) => r.fulfill({ json: { ...empty, active_now: 0, anomaly_facts: false, notice: null, baseline_building: [] } }));
      await go(page, theme, w, h);
      await shot(page, `empty-${w}-${theme}`, w === 1440);
      await page.unrouteAll({ behavior: 'ignoreErrors' });
      await page.route('**/v1/suggestions?**', (r) => r.fulfill({ status: 500, json: { error: { type: 'api_error', code: 'internal_error', message: 'The suggestions could not be read just now.', param: null, request_id: 'req_01m4shotsrouted0error0000', doc_url: 'https://docs.deedglass.com/errors/internal_error' } } }));
      await go(page, theme, w, h);
      await shot(page, `error-${w}-${theme}`);
      await page.unrouteAll({ behavior: 'ignoreErrors' });
      if (w === 1440) {
        await page.route('**/v1/**', async (r) => { if (/\/v1\/(suggestions|insights)/.test(r.request().url())) await new Promise((x) => setTimeout(x, 4000)); await r.fallback(); });
        await go(page, theme, w, h, false);
        await page.waitForTimeout(600);
        await shot(page, `loading-${w}-${theme}`);
        await page.unrouteAll({ behavior: 'wait' });
        await page.route('**/v1/org', async (r) => { const res = await r.fetch(); const j = await res.json(); await r.fulfill({ response: res, json: { ...j, settings: { ...(j.settings ?? {}), simulated: true } } }); });
        await go(page, theme, w, h);
        await shot(page, `simulated-banner-${w}-${theme}`);
        await page.unrouteAll({ behavior: 'ignoreErrors' });
      }
    }
  }
});

test('viewer', async ({ page }) => {
  await signIn(page, EMAIL.viewer, slug());
  for (const theme of THEMES) {
    for (const [w, h] of [[1440, 900], [360, 780]] as const) {
      await go(page, theme, w, h);
      await page.locator('.ins-card').first().locator('.ins-actions').scrollIntoViewIfNeeded();
      await settle(page, 200);
      await shot(page, `viewer-${w}-${theme}`);
    }
  }
});

test('the flow: accept → shadow → promote → enforced', async ({ page }) => {
  test.setTimeout(300_000);
  await signIn(page, EMAIL.admin, slug());
  await go(page, 'dark', 1440, 900);
  await loosen(page).getByRole('button', { name: 'Accept and run in shadow' }).click();
  const sheet = page.getByRole('dialog', { name: 'Accept this suggestion?' });
  await sheet.getByRole('checkbox', { name: 'Replace v2 in shadow' }).check();
  await sheet.getByLabel(/Reason/).fill('Ops approves these every time');
  await settle(page, 200);
  await shot(page, 'accept-sheet-filled-1440-dark');
  await sheet.getByRole('button', { name: 'Accept and run in shadow' }).click();
  await expect(page.locator('.toast').last()).toContainText('acme-sim v3 is running in shadow');
  await page.locator('.toast').last().hover();
  await page.locator('#sec-shadow').scrollIntoViewIfNeeded();
  await settle(page, 300);
  await shot(page, 'accepted-1440-dark');
  for (const [w, h, theme] of [[1440, 900, 'light'], [360, 780, 'dark']] as const) {
    await go(page, theme, w, h);
    await page.locator('#sec-shadow').scrollIntoViewIfNeeded();
    await settle(page, 200);
    await shot(page, `accepted-${w}-${theme}`);
  }
  appendQuietDays(slug());
  for (const [w, h, theme] of [[1440, 900, 'dark'], [360, 780, 'dark'], [1440, 900, 'light']] as const) {
    await go(page, theme, w, h);
    await page.locator('#sec-shadow').scrollIntoViewIfNeeded();
    await settle(page, 200);
    await shot(page, `shadow-v3-${w}-${theme}`);
    await page.locator('[data-promote]').click();
    const pr = page.getByRole('alertdialog', { name: 'Enforce acme-sim v3?' });
    await pr.getByLabel('Type acme-sim to confirm').fill('acme-sim');
    await pr.getByLabel(/Reason/).fill('Promoted after two days in shadow');
    await settle(page, 300);
    await shot(page, `promote-sheet-${w}-${theme}`);
    if (theme === 'light') {
      const ack = pr.getByRole('checkbox');
      if (await ack.count()) await ack.check();
      await pr.getByRole('button', { name: 'Enforce v3' }).click();
      await expect(page.locator('.toast').last()).toContainText('acme-sim v3 enforced · deed sealed');
      await page.locator('.toast').last().hover();
      await settle(page, 300);
      await shot(page, `enforced-${w}-${theme}`);
    } else {
      await page.keyboard.press('Escape');
    }
  }
  for (const theme of THEMES) {
    await prep(page, theme, 1440, 900);
    await page.goto('/app/policies');
    await settle(page);
    await shot(page, `enforced-policies-1440-${theme}`);
  }
});
