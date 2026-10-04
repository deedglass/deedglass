// The S26 screenshot pass (00_DESIGN_SKILLS §2.5, S26 §5.D and §6): Approvals (360 first), Policies,
// Connectors and Settings at 360, 768 and 1440 in dark and light; the sheets (approve, deny, enforce
// with the typed name), four-eyes progress, the editor with a diagnostic and its quick fix, the dry-run
// trace, the version diff, the readiness report (and one ✕ row, from a routed answer, labelled as
// such), the add-connector steps; loading, empty, error and viewer states at 360 and 1440; a
// reduced-motion pass; keyboard focus; and the phone crop of the approval card. Written to SHOTS_DIR.
import { copyFileSync, mkdirSync, readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { AUTH, apiAs, pendingRefund, seeds, test } from './fixtures.ts';

const DIR = process.env.SHOTS_DIR ?? `${process.cwd()}/test-results/screens`;
const PICKS = `${DIR}/../../screenshots/S26`;
mkdirSync(DIR, { recursive: true });
mkdirSync(PICKS, { recursive: true });
const SIZES = [[360, 780], [768, 1024], [1440, 900]] as const;
const THEMES = ['dark', 'light'] as const;

async function prep(page: Page, theme: string, w: number, h: number) {
  await page.setViewportSize({ width: w, height: h });
  await page.emulateMedia({ colorScheme: theme as 'dark' | 'light' });
  await page.addInitScript((t) => { try { localStorage.setItem('deedglass.theme', t); } catch { /* */ } }, theme);
}
async function settle(page: Page, ms = 500) {
  await page.waitForLoadState('networkidle').catch(() => undefined);
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(ms);
}
const shot = (page: Page, name: string, full = false) => page.screenshot({ path: `${DIR}/${name}.png`, fullPage: full });

let ids: { single: string; four: string; policy: string; pg: string } = { single: '', four: '', policy: '', pg: '' };

test.use({ storageState: AUTH('owner') });
test.describe.configure({ mode: 'serial' });

test('seed approvals and find ids', async () => {
  const a = await pendingRefund();
  const b = await pendingRefund({ fourEyes: true });
  const pols = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/policies');
  const cons = await apiAs<{ data: { id: string; name: string }[] }>(seeds().adminKey, 'GET', '/v1/connectors');
  ids = { single: a.approval, four: b.approval, policy: pols.body.data.find((p) => p.name === 'acme-demo')!.id, pg: cons.body.data.find((c) => c.name === 'postgres')!.id };
});

const PAGES = () => [
  { key: 'approvals', path: '/app/approvals' },
  { key: 'approval', path: `/app/approvals/${ids.single}` },
  { key: 'approval-foureyes', path: `/app/approvals/${ids.four}` },
  { key: 'policies', path: '/app/policies' },
  { key: 'policy-editor', path: `/app/policies/${ids.policy}?v=2` },
  { key: 'policy-versions', path: `/app/policies/${ids.policy}?tab=versions` },
  { key: 'connectors', path: '/app/connectors' },
  { key: 'connector-new', path: '/app/connectors/new' },
  { key: 'connector-new-configure', path: '/app/connectors/new?step=configure&type=postgres' },
  { key: 'connector-postgres', path: `/app/connectors/${ids.pg}` },
  { key: 'settings-general', path: '/app/settings/general' },
  { key: 'settings-members', path: '/app/settings/members' },
  { key: 'settings-keys', path: '/app/settings/keys' },
  { key: 'settings-approvals', path: '/app/settings/approvals' },
  { key: 'settings-webhooks', path: '/app/settings/webhooks' },
  { key: 'settings-seal-keys', path: '/app/settings/seal-keys' },
  { key: 'settings-notifications', path: '/app/settings/notifications' },
  { key: 'settings-danger', path: '/app/settings/danger' },
];

for (const theme of THEMES) {
  for (const [w, h] of SIZES) {
    test(`screens · ${w} · ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await prep(page, theme, w, h);
      for (const p of PAGES()) {
        await page.goto(p.path);
        await settle(page);
        await shot(page, `${p.key}-${w}-${theme}`);
      }
    });
  }
}

for (const theme of THEMES) {
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`moments · ${w} · ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await prep(page, theme, w, h);
      // the approve and deny sheets
      await page.goto(`/app/approvals/${ids.single}`);
      await settle(page);
      await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
      await page.getByRole('dialog').getByLabel(/Why is this safe/).fill('Customer confirmed by phone');
      await settle(page, 300);
      await shot(page, `approve-sheet-${w}-${theme}`);
      await page.keyboard.press('Escape');
      await page.locator('.apv-bar').getByRole('button', { name: 'Deny' }).click();
      await settle(page, 300);
      await shot(page, `deny-sheet-empty-${w}-${theme}`);
      await page.getByRole('dialog').getByLabel(/Reason/).fill('Refund already issued by phone');
      await shot(page, `deny-sheet-${w}-${theme}`);
      await page.keyboard.press('Escape');
      // the editor with a diagnostic and its fix
      await page.goto(`/app/policies/${ids.policy}?v=2`);
      await settle(page);
      const ta = page.locator('textarea.pe-input, .pe textarea').first();
      const src = await ta.inputValue();
      const at = src.indexOf('    decision: deny\n    reason: DROP is never allowed');
      await ta.evaluate((el: HTMLTextAreaElement, pos: number) => { el.focus(); el.setSelectionRange(pos + 4, pos + 12); }, at);
      await page.keyboard.type('desicion');
      await page.locator('.pe-diags').getByText('Unknown key').waitFor();
      await page.evaluate(() => document.querySelector('.pe-diags')?.scrollIntoView({ block: 'end' }));
      await settle(page, 300);
      await shot(page, `policy-diagnostic-${w}-${theme}`);
      // the dry-run trace
      await page.goto(`/app/policies/${ids.policy}?tab=dry-run&v=2`);
      await settle(page);
      await page.getByLabel('Tool').fill('postgres.execute');
      await page.getByLabel('SQL', { exact: true }).fill('DELETE FROM public.orders');
      await page.getByRole('button', { name: 'Run dry run' }).click();
      await page.locator('.dr-result').getByText('block-unscoped-writes').first().waitFor();
      await settle(page, 300);
      await shot(page, `policy-dryrun-${w}-${theme}`, w === 1440);
      // tests
      await page.goto(`/app/policies/${ids.policy}?tab=tests&v=2`);
      await settle(page);
      await page.getByLabel('Cases (YAML)').fill(readFileSync('../examples/demo/policies/acme-demo.cases.yaml', 'utf8'));
      await page.getByRole('button', { name: 'Run tests' }).click();
      await page.locator('#pp-tests').getByText(/cases passed|failed/).first().waitFor();
      await settle(page, 300);
      await shot(page, `policy-tests-${w}-${theme}`);
      // the enforce sheet, name half typed (the button stays disabled)
      await page.goto(`/app/policies/${ids.policy}?tab=versions`);
      await settle(page);
      const enforce = page.getByRole('button', { name: /Enforce v1 again/ }).first();
      if (await enforce.count()) {
        await enforce.click();
        const dlg = page.getByRole('alertdialog');
        await dlg.getByLabel(/Reason/).fill('Roll back while we review the refund rule');
        await dlg.getByLabel(/to confirm/).fill('acme-dem');
        await settle(page, 300);
        await shot(page, `enforce-sheet-${w}-${theme}`);
        await page.keyboard.press('Escape');
      }
      // the readiness report, live
      await page.goto(`/app/connectors/${ids.pg}`);
      await settle(page);
      await page.getByRole('button', { name: /Run (test|check again)/ }).click();
      await page.locator('.readiness').waitFor();
      await settle(page, 300);
      await shot(page, `readiness-${w}-${theme}`, w === 1440);
    });
  }
}

