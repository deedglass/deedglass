// The S25 screenshot pass (00_DESIGN_SKILLS §2.5, S25 §5.D and §6): Overview, Actions, Action detail,
// Deeds, a deed, Agents and an agent, at 360, 768 and 1440 in dark and light; every state (loading,
// empty, filtered empty, error, viewer, offline) at 360 and 1440 in both themes; the MISMATCH hero at
// 1440 dark with the 1200 × 675 crop for X; the first-run flow; the feed mid-stream; the undo sheet;
// a reduced-motion pass and a keyboard-focus shot. Written to SHOTS_DIR.
import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { AUTH, EMAIL, hero, signIn, test } from './fixtures.ts';

const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens`;
mkdirSync(DIR, { recursive: true });
const SIZES = [[360, 780], [768, 1024], [1440, 900]] as const;
const THEMES = ['dark', 'light'] as const;
const DG = process.env.WEB_E2E_DG ?? '../bin/deedglass';

async function prep(page: Page, theme: string, w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.emulateMedia({ colorScheme: theme as 'dark' | 'light' });
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
}
async function settle(page: Page, ms = 400) {
  await page.waitForLoadState('networkidle').catch(() => undefined);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}
const shot = (page: Page, name: string, full = false) => page.screenshot({ path: `${DIR}/${name}.png`, fullPage: full });

const SCREENS: { key: string; path: string | (() => string); api: string }[] = [
  { key: 'overview', path: '/app', api: '**/v1/overview?*' },
  { key: 'actions', path: '/app/actions', api: '**/v1/actions?*' },
  { key: 'action-mismatch', path: () => `/app/actions/${hero().mismatch}`, api: '**/v1/actions/act_*' },
  { key: 'action-verified', path: () => `/app/actions/${hero().verified}`, api: '**/v1/actions/act_*' },
  { key: 'deeds', path: '/app/deeds', api: '**/v1/deeds?*' },
  { key: 'agents', path: '/app/agents', api: '**/v1/agents?*' },
];

test.use({ storageState: AUTH('owner') });
test.describe.configure({ mode: 'serial' });

let deedPath = '';
let agentPath = '';

test('find a deed and an agent', async ({ page }) => {
  await page.goto('/app/deeds');
  await page.locator('table a.rowlink', { hasText: 'MISMATCH' }).first().click();
  deedPath = new URL(page.url()).pathname;
  await page.goto('/app/agents');
  await page.getByRole('link', { name: 'support-bot' }).click();
  agentPath = new URL(page.url()).pathname;
});

for (const theme of THEMES) {
  for (const [w, h] of SIZES) {
    test(`screens · ${w} · ${theme}`, async ({ page }) => {
      test.setTimeout(90_000);
      await prep(page, theme, w, h);
      for (const s of SCREENS) {
        await page.goto(typeof s.path === 'string' ? s.path : await s.path());
        await settle(page);
        await shot(page, `${s.key}-${w}-${theme}`);
        if (w === 1440 && (s.key === 'overview' || s.key.startsWith('action-'))) await shot(page, `${s.key}-full-${w}-${theme}`, true);
      }
      await page.goto(deedPath);
      await settle(page);
      await shot(page, `deed-${w}-${theme}`);
      for (const tab of ['', 'keys', 'budgets', 'security', 'danger']) {
        await page.goto(tab ? `${agentPath}?tab=${tab}` : agentPath);
        await settle(page);
        await shot(page, `agent${tab ? '-' + tab : ''}-${w}-${theme}`);
      }
    });
  }
}

for (const theme of THEMES) {
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`states · ${w} · ${theme}`, async ({ page, context }) => {
      test.setTimeout(120_000);
      await prep(page, theme, w, h);
      for (const s of SCREENS) {
        const url = typeof s.path === 'string' ? s.path : await s.path();
        // Loading: hold the screen's request, shoot the skeleton in the final geometry.
        let release: () => void = () => undefined;
        const held = new Promise<void>((r) => (release = r));
        await page.route(s.api, async (r) => { await held; await r.continue().catch(() => undefined); });
        await page.goto(url);
        await page.waitForTimeout(500);
        await shot(page, `${s.key}-loading-${w}-${theme}`);
        release();
        await page.unroute(s.api);
        // Error: the server's message, the request id, Retry.
        await page.route(s.api, (r) => r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { type: 'api_error', code: 'internal_error', message: 'Something went wrong on our side. Retry, and share the request id if it keeps happening.', param: null, request_id: 'req_01k633rrmh3bcz2p39gg4vp46q', doc_url: 'https://docs.deedglass.com/errors/internal_error' } }) }));
        await page.goto(url);
        await settle(page);
        await shot(page, `${s.key}-error-${w}-${theme}`);
        await page.unroute(s.api);
        // Empty (never used): an empty first page.
        if (['actions', 'deeds', 'agents'].includes(s.key)) {
          await page.route(s.api, (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ object: 'list', data: [], has_more: false, next_cursor: null }) }));
          await page.goto(url);
          await settle(page);
          await shot(page, `${s.key}-empty-${w}-${theme}`);
          await page.unroute(s.api);
        }
      }
      // Filtered empty.
      await page.goto('/app/actions?tool=nothing.matches.*&verdict=MISMATCH');
      await settle(page);
      await shot(page, `actions-filtered-empty-${w}-${theme}`);
      await page.goto('/app/deeds?kind=amendment');
      await settle(page);
      await shot(page, `deeds-filtered-empty-${w}-${theme}`);
      // Not found.
      await page.goto('/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8');
      await settle(page);
      await shot(page, `action-404-${w}-${theme}`);
      // Offline: the banner, content kept.
      await page.goto('/app');
      await settle(page);
      await context.setOffline(true);
      await page.evaluate(() => window.dispatchEvent(new Event('offline')));
      await page.waitForTimeout(400);
      await shot(page, `overview-offline-${w}-${theme}`);
      await context.setOffline(false);
      await page.evaluate(() => window.dispatchEvent(new Event('online')));
    });
  }
}

for (const theme of THEMES) {
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`viewer · ${w} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ storageState: AUTH('viewer'), viewport: { width: w, height: h }, colorScheme: theme });
      const page = await ctx.newPage();
      await prep(page, theme, w, h);
      await page.goto('/app/agents');
      await settle(page);
      await shot(page, `agents-viewer-${w}-${theme}`);
      await page.goto(`${agentPath}?tab=danger`);
      await settle(page);
      await shot(page, `agent-danger-viewer-${w}-${theme}`);
      await page.goto(`/app/actions/${hero().mismatch}`);
      await settle(page);
      await shot(page, `action-mismatch-viewer-${w}-${theme}`);
      await ctx.close();
    });
  }
}

