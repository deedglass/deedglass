// S26 §3.5 roles sweep, generated from the role matrix: for each role, the UI state (hidden or
// disabled) AND the matching API call made from the page's own session, expecting the documented
// status. Calls that would change the org use a body the route rejects after authorization (a 400 or
// 409 shows the role passed; 403 shows it didn't), so the sweep never stops agents or edits data.
import type { Page } from '@playwright/test';
import { AUTH, expect, guard, pendingRefund, seeds, apiAs, test } from './fixtures.ts';

type Role = 'viewer' | 'admin' | 'owner';

interface Cell {
  name: string;
  method: string;
  path: (ctx: Ctx) => string;
  body: (ctx: Ctx, role: Role) => unknown;
  want: Record<Role, number | number[]>;
}

interface Ctx { approval: string; digest: string; policy: string; policySource: string; member: string; tz: string; retention: number }

const MATRIX: Cell[] = [
  { name: 'approve (rule approvers: role:admin)', method: 'POST', path: (c) => `/v1/approvals/${c.approval}/approve`, body: (c, role) => ({ reason: '', context_digest: role === 'viewer' ? c.digest : 'sha256:' + '0'.repeat(64) }), want: { viewer: 403, admin: 409, owner: 409 } }, // a stale digest shows an admin passed the role check without deciding
  { name: 'policy save', method: 'POST', path: (c) => `/v1/policies/${c.policy}/versions`, body: (c) => ({ source: c.policySource }), want: { viewer: 403, admin: [200, 201], owner: [200, 201] } },
  { name: 'policy dry run', method: 'POST', path: () => '/v1/policies/evaluate', body: () => ({ policy: 'active', input: { tool: 'fake.echo' } }), want: { viewer: 200, admin: 200, owner: 200 } },
  { name: 'connector create', method: 'POST', path: () => '/v1/connectors', body: () => ({ type: 'rest', name: 'Bad Name', config: {} }), want: { viewer: 403, admin: 400, owner: 400 } },
  { name: 'member invite', method: 'POST', path: () => '/v1/org/members', body: () => ({ email: 'not-an-email', role: 'viewer' }), want: { viewer: 403, admin: 400, owner: 400 } },
  { name: 'member role change', method: 'PATCH', path: (c) => `/v1/org/members/${c.member}`, body: () => ({ role: 'superuser' }), want: { viewer: 403, admin: 403, owner: 400 } },
  { name: 'org keys', method: 'GET', path: () => '/v1/org/keys', body: () => undefined, want: { viewer: 403, admin: 200, owner: 200 } },
  { name: 'retention', method: 'PATCH', path: () => '/v1/org', body: (c) => ({ retention_days: c.retention }), want: { viewer: 403, admin: 403, owner: 200 } },
  { name: 'approval settings', method: 'PATCH', path: () => '/v1/org', body: () => ({ settings: { approvals: { email_links: 'decide' } } }), want: { viewer: 403, admin: 403, owner: 200 } },
  { name: 'webhooks', method: 'GET', path: () => '/v1/webhooks', body: () => undefined, want: { viewer: 403, admin: 200, owner: 200 } },
  { name: 'stop all (no reason → 400 after the role check)', method: 'POST', path: () => '/v1/org/stop-all', body: () => ({ reason: 1 }), want: { viewer: 403, admin: 400, owner: 400 } },
  { name: 'start all', method: 'POST', path: () => '/v1/org/start-all', body: () => ({ reason: 1 }), want: { viewer: 403, admin: 403, owner: 400 } },
];

async function call(page: Page, method: string, path: string, body: unknown): Promise<number> {
  return page.evaluate(async ([m, p, b]) => {
    const me = await (await fetch('/v1/me', { headers: { 'Deedglass-Version': '2026-10-01' } })).json();
    const h: Record<string, string> = { 'Deedglass-Version': '2026-10-01' };
    if (m !== 'GET') { h['Deedglass-CSRF'] = me.csrf_token; h['Content-Type'] = 'application/json'; }
    if (m === 'POST') h['Idempotency-Key'] = crypto.randomUUID();
    const r = await fetch(p as string, { method: m as string, headers: h, body: b === undefined ? undefined : JSON.stringify(b) });
    return r.status;
  }, [method, path, body] as const);
}

let ctx: Ctx;

test.beforeAll(async () => {
  const ap = await pendingRefund({ cents: 111 });
  const pols = await apiAs<{ data: { id: string; name: string; latest_version: number }[] }>(seeds().adminKey, 'GET', '/v1/policies');
  const pol = pols.body.data.find((p) => p.name === 'acme-demo')!;
  const v = await apiAs<{ source: string }>(seeds().adminKey, 'GET', `/v1/policies/${pol.id}/versions/${pol.latest_version}`);
  const org = await apiAs<{ timezone: string; retention_days?: number }>(seeds().adminKey, 'GET', '/v1/org').catch(() => ({ body: { timezone: 'UTC', retention_days: 90 } }));
  const apd = await apiAs<{ context_digest: string }>(seeds().adminKey, 'GET', `/v1/approvals/${ap.approval}`);
  ctx = { approval: ap.approval, digest: apd.body.context_digest, policy: pol.id, policySource: v.body.source, member: '', tz: org.body.timezone ?? 'UTC', retention: org.body.retention_days ?? 90 };
});

for (const role of ['viewer', 'admin', 'owner'] as Role[]) {
  test.describe(`roles · ${role}`, () => {
    test.use({ storageState: AUTH(role) });

    test(`${role}: the API answers every cell as documented`, async ({ page }) => {
      await page.goto('/app');
      if (!ctx.member) {
        ctx.member = await page.evaluate(async () => {
          const r = await (await fetch('/v1/org/members', { headers: { 'Deedglass-Version': '2026-10-01' } })).json();
          return (r.data as { id: string; user: { email: string } }[]).find((m) => m.user.email === 'sam.okafor@acme.example')!.id;
        });
      }
      const rows: string[] = [];
      for (const cell of MATRIX) {
        const got = await call(page, cell.method, cell.path(ctx), cell.body(ctx, role));
        const want = ([] as number[]).concat(cell.want[role]);
        rows.push(`${cell.name}: ${got}`);
        expect(want, `${role} · ${cell.name} answered ${got}`).toContain(got);
      }
      test.info().annotations.push({ type: `roles ${role}`, description: rows.join(' · ') });
    });

    test(`${role}: the UI hides or disables what the role can't do`, async ({ page }) => {
      const done = await guard(page);
      const admin = role !== 'viewer';
      await page.goto('/app/policies');
      await expect(page.locator('table.dt')).toBeVisible();
      await expect(page.getByRole('button', { name: 'New policy' })).toHaveCount(admin ? 1 : 0);
      await page.goto('/app/connectors');
      await expect(page.locator('table.con-table')).toBeVisible();
      await expect(page.getByRole('link', { name: 'Add connector' })).toHaveCount(admin ? 1 : 0);
      await page.goto(`/app/approvals/${ctx.approval}`);
      if (admin) await expect(page.locator('.apv-bar').getByRole('button', { name: 'Approve' })).toBeVisible();
      else {
        await expect(page.locator('.apv-bar-note')).toBeVisible();
        await expect(page.locator('.apv-bar').getByRole('button')).toHaveCount(0);
      }
      await done();
    });
  });
}
