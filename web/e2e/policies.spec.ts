// S26 §6 Playwright, Policies, on the seeded acme-demo (v2 enforced by the seed):
//  1. a typo `desicion` → the diagnostic at its line and column → Apply fix → clean
//  2. dry run a DELETE without WHERE → deny by block-unscoped-writes, its line reveals the editor
//  3. tests pass against the buffer
//  4. save v3 → shadow → enforce (typing the name) → toast → View deed shows policy.activated,
//     and both deeds pass deedglass verify after export
//  5. a viewer gets the editor read-only and the API refuses its save
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { AUTH, apiAs, expect, guard, seeds, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

async function policyId(): Promise<string> {
  const r = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/policies');
  return r.body.data.find((p) => p.name === 'acme-demo')!.id;
}

test.describe('policies', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · a typo shows at its line and column, Apply fix makes it clean', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app/policies');
    await page.getByRole('link', { name: 'acme-demo' }).click();
    await expect(page).toHaveURL(/\/app\/policies\/pol_/);
    await page.goto(`${new URL(page.url()).pathname}?v=2`); // the seed's v2 (the demo loop may have added later ones)
    const ta = page.locator('.pe textarea, textarea.pe-input').first();
    await expect(ta).toBeVisible();
    const src = await ta.inputValue();
    const at = src.indexOf('    decision: deny\n    reason: DROP is never allowed');
    expect(at).toBeGreaterThan(0);
    const line = src.slice(0, at).split('\n').length;
    await ta.evaluate((el: HTMLTextAreaElement, pos: number) => { el.focus(); el.setSelectionRange(pos + 4, pos + 12); }, at);
    await page.keyboard.type('desicion');
    const row = page.locator('.pe-diags').getByText(`Line ${line}, col 5`);
    await expect(row).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.pe-diags')).toContainText('Unknown key "desicion"');
    await page.locator('.pe-diags').getByRole('button', { name: /Apply fix/ }).first().click();
    await expect(page.locator('.pe-diags')).toContainText(/No problems · \d+ rules · 1 budget/, { timeout: 5000 });
    expect(await ta.inputValue()).toBe(src);
    await done();
  });

  test('2 · dry run a DELETE without WHERE: deny, the rule and its line', async ({ page }) => {
    const done = await guard(page);
    await page.goto(`/app/policies/${await policyId()}?tab=dry-run&v=2`);
    await page.getByLabel('Tool').fill('postgres.execute');
    await page.getByLabel('SQL', { exact: true }).fill('DELETE FROM public.orders');
    await page.getByRole('button', { name: 'Run dry run' }).click();
    const res = page.locator('.dr-result');
    await expect(res).toContainText('Denied');
    await expect(res).toContainText('block-unscoped-writes');
    await expect(res).toContainText(/line \d+/);
    await expect(res).toContainText(/evaluated in [\d,]+ µs/);
    await res.getByRole('button', { name: /line \d+/ }).first().click();
    await expect(page.getByRole('tab', { name: 'Editor' })).toHaveAttribute('aria-selected', 'true');
    await done();
  });

  test('3 · the cases pass against the buffer', async ({ page }) => {
    const done = await guard(page);
    await page.goto(`/app/policies/${await policyId()}?tab=tests&v=2`);
    const cases = readFileSync('../examples/demo/policies/acme-demo.cases.yaml', 'utf8');
    await page.getByLabel('Cases (YAML)').fill(cases);
    await page.getByRole('button', { name: 'Run tests' }).click();
    await expect(page.locator('#pp-tests')).toContainText(/cases passed/, { timeout: 10_000 });
    await done();
  });

  test('4 · save v3, shadow it, enforce it with the typed name; both deeds verify', async ({ page }) => {
    const done = await guard(page);
    const id = await policyId();
    await page.goto(`/app/policies/${id}?v=2`); // the seed's v2, so later specs keep its rules
    const ta = page.locator('.pe textarea, textarea.pe-input').first();
    await expect(ta).toBeVisible();
    const src = await ta.inputValue();
    // Comments don't change a version's hash, so change the description line (a real change).
    const at = src.indexOf('description: ') + 'description: '.length;
    const eol = src.indexOf('\n', at);
    await ta.evaluate((el: HTMLTextAreaElement, pos: number) => { el.focus(); el.setSelectionRange(pos, pos); }, eol);
    await page.keyboard.type(` (S26 e2e ${Date.now().toString(36)})`);
    const save = page.getByRole('button', { name: /^Save as v\d+$/ });
    await expect(save).toBeVisible();
    const vText = (await save.innerText()).replace('Save as v', '');
    await save.click();
    await expect(page.locator('.toast').last()).toContainText(`Saved as v${vText}`);
    // shadow
    await page.getByRole('button', { name: 'Run in shadow' }).click();
    const sh = page.getByRole('dialog', { name: `Run acme-demo v${vText} in shadow?` });
    await sh.getByLabel(/Reason/).fill('Try the comment change next to the enforced version');
    await sh.getByRole('button', { name: `Run v${vText} in shadow` }).click();
    await expect(page.locator('.toast').last()).toContainText(`acme-demo v${vText} running in shadow · record sealed`);
    // enforce: the button stays disabled until the name is typed exactly
    await page.getByRole('button', { name: 'Enforce', exact: true }).click();
    const en = page.getByRole('alertdialog', { name: `Enforce acme-demo v${vText}?` });
    const go = en.getByRole('button', { name: `Enforce v${vText}` });
    await en.getByLabel(/Reason/).fill('Promote after the shadow run');
    await en.getByLabel('Type acme-demo to confirm').fill('acme-dem');
    await expect(go).toBeDisabled();
    await en.getByLabel('Type acme-demo to confirm').fill('acme-demo');
    await expect(go).toBeEnabled();
    await go.click();
    const toast = page.locator('.toast').last();
    await expect(toast).toContainText(`acme-demo v${vText} enforced · record sealed`);
    await toast.getByRole('link', { name: 'View record' }).click();
    await expect(page).toHaveURL(/\/app\/deeds\/deed_/);
    await expect(page.locator('main')).toContainText('policy.activated');
    // export the chain and verify it offline
    const dir = mkdtempSync(join(tmpdir(), 'dg-s26-'));
    const exp = spawnSync(process.env.WEB_E2E_DG!, ['deeds', 'export', '--org', 'acme', '--out', join(dir, 'deeds.tar.gz')], { cwd: process.env.WEB_E2E_WORK, env: { ...process.env, DEEDGLASS_API_KEY: seeds().adminKey, DEEDGLASS_SERVER: process.env.WEB_E2E_BASE }, encoding: 'utf8' });
    expect(exp.status, exp.stderr).toBe(0);
    const keys = await (await fetch(`${process.env.WEB_E2E_BASE}/.well-known/deedglass/keys.json`)).text();
    writeFileSync(join(dir, 'keys.json'), keys);
    const ver = spawnSync(process.env.WEB_E2E_DG!, ['verify', join(dir, 'deeds.tar.gz'), '--keys', join(dir, 'keys.json')], { encoding: 'utf8' });
    expect(ver.status, ver.stdout + ver.stderr).toBe(0);
    await done();
  });
});

test.describe('policies as a viewer', () => {
  test.use({ storageState: AUTH('viewer') });
  test('5 · read-only editor, and the API refuses a save', async ({ page }) => {
    const done = await guard(page);
    const id = await policyId();
    await page.goto(`/app/policies/${id}`);
    await expect(page.getByText('Viewers can read policies. Editing needs the Admin role.')).toBeVisible();
    await expect(page.locator('.pe textarea, textarea.pe-input').first()).toHaveAttribute('readonly', '');
    await expect(page.getByRole('button', { name: 'Enforce', exact: true })).toHaveCount(0);
    const status = await page.evaluate(async (pid) => {
      const me = await (await fetch('/v1/me', { headers: { 'Deedglass-Version': '2026-10-01' } })).json();
      const r = await fetch(`/v1/policies/${pid}/versions`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Deedglass-Version': '2026-10-01', 'Deedglass-CSRF': me.csrf_token, 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ source: 'x' }) });
      return r.status;
    }, id);
    expect(status).toBe(403);
    await done();
  });
});
