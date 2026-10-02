// Signs in the Acme owner, admin and viewer through real sign-in links (deedglass org login-link)
// and saves each storageState.
import { test as setup, expect } from '@playwright/test';
import { AUTH, EMAIL, loginLink } from './fixtures.ts';

for (const role of ['owner', 'admin', 'viewer'] as const) {
  setup(`sign in as ${role}`, async ({ page }) => {
    await page.goto(loginLink(EMAIL[role]));
    await page.getByRole('button', { name: /^(Sign in|Accept invite)$/ }).click();
    await expect(page).toHaveURL(/\/app\/?$/);
    await expect(page.locator('#org-name')).toHaveText('Acme');
    await page.context().storageState({ path: AUTH(role) });
  });
}
