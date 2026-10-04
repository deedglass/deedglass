#!/usr/bin/env node
// og.mjs: the docs' Open Graph images (brand §13.7), made once and committed to public/og/:
// 1200×630, ink-950, the mark top-left, the title in Inter 64/600 snow-text, the subtitle in
// slate-400, an aurora glow bottom-right at 30%. Rendered with Playwright from web/node_modules.
// Re-run after a copy change: node scripts/og.mjs
import { createRequire } from 'node:module';
import { mkdirSync, readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const site = join(fileURLToPath(new URL('.', import.meta.url)), '..');
const require = createRequire(new URL('../../web/package.json', import.meta.url));
const { chromium } = require('@playwright/test');
export const SECTIONS = {
  default: ['Deedglass docs', 'See what your agents actually did.'],
  'get-started': ['Get started', 'From an empty directory to a sealed deed in five minutes.'],
  concepts: ['Concepts', 'The Gate, the Witness, deeds and the deed chain.'],
  guides: ['Guides', 'Block, witness, approve and undo, step by step.'],
  reference: ['Reference', 'The CLI, the REST API, policies, connectors and settings.'],
  errors: ['Errors', 'Every code: what happened, the exact envelope, the next step.'],
  security: ['Security', 'The threat model, the limits and the attack suite results.'],
};
const font = pathToFileURL(join(site, 'node_modules/@fontsource-variable/inter/files/inter-latin-wght-normal.woff2')).href;
const mark = readFileSync(join(site, 'public/favicon.svg'), 'utf8').replace(/<style>.*<\/style>/, '<style>.r{fill:#E6EAF5;fill-opacity:.9}.p{fill:#FFFFFF}</style>');
const page = (t, s) => `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:Inter;src:url(${font}) format('woff2');font-weight:100 900}
html,body{margin:0;width:1200px;height:630px;background:#07080C;overflow:hidden;font-family:Inter}
.glow{position:absolute;right:-180px;bottom:-220px;width:620px;height:520px;border-radius:50%;background:linear-gradient(135deg,#7C6CFF,#4F8CFF 50%,#2FE6D1);filter:blur(40px);opacity:.3}
.mark{position:absolute;left:80px;top:72px;display:flex;align-items:center;gap:18px;color:#F5F6FA;font-size:34px;font-weight:600;letter-spacing:-.04em}
.mark svg{width:56px;height:56px}
h1{position:absolute;left:80px;top:250px;margin:0;color:#F5F6FA;font-size:64px;font-weight:600;letter-spacing:-.035em;line-height:1.05;max-width:960px}
p{position:absolute;left:80px;top:350px;margin:0;color:#8B90A0;font-size:30px;line-height:1.35;max-width:900px}
.url{position:absolute;left:80px;bottom:64px;color:#8B90A0;font-size:22px;letter-spacing:.01em}
</style><div class="glow"></div><div class="mark">${mark}<span>deedglass</span></div><h1>${t}</h1><p>${s}</p><div class="url">docs.deedglass.com</div>`;
mkdirSync(join(site, 'public/og'), { recursive: true });
const browser = await chromium.launch({ channel: 'chrome' });
const ctx = await browser.newContext({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
const p = await ctx.newPage();
const dir = mkdtempSync(join(tmpdir(), 'dg-og-'));
for (const [k, [t, s]] of Object.entries(SECTIONS)) {
  writeFileSync(join(dir, `${k}.html`), page(t, s));
  await p.goto(pathToFileURL(join(dir, `${k}.html`)).href);
  await p.waitForTimeout(150);
  await p.screenshot({ path: join(site, 'public/og', `${k}.png`) });
  console.log(`public/og/${k}.png`);
}
await browser.close();
