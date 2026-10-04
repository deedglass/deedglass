import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { expect, test as base, type Page } from '@playwright/test';

export const AUTH = (role: 'owner' | 'admin' | 'viewer') => `e2e/.auth/${role}.json`;
export const EMAIL = { owner: 'owner@acme.example', admin: 'priya.raman@acme.example', viewer: 'sam.okafor@acme.example' } as const;

/** dg runs the real CLI in the stack's working directory (DEEDGLASS_* come from web_e2e.sh). */
export function dg(...args: string[]): string {
  return execFileSync(process.env.WEB_E2E_DG ?? '../bin/deedglass', args, { cwd: process.env.WEB_E2E_WORK, encoding: 'utf8', env: process.env });
}

export function loginLink(email: string): string {
  return JSON.parse(dg('org', 'login-link', '--email', email, '--org', 'acme', '--json')).link as string;
}

/**
 * guard records CSP violations and console errors on a page; the test asserts zero at the end.
 * (Trusted Types 'none' turns any HTML-string sink into a violation here.)
 */
export async function guard(page: Page): Promise<() => Promise<void>> {
  const errors: string[] = [];
  page.on('console', (m) => {
    if (m.type() === 'error' && !/Failed to load resource: the server responded with a status of 40[134]/.test(m.text())) errors.push(m.text());
  });
  page.on('pageerror', (e) => errors.push(e.message));
  await page.addInitScript(() => {
    (window as unknown as { __csp: string[] }).__csp = [];
    document.addEventListener('securitypolicyviolation', (e) => (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  return async () => {
    const csp = await page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []).catch(() => []);
    expect(csp, 'CSP violations').toEqual([]);
    expect(errors, 'console errors').toEqual([]);
  };
}

export const test = base;
export { expect };

/** hero is the real L2 pair scripts/web_e2e.sh seeded (fixtures.json in the stack's directory). */
export function hero(): { verified: string; mismatch: string } {
  return JSON.parse(readFileSync(`${process.env.WEB_E2E_WORK}/fixtures.json`, 'utf8'));
}

/** loginAs signs a fresh context in through a real link for an org (first run uses acme-staging). */
export async function signIn(page: Page, email: string, org = 'acme'): Promise<void> {
  const link = JSON.parse(dg('org', 'login-link', '--email', email, '--org', org, '--json')).link as string;
  await page.goto(link);
  await page.getByRole('button', { name: /^(Sign in|Accept invite)$/ }).click();
  await page.waitForURL(/\/app\/?$/);
}

// ---------- S26 helpers: real approvals created through the API with the seeded agent key ----------

export interface Seeds { verified: string; mismatch: string; adminKey: string; agentKey: string }

export function seeds(): Seeds {
  return JSON.parse(readFileSync(`${process.env.WEB_E2E_WORK}/fixtures.json`, 'utf8'));
}

const apiBase = () => process.env.WEB_E2E_BASE ?? 'http://127.0.0.1:18241';

/** apiAs calls the API with a bearer key (Node fetch, outside the browser). */
export async function apiAs<T = Record<string, unknown>>(key: string, method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<{ status: number; body: T }> {
  const r = await fetch(apiBase() + path, {
    method,
    headers: { Authorization: `Bearer ${key}`, 'Deedglass-Version': '2026-10-01', ...(body ? { 'Content-Type': 'application/json' } : {}), ...(method === 'POST' ? { 'Idempotency-Key': crypto.randomUUID() } : {}), ...headers },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: r.status, body: (await r.json().catch(() => ({}))) as T };
}

let refundSeq = 20_000 + Math.floor(Math.random() * 9_000) * 10;

/**
 * pendingRefund makes support-bot ask to INSERT a refund (one approver, L2) or, with fourEyes,
 * to UPDATE an existing refund (two approvers), and returns the action and approval ids.
 */
export async function pendingRefund(o: { fourEyes?: boolean; onBehalfOf?: string; cents?: number } = {}): Promise<{ action: string; approval: string; refund: number }> {
  const id = ++refundSeq;
  const sql = o.fourEyes
    ? `UPDATE public.refunds SET reason = 'Customer called back (${id})' WHERE id = ${1 + (id % 50)}`
    : `INSERT INTO public.refunds (id, order_id, amount_cents, reason, created_at) VALUES (${id}, 42, ${o.cents ?? 12000}, 'Damaged in transit', '2026-10-04 10:00:00+00')`;
  const r = await apiAs<{ id: string; approval?: { id: string }; status: string }>(seeds().agentKey, 'POST', '/v1/actions', { tool: 'postgres.execute', args: { sql } },
    { 'Deedglass-On-Behalf-Of': o.onBehalfOf ?? 'u_4821', 'Deedglass-Session': 'chat_77' });
  if (r.status !== 202 || !r.body.approval) throw new Error(`expected a pending approval, got ${r.status} ${JSON.stringify(r.body).slice(0, 300)}`);
  return { action: r.body.id, approval: r.body.approval.id, refund: id };
}

// ---------- S28 helpers: the simulated org ----------

/**
 * appendQuietDays runs `deedglass dev simulate --append --days N --seed 8` on the simulated org.
 * The embedded scenario's incidents sit on days 2 to 7, and the planner refuses a window shorter
 * than its last incident day even when appending (reported to the backend session), so the append
 * uses a copy of examples/sim/acme.yaml with the incidents removed: the same agents, tools and
 * policies, just ordinary traffic.
 */
export function appendQuietDays(slug: string, days = 2): void {
  const src = `${process.cwd()}/../examples/sim`;
  const dir = `${process.env.WEB_E2E_WORK}/sim-quiet`;
  mkdirSync(dir, { recursive: true });
  for (const f of ['acme-sim.v1.yaml', 'acme-sim.v2.yaml']) copyFileSync(`${src}/${f}`, `${dir}/${f}`);
  const yaml = readFileSync(`${src}/acme.yaml`, 'utf8');
  const i = yaml.indexOf('\nincidents:');
  writeFileSync(`${dir}/acme.yaml`, (i >= 0 ? yaml.slice(0, i) : yaml) + '\nincidents: []\n');
  try {
    dg('dev', 'simulate', '--org-slug', slug, '--append', '--days', String(days), '--seed', '8', '--no-color');
  } catch {
    dg('dev', 'simulate', '--org-slug', slug, '--append', '--days', String(days), '--seed', '8', '--scenario', `${dir}/acme.yaml`, '--no-color');
  }
}
