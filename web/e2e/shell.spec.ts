// The app shell (S24 §6 Playwright): keyboard reach at 1440, 900 and 360, the palette and id
// jump, the theme, Stop all for each role, the bottom bar, the sign-in redirect and the 404.
import { AUTH, EMAIL, expect, guard, loginLink, test } from './fixtures.ts';

const NAV = ['Overview', 'Actions', 'Approvals', 'Sessions', 'Agents', 'Policies', 'Connectors', 'Insights', 'Records', 'Settings'];

test.describe('signed out', () => {
  test('every page redirects to sign-in with next, and comes back after', async ({ page }) => {
    const res = await page.request.get('/app/actions?verdict=MISMATCH', { maxRedirects: 0 });
    expect(res.status()).toBe(302);
    expect(res.headers().location).toBe('/login?next=%2Fapp%2Factions%3Fverdict%3DMISMATCH');
    await page.goto('/app/deeds');
    await expect(page).toHaveURL(/\/login\?next=%2Fapp%2Fdeeds$/);
    // The same tab opens the emailed link: the return path survives.
    await page.goto(loginLink(EMAIL.viewer));
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page).toHaveURL(/\/app\/deeds$/);
  });
  test('a hostile next is dropped', async ({ page }) => {
    const res = await page.request.get('/app/x%5C%5Cevil', { maxRedirects: 0 });
    expect(res.headers().location).toBe('/login');
  });
});

test.describe('owner', () => {
  test.use({ storageState: AUTH('owner') });

  for (const [w, h] of [[1440, 900], [900, 900]] as const) {
    test(`every destination is reachable by keyboard at ${w}`, async ({ page }) => {
      const done = await guard(page);
      await page.setViewportSize({ width: w, height: h });
      await page.goto('/app');
      await page.keyboard.press('Tab');
      await expect(page.locator('.skip')).toBeFocused();
      const reached: string[] = [];
      for (let i = 0; i < 30 && reached.length < NAV.length; i++) {
        await page.keyboard.press('Tab');
        const info = await page.evaluate(() => {
          const el = document.activeElement as HTMLElement;
          return { nav: el?.getAttribute('data-nav'), ring: getComputedStyle(el).boxShadow };
        });
        if (info.nav) {
          reached.push(info.nav);
          expect(info.ring, `focus ring on ${info.nav}`).not.toBe('none');
        }
      }
      expect(reached).toEqual(NAV.map((n) => (n === 'Records' ? 'deeds' : n.toLowerCase())));
      await page.keyboard.press('Enter');
      await expect(page).toHaveURL(/\/app\/settings$/);
      await done();
    });
  }

  test('the bottom bar at 360 reaches every destination', async ({ page }) => {
    const done = await guard(page);
    await page.setViewportSize({ width: 360, height: 780 });
    await page.goto('/app');
    const bar = page.getByRole('navigation', { name: 'Console' }).last();
    await expect(page.locator('.sidebar')).toBeHidden();
    await expect(bar).toBeVisible();
    for (const n of ['Overview', 'Actions', 'Approvals', 'Agents']) await expect(bar.getByRole('link', { name: n })).toBeVisible();
    await bar.getByRole('button', { name: 'More' }).focus();
    await page.keyboard.press('Enter');
    const sheet = page.getByRole('dialog', { name: 'More' });
    await expect(sheet).toBeVisible();
    for (const n of ['Sessions', 'Policies', 'Connectors', 'Insights', 'Records', 'Settings']) await expect(sheet.getByRole('link', { name: n })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(sheet).toBeHidden();
    // No horizontal page scroll at 360.
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360);
    await done();
  });

  test('the palette searches, and a pasted id jumps', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app');
    const t0 = Date.now();
    await page.keyboard.press('ControlOrMeta+k');
    const input = page.getByRole('combobox', { name: 'Search or paste an id' });
    await expect(input).toBeFocused();
    expect(Date.now() - t0).toBeLessThan(1000);
    await input.fill('appr');
    await expect(page.getByRole('option').first()).toHaveText(/Approvals/);
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/app\/approvals$/);
    await page.keyboard.press('/');
    await page.keyboard.insertText('act_01k6h7mz3c9d8e7f6g5h4jq3m8');
    await expect(page.getByRole('option').first()).toHaveText('Open act_01k6h7mz3c9d8e7f6g5h4jq3m8');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/app\/actions\/act_01k6h7mz3c9d8e7f6g5h4jq3m8$/);
    // A miss teaches.
    await page.keyboard.press('ControlOrMeta+k');
    await page.getByRole('combobox').fill('zzqqxx');
    await expect(page.getByText('No matches. Paste an id like act_… to jump straight to it.')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('combobox')).toHaveCount(0);
    await done();
  });

  test('g-shortcuts go and never act; ? lists them', async ({ page }) => {
    await page.goto('/app');
    await page.keyboard.press('g');
    await page.keyboard.press('d');
    await expect(page).toHaveURL(/\/app\/deeds$/);
    await page.keyboard.press('?');
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
  });

  test('the theme persists', async ({ page }) => {
    await page.goto('/app');
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await page.getByRole('button', { name: 'Theme' }).click();
    await page.getByRole('menuitemradio', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await page.getByRole('button', { name: 'Theme' }).click();
    await page.getByRole('menuitemradio', { name: 'Dark' }).click();
  });

  test('unknown pages are the Console 404', async ({ page }) => {
    const r = await page.goto('/app/actions/not-an-id');
    expect(r?.status()).toBe(404);
    await expect(page.getByRole('heading', { name: "This page doesn't exist." })).toBeVisible();
    await expect(page.getByText('not-an-id')).toHaveCount(0);
  });
});