test('the hero: MISMATCH detail at 1440 dark, the seal checked, and the X crop', async ({ page }) => {
  await prep(page, 'dark', 1440, 900);
  await page.goto(`/app/actions/${hero().mismatch}`);
  await settle(page);
  await page.getByRole('button', { name: 'Check in this browser' }).click();
  await page.waitForTimeout(900);
  await shot(page, 'hero-mismatch-1440-dark');
  await page.screenshot({ path: `${DIR}/hero-mismatch-1200x675.png`, clip: { x: 248, y: 56, width: 1192, height: 675 } });
  await page.setViewportSize({ width: 1200, height: 675 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.waitForTimeout(300);
  await shot(page, 'hero-mismatch-1200x675-viewport');
});

test('the undo sheet, light and dark', async ({ page }) => {
  for (const theme of THEMES) {
    await prep(page, theme, 1440, 900);
    await page.goto(`/app/actions/${hero().mismatch}`);
    await settle(page);
    await page.getByRole('button', { name: 'Preview undo' }).click();
    await page.locator('.sheet table.diff').waitFor();
    await page.waitForTimeout(400);
    await shot(page, `undo-sheet-1440-${theme}`);
    await page.keyboard.press('Escape');
  }
  await prep(page, 'dark', 360, 780);
  await page.goto(`/app/actions/${hero().mismatch}`);
  await settle(page);
  await page.getByRole('button', { name: 'Preview undo' }).click();
  await page.locator('.sheet table.diff').waitFor();
  await page.waitForTimeout(400);
  await shot(page, 'undo-sheet-360-dark');
});

test('the feed mid-stream, paused with the pill', async ({ page }) => {
  await prep(page, 'dark', 1440, 900);
  await page.goto('/app/actions');
  await settle(page);
  const child = spawn(DG, ['dev', 'fake-action', '--org', 'acme', '--n', '30', '--pace', '120ms', '--quiet'], { cwd: process.env.WEB_E2E_WORK, env: process.env, stdio: 'ignore' });
  await page.waitForTimeout(1500);
  await shot(page, 'actions-midstream-1440-dark');
  await page.keyboard.press('p');
  await page.waitForTimeout(1500);
  await shot(page, 'actions-paused-1440-dark');
  await new Promise<void>((r) => child.on('exit', () => r()));
  await page.keyboard.press('p');
});

test('first run, step by step', async ({ browser }) => {
  for (const theme of THEMES) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
    await signIn(page, EMAIL.owner, 'acme-staging');
    await settle(page);
    await shot(page, `first-run-1-1440-${theme}`);
    if (theme === 'dark') {
      await page.getByRole('button', { name: 'Create demo agent' }).click();
      await page.locator('[data-secret]').waitFor();
      await page.waitForTimeout(300);
      await page.screenshot({ path: `${DIR}/first-run-2-keysheet-1440-dark.png`, mask: [page.locator('[data-secret]')] });
      await page.getByLabel("I've stored this key in a secret manager").check();
      await page.getByRole('button', { name: 'Done' }).click();
      await page.waitForTimeout(400);
      await page.screenshot({ path: `${DIR}/first-run-3-snippets-1440-dark.png`, mask: [page.locator('.snippets pre')] });
      await page.setViewportSize({ width: 360, height: 780 });
      await page.waitForTimeout(300);
      await page.screenshot({ path: `${DIR}/first-run-3-snippets-360-dark.png`, fullPage: true, mask: [page.locator('.snippets pre')] });
    }
    await ctx.close();
  }
});

test('reduced motion and keyboard focus', async ({ page }) => {
  await prep(page, 'dark', 1440, 900);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await page.goto('/app');
  await settle(page);
  await shot(page, 'overview-1440-dark-reduced-motion');
  await page.goto('/app/actions');
  await settle(page);
  await page.locator('#feed-q').focus();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');
  await page.waitForTimeout(200);
  await shot(page, 'focus-filterbar-1440-dark');
  await page.goto(`/app/actions/${hero().mismatch}`);
  await settle(page);
  await page.getByRole('button', { name: 'Check in this browser' }).focus();
  await shot(page, 'focus-deedpanel-1440-dark');
});
