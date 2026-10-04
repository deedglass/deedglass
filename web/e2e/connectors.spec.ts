// S26 §6 Playwright, Connectors:
//  1. add a REST connector against S16's mock orders API: type → configure → credential → test ✓;
//     the credential value is nowhere in the DOM after save (a walk over every node and attribute)
//  2. remove the credential → Not set
//  3. the demo Postgres connector: Ready for L2 · … · 0 failed, and the Witness card is healthy
//  4. a viewer reads connectors but gets no Add, Replace or Remove, and the API refuses a create
import { AUTH, apiAs, expect, guard, seeds, test } from './fixtures.ts';

test.describe.configure({ mode: 'serial' });

const NAME = `orders-e2e-${Date.now().toString(36)}`;

async function domHas(page: import('@playwright/test').Page, secret: string): Promise<boolean> {
  return page.evaluate((s) => {
    const walk = (n: Node): boolean => {
      if (n.nodeType === Node.TEXT_NODE && (n.textContent ?? '').includes(s)) return true;
      if (n instanceof Element) {
        for (const a of n.getAttributeNames()) if ((n.getAttribute(a) ?? '').includes(s)) return true;
        if ((n instanceof HTMLInputElement || n instanceof HTMLTextAreaElement) && n.value.includes(s)) return true;
      }
      for (const c of n.childNodes) if (walk(c)) return true;
      return false;
    };
    return walk(document.documentElement) || document.documentElement.outerHTML.includes(s);
  }, secret);
}

test.describe('connectors', () => {
  test.use({ storageState: AUTH('owner') });

  test('1 · add a REST connector, set its credential, the test passes, the secret is gone', async ({ page }) => {
    test.skip(!process.env.WEB_E2E_MOCK_URL, 'the mock orders API did not start');
    const done = await guard(page);
    await page.goto('/app/connectors');
    await page.getByRole('link', { name: 'Add connector' }).first().click();
    await expect(page).toHaveURL(/\/app\/connectors\/new/);
    await page.locator('.type-card[data-type="rest"]').click();
    await expect(page).toHaveURL(/step=configure&type=rest/);
    await page.getByLabel('Name', { exact: true }).fill(NAME);
    await page.locator('#cfg-base_url').fill(process.env.WEB_E2E_MOCK_URL!);
    // The REST schema as SchemaForm draws it: auth and test as fieldsets, methods as checkboxes,
    // prefixes as chips, and tools (an array of objects) as JSON where the subset stops.
    await page.locator('#cfg-auth-kind').selectOption('bearer');
    await page.locator('#cfg-allowed_methods-0').check();
    await page.locator('#cfg-allowed_path_prefixes').fill('/v1/orders');
    await page.locator('#cfg-allowed_path_prefixes').press('Enter');
    await page.locator('#cfg-test-method').selectOption('GET');
    await page.locator('#cfg-test-path').fill('/v1/health');
    await page.locator('#cfg-tools').fill('[{"name": "get", "description": "Read one order.", "method": "GET", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string", "pattern": "^[0-9]{1,12}$", "required": true}}}]');
    await page.getByRole('button', { name: 'Create connector' }).click();
    await expect(page).toHaveURL(/step=credentials&id=con_/, { timeout: 10_000 });
    const secret = process.env.WEB_E2E_MOCK_TOKEN!;
    await page.locator('input[type="password"]').first().fill(secret);
    await page.getByRole('button', { name: 'Save credentials' }).click();
    await expect(page.locator('.cl-summary, .readiness')).toContainText(/checks passed/, { timeout: 15_000 });
    expect(await domHas(page, secret)).toBe(false);
    await done();
  });

  test('2 · remove the credential → Not set', async ({ page }) => {
    test.skip(!process.env.WEB_E2E_MOCK_URL, 'the mock orders API did not start');
    const done = await guard(page);
    const list = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/connectors');
    const con = list.body.data.find((x) => x.name === NAME)!;
    await page.goto(`/app/connectors/${con.id}`);
    await expect(page.locator('.cred-row')).toContainText('Set · bearer');
    await page.locator('.cred-row').getByRole('button', { name: 'Remove' }).click();
    const dlg = page.getByRole('alertdialog', { name: `Remove the credential for ${NAME}?` });
    await expect(dlg).toContainText(`Agents using ${NAME} will get connector_unavailable until a new credential is set.`);
    await dlg.getByRole('button', { name: 'Remove credential' }).click();
    await expect(page.locator('.cred-row')).toContainText('Not set');
    await done();
  });

  test('3 · the demo Postgres connector is Ready for L2 and its Witness is healthy', async ({ page }) => {
    const done = await guard(page);
    const list = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/connectors');
    const pg = list.body.data.find((x) => x.name === 'postgres')!;
    await page.goto(`/app/connectors/${pg.id}`);
    await expect(page.locator('.wit-line')).toContainText('Witness · healthy');
    await page.getByRole('button', { name: /Run (test|check again)/ }).click();
    await expect(page.locator('.readiness')).toContainText(/Ready for L2 · \d+ passed · .* 0 failed/, { timeout: 15_000 });
    await expect(page.getByText('deedglass connector postgres check --connector postgres')).toBeVisible();
    await done();
  });
});

test.describe('connectors as a viewer', () => {
  test.use({ storageState: AUTH('viewer') });
  test('4 · read-only, and the API refuses a create', async ({ page }) => {
    const done = await guard(page);
    await page.goto('/app/connectors');
    await expect(page.locator('table.con-table')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Add connector' })).toHaveCount(0);
    await page.locator('table.con-table a.rowlink').first().click();
    await expect(page.locator('.cred-row')).toBeVisible();
    await expect(page.locator('.cred-row').getByRole('button')).toHaveCount(0);
    const status = await page.evaluate(async () => {
      const me = await (await fetch('/v1/me', { headers: { 'Deedglass-Version': '2026-10-01' } })).json();
      const r = await fetch('/v1/connectors', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Deedglass-Version': '2026-10-01', 'Deedglass-CSRF': me.csrf_token, 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ type: 'rest', name: 'nope', config: {} }) });
      return r.status;
    });
    expect(status).toBe(403);
    await done();
  });
});
