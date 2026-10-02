// Lighthouse 13 on the Console (S24 §6): /app, /app/_kit and /login, desktop and mobile. Playwright
// launches the installed Chrome with a remote-debugging port in a persistent profile, signs in with a
// real link, and Lighthouse runs on the same browser (storage kept, so the session holds). Writes the
// JSON reports to LIGHTHOUSE_DIR and fails when accessibility or best practices is below 95.
// Run through scripts/web_e2e.sh lighthouse (make web-lighthouse).
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from '@playwright/test';
import lighthouse from 'lighthouse';
import desktopConfig from 'lighthouse/core/config/desktop-config.js';

const BASE = process.env.WEB_E2E_BASE ?? 'http://127.0.0.1:18241';
// scripts/web_e2e.sh sets LIGHTHOUSE_DIR ($HOME/txv/ops/plan/prompts-v7/reports/lighthouse/S24).
const OUT = process.env.LIGHTHOUSE_DIR ?? join(process.cwd(), 'test-results', 'lighthouse');
const PORT = 9341;
mkdirSync(OUT, { recursive: true });

const link = JSON.parse(execFileSync(process.env.WEB_E2E_DG ?? '../bin/deedglass', ['org', 'login-link', '--email', 'owner@acme.example', '--org', 'acme', '--json'],
  { cwd: process.env.WEB_E2E_WORK, encoding: 'utf8', env: process.env })).link;

const profile = mkdtempSync(join(tmpdir(), 'dg-lh-'));
const ctx = await chromium.launchPersistentContext(profile, { channel: 'chrome', headless: true, args: [`--remote-debugging-port=${PORT}`] });
const page = ctx.pages()[0] ?? (await ctx.newPage());
await page.goto(BASE + '/login'); // warm the browser once, signed out

// /login first, signed out (signed in, it hands over to the Console); then sign in for the rest.
const plan = [['/login', false], ['/app', true], ['/app/_kit', true]];
const rows = [];
let failed = 0;
let signedIn = false;
for (const [p, needsSession] of plan) {
  if (needsSession && !signedIn) {
    await page.goto(link);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.waitForURL(/\/app\/?$/);
    signedIn = true;
  }
  for (const formFactor of ['desktop', 'mobile']) {
    const flags = { port: PORT, output: 'json', logLevel: 'error', disableStorageReset: true, onlyCategories: ['accessibility', 'best-practices', 'performance'] };
    const r = await lighthouse(BASE + p, flags, formFactor === 'desktop' ? desktopConfig : undefined);
    const lhr = r.lhr;
    const score = (k) => Math.round((lhr.categories[k]?.score ?? 0) * 100);
    const row = { page: p, formFactor, accessibility: score('accessibility'), bestPractices: score('best-practices'), performance: score('performance'),
      lcpMs: Math.round(lhr.audits['largest-contentful-paint']?.numericValue ?? 0), cls: lhr.audits['cumulative-layout-shift']?.numericValue ?? 0 };
    rows.push(row);
    const name = `${p === '/app' ? 'app' : p.replace(/^\/(app\/)?_?/, '').replace(/\//g, '-')}-${formFactor}.json`;
    writeFileSync(join(OUT, name), JSON.stringify(lhr, null, 1));
    if (row.accessibility < 95 || row.bestPractices < 95) {
      failed++;
      const bad = Object.values(lhr.audits).filter((a) => a.score !== null && a.score < 1 && a.scoreDisplayMode === 'binary').map((a) => a.id);
      console.error(`✕ ${p} ${formFactor}: accessibility ${row.accessibility}, best practices ${row.bestPractices} · ${bad.join(', ')}`);
    }
  }
}
await ctx.close();
rmSync(profile, { recursive: true, force: true });
console.log('page            form     a11y  best-pr  perf  LCP ms  CLS');
for (const r of rows) console.log(`${r.page.padEnd(15)} ${r.formFactor.padEnd(8)} ${String(r.accessibility).padStart(4)}  ${String(r.bestPractices).padStart(7)}  ${String(r.performance).padStart(4)}  ${String(r.lcpMs).padStart(6)}  ${r.cls.toFixed(3)}`);
writeFileSync(join(OUT, 'summary.json'), JSON.stringify(rows, null, 2));
if (failed) process.exit(1);
console.log(`✓ accessibility and best practices ≥ 95 on ${rows.length} runs · reports in ${OUT}`);