test.describe.serial('Stop all', () => {
  test('a viewer sees it disabled, with the reason', async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: AUTH('viewer') });
    const page = await ctx.newPage();
    await page.goto('/app');
    const btn = page.getByRole('button', { name: 'Stop all agents' });
    await expect(btn).toHaveAttribute('aria-disabled', 'true');
    await btn.click({ force: true }); // aria-disabled, but still focusable and clickable for its reason
    await expect(page.getByText('Stopping every agent needs the Admin role. Ask an admin or owner in Acme.')).toBeVisible();
    await expect(page.getByText("You're a Viewer in Acme. You can see everything; changes need an Admin.")).toBeVisible();
    await ctx.close();
  });

  test('an admin stops all with a typed confirmation', async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: AUTH('admin') });
    const page = await ctx.newPage();
    await page.goto('/app');
    await page.getByRole('button', { name: 'Stop all agents' }).click();
    const sheet = page.getByRole('alertdialog', { name: 'Stop all agents in Acme?' });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByRole('button', { name: 'Cancel' })).toBeFocused();
    const go = sheet.getByRole('button', { name: 'Stop all agents' });
    await expect(go).toBeDisabled();
    await sheet.getByLabel('Type acme to confirm').fill('Acme');
    await expect(go).toBeDisabled();
    await sheet.getByLabel('Type acme to confirm').fill('acme');
    await expect(go).toBeEnabled();
    await sheet.getByLabel('Reason (optional, sealed in the record)').fill('Investigating unexpected refunds');
    await go.click();
    await expect(page.getByText(/All agents in Acme are stopped · since \d\d:\d\d · by Priya Raman/)).toBeVisible();
    await expect(page.locator('.toast').filter({ hasText: 'All agents in Acme stopped · record sealed' })).toBeVisible();
    // Admins can't start them again: no Start all button.
    await expect(page.getByRole('button', { name: 'Start all agents' })).toHaveCount(0);
    await ctx.close();
  });

  test('an owner starts all again, with a reason', async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: AUTH('owner') });
    const page = await ctx.newPage();
    await page.goto('/app');
    await page.getByRole('button', { name: 'Start all agents' }).click();
    const sheet = page.getByRole('dialog', { name: 'Start all agents in Acme?' });
    const go = sheet.getByRole('button', { name: 'Start all agents' });
    await expect(go).toBeDisabled();
    await sheet.getByLabel('Why is it safe to start again?').fill('Refunds job fixed and redeployed.');
    await go.click();
    await expect(page.locator('#banner-stopped')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Stop all agents' })).toBeVisible();
    await ctx.close();
  });
});
