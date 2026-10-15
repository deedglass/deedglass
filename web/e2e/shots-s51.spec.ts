// The S51 screenshot pass (S51 §5.D): Settings › Security review at 360 and 1440 dark plus 1440
// light, for the demo org (its real readiness), a brand-new org and an org where every question is
// in place (those two are routed answers, labelled as such in the file name), the viewer's disabled
// build, the Pro sheet, and the sample pack's index.html at 1440 and as its print preview's first
// page. Written to SHOTS_DIR. Also asserts the screen's behaviour: gaps first, words on every chip,
// the Pro sheet on pro_required, and the HTML document making no request.
import { mkdirSync, readFileSync } from 'node:fs';
import type { Page, Route } from '@playwright/test';
import { expect } from '@playwright/test';
import { AUTH, apiAs, seeds, test } from './fixtures.ts';

const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens-s51`;
const SAMPLE = process.env.S51_SAMPLE ?? '';
mkdirSync(DIR, { recursive: true });

async function prep(page: Page, theme: 'dark' | 'light', w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.emulateMedia({ colorScheme: theme });
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
}
async function settle(page: Page, ms = 400) {
  await page.waitForLoadState('networkidle').catch(() => undefined);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}
const shot = (page: Page, name: string, full = false) => page.screenshot({ path: `${DIR}/${name}.png`, fullPage: full });

type Row = { id: string; status: string; fix: string };
type RD = { rows: Row[]; summary: Record<string, number> };

test.describe.configure({ mode: 'serial' });

let real: RD;
test('the demo org\'s real readiness', async () => {
  const r = await apiAs<RD>(seeds().adminKey, 'GET', '/v1/security_pack/readiness');
  expect(r.status).toBe(200);
  expect(r.body.rows).toHaveLength(24);
  real = r.body;
});

const allInPlace = (): RD => ({ ...real, summary: { in_place: 24, partly: 0, not_set_up: 0, not_applicable: 0, cant_tell: 0 },
  rows: real.rows.map((r) => ({ ...r, status: 'in_place', fix: '' })) });
// A brand-new org's real answer (S51_FRESH: GET /v1/security_pack/readiness with that org's admin
// key), shown in the Acme owner's Console through a routed response.
const fresh = (): RD => JSON.parse(readFileSync(process.env.S51_FRESH ?? '', 'utf8')) as RD;
const routeWith = (page: Page, body: () => RD) => page.route('**/v1/security_pack/readiness*', (route: Route) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body()) }));

test.describe('owner', () => {
  test.use({ storageState: AUTH('owner') });
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`security review ${w} dark`, async ({ page }) => {
      await prep(page, 'dark', w, h);
      await page.goto('/app/settings/security');
      await expect(page.locator('.sp-row').first()).toBeVisible();
      await settle(page);
      // Gaps first, and every chip carries its word.
      const first = await page.locator('section[aria-labelledby="sp-todo-h"] .sp-row').first().getAttribute('data-status');
      expect(['not_set_up', 'partly', 'cant_tell']).toContain(first);
      for (const t of await page.locator('.sp-row .chip').allInnerTexts()) expect(t.trim().length).toBeGreaterThan(0);
      await shot(page, `security-partial-${w}-dark`, true);
      test.skip(!process.env.S51_FRESH, 'S51_FRESH names a new org\'s readiness');
      await routeWith(page, fresh);
      await page.reload();
      await expect(page.locator('.sp-row').first()).toBeVisible();
      await settle(page);
      await shot(page, `security-empty-org-routed-${w}-dark`, true);
      await page.unroute('**/v1/security_pack/readiness*');
      await routeWith(page, allInPlace);
      await page.reload();
      await expect(page.locator('.sp-none')).toBeVisible();
      await settle(page);
      await shot(page, `security-all-in-place-routed-${w}-dark`, true);
    });
  }
  test('security review 1440 light', async ({ page }) => {
    await prep(page, 'light', 1440, 900);
    await page.goto('/app/settings/security');
    await expect(page.locator('.sp-row').first()).toBeVisible();
    await settle(page);
    await shot(page, 'security-partial-1440-light', true);
  });
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`pro sheet ${w}`, async ({ page }) => {
      await prep(page, 'dark', w, h);
      await page.goto('/app/settings/security');
      await page.getByRole('button', { name: /Build the pack/ }).click();
      await expect(page.getByRole('dialog')).toContainText('The readiness list stays free.');
      await settle(page, 600);
      await shot(page, `pro-sheet-${w}-dark`);
    });
  }
});

test.describe('viewer', () => {
  test.use({ storageState: AUTH('viewer') });
  test('viewer sees the list and a disabled build', async ({ page }) => {
    await prep(page, 'dark', 1440, 900);
    await page.goto('/app/settings/security');
    await expect(page.locator('.sp-row').first()).toBeVisible();
    await expect(page.getByRole('button', { name: /Build the pack/ })).toBeDisabled();
    await expect(page.locator('#sp-build-why')).toContainText('Admin role');
    await settle(page);
    await shot(page, 'security-viewer-1440-dark');
  });
});

test('the sample pack document', async ({ page }) => {
  test.skip(!SAMPLE, 'S51_SAMPLE names the sample index.html');
  const requests: string[] = [];
  page.on('request', (r) => { if (!r.url().startsWith('file:')) requests.push(r.url()); });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: 'light' });
  await page.goto(`file://${SAMPLE}`);
  await settle(page);
  await shot(page, 'pack-index-1440-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await shot(page, 'pack-index-1440-dark');
  await page.setViewportSize({ width: 360, height: 780 });
  await shot(page, 'pack-index-360-dark');
  // The print preview's first page: A4 at 96 dpi, print media.
  await page.setViewportSize({ width: 794, height: 1123 });
  await page.emulateMedia({ media: 'print', colorScheme: 'light' });
  await settle(page);
  await shot(page, 'pack-print-a4-page1');
  await page.pdf({ path: `${DIR}/pack-print-a4.pdf`, format: 'A4', printBackground: true });
  await page.pdf({ path: `${DIR}/pack-print-letter.pdf`, format: 'Letter', printBackground: true });
  expect(requests).toEqual([]);
});
