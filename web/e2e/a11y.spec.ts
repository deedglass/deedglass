// axe (S24 §6): the kit, the shell stub and /login in both themes → 0 serious or critical.
import AxeBuilder from '@axe-core/playwright';
import { AUTH, expect, test } from './fixtures.ts';

for (const theme of ['dark', 'light'] as const) {
  test.describe(`${theme}`, () => {
    test.use({ storageState: AUTH('owner'), colorScheme: theme });
    for (const path of ['/app/_kit', '/app', '/app/actions', '/app/settings']) {
      test(`axe ${path}`, async ({ page }) => {
        await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
        await page.goto(path);
        await page.waitForLoadState('networkidle');
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
