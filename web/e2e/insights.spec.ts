// S28 §6 Playwright: the Insights screen on the simulated org scripts/web_e2e.sh seeds (acme-sim,
// seed 7: a loosen, a tighten and a budget suggestion, 5 anomalies, v2 in shadow), signed in as
// Priya (admin) and Sam (viewer). The flow runs in order: keyboard, reject, accept into shadow,
// two more simulated days, promote, the Policies screen. axe in both themes.
import { existsSync, readFileSync } from 'node:fs';
import AxeBuilder from '@axe-core/playwright';
import { appendQuietDays, EMAIL, expect, guard, signIn, test } from './fixtures.ts';
import type { Page } from '@playwright/test';

const SIM = `${process.env.WEB_E2E_WORK}/sim.json`;
const hasSim = () => existsSync(SIM);
const sim = () => JSON.parse(readFileSync(SIM, 'utf8')) as { org: string; slug: string; adminKey: string };

test.describe.configure({ mode: 'serial' });
test.use({ storageState: { cookies: [], origins: [] } });

async function open(page: Page, who: 'admin' | 'viewer', theme: 'dark' | 'light' = 'dark'): Promise<void> {
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
  await signIn(page, EMAIL[who], sim().slug);
  await page.goto('/app/insights');
  await expect(page.locator('#body-suggestions')).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator('#body-trends')).toHaveAttribute('aria-busy', 'false');
}

const cardOf = (page: Page, kind: string) => page.locator(`.ins-card[data-kind="${kind}"]`);

test.beforeEach(() => test.skip(!hasSim(), 'no simulated org (WEB_E2E_SIM=0)'));

test('renders the four sections with real data', async ({ page }) => {
  const check = await guard(page);
  await open(page, 'admin');
  for (const name of ['Suggestions', 'Shadow', 'Anomalies', 'Trends']) await expect(page.getByRole('heading', { level: 2, name })).toBeVisible();
  await expect(page.locator('.ins-card')).toHaveCount(3);
  await expect(page.locator('.ins-card').first()).toHaveAttribute('data-kind', 'loosen');
  await expect(cardOf(page, 'loosen').getByRole('button', { name: 'Accept and run in shadow' })).toBeVisible();
  await expect(cardOf(page, 'loosen').locator('[data-diff-toggle]')).toHaveText(/Policy change\s*\+8 lines/);
  await expect(page.locator('.ins-shadow')).toContainText('acme-sim v2');
  await expect(page.locator('.ins-anom')).toHaveCount(5);
  await expect(page.locator('#body-trends svg[role="img"]')).toHaveCount(3);
  await expect(page.locator('#rail-settings')).toContainText("Off · Deedglass's own wording");
  await check();
});

for (const theme of ['dark', 'light'] as const) {
  test(`axe ${theme}: 0 serious, diff open and the accept sheet`, async ({ page }) => {
    await open(page, 'admin', theme);
    await cardOf(page, 'loosen').locator('[data-diff-toggle]').click();
    const scan = async (what: string) => {
      const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
      expect(r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
        .map((v) => `${what} ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`)).toEqual([]);
    };
    await scan('page');
    await cardOf(page, 'loosen').getByRole('button', { name: 'Accept and run in shadow' }).click();
    await expect(page.getByRole('dialog', { name: 'Accept this suggestion?' })).toBeVisible();
    await page.waitForTimeout(300);
    await scan('accept sheet');
  });
}

test('a viewer sees no buttons, only the footnote', async ({ page }) => {
  await open(page, 'viewer');
  await expect(page.locator('.ins-card')).toHaveCount(3);
  await expect(page.locator('[data-accept], [data-reject], [data-promote], [data-resolve], [data-ack], [data-generate], [data-settings]')).toHaveCount(0);
  await expect(page.getByText('Only admins can accept or reject suggestions.').first()).toBeVisible();
});

