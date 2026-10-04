// Screenshots for the design loop (00_DESIGN_SKILLS §2.5): pages × widths × themes, from dist/
// (or SCREENS_BASE for `npm run dev`), with Playwright from web/node_modules.
// SCREENS_OUT=<dir> SCREENS='home:/,quickstart:/quickstart/' SCREENS_SET='360:dark,1440:dark,1440:light' node scripts/screens.mjs
import { createRequire } from 'node:module';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { serve } from './serve-dist.mjs';

const require = createRequire(new URL('../../web/package.json', import.meta.url));
const { chromium } = require('@playwright/test');
const out = process.env.SCREENS_OUT ?? join(fileURLToPath(new URL('../../..', import.meta.url)), 'ops/plan/prompts-v7/reports/screens/S33');
mkdirSync(out, { recursive: true });
let base = process.env.SCREENS_BASE;
let server;
if (!base) {
  server = await serve(join(fileURLToPath(new URL('..', import.meta.url)), 'dist'));
  base = `http://127.0.0.1:${server.address().port}`;
}
const pages = (process.env.SCREENS ?? 'home:/').split(',').map((s) => s.split(/:(.*)/s).slice(0, 2));
const set = (process.env.SCREENS_SET ?? '360:dark,1440:dark,1440:light').split(',').map((s) => s.split(':'));
const browser = await chromium.launch({ channel: 'chrome' });
for (const [w, theme] of set) {
  const ctx = await browser.newContext({ viewport: { width: Number(w), height: 900 }, colorScheme: theme, reducedMotion: process.env.SCREENS_REDUCED ? 'reduce' : 'no-preference', deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  for (const [name, path] of pages) {
    await page.goto(base + path, { waitUntil: 'networkidle' });
    if (name.startsWith('search')) {
      await page.locator('button[data-open-modal]').first().click();
      await page.waitForTimeout(400);
      await page.keyboard.type(process.env.SCREENS_QUERY ?? 'policy_denied');
      await page.waitForTimeout(900);
    }
    await page.waitForTimeout(500);
    const file = join(out, `${name}-${w}-${theme}.png`);
    await page.screenshot({ path: file, fullPage: process.env.SCREENS_FULL === '1' });
    console.log(file);
  }
  await ctx.close();
}
await browser.close();
server?.close();
