// axe (S24 §6, S25 §6): the kit, the S25 screens (with the real L2 hero) and /login in both
// themes → 0 serious or critical.
import AxeBuilder from '@axe-core/playwright';
import { AUTH, expect, hero, test } from './fixtures.ts';

for (const theme of ['dark', 'light'] as const) {
  test.describe(`${theme}`, () => {
    test.use({ storageState: AUTH('owner'), colorScheme: theme });
    for (const path of ['/app/_kit', '/app', '/app/actions', 'hero', '/app/deeds', 'deed', '/app/agents', 'agent', '/app/settings']) {
      test(`axe ${path}`, async ({ page }) => {
        await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
        let url = path;
        if (path === 'hero') url = `/app/actions/${hero().mismatch}`;
        if (path === 'deed' || path === 'agent') {
          await page.goto(path === 'deed' ? '/app/deeds' : '/app/agents');
          await page.locator('table a.rowlink').first().click();
          url = page.url();
        }
        await page.goto(url);
        await page.waitForLoadState('networkidle');
        if (path === 'hero') await page.getByRole('button', { name: 'Check in this browser' }).click();
        const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
        const bad = r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
        expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`)).toEqual([]);
      });
    }
  });
}

test('axe /login', async ({ browser }) => {
  for (const colorScheme of ['dark', 'light'] as const) {
    const ctx = await browser.newContext({ colorScheme });
    const page = await ctx.newPage();
    await page.goto('/login');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500); // the card fades in
    const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze();
    expect(r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
      .map((v) => `${colorScheme} ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`)).toEqual([]);
    await ctx.close();
  }
});