test('the keyboard path j j a Enter', async ({ page }) => {
  await open(page, 'admin');
  const second = page.locator('.ins-card').nth(1);
  const title = (await second.locator('h3').textContent()) ?? '';
  await page.locator('body').click({ position: { x: 5, y: 300 } });
  await page.keyboard.press('j');
  await expect(page.locator('.ins-card').first()).toBeFocused();
  await page.keyboard.press('j');
  await expect(second).toBeFocused();
  // Enter on the card toggles its diff
  await page.keyboard.press('Enter');
  await expect(second.locator('[data-diff-toggle]')).toHaveAttribute('aria-expanded', 'true');
  await page.keyboard.press('Enter');
  await expect(second.locator('[data-diff-toggle]')).toHaveAttribute('aria-expanded', 'false');
  await page.keyboard.press('a');
  const sheet = page.getByRole('dialog', { name: 'Accept this suggestion?' });
  await expect(sheet).toBeVisible();
  await expect(sheet).toContainText(title);
  // v2 runs in shadow: Enter asks for the replace box instead of sending anything
  await expect(sheet.getByRole('checkbox', { name: 'Replace v2 in shadow' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(sheet.getByRole('alert')).toContainText('Tick the box to replace v2');
  await page.keyboard.press('Escape');
  await expect(sheet).toHaveCount(0);
  await expect(second).toBeFocused();
  // ? lists the screen's own keys
  await page.keyboard.press('?');
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toContainText('Accept the selected suggestion');
});

test('reject requires a reason', async ({ page }) => {
  const check = await guard(page);
  await open(page, 'admin');
  const card = cardOf(page, 'budget');
  await card.getByRole('button', { name: 'Reject' }).click();
  const sheet = page.getByRole('dialog', { name: 'Reject this suggestion?' });
  await expect(sheet).toContainText("Deedglass won't suggest this again for 30 days unless the numbers double.");
  await sheet.getByRole('button', { name: 'Reject suggestion' }).click();
  await expect(sheet.locator('#rej-reason-e')).toHaveText('A reason is required to reject a suggestion. It goes into the sealed record.');
  await expect(sheet.getByLabel(/Reason/)).toHaveAttribute('aria-invalid', 'true');
  await sheet.getByLabel(/Reason/).fill('Refund volume is seasonal this month');
  await sheet.getByRole('button', { name: 'Reject suggestion' }).click();
  await expect(page.locator('.toast').last()).toContainText('Suggestion rejected · record sealed');
  await expect(cardOf(page, 'budget')).toHaveCount(0);
  await check();
});

test('accept → a toast with the deed → Shadow shows v3', async ({ page }) => {
  const check = await guard(page);
  await open(page, 'admin');
  await cardOf(page, 'loosen').getByRole('button', { name: 'Accept and run in shadow' }).click();
  const sheet = page.getByRole('dialog', { name: 'Accept this suggestion?' });
  await expect(sheet).toContainText('Deedglass creates acme-sim v3 from it and runs it in shadow next to v1.');
  await expect(sheet).toContainText('acme-sim v2 is running in shadow now. Accepting replaces it.');
  await sheet.getByRole('checkbox', { name: 'Replace v2 in shadow' }).check();
  await sheet.getByLabel(/Reason/).fill('Ops approves these every time');
  await sheet.getByRole('button', { name: 'Accept and run in shadow' }).click();
  const toast = page.locator('.toast').last();
  await expect(toast).toContainText(/acme-sim v3 is running in shadow · deed_\w{4}…\w{4} sealed/);
  await expect(toast.getByRole('link', { name: 'View record' })).toHaveAttribute('href', /^\/app\/deeds\/deed_/);
  await expect(page.locator('.ins-shadow')).toContainText('acme-sim v3');
  await expect(cardOf(page, 'loosen')).toHaveCount(0);
  await check();
});

test('two more simulated days → Promote (typed) → Policies shows v3 enforced', async ({ page }) => {
  test.setTimeout(240_000);
  appendQuietDays(sim().slug);
  const check = await guard(page);
  await open(page, 'admin');
  const shadow = page.locator('.ins-shadow');
  await expect(shadow).toContainText('acme-sim v3');
  await expect(shadow.locator('.ins-agree')).toContainText(/Agrees on [\d,]+ of [\d,]+ actions/);
  await shadow.getByRole('button', { name: /Promote acme-sim v3/ }).click();
  const sheet = page.getByRole('alertdialog', { name: 'Enforce acme-sim v3?' });
  await expect(sheet.locator('.ins-promote')).toContainText(/agreement/);
  const go = sheet.getByRole('button', { name: 'Enforce v3' });
  await expect(go).toBeDisabled();
  await sheet.getByLabel('Type acme-sim to confirm').fill('acme-sim');
  await sheet.getByLabel(/Reason/).fill('Promoted after two days in shadow');
  const ack = sheet.getByRole('checkbox');
  if (await ack.count()) await ack.check();
  await go.click();
  await expect(page.locator('.toast').last()).toContainText('acme-sim v3 enforced · record sealed');
  await page.goto('/app/policies');
  const row = page.locator('tr', { hasText: 'acme-sim' });
  await expect(row).toContainText('Enforced');
  await expect(row).toContainText('v3');
  await check();
});
