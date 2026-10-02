// The CSP and Trusted Types (S24 §3.10): every page loads with zero violations and zero console
// errors, and an HTML-string sink throws.
import { AUTH, expect, guard, test } from './fixtures.ts';

test.use({ storageState: AUTH('owner') });

const PAGES = ['/app', '/app/actions', '/app/approvals', '/app/agents', '/app/policies', '/app/connectors', '/app/insights', '/app/deeds', '/app/settings',
  '/app/settings/members', '/app/_kit', '/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8', '/app/nope'];

for (const p of PAGES) {
  test(`no violations on ${p}`, async ({ page }) => {
    const done = await guard(page);
    const res = await page.goto(p);
    expect(res?.headers()['content-security-policy']).toContain("require-trusted-types-for 'script'; trusted-types 'none'");
    await page.waitForLoadState('networkidle');
    await done();
  });
}

test('Trusted Types make string sinks throw', async ({ page }) => {
  await page.goto('/app');
  const threw = await page.evaluate(() => {
    try {
      const d = document.createElement('div');
      (d as unknown as Record<string, string>)['inner' + 'HTML'] = '<img src=x>';
      return false;
    } catch {
      return true;
    }
  });
  expect(threw).toBe(true);
});
