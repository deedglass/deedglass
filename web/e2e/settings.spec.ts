// S26 §6 Playwright, Settings:
//  1. invite: the sheet, the toast, the dev sign-in link accepted in a second context → Active
//  2. a webhook to a local receiver: the secret shows once (gone after Done), Send test event
//     → ✓ Delivered webhook.test, then delete with the typed host
//  3. retention and approval settings: owner saves; the help sentence follows the number
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { AUTH, dg, expect, guard, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

test.describe('settings', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · invite → accept in a second context → Active', async ({ page, browser }) => {
    const done = await guard(page);
    const email = `sam.e2e.${Date.now().toString(36)}@acme.example`;
    await page.goto('/app/settings/members');
    await page.getByRole('button', { name: 'Invite people' }).click();
    const sheet = page.getByRole('dialog', { name: 'Invite to Acme' });
    await sheet.getByLabel('Work email').fill(email);
    await sheet.getByRole('button', { name: 'Send invite' }).click();
    await expect(page.locator('.toast').last()).toContainText(`Invite sent to ${email} · expires in 7 days`);
    await expect(page.locator('main')).toContainText(email);
    await expect(page.locator('tr', { hasText: email })).toContainText('Invited');
    const link = JSON.parse(dg('org', 'login-link', '--email', email, '--org', 'acme', '--json')).link as string;
    const ctx = await browser.newContext();
    const p2 = await ctx.newPage();
    await p2.goto(link);
    await p2.getByRole('button', { name: /^(Sign in|Accept invite)$/ }).click();
    await p2.waitForURL(/\/app\/?$/);
    await ctx.close();
    await page.reload();
    await expect(page.locator('tr', { hasText: email })).toContainText('Active');
    await done();
  });

  test('2 · a webhook: secret once, test delivered, delete with the typed host', async ({ page }) => {
    const done = await guard(page);
    let hits = 0;
    const server: Server = createServer((req, res) => { hits++; req.resume(); req.on('end', () => { res.writeHead(200); res.end('ok'); }); });
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
    const port = (server.address() as AddressInfo).port;
    try {
      await page.goto('/app/settings/webhooks');
      await page.getByRole('button', { name: 'Add endpoint' }).first().click();
      const sheet = page.getByRole('dialog', { name: 'Add webhook endpoint' });
      await sheet.getByLabel('Endpoint URL').fill(`http://127.0.0.1:${port}/deedglass`);
      await sheet.getByRole('button', { name: 'Select all' }).click();
      await sheet.getByRole('button', { name: 'Add endpoint' }).click();
      const shown = page.getByRole('dialog', { name: 'Endpoint added' });
      await expect(shown).toContainText('Signing secret (shown once)');
      const secret = await shown.locator('input').first().inputValue();
      expect(secret).toMatch(/^whsec_/);
      const doneBtn = shown.getByRole('button', { name: 'Done' });
      await expect(doneBtn).toBeDisabled();
      await shown.getByLabel('I stored it').check();
      await doneBtn.click();
      expect(await page.content()).not.toContain(secret);
      const row = page.locator('.set-hook', { hasText: `127.0.0.1:${port}/deedglass` });
      await row.getByRole('button', { name: 'Send test event' }).click();
      await expect(row).toContainText(/✓ Delivered webhook\.test · 200 in \d+ ms/, { timeout: 15_000 });
      expect(hits).toBeGreaterThan(0);
      await row.getByRole('button', { name: /Actions for 127\.0\.0\.1|Delete endpoint/ }).click();
      const del = page.getByRole('menuitem', { name: 'Delete endpoint' });
      if (await del.count()) await del.click();
      const host = `127.0.0.1:${port}`;
      const dlg = page.getByRole('alertdialog', { name: `Delete the endpoint for ${host}?` });
      await dlg.getByLabel(`Type ${host} to confirm`).fill(host);
      await dlg.getByRole('button', { name: 'Delete endpoint' }).click();
      await expect(page.locator('.toast').last()).toContainText('deleted · record sealed');
    } finally {
      server.close();
    }
    await done();
  });

  test('3 · retention and approval settings save as the owner', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app/settings/general');
    const days = page.getByLabel(/Payload retention/);
    await days.fill('120');
    await expect(page.locator('main')).toContainText('After 120 days, request and result bodies are removed.');
    await page.getByRole('button', { name: 'Save' }).press('Enter'); // a toast may sit over it
    await expect(page.locator('.toast').last()).toContainText('Saved');
    await days.fill('90');
    await page.getByRole('button', { name: 'Save' }).press('Enter'); // a toast may sit over it
    await page.goto('/app/settings/approvals');
    await page.getByRole('radio', { name: 'Review only' }).check();
    await page.getByRole('button', { name: 'Save' }).press('Enter'); // a toast may sit over it
    await expect(page.locator('.toast').last()).toContainText('Saved');
    await page.getByRole('radio', { name: 'Decide' }).check();
    await page.getByRole('button', { name: 'Save' }).press('Enter'); // a toast may sit over it
    await done();
  });
});
