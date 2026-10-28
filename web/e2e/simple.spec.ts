// DL-143 (F13): first run, Connect and the Simple view against the real seeded serve. Every page runs
// under the CSP and console-error guard; axe 0 serious or critical in both themes; 360 px has no
// sideways scroll. Nothing here decides an approval, so the other specs' seeded state is untouched.
import AxeBuilder from '@axe-core/playwright';
import { AUTH, expect, guard, pendingRefund, test } from './fixtures.ts';

test.use({ storageState: AUTH('owner') });

const view = (page: import('@playwright/test').Page) => page.evaluate(() => document.documentElement.getAttribute('data-view'));

test('first run: one sentence, the local line, a path, then Connect', async ({ page }) => {
  const done = await guard(page);
  await page.goto('/app/start');
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expect(page.getByText('Running on this computer')).toBeVisible();
  await expect(page.getByText('Nothing leaves it')).toBeVisible();
  await page.getByRole('button', { name: /get started/i }).click();
  await expect(page.locator('#fs-h2')).toBeFocused();
  await page.getByRole('radio', { name: /I run a clinic/ }).check();
  await page.getByRole('button', { name: /continue to connect/i }).click();
  await page.waitForURL(/\/app\/connect\?from=start/);
  expect(await view(page)).toBe('simple');
  await expect(page.locator('[data-tile]').first()).toHaveAttribute('data-tile', 'scribe');
  await done();
});

test('Connect: seven tiles, a deep link, a copy fallback, a QR code that decodes, no secret anywhere', async ({ page }) => {
  const done = await guard(page);
  await page.goto('/app/connect');
  await expect(page.locator('[data-tile]')).toHaveCount(7);
  for (const pill of await page.locator('[data-tile] [data-status]').all()) await expect(pill).toHaveText(/Not connected|Waiting for first action|Connected/);

  await page.locator('[data-tile="cursor"]').click();
  const sheet = page.getByRole('dialog');
  await expect(sheet.locator('ol li')).toHaveCount(3);
  const primary = sheet.getByRole('link', { name: 'Add to Cursor' });
  const href = (await primary.getAttribute('href')) ?? '';
  expect(href.startsWith('cursor://anysphere.cursor-deeplink/mcp/install?name=deedglass&config=')).toBe(true);
  await sheet.getByText(/Didn.t open/).click();
  await expect(sheet.locator('[data-copy]').first()).toBeVisible();
  await sheet.getByText('On another computer').click();
  const qr = sheet.locator('svg[data-qr]');
  await expect(qr).toBeVisible();
  const text = (await qr.getAttribute('data-qr')) ?? '';
  expect(text).not.toMatch(/dg_(live|test)_|[?&](key|token|secret)=/i);
  const decoded = await page.evaluate(async () => {
    const BD = (window as unknown as { BarcodeDetector?: new (o: unknown) => { detect(s: ImageBitmapSource): Promise<{ rawValue: string }[]> } }).BarcodeDetector;
    if (!BD) return null;
    const svg = document.querySelector('svg[data-qr]')!.cloneNode(true) as SVGSVGElement;
    svg.setAttribute('width', '400');
    svg.setAttribute('height', '400');
    svg.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
    svg.querySelector('path')?.setAttribute('fill', 'black');
    const xml = new XMLSerializer().serializeToString(svg);
    const img = new Image();
    img.src = 'data:image/svg+xml;base64,' + btoa(xml); // the CSP allows data: images, not blob:
    await img.decode();
    const c = document.createElement('canvas');
    c.width = c.height = 400;
    const g = c.getContext('2d')!;
    g.fillStyle = 'white';
    g.fillRect(0, 0, 400, 400);
    g.imageSmoothingEnabled = false;
    g.drawImage(img, 0, 0, 400, 400);
    const r = await new BD({ formats: ['qr_code'] }).detect(c);
    return r[0]?.rawValue ?? '';
  });
  if (decoded === null) test.info().annotations.push({ type: 'skipped', description: 'BarcodeDetector is not in this Chrome; the QR structure is covered by test/qr.test.ts' });
  else expect(decoded).toBe(text);
  await page.keyboard.press('Escape');
  await expect(page.locator('[data-tile="cursor"]')).toBeFocused();
  await done();
});

