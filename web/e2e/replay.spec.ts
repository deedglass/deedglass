// S27 §3.4 Playwright, the Policies screen's Replay tab, on the seeded acme-demo:
//  1. an admin runs a 7-day replay of v2 against the recorded decisions: the summary line and the
//     result digest show, the digest copies in full, and axe finds nothing serious in either theme
//  2. a version baseline (v1) runs and the result names it
//  3. routed answers: running (a determinate progress bar) → Cancel → the neutral cancelled line;
//     failed (an alert with the next step); an older server's 404 (S26's sentence)
//  4. a viewer reads the latest result, sees Run disabled beside the role sentence, and the API
//     refuses a viewer's replay
import AxeBuilder from '@axe-core/playwright';
import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { AUTH, apiAs, expect, guard, seeds, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

const fx = (n: string) => JSON.parse(readFileSync(new URL(`../test/fixtures/s27/${n}.json`, import.meta.url), 'utf8'));

async function policyId(): Promise<string> {
  const r = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/policies');
  return r.body.data.find((p) => p.name === 'acme-demo')!.id;
}

async function axe(page: Page): Promise<string[]> {
  const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']).analyze();
  return r.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`);
}

const SUMMARY = /^[\d,]+ decisions? would change( · [\d,]+ by a different rule)? · [\d,]+ unchanged · [\d,]+ unknown$/;

test.describe('replay', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · a 7-day replay of v2 shows the summary line and the digest', async ({ page, context }) => {
    test.setTimeout(90_000);
    const done = await guard(page);
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.goto(`/app/policies/${await policyId()}?tab=replay&v=2`);
    await expect(page.getByRole('heading', { name: 'Replay v2' })).toBeVisible();
    await page.getByRole('group', { name: 'Window' }).getByRole('button', { name: 'Last 7 d' }).click();
    await expect(page.getByLabel('Compare with')).toHaveValue('0');
    const posted = page.waitForRequest((r) => r.method() === 'POST' && /\/v1\/policies\/pol_[^/]+\/versions\/2\/replay$/.test(r.url()));
    await page.getByRole('button', { name: 'Run replay' }).click();
    const body = (await posted).postDataJSON() as { from: string; to: string; baseline: { type: string } };
    expect(body.baseline).toEqual({ type: 'recorded' });
    expect(Date.parse(body.to) - Date.parse(body.from)).toBe(7 * 86_400_000);
    const res = page.locator('[data-replay-status="succeeded"]');
    await expect(res).toBeVisible({ timeout: 20_000 });
    await expect(res.locator('.rp-summary')).toHaveText(SUMMARY);
    await expect(res.locator('.rp-meta')).toContainText('acme-demo v2 against recorded decisions');
    const foot = res.locator('.rp-foot');
    await expect(foot).toHaveText(/^Result sha256:[0-9a-f]{4}…[0-9a-f]{4} · same inputs and version always give this digest$/);
    const full = await foot.locator('[data-copy]').getAttribute('data-copy');
    expect(full).toMatch(/^sha256:[0-9a-f]{64}$/);
    await foot.getByRole('button', { name: 'Copy the full result digest' }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(full);
    // a sample links to its action
    const first = res.locator('.rp-samples').first();
    if (await first.count()) {
      await first.locator('summary').click();
      await expect(first.locator('a').first()).toHaveAttribute('href', /^\/app\/actions\/act_/);
    }
    // the same inputs and version give the same digest again
    const again = await apiAs<{ id: string }>(seeds().adminKey, 'POST', `/v1/policies/${await policyId()}/versions/2/replay`, { from: body.from, to: body.to });
    let r: { status: string; result_digest?: string } = { status: 'queued' };
    for (let i = 0; i < 80 && (r.status === 'queued' || r.status === 'running'); i++) {
      await page.waitForTimeout(250);
      r = (await apiAs<{ status: string; result_digest?: string }>(seeds().adminKey, 'GET', `/v1/replays/${again.body.id}`)).body;
    }
    expect(r.result_digest).toBe(full);
    // axe on the result in both themes: the tab reopens on the latest replay
    for (const theme of ['dark', 'light'] as const) {
      await page.evaluate((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
      await page.emulateMedia({ colorScheme: theme });
      await page.reload();
      await expect(page.locator('[data-replay-status="succeeded"] .rp-summary')).toHaveText(SUMMARY);
      if (await page.locator('.rp-samples summary').count()) await page.locator('.rp-samples summary').first().click();
      await page.waitForLoadState('networkidle');
      expect(await axe(page), theme).toEqual([]);
    }
    await done();
  });

  test('2 · a version baseline names it', async ({ page }) => {
    const done = await guard(page);
    await page.goto(`/app/policies/${await policyId()}?tab=replay&v=2`);
    await page.getByLabel('Compare with').selectOption('1');
    const posted = page.waitForRequest((r) => r.method() === 'POST' && /\/versions\/2\/replay$/.test(r.url()));
    await page.getByRole('button', { name: 'Run replay' }).click();
    expect(((await posted).postDataJSON() as { baseline: unknown }).baseline).toEqual({ type: 'version', version: 1 });
    const res = page.locator('[data-replay-status="succeeded"]');
    await expect(res).toBeVisible({ timeout: 20_000 });
    await expect(res.locator('.rp-meta')).toContainText('acme-demo v2 against v1');
    await expect(res.locator('.rp-summary')).toHaveText(SUMMARY);
    await done();
  });

  test('3 · routed: running → Cancel → cancelled; failed; an older server', async ({ page }) => {
    const done = await guard(page);
    const pol = await policyId();
    const running = { ...fx('replay-running'), progress: { processed: 2400, total: 5890, percent: 41 } };
    let cancelled = false;
    await page.route(/\/v1\/policies\/[^/]+\/versions\/\d+\/replay$/, (route) => route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify(running) }));
    await page.route(/\/v1\/replays\/rpl_[^/]+$/, (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(cancelled ? fx('replay-cancelled') : running) }));
    await page.route(/\/v1\/replays\/rpl_[^/]+\/cancel$/, (route) => { cancelled = true; return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...fx('replay-cancelled'), progress: { processed: 2400, total: 5890, percent: 41 } }) }); });
    await page.goto(`/app/policies/${pol}?tab=replay&v=2`);
    await page.getByRole('button', { name: 'Run replay' }).click();
    const bar = page.getByRole('progressbar', { name: 'Replay progress' });
    await expect(bar).toHaveAttribute('aria-valuenow', '41');
    await expect(page.locator('[data-replay-status="running"]')).toContainText('Replaying 5,890 actions · 41%');
    await expect(page.getByRole('button', { name: 'Run replay' })).toHaveAttribute('aria-disabled', 'true');
    await page.getByRole('button', { name: 'Cancel replay' }).click();
    const cx = page.locator('[data-replay-status="cancelled"]');
    await expect(cx).toContainText('Replay cancelled after 2,400 of 5,890 actions.');
    await expect(cx.locator('[role=alert]')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Cancel replay' })).toBeHidden();

    await page.unroute(/\/v1\/policies\/[^/]+\/versions\/\d+\/replay$/);
    await page.route(/\/v1\/policies\/[^/]+\/versions\/\d+\/replay$/, (route) => route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify(fx('replay-failed')) }));
    await page.getByRole('button', { name: 'Run replay' }).click();
    const failed = page.getByRole('alert').filter({ hasText: 'The replay failed.' });
    await expect(failed).toContainText('The replay failed. The replay stopped on a server error. Start it again; the result will be the same.');
    await expect(page.getByRole('button', { name: 'Run replay' })).toHaveAttribute('aria-disabled', 'false');

    await page.unroute(/\/v1\/policies\/[^/]+\/versions\/\d+\/replay$/);
    await page.route(/\/v1\/policies\/[^/]+\/versions\/\d+\/replay$/, (route) => route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { type: 'invalid_request_error', code: 'not_found', message: 'Not found.', param: null, request_id: 'req_x', doc_url: '' } }) }));
    await page.getByRole('button', { name: 'Run replay' }).click();
    await expect(page.locator('.rp-out')).toContainText("Replay isn't available on this server yet. Use Dry run to test a single action.");
    await done();
  });
});

test.describe('replay as a viewer', () => {
  test.use({ storageState: AUTH('viewer') });

  test('4 · a viewer reads the latest result, Run is disabled with the role sentence', async ({ page }) => {
    const done = await guard(page);
    const pol = await policyId();
    await page.goto(`/app/policies/${pol}?tab=replay`);
    await expect(page.getByText('Viewers can read replays. Running one needs the Admin role.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Run replay' })).toBeDisabled();
    await expect(page.getByRole('group', { name: 'Window' })).toBeHidden();
    await expect(page.getByLabel('Compare with')).toBeHidden();
    const res = page.locator('[data-replay-status="succeeded"]');
    await expect(res.locator('.rp-summary')).toHaveText(SUMMARY);
    await expect(res.locator('.rp-meta')).toContainText('Latest replay');
    // the page's own call, as the viewer: refused
    const st = await page.evaluate(async (id) => {
      const csrf = (await (await fetch('/v1/me', { headers: { 'Deedglass-Version': '2026-10-01' } })).json()).csrf_token as string;
      const r = await fetch(`/v1/policies/${id}/versions/2/replay`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Deedglass-Version': '2026-10-01', 'Deedglass-CSRF': csrf, 'Idempotency-Key': crypto.randomUUID() }, body: '{}' });
      return r.status;
    }, pol);
    expect(st).toBe(403);
    await done();
  });
});
