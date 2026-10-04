#!/usr/bin/env node
// lighthouse.mjs (S33 §3.6): Lighthouse 13 (web/'s pinned copy, ADR-053) on the six docs pages,
// desktop and mobile, against dist/ served locally with the production headers. Fails when
// accessibility or best practices < 95, performance < 90, or CLS > 0. JSON reports go to
// LIGHTHOUSE_DIR (default the ops repo's plan/prompts-v7/reports/lighthouse/S33).
import { createRequire } from 'node:module';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { serve } from './serve-dist.mjs';

const web = new URL('../../web/', import.meta.url);
const require = createRequire(new URL('package.json', web));
const { chromium } = require('@playwright/test');
const lighthouse = (await import(pathToFileURL(require.resolve('lighthouse')).href)).default;
const desktopConfig = (await import(pathToFileURL(require.resolve('lighthouse/core/config/desktop-config.js')).href)).default;

const OUT = process.env.LIGHTHOUSE_DIR ?? join(fileURLToPath(new URL('../../..', import.meta.url)), 'ops/plan/prompts-v7/reports/lighthouse/S33');
mkdirSync(OUT, { recursive: true });
const PAGES = (process.env.LIGHTHOUSE_PAGES ?? '/,/quickstart/,/concepts/verification-levels/,/errors/policy_denied/,/reference/api/,/security/attack-suite/').split(',');
const server = await serve(join(fileURLToPath(new URL('..', import.meta.url)), 'dist'));
const base = `http://127.0.0.1:${server.address().port}`;
const PORT = 9351;
const ctx = await chromium.launchPersistentContext(mkdtempSync(join(tmpdir(), 'dg-lh-')), { channel: 'chrome', headless: true, args: [`--remote-debugging-port=${PORT}`] });
const rows = [];
let failed = 0;
for (const p of PAGES) {
  for (const ff of ['desktop', 'mobile']) {
    const r = await lighthouse(base + p, { port: PORT, output: 'json', logLevel: 'error', onlyCategories: ['accessibility', 'best-practices', 'performance'] }, ff === 'desktop' ? desktopConfig : undefined);
    const lhr = r.lhr;
    const score = (k) => Math.round((lhr.categories[k]?.score ?? 0) * 100);
    const row = { page: p, ff, a11y: score('accessibility'), bp: score('best-practices'), perf: score('performance'), cls: +(lhr.audits['cumulative-layout-shift']?.numericValue ?? 0).toFixed(3), lcp: Math.round(lhr.audits['largest-contentful-paint']?.numericValue ?? 0) };
    rows.push(row);
    writeFileSync(join(OUT, `${p === '/' ? 'home' : p.replace(/^\/|\/$/g, '').replace(/\//g, '-')}-${ff}.json`), JSON.stringify(lhr));
    const bad = row.a11y < 95 || row.bp < 95 || row.perf < 90 || row.cls > 0;
    if (bad) {
      failed++;
      const audits = Object.values(lhr.audits).filter((a) => a.score !== null && a.score < 1 && ['binary', 'numeric'].includes(a.scoreDisplayMode) && a.details).map((a) => a.id).slice(0, 12);
      console.error(`✕ ${p} ${ff}: a11y ${row.a11y} · bp ${row.bp} · perf ${row.perf} · CLS ${row.cls} · ${audits.join(', ')}`);
    }
  }
}
await ctx.close();
server.close();
console.log('| Page | Form factor | Accessibility | Best practices | Performance | CLS | LCP ms |\n|---|---|---|---|---|---|---|');
for (const r of rows) console.log(`| ${r.page} | ${r.ff} | ${r.a11y} | ${r.bp} | ${r.perf} | ${r.cls} | ${r.lcp} |`);
writeFileSync(join(OUT, 'summary.json'), JSON.stringify(rows, null, 1));
if (failed) process.exit(1);
