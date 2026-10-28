// shots-simple.mjs: the DL-143 screenshot pass (first run, Connect and a sheet, the Simple view's
// three panels, a decline sheet) at 375 and 1280 in both themes, written as PNGs.
//   node web/scripts/shots-simple.mjs <base url> <out dir>
// Uses the installed Chrome through @playwright/test (no download). Read-only against the server
// except the first-run choice, which it makes on the page like a person would.
import { chromium } from '@playwright/test';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const [base = 'http://127.0.0.1:18241', out = 'shots'] = process.argv.slice(2);
mkdirSync(out, { recursive: true });
const browser = await chromium.launch({ channel: 'chrome' });
const errors = [];

async function page(theme, width) {
  const ctx = await browser.newContext({ viewport: { width, height: width < 600 ? 812 : 860 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: 'reduce', storageState: process.env.STATE || undefined });
  await ctx.addInitScript(([t]) => {
    try { localStorage.setItem('deedglass.theme', t); } catch { /* blocked */ }
  }, [theme]);
  const p = await ctx.newPage();
  p.on('console', (m) => { if (m.type() === 'error') errors.push(`${theme} ${width} ${p.url()}: ${m.text()}`); });
  p.on('pageerror', (e) => errors.push(`${theme} ${width} ${p.url()}: ${e.message}`));
  return { ctx, p };
}

const settle = async (p) => { await p.waitForLoadState('networkidle').catch(() => {}); await p.waitForTimeout(500); };

for (const theme of ['light', 'dark']) {
  for (const width of [375, 1280]) {
    const tag = `${width}-${theme}`;
    const { ctx, p } = await page(theme, width);
    const shot = async (name, full = true) => { await settle(p); await p.screenshot({ path: join(out, `${name}-${tag}.png`), fullPage: full, animations: 'disabled' }); };

    await p.goto(base + '/app/start');
    await shot('01-start-welcome');
    await p.getByRole('button', { name: /get started/i }).click();
    await p.locator('input[value="clinic"]').check();
    await shot('02-start-who');

    await p.goto(base + '/app/connect?from=start');
    await shot('03-connect');
    await p.locator('[data-tile="cursor"]').first().click();
    await shot('04-connect-sheet-cursor', false);
    await p.keyboard.press('Escape');
    await p.locator('[data-tile="scribe"]').first().click();
    await shot('05-connect-sheet-scribe', false);
    await p.keyboard.press('Escape');

    await p.goto(base + '/app/simple');
    await shot('06-simple-activity');
    const firstRow = p.locator('.sv-row > button').first();
    if (await firstRow.count()) { await firstRow.click(); await shot('07-simple-activity-open'); }

    await p.goto(base + '/app/simple?show=waiting');
    await shot('08-simple-waiting');
    const decline = p.locator('[data-decide="deny"]').first();
    if (await decline.count()) { await decline.click(); await shot('09-simple-decline-sheet', false); await p.keyboard.press('Escape'); }

    await p.goto(base + '/app/simple?show=records');
    await shot('10-simple-records');
    const rec = p.locator('#sv-body-records .sv-row > button').first();
    if (await rec.count()) {
      await rec.click();
      const check = p.locator('#sv-body-records [data-check]').first();
      if (await check.count()) await check.click();
      await shot('11-simple-record-checked');
    }
    await ctx.close();
  }
}
await browser.close();
if (errors.length) { console.log(errors.join('\n')); }
console.log(`shots written to ${out}`);
