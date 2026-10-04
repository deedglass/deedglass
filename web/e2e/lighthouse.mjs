// Lighthouse 13 on the Console (S24 §6, S25 §6, S26 §6): /login, /app/_kit, the seven S25 pages and the seven S26 pages (with the
// real L2 hero, a real deed and a real agent), desktop and mobile. Playwright
// launches the installed Chrome with a remote-debugging port in a persistent profile, signs in with a
// real link, and Lighthouse runs on the same browser (storage kept, so the session holds). Writes the
// JSON reports to LIGHTHOUSE_DIR and fails when accessibility or best practices is below 95.
// Run through scripts/web_e2e.sh lighthouse (make web-lighthouse).
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
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
const hero = JSON.parse(readFileSync(join(process.env.WEB_E2E_WORK ?? '.', 'fixtures.json'), 'utf8'));
const plan = [['/login', false], ['/app', true], ['/app/_kit', true], ['/app/actions', true], [`/app/actions/${hero.mismatch}`, true], ['/app/deeds', true], ['DEED', true], ['/app/agents', true], ['AGENT', true],
  // S26: the seven control pages (a pending approval, the acme-demo policy, the demo Postgres connector);
  // S27: the policy's Replay tab (it opens on the latest replay)
  ['/app/approvals', true], ['APPROVAL', true], ['/app/policies', true], ['POLICY', true], ['POLICY_REPLAY', true], ['/app/connectors', true], ['CONNECTOR', true], ['/app/settings/members', true]];
