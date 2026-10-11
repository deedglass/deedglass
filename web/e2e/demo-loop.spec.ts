// S26 §9, the founder's loop, UI only after seeding (S34's click path). Seeding (API, before the
// UI): acme-demo with approvals on (S22's variant, as `make demo-approvals-on` does) and
// claude-desktop asking to mark order 42 as shipped. Then, only in the browser:
//  Approvals at 390 px → approve → ✓ Executed · L2 · VERIFIED → open the action → Check in this
//  browser ✓ → Policies › acme-demo: edit, Save, Run in shadow, Enforce (typed) → Deeds shows both
//  policy.activated deeds. Afterwards the seed's v2 is enforced again so later specs see it.
import { readFileSync } from 'node:fs';
import { apiAs, AUTH, expect, guard, seeds, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

let approval = '';
let policy = '';

test.beforeAll(async () => {
  const admin = seeds().adminKey;
  const src = readFileSync('../examples/demo/policies/acme-demo-approvals.yaml', 'utf8');
  const pols = await apiAs<{ data: { id: string; name: string }[] }>(admin, 'GET', '/v1/policies');
  policy = pols.body.data.find((p) => p.name === 'acme-demo')!.id;
  const v = await apiAs<{ version: number }>(admin, 'POST', `/v1/policies/${policy}/versions`, { source: src });
  const act = await apiAs(admin, 'POST', `/v1/policies/${policy}/versions/${v.body.version}/activate`, { mode: 'enforce', reason: 'S26 demo loop: approvals on (make demo-approvals-on)' });
  expect(act.status).toBeLessThan(300);
  let agent = await apiAs<{ data: { id: string; name: string }[] }>(admin, 'GET', '/v1/agents?limit=100');
  let cd = agent.body.data.find((a) => a.name === 'claude-desktop');
  if (!cd) {
    const made = await apiAs<{ id: string; name: string }>(admin, 'POST', '/v1/agents', { name: 'claude-desktop', description: 'The demo laptop agent' });
    cd = made.body;
  }
  const key = await apiAs<{ secret: string }>(admin, 'POST', `/v1/agents/${cd.id}/keys`, { name: 'demo-loop' });
  const r = await apiAs<{ id: string; approval?: { id: string } }>(key.body.secret, 'POST', '/v1/actions',
    { tool: 'postgres.execute', args: { sql: "UPDATE public.orders SET status = 'shipped' WHERE id = 42" } }, { 'Deedglass-On-Behalf-Of': 'u_4821' });
  expect(r.status, JSON.stringify(r.body)).toBe(202);
  approval = r.body.approval!.id;
});

test.afterAll(async () => {
  await apiAs(seeds().adminKey, 'POST', `/v1/policies/${policy}/versions/2/activate`, { mode: 'enforce', reason: 'S26 demo loop: back to the e2e seed v2' });
});

test.describe('the founder loop', () => {
  test.use({ storageState: AUTH('owner') });

  test('approve on a phone, watch it verify, check the deed, then shadow and enforce a policy', async ({ page }) => {
    test.setTimeout(90_000);
    const done = await guard(page);
    const log: string[] = [];
    const t0 = Date.now();
    const step = (s: string) => log.push(`${((Date.now() - t0) / 1000).toFixed(1)} s · ${s}`);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/app/approvals');
    const card = page.locator(`.apv-card[data-id="${approval}"]`);
    await expect(card).toContainText('UPDATE ~1 row in public.orders');
    step('Approvals at 390 px: the card for "Mark order 42 as shipped"');
    await card.click();
    await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
    await page.getByRole('dialog', { name: 'Approve postgres.execute?' }).getByLabel(/Why is this safe/).fill('Customer confirmed by phone');
    await page.getByRole('dialog', { name: 'Approve postgres.execute?' }).getByRole('button', { name: 'Approve' }).click();
    step('Approve with a reason');
    await expect(page.locator('.apv-bar-note')).toContainText(/Executed · .*Proven.*Matches.*record sealed/, { timeout: 15_000 });
    step('✓ Executed · L2 · VERIFIED · record sealed');
    await page.locator('.apv-bar-note').getByRole('link', { name: 'View action' }).click();
    await expect(page).toHaveURL(/\/app\/actions\/act_/);
    await expect(page.locator('[data-sentence]')).toContainText(/Proven by Postgres|Matches/);
    step('the action: VERIFIED');
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByRole('button', { name: 'Check in this browser' }).click();
    await expect(page.locator('main')).toContainText('Seal valid', { timeout: 10_000 });
    step('the deed checks in the browser ✓');
    await page.goto(`/app/policies/${policy}`);
    const ta = page.locator('.pe textarea, textarea.pe-input').first();
    await expect(ta).toBeVisible();
    const src = await ta.inputValue();
    // Comments don't change a version's hash, so change the description line (a real change).
    const at = src.indexOf('description: ') + 'description: '.length;
    const eol = src.indexOf('\n', at);
    await ta.evaluate((el: HTMLTextAreaElement, pos: number) => { el.focus(); el.setSelectionRange(pos, pos); }, eol);
    await page.keyboard.type(` (demo loop ${Date.now().toString(36)})`);
    const save = page.getByRole('button', { name: /^Save as v\d+$/ });
    const v = (await save.innerText()).replace('Save as v', '');
    await save.click();
    await expect(page.locator('.toast').last()).toContainText(`Saved as v${v}`);
    step(`acme-demo saved as v${v}`);
    await page.getByRole('button', { name: 'Run in shadow' }).click();
    const sh = page.getByRole('dialog', { name: `Run acme-demo v${v} in shadow?` });
    await sh.getByLabel(/Reason/).fill('Demo loop: shadow first');
    await sh.getByRole('button', { name: `Run v${v} in shadow` }).click();
    await expect(page.locator('.toast').last()).toContainText('running in shadow · record sealed');
    step(`v${v} in shadow`);
    await page.getByRole('button', { name: 'Enforce', exact: true }).click();
    const en = page.getByRole('alertdialog', { name: `Enforce acme-demo v${v}?` });
    await en.getByLabel(/Reason/).fill('Demo loop: enforce');
    await en.getByLabel('Type acme-demo to confirm').fill('acme-demo');
    await en.getByRole('button', { name: `Enforce v${v}` }).click();
    await expect(page.locator('.toast').last()).toContainText(`acme-demo v${v} enforced · record sealed`);
    step(`v${v} enforced`);
    await page.goto('/app/deeds');
    await expect(page.locator('table tbody tr').first()).toBeVisible();
    const top = await page.locator('table tbody tr').allInnerTexts();
    expect(top.slice(0, 6).filter((t) => /admin/i.test(t)).length).toBeGreaterThanOrEqual(2);
    step('Deeds: both admin deeds at the top of the chain');
    test.info().annotations.push({ type: 'demo loop', description: log.join('\n') });
    await done();
  });
});
