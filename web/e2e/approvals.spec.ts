// S26 §6 Playwright, Approvals: real approvals created by support-bot through the API, decided in
// the Console, executed by the Gate and witnessed by Postgres (L2) on the throwaway Acme Store.
//  1. approve at 360 px: the card, the detail with the sticky bar, the sheet, ✓ Executed · L2 · VERIFIED
//  2. approve at 1440 px in the two-pane layout, with the keyboard (A opens the sheet, never submits)
//  3. deny: the button stays disabled without a reason; with one, the agent's action carries it
//  4. four-eyes: A approves (1 of 2), A has no buttons, B approves in a second context, it executes
//  5. the race: A and B decide within 100 ms, one wins, the other sees who decided
//  6. self: an action on behalf of the owner shows the self sentence and Deny only
import { AUTH, apiAs, expect, guard, pendingRefund, seeds, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

test.describe('approvals', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · approve at 360 px ends ✓ Executed · L2 · VERIFIED without a reload', async ({ page }) => {
    const done = await guard(page);
    await page.setViewportSize({ width: 360, height: 780 });
    const ap = await pendingRefund();
    await page.goto('/app/approvals');
    const card = page.locator(`.apv-card[data-id="${ap.approval}"]`);
    await expect(card).toBeVisible();
    await expect(card).toContainText('postgres.execute');
    await expect(card).toContainText('INSERT ~1 row in public.refunds');
    await expect(card.locator('.apv-count')).toContainText(/in \d+ min/);
    const box = await card.boundingBox();
    expect(box!.height).toBeGreaterThanOrEqual(96);
    await card.click();
    await expect(page).toHaveURL(new RegExp(`/app/approvals/${ap.approval}$`));
    await expect(page.locator('.apv-effect')).toHaveText('INSERT ~1 row in public.refunds');
    const bar = page.locator('.apv-bar');
    await expect(bar).toBeInViewport();
    const approve = bar.getByRole('button', { name: 'Approve' });
    expect((await approve.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await approve.click();
    const sheet = page.getByRole('dialog', { name: 'Approve postgres.execute?' });
    await expect(sheet).toBeVisible();
    await sheet.getByLabel('Why is this safe? (optional, sealed in the record)').fill('Customer confirmed by phone');
    const t0 = Date.now();
    await sheet.getByRole('button', { name: 'Approve' }).click();
    await expect(page.locator('.apv-bar-note')).toContainText(/Executed · .*Proven.*Matches.*record sealed/, { timeout: 15_000 });
    test.info().annotations.push({ type: 'approve→verified ms (360)', description: String(Date.now() - t0) });
    const a = await apiAs<{ approval: { status: string } }>(seeds().adminKey, 'GET', `/v1/actions/${ap.action}`);
    expect(a.body.approval.status).toBe('approved');
    await done();
  });

  test('2 · approve at 1440 px in two panes, keyboard opens the sheet but never submits', async ({ page }) => {
    const done = await guard(page);
    await page.setViewportSize({ width: 1440, height: 900 });
    const ap = await pendingRefund({ cents: 4500 });
    await page.goto(`/app/approvals/${ap.approval}`);
    await expect(page.locator('.apv-listcol')).toBeVisible();
    await expect(page.locator('.apv-detail')).toHaveAttribute('data-approval', ap.approval);
    await page.locator('main').click({ position: { x: 900, y: 10 } });
    await page.keyboard.press('a');
    const sheet = page.getByRole('dialog', { name: 'Approve postgres.execute?' });
    await expect(sheet).toBeVisible();
    await page.waitForTimeout(400);
    let st = await apiAs<{ status: string }>(seeds().adminKey, 'GET', `/v1/approvals/${ap.approval}`);
    expect(st.body.status).toBe('pending'); // A opened the sheet; nothing was sent
    await page.keyboard.press('Escape');
    await expect(sheet).toBeHidden();
    await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
    await page.getByRole('dialog', { name: 'Approve postgres.execute?' }).getByLabel(/Why is this safe/).press('ControlOrMeta+Enter');
    await expect(page.locator('.apv-bar-note')).toContainText(/Executed · .*Proven.*Matches/, { timeout: 15_000 });
    st = await apiAs<{ status: string }>(seeds().adminKey, 'GET', `/v1/approvals/${ap.approval}`);
    expect(st.body.status).toBe('approved');
    await done();
  });

  test('3 · deny needs a reason, and the agent receives it', async ({ page }) => {
    const done = await guard(page);
    const ap = await pendingRefund({ cents: 900 });
    await page.goto(`/app/approvals/${ap.approval}`);
    await page.locator('.apv-bar').getByRole('button', { name: 'Deny' }).click();
    const sheet = page.getByRole('dialog', { name: 'Deny postgres.execute?' });
    const go = sheet.getByRole('button', { name: 'Deny' });
    await expect(go).toBeDisabled();
    await sheet.getByLabel('Reason (required, the agent sees it)').fill('no');
    await expect(go).toBeDisabled();
    await sheet.getByLabel('Reason (required, the agent sees it)').fill('Refund already issued by phone');
    await expect(go).toBeEnabled();
    await go.click();
    await expect(page.locator('.apv-bar-note')).toContainText('Denied');
    const a = await apiAs<{ approval: { status: string; decision_reason: string } }>(seeds().agentKey, 'GET', `/v1/actions/${ap.action}`);
    expect(a.body.approval.status).toBe('denied');
    expect(a.body.approval.decision_reason).toBe('Refund already issued by phone');
    await done();
  });

  test('4 · four-eyes: 1 of 2, no second tap, a second person executes it', async ({ page, browser }) => {
    const done = await guard(page);
    const ap = await pendingRefund({ fourEyes: true });
    await page.goto(`/app/approvals/${ap.approval}`);
    await expect(page.locator('.apv-l4 .apv-fe, .apv-fe-line').first()).toBeVisible();
    await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
    await page.getByRole('dialog', { name: /Approve/ }).getByRole('button', { name: 'Approve' }).click();
    await expect(page.locator('.apv-bar-note')).toContainText('You countersigned at');
    await expect(page.locator('.apv-bar').getByRole('button')).toHaveCount(0);
    await expect(page.locator('.apv-fe-line')).toContainText('1 of 2 countersigned · Arjun Mehta');
    const b = await browser.newContext({ storageState: AUTH('admin') });
    const pb = await b.newPage();
    await pb.goto(`/app/approvals/${ap.approval}`);
    await pb.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
    await pb.getByRole('dialog', { name: /Approve/ }).getByRole('button', { name: 'Approve' }).click();
    await expect(pb.locator('.apv-bar-note')).toContainText(/Executed · .*Proven/, { timeout: 15_000 });
    await b.close();
    await done();
  });

  test('5 · the race: one decision wins, the other sees who decided', async ({ page, browser }) => {
    const ap = await pendingRefund({ cents: 700 });
    const b = await browser.newContext({ storageState: AUTH('admin') });
    const pb = await b.newPage();
    await Promise.all([page.goto(`/app/approvals/${ap.approval}`), pb.goto(`/app/approvals/${ap.approval}`)]);
    for (const p of [page, pb]) {
      await p.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
      await expect(p.getByRole('dialog', { name: /Approve/ })).toBeVisible();
    }
    await Promise.all([
      page.getByRole('dialog', { name: /Approve/ }).getByRole('button', { name: 'Approve' }).click(),
      pb.getByRole('dialog', { name: /Approve/ }).getByRole('button', { name: 'Approve' }).click(),
    ]);
    const notes = await Promise.all([page, pb].map(async (p) => {
      await expect(p.locator('.apv-bar-note')).toBeVisible({ timeout: 10_000 });
      return p.locator('.apv-bar-note').innerText();
    }));
    const lost = notes.filter((n) => /approved this|already decided|decided/i.test(n));
    expect(lost.length, notes.join(' | ')).toBe(1);
    await b.close();
  });

  test('6 · self: on behalf of you, someone else must approve', async ({ page }) => {
    const done = await guard(page);
    const ap = await pendingRefund({ onBehalfOf: 'owner@acme.example', cents: 300 });
    await page.goto(`/app/approvals/${ap.approval}`);
    await expect(page.locator('.apv-bar-note')).toContainText('You asked for this action, so someone else must approve it.');
    await expect(page.locator('.apv-bar').getByRole('button', { name: 'Approve' })).toHaveCount(0);
    await expect(page.locator('.apv-bar').getByRole('button', { name: 'Deny' })).toBeVisible();
    await done();
  });
});