test('readiness with a ✕ row (a routed answer: wal_level=replica on a cluster that is not set up)', async ({ page }) => {
  for (const theme of THEMES) {
    for (const [w, h] of [[360, 780], [1440, 900]] as const) {
      await prep(page, theme, w, h);
      await page.route('**/v1/connectors/*/test', async (r) => {
        const real = await r.fetch();
        const body = await real.json();
        body.status = 'fail';
        body.checks = body.checks.map((c: Record<string, unknown>) => c.id === 'wal_level'
          ? { ...c, status: 'fail', detail: 'replica · logical decoding is off', fix: 'Set wal_level = logical and restart PostgreSQL. On Amazon RDS: set rds.logical_replication = 1 in the DB parameter group, then reboot.', fix_sql: 'ALTER SYSTEM SET wal_level = logical;\n-- then restart PostgreSQL' }
          : c);
        await r.fulfill({ response: real, json: body });
      });
      await page.goto(`/app/connectors/${ids.pg}`);
      await settle(page);
      await page.getByRole('button', { name: /Run (test|check again)/ }).click();
      await page.locator('.readiness').waitFor();
      await page.locator('[data-check="wal_level"]').scrollIntoViewIfNeeded();
      await settle(page, 300);
      await shot(page, `readiness-fail-${w}-${theme}`);
      await page.unroute('**/v1/connectors/*/test');
    }
  }
});