test('Simple view: plain feed with result words, filters, and the Detailed toggle', async ({ page }) => {
  const done = await guard(page);
  await page.goto('/app/simple');
  const rows = page.locator('#sv-body-activity .sv-row');
  await expect(rows.first()).toBeVisible();
  await expect(rows.first().locator('.sx-pill')).toHaveText(/Checked|Problem found|Waiting for you|Stopped by a rule|Sent back|Couldn.t check|Done|Didn.t run|Running|Expired|Cancelled/);
  await page.getByRole('radio', { name: 'Problems' }).or(page.getByRole('button', { name: 'Problems' })).first().click();
  // Only problems remain (the seeded MISMATCH may be past the first page after other specs ran).
  await expect(page.locator('#sv-body-activity .sv-row:not([data-result="problem"]):not([data-result="couldnt"]):not([data-result="failed"])')).toHaveCount(0);
  await page.getByRole('radio', { name: 'Everything' }).or(page.getByRole('button', { name: 'Everything' })).first().click();
  const first = page.locator('#sv-body-activity .sv-row > button').first();
  await first.click();
  await expect(first).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('#sv-body-activity .sv-more-panel:not([hidden]) a[href^="/app/actions/"]')).toBeVisible();

  // Detailed is today's Console; Simple brings it back.
  await page.locator('.viewswitch a[data-view-set="detailed"]').click();
  await page.waitForURL(/\/app\/?$/);
  expect(await view(page)).toBe('detailed');
  await page.locator('.viewswitch a[data-view-set="simple"]').click();
  await page.waitForURL(/\/app\/simple/);
  await page.goto('/app');
  await page.waitForURL(/\/app\/simple/); // the saved view opens Simple first
  await done();
});

test('Waiting for you: the exact change first, large buttons, decline needs a reason', async ({ page }) => {
  await pendingRefund(); // a fresh request through the real Gate; this test never decides it
  const done = await guard(page);
  await page.goto('/app/simple?show=waiting');
  const card = page.locator('.sv-card').first();
  await expect(card).toBeVisible();
  const change = card.locator('.sv-change');
  const approve = card.locator('[data-decide="approve"]');
  expect((await change.boundingBox())!.y).toBeLessThan((await approve.boundingBox())!.y);
  expect((await approve.boundingBox())!.height).toBeGreaterThanOrEqual(44);
  await card.locator('[data-decide="deny"]').click();
  const sheet = page.getByRole('dialog');
  const send = sheet.getByRole('button', { name: 'Decline' });
  await expect(send).toBeDisabled();
  await sheet.getByRole('textbox').fill('Wrong patient');
  await expect(send).toBeEnabled();
  await sheet.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('.sv-card').first()).toBeVisible();
  await done();
});

test('Locked records: check in the browser and download for an auditor', async ({ page }) => {
  const done = await guard(page);
  await page.goto('/app/simple?show=records');
  await expect(page.locator('.sv-chain')).toHaveAttribute('data-state', /ok|checking|bad/);
  await page.locator('#sv-body-records .sv-row > button').first().click();
  await page.locator('#sv-body-records [data-check]').click();
  await expect(page.locator('#sv-body-records .sv-result')).toHaveAttribute('data-state', 'valid');
  const [dl] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download for an auditor' }).click()]);
  expect(dl.suggestedFilename()).toMatch(/^deedglass-record-\d+\.jsonl$/);
  await done();
});

test('360 px: no sideways scroll on the new screens', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  for (const path of ['/app/start', '/app/connect', '/app/simple', '/app/simple?show=waiting', '/app/simple?show=records']) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(over, path).toBeLessThanOrEqual(0);
  }
});

for (const theme of ['dark', 'light'] as const) {
  test(`axe ${theme}: first run, Connect and its sheet, the three Simple panels`, async ({ page }) => {
    await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* blocked */ } }, theme);
    await page.emulateMedia({ colorScheme: theme });
    const scan = async (where: string) => {
      const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
      const bad = r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
      expect(bad.map((v) => `${where} ${v.id}: ${v.nodes.map((n) => `${n.target.join(' ')} ${n.failureSummary ?? ''}`).slice(0, 3).join(' | ')}`)).toEqual([]);
    };
    for (const path of ['/app', '/app/connect', '/app/simple', '/app/simple?show=waiting', '/app/simple?show=records', '/app/start']) {
      await page.goto(path);
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(300);
      await scan(path);
      if (path === '/app/start') {
        await page.getByRole('button', { name: /get started/i }).click();
        await page.waitForTimeout(600); // step 2 has finished fading in (--dur-reveal)
        await scan('start step 2');
      }
      if (path === '/app/connect') {
        await page.locator('[data-tile="terminal"]').click();
        await page.getByRole('dialog').waitFor();
        await page.waitForTimeout(400); // the sheet has finished sliding in
        await scan('connect sheet');
        await page.keyboard.press('Escape');
      }
    }
  });
}
