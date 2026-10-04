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

// S26 §6: every S26 screen and sheet in both themes → 0 serious or critical.
import { apiAs, pendingRefund, seeds } from './fixtures.ts';
import type { Page } from '@playwright/test';

async function axe(page: Page): Promise<string[]> {
  const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  return r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => `${v.id}: ${v.nodes.map((n) => `${n.target.join(' ')} (${(n.any[0]?.message ?? '').slice(0, 140)})`).slice(0, 3).join(' | ')}`);
}

for (const theme of ['dark', 'light'] as const) {
  test.describe(`S26 ${theme}`, () => {
    test.use({ storageState: AUTH('owner'), colorScheme: theme });
    test(`axe S26 screens and sheets · ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
      const ap = await pendingRefund({ cents: 222 });
      const four = await pendingRefund({ fourEyes: true });
      const admin = seeds().adminKey;
      const pol = (await apiAs<{ data: { id: string; name: string }[] }>(admin, 'GET', '/v1/policies')).body.data.find((p) => p.name === 'acme-demo')!.id;
      const con = (await apiAs<{ data: { id: string; name: string }[] }>(admin, 'GET', '/v1/connectors')).body.data.find((p) => p.name === 'postgres')!.id;
      const found: string[] = [];
      // sheets slide in over 240 ms; axe reads colours mid-animation as low contrast, so wait it out
      const check = async (label: string) => { if (await page.locator('.sheet, .dialog').count()) { await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running')); await page.mouse.move(1, 1); await page.waitForTimeout(600); } for (const f of await axe(page)) found.push(`${label} · ${f}`); };
      const pages = ['/app/approvals', `/app/approvals/${ap.approval}`, `/app/approvals/${four.approval}`, '/app/approvals?tab=history', '/app/policies',
        `/app/policies/${pol}?v=2`, `/app/policies/${pol}?tab=dry-run&v=2`, `/app/policies/${pol}?tab=tests&v=2`, `/app/policies/${pol}?tab=versions`, `/app/policies/${pol}?tab=replay`,
        '/app/connectors', '/app/connectors/new', '/app/connectors/new?step=configure&type=rest', `/app/connectors/${con}`,
        ...['general', 'members', 'keys', 'approvals', 'webhooks', 'seal-keys', 'notifications', 'danger'].map((s) => `/app/settings/${s}`)];
      for (const p of pages) {
        await page.goto(p);
        await page.waitForLoadState('networkidle');
        await check(p);
      }
      // sheets: approve, deny, enforce, the readiness report after a run, the starter picker, invite
      await page.goto(`/app/approvals/${ap.approval}`);
      await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
      await check('approve sheet');
      await page.keyboard.press('Escape');
      await page.locator('.apv-bar').getByRole('button', { name: 'Deny' }).click();
      await check('deny sheet');
      await page.keyboard.press('Escape');
      await page.goto(`/app/policies/${pol}?tab=versions`);
      await page.waitForLoadState('networkidle');
      const again = page.getByRole('button', { name: /^Enforce v1 again$/ });
      if (await again.count()) { await again.click(); await page.getByRole('alertdialog').waitFor(); await check('enforce sheet'); await page.keyboard.press('Escape'); }
      await page.goto('/app/policies');
      await page.getByRole('button', { name: 'New policy' }).click();
      await page.locator('.starter').first().waitFor();
      await check('starter picker');
      await page.keyboard.press('Escape');
      await page.goto(`/app/connectors/${con}`);
      await page.getByRole('button', { name: /Run (test|check again)/ }).click();
      await page.locator('.readiness').waitFor();
      await check('readiness');
      await page.goto('/app/settings/members');
      await page.getByRole('button', { name: 'Invite people' }).click();
      await check('invite sheet');
      expect(found).toEqual([]);
    });
  });
}
