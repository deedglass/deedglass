// The S52 screenshot pass (S52 §5.D): Overview's This week card (the demo org's real summary), the
// Clients page without the Agency plan (the real pro_required answer), and with it (routed answers,
// labelled as such in the file name: three clients, then one lost to a removed membership, then one
// org only), at 360 and 1440 dark plus 1440 light. Also asserts behaviour: the free path on every
// caught item, one row per client, nothing locked behind the plan, axe clean.
import { mkdirSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { AUTH, apiAs, seeds, test } from './fixtures.ts';

const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens-s52`;
mkdirSync(DIR, { recursive: true });

async function prep(page: Page, theme: 'dark' | 'light', w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.emulateMedia({ colorScheme: theme });
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
}
async function settle(page: Page, ms = 500) {
  await page.waitForLoadState('networkidle').catch(() => undefined);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}
const shot = (page: Page, name: string, full = false) => page.screenshot({ path: `${DIR}/${name}.png`, fullPage: full });

const row = (slug: string, name: string, over: Record<string, unknown> = {}) => ({
  object: 'client', org: `org_01m50${slug.padEnd(21, '0').slice(0, 21)}`.replace(/[^0-9a-z_]/g, '0'), name, slug, role: 'owner', actions: 0,
  doesnt_match: 0, caught: 0, waiting: 0, stopped: false, chain: 'intact', last_activity: null, ...over });
const three = () => ({ object: 'list', period: { key: '2026-W43', label: '19 to 25 Oct 2026' }, has_more: false, next_cursor: null, total: 3, data: [
  row('acme', 'Acme', { actions: 412, caught: 1, waiting: 2, last_activity: new Date(Date.now() - 600_000).toISOString() }),
  row('globex', 'Globex', { actions: 88, last_activity: new Date(Date.now() - 7_200_000).toISOString() }),
  row('initech', 'Initech', { chain: 'no_records' }),
] });
const routed = (page: Page, body: () => object) => page.route('**/v1/clients*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body()) }));

test.describe.configure({ mode: 'serial' });

test('the weekly summary answers for the demo org', async () => {
  const r = await apiAs<{ object: string; summary: { object: string } }>(seeds().adminKey, 'GET', '/v1/weekly?period=current');
  expect(r.status).toBe(200);
  expect(r.body.summary.object).toBe('weekly_summary');
});

test.describe('owner', () => {
  test.use({ storageState: AUTH('owner') });
  const sizes = [[360, 780, 'dark'], [1440, 900, 'dark'], [1440, 900, 'light']] as const;
  for (const [w, h, theme] of sizes) {
    test(`this week card ${w} ${theme}`, async ({ page }) => {
      await prep(page, theme, w, h);
      // The demo actions are today's: show the week so far.
      await page.route('**/v1/weekly', (r) => r.continue({ url: r.request().url() + '?period=current' }));
      await page.goto('/app');
      const card = page.locator('.wk');
      await expect(card.locator('.wk-numbers')).toBeVisible();
      for (const li of await card.locator('.wk-caught li').all()) await expect(li.locator('[data-free-path]')).toHaveCount(1);
      await settle(page);
      await card.scrollIntoViewIfNeeded();
      await shot(page, `this-week-${w}-${theme}`, w === 360);
    });
    test(`clients without the plan ${w} ${theme}`, async ({ page }) => {
      await prep(page, theme, w, h);
      await page.goto('/app/clients');
      await expect(page.locator('.cl-pro')).toBeVisible();
      await expect(page.locator('.cl-pro')).toContainText('deedglass report --org <client>');
      await settle(page);
      await shot(page, `clients-pro-required-${w}-${theme}`);
    });
    test(`clients, three (routed) ${w} ${theme}`, async ({ page }) => {
      await prep(page, theme, w, h);
      await routed(page, three);
      await page.goto('/app/clients');
      await expect(page.locator('.cl-list tbody tr')).toHaveCount(3);
      await settle(page);
      await shot(page, `clients-three-routed-${w}-${theme}`, w === 360);
      if (w === 1440 && theme === 'dark') {
        const axe = await new AxeBuilder({ page }).analyze();
        expect(axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => v.id)).toEqual([]);
      }
    });
  }
  test('clients: lost access, one org (routed)', async ({ page }) => {
    await prep(page, 'dark', 1440, 900);
    await routed(page, () => { const l = three(); l.data = l.data.filter((d) => d.slug !== 'globex'); l.total = 2; return l; });
    await page.goto('/app/clients');
    await expect(page.locator('.cl-list tbody tr')).toHaveCount(2);
    await settle(page);
    await shot(page, 'clients-lost-access-routed-1440-dark');
    await page.unroute('**/v1/clients*');
    await routed(page, () => { const l = three(); l.data = l.data.slice(0, 1); l.total = 1; return l; });
    await page.goto('/app/clients');
    await expect(page.getByText('You look after one org.')).toBeVisible();
    await settle(page);
    await shot(page, 'clients-one-org-routed-1440-dark');
  });
});