const rows = [];
let failed = 0;
let signedIn = false;
// LIGHTHOUSE_ONLY=POLICY_REPLAY,/login runs just those entries (a session's own page, cheaply)
const only = process.env.LIGHTHOUSE_ONLY ? process.env.LIGHTHOUSE_ONLY.split(',') : null;
for (let [p, needsSession] of plan) {
  if (only && !only.includes(p)) continue;
  if (needsSession && !signedIn) {
    await page.goto(link);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.waitForURL(/\/app\/?$/);
    signedIn = true;
  }
  if (['DEED', 'AGENT', 'APPROVAL', 'POLICY', 'POLICY_REPLAY', 'CONNECTOR'].includes(p)) {
    const r = await page.evaluate(async ([kind, act]) => {
      const h = { 'Deedglass-Version': '2026-10-01' };
      const get = async (u) => (await fetch(u, { headers: h })).json();
      if (kind === 'DEED') return (await get(`/v1/actions/${act}`)).deed;
      if (kind === 'AGENT') return (await get('/v1/agents?limit=1')).data[0].id;
      if (kind === 'APPROVAL') return (await get('/v1/approvals?status=pending&limit=1')).data[0]?.id ?? null;
      if (kind === 'POLICY' || kind === 'POLICY_REPLAY') return (await get('/v1/policies')).data.find((x) => x.name === 'acme-demo')?.id ?? null;
      return (await get('/v1/connectors')).data.find((x) => x.name === 'postgres')?.id ?? null;
    }, [p, hero.mismatch]);
    if (!r) { console.log(`lighthouse: no ${p.toLowerCase()} to open; skipped`); continue; }
    const base = { DEED: 'deeds', AGENT: 'agents', APPROVAL: 'approvals', POLICY: 'policies', POLICY_REPLAY: 'policies', CONNECTOR: 'connectors' }[p];
    p = `/app/${base}/${r}${p === 'POLICY_REPLAY' ? '?tab=replay' : ''}`;
  }
  for (const formFactor of ['desktop', 'mobile']) {
    const flags = { port: PORT, output: 'json', logLevel: 'error', disableStorageReset: true, onlyCategories: ['accessibility', 'best-practices', 'performance'] };
    const r = await lighthouse(BASE + p, flags, formFactor === 'desktop' ? desktopConfig : undefined);
    const lhr = r.lhr;
    const score = (k) => Math.round((lhr.categories[k]?.score ?? 0) * 100);
    const row = { page: p, formFactor, accessibility: score('accessibility'), bestPractices: score('best-practices'), performance: score('performance'),
      lcpMs: Math.round(lhr.audits['largest-contentful-paint']?.numericValue ?? 0), cls: lhr.audits['cumulative-layout-shift']?.numericValue ?? 0 };
    rows.push(row);
    const [path, query] = p.split('?');
    const name = `${path === '/app' ? 'app' : path.replace(/^\/(app\/)?_?/, '').replace(/\//g, '-').replace(/-(act|deed|agt|apr|pol|con)_[0-9a-z]+$/, '-$1')}${query ? '-' + query.replace(/^tab=/, '') : ''}-${formFactor}.json`;
    writeFileSync(join(OUT, name), JSON.stringify(lhr, null, 1));
    if (row.accessibility < 95 || row.bestPractices < 95) {
      failed++;
      const bad = Object.values(lhr.audits).filter((a) => a.score !== null && a.score < 1 && a.scoreDisplayMode === 'binary').map((a) => a.id);
      console.error(`✕ ${p} ${formFactor}: accessibility ${row.accessibility}, best practices ${row.bestPractices} · ${bad.join(', ')}`);
    }
  }
}
// S28: Insights on the simulated org (acme-sim, scripts/web_e2e.sh), signed in as Priya, in the
// dark and the light theme (the theme lives in localStorage, which disableStorageReset keeps).
const simFile = join(process.env.WEB_E2E_WORK ?? '.', 'sim.json');
let simReady = false;
try { readFileSync(simFile); simReady = true; } catch { console.log('lighthouse: no simulated org; Insights skipped'); }
if (simReady && (!only || only.some((x) => x.startsWith('INSIGHTS')))) {
  const simLink = JSON.parse(execFileSync(process.env.WEB_E2E_DG ?? '../bin/deedglass', ['org', 'login-link', '--email', 'priya.raman@acme.example', '--org', 'acme-sim', '--json'],
    { cwd: process.env.WEB_E2E_WORK, encoding: 'utf8', env: process.env })).link;
  await page.goto(simLink);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/\/app\/?$/);
  for (const theme of ['dark', 'light']) {
    await page.evaluate((t) => localStorage.setItem('deedglass.theme', t), theme);
    for (const formFactor of ['desktop', 'mobile']) {
      const flags = { port: PORT, output: 'json', logLevel: 'error', disableStorageReset: true, onlyCategories: ['accessibility', 'best-practices', 'performance'] };
      const r = await lighthouse(BASE + '/app/insights', flags, formFactor === 'desktop' ? desktopConfig : undefined);
      const lhr = r.lhr;
      const score = (k) => Math.round((lhr.categories[k]?.score ?? 0) * 100);
      const row = { page: `/app/insights (${theme})`, formFactor, accessibility: score('accessibility'), bestPractices: score('best-practices'), performance: score('performance'),
        lcpMs: Math.round(lhr.audits['largest-contentful-paint']?.numericValue ?? 0), cls: lhr.audits['cumulative-layout-shift']?.numericValue ?? 0 };
      rows.push(row);
      writeFileSync(join(OUT, `insights-${theme}-${formFactor}.json`), JSON.stringify(lhr, null, 1));
      if (row.accessibility < 95 || row.bestPractices < 95) {
        failed++;
        const bad = Object.values(lhr.audits).filter((a) => a.score !== null && a.score < 1 && a.scoreDisplayMode === 'binary').map((a) => a.id);
        console.error(`✕ /app/insights ${theme} ${formFactor}: accessibility ${row.accessibility}, best practices ${row.bestPractices} · ${bad.join(', ')}`);
      }
    }
  }
}
await ctx.close();
rmSync(profile, { recursive: true, force: true });
console.log('page                          form     a11y  best-pr  perf  LCP ms  CLS');
for (const r of rows) console.log(`${r.page.replace(/(act|deed|agt)_[0-9a-z]+$/, '$1_…').padEnd(26)} ${r.formFactor.padEnd(8)} ${String(r.accessibility).padStart(4)}  ${String(r.bestPractices).padStart(7)}  ${String(r.performance).padStart(4)}  ${String(r.lcpMs).padStart(6)}  ${r.cls.toFixed(3)}`);
writeFileSync(join(OUT, 'summary.json'), JSON.stringify(rows, null, 2));
if (failed) process.exit(1);
console.log(`✓ accessibility and best practices ≥ 95 on ${rows.length} runs · reports in ${OUT}`);
