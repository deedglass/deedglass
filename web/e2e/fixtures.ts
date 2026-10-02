import { execFileSync } from 'node:child_process';
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