for (const theme of THEMES) {
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`states · ${w} · ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await prep(page, theme, w, h);
      const screens = [
        { key: 'approvals', path: '/app/approvals', api: '**/v1/approvals?*' },
        { key: 'policies', path: '/app/policies', api: '**/v1/policies?*' },
        { key: 'connectors', path: '/app/connectors', api: '**/v1/connectors?*' },
        { key: 'settings-members', path: '/app/settings/members', api: '**/v1/org/members*' },
      ];
      for (const s of screens) {
        await page.route(s.api, async (r) => { await new Promise((x) => setTimeout(x, 4000)); await r.continue().catch(() => undefined); });
        await page.goto(s.path, { waitUntil: 'domcontentloaded' });
        await page.waitForTimeout(700);
        await shot(page, `${s.key}-loading-${w}-${theme}`);
        await page.unroute(s.api);
        await page.route(s.api, (r) => r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { type: 'api_error', code: 'internal_error', message: 'Something failed on our side. Try again in a moment.', param: null, request_id: 'req_01m42zzzzzzzzzzzzzzzzzzzzz', doc_url: 'https://docs.deedglass.com/errors/internal_error' } }) }));
        await page.goto(s.path);
        await settle(page);
        await shot(page, `${s.key}-error-${w}-${theme}`);
        await page.unroute(s.api);
        if (s.key !== 'settings-members') {
          await page.route(s.api, (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ object: 'list', data: [], has_more: false, next_cursor: null }) }));
          await page.goto(s.path);
          await settle(page);
          await shot(page, `${s.key}-empty-${w}-${theme}`);
          await page.unroute(s.api);
        }
      }
    });
  }
}

test.describe('viewer', () => {
  test.use({ storageState: AUTH('viewer') });
  for (const [w, h] of [[360, 780], [1440, 900]] as const) {
    test(`viewer · ${w}`, async ({ page }) => {
      await prep(page, 'dark', w, h);
      for (const [key, path] of [['approval', `/app/approvals/${ids.single}`], ['policy-editor', `/app/policies/${ids.policy}?v=2`], ['connector-postgres', `/app/connectors/${ids.pg}`], ['settings-keys', '/app/settings/keys'], ['settings-general', '/app/settings/general']] as const) {
        await page.goto(path);
        await settle(page);
        await shot(page, `${key}-viewer-${w}-dark`);
      }
    });
  }
});

test('reduced motion, keyboard focus, and the phone crop', async ({ page }) => {
  await prep(page, 'dark', 390, 844);
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' });
  await page.goto('/app/approvals');
  await settle(page);
  await shot(page, 'approvals-390-dark-reduced-motion');
  const card = page.locator(`.apv-card[data-id="${ids.single}"]`);
  await card.screenshot({ path: `${DIR}/approval-card-390-crop.png` });
  await card.click();
  await settle(page);
  await shot(page, 'approval-390-dark');
  await page.locator('.apv-bar').getByRole('button', { name: 'Approve' }).click();
  await page.getByRole('dialog').getByLabel(/Why is this safe/).fill('Customer confirmed by phone');
  await settle(page, 300);
  await shot(page, 'approvals-390-sheet');
  await page.keyboard.press('Escape');
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`/app/approvals/${ids.single}`);
  await settle(page);
  for (let i = 0; i < 12; i++) await page.keyboard.press('Tab');
  await shot(page, 'focus-approvals-1440-dark');
  await page.goto(`/app/policies/${ids.policy}?v=2`);
  await settle(page);
  for (let i = 0; i < 14; i++) await page.keyboard.press('Tab');
  await shot(page, 'focus-policy-1440-dark');
  for (const f of ['approvals-390-sheet', 'approval-card-390-crop', 'approval-390-dark']) copyFileSync(`${DIR}/${f}.png`, `${PICKS}/${f}.png`);
});
