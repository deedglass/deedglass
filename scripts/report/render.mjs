#!/usr/bin/env node
// render.mjs (S37 §3.9): `deedglass scan --json` in, the one-page full scan report out.
//
//   node scripts/report/render.mjs --scan scan.json --out report.pdf [--sample] [--html report.html]
//
// The scan JSON is the only input: there is no notes option, and unknown flags are refused, because
// S48's `scan --full` renders the same template on the buyer's machine with no person involved.
// Every sentence, row and number comes from the scan's own fields; a field the scan didn't fill
// shows "not checked". The PDF is tagged with an outline (Playwright's page.pdf, Chrome).
import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const ALLOWED = new Set(['--scan', '--out', '--html', '--sample']);

export function parseArgs(argv) {
  const o = { sample: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const [k, inline] = a.includes('=') ? [a.slice(0, a.indexOf('=')), a.slice(a.indexOf('=') + 1)] : [a, undefined];
    if (!ALLOWED.has(k)) {
      throw new Error(`render.mjs doesn't take ${k}. It reads only the scan JSON: --scan <file> --out <pdf> [--sample] [--html <file>].`);
    }
    if (k === '--sample') { o.sample = true; continue; }
    const v = inline ?? argv[++i];
    if (!v) throw new Error(`${k} needs a value.`);
    o[k.slice(2)] = v;
  }
  if (!o.scan || !(o.out || o.html)) throw new Error('Pass --scan <scan.json> and --out <report.pdf>.');
  return o;
}

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const NC = '<span class="nc">not checked</span>';
const plural = (n, one, many) => `${n.toLocaleString('en-US')} ${n === 1 ? one : many}`;
const short = (d) => (/^sha256:[0-9a-f]{64}$/.test(d) ? `${d.slice(0, 11)}…${d.slice(-4)}` : d);

// Lucide-style 1.5 px icons on a 24 grid (brand §11).
const ICON = {
  undoNo: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-15-6.7L3 13"/><path d="M4 4l16 16"/></svg>',
  undoYes: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-15-6.7L3 13"/></svg>',
  noApproval: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="4"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 8l5 5M22 8l-5 5"/></svg>',
  routed: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6L9 17l-5-5"/></svg>',
};
const chip = (cls, icon, word) => `<span class="chip ${cls}">${ICON[icon]}${esc(word)}</span>`;

/** model turns a scan_report v1 into the report's values (pure; tested). */
export function model(scan) {
  if (scan?.object !== 'scan_report' || scan.version !== 1) throw new Error('This isn\'t a scan_report v1. Run deedglass scan --json and pass its output.');
  const pg = scan.postgres ?? null;
  const tables = pg?.tables ?? [];
  const score = scan.score ?? null;
  const unrouted = new Set((score?.deductions ?? []).filter((d) => d.id === 'unchecked_write_path').map((d) => d.subject));
  const unroutedTables = [...unrouted].filter((x) => (scan.postgres?.tables ?? []).some((t) => `${t.schema}.${t.table}` === x));
  const sum = scan.summary ?? {};
  const truncatable = tables.filter((t) => (t.can ?? []).includes('TRUNCATE'));
  const notUndoable = tables.filter((t) => !t.undo_ready);
  const tools = scan.tools ?? [];
  const writeTools = sum.write_capable_tools ?? 0;
  // The three summary sentences, computed from the rows.
  const s = [];
  if (pg) {
    let line = `Your agent can change ${plural(tables.length, 'table', 'tables')} in ${pg.database}`;
    if (writeTools > 0) line += ` and use ${plural(writeTools, 'write tool', 'write tools')}`;
    s.push(line + '.');
  } else if (writeTools > 0) {
    s.push(`Your agent can use ${plural(writeTools, 'write tool', 'write tools')}.`);
  } else {
    s.push('The scan found nothing your agent can change.');
  }
  if (truncatable.length || notUndoable.length) {
    const n = new Set([...truncatable, ...notUndoable].map((t) => t.schema + '.' + t.table)).size;
    s.push(`${plural(n, 'table', 'tables')} can lose rows in a way undo can't put back.`);
  }
  if (unrouted.size) s.push(`${plural(unrouted.size, 'write path', 'write paths')} reach the database with no policy and no person in between.`);
  const policyFor = (name) => (scan.policies ?? []).slice(0, 3).map((p, i) => (p.why?.includes(name) || p.rule === 'block-destructive-sql' ? String(i + 1) : null)).filter(Boolean);
  const rows = tables.map((t) => {
    const name = `${t.schema}.${t.table}`;
    const trig = (t.triggers ?? []).filter((x) => x.enabled).map((x) => x.name);
    const all = ['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE'].every((x) => (t.can ?? []).includes(x));
    const can = all ? 'every row' : (t.can ?? []).map((x) => x.toLowerCase()).join(', ') || 'nothing';
    let worst = (t.can ?? []).includes('TRUNCATE') ? `TRUNCATE, about ${Number(t.est_rows ?? 0).toLocaleString('en-US')} rows` :
      (t.can ?? []).includes('DELETE') ? 'DELETE without a limit' : (t.can ?? []).includes('UPDATE') ? 'UPDATE without a limit' : 'INSERT';
    if (trig.length) worst += `; UPDATE fires ${trig.join(', ')}`;
    const undo = !t.undo_ready || (t.can ?? []).includes('TRUNCATE') ? ['undo-no', 'undoNo', "Can't be undone"] : ['undo-yes', 'undoYes', 'Undo ready'];
    const today = unrouted.has(name) ? ['no-approval', 'noApproval', 'No approval'] : ['routed', 'routed', 'Routed'];
    return { name, can, worst, undo, today, add: policyFor(name) };
  });
  // MCP tools that can write, before the tables: the ones the Gate sees share one row; each one it
  // doesn't see gets its own.
  const writes = tools.filter((t) => t.side_effect === 'write');
  const seen = writes.filter((t) => t.routed).map((t) => `${t.server}.${t.tool}`);
  const toolRows = writes.filter((t) => !t.routed).map((t) => ({ name: `${t.server}.${t.tool}`, can: null, worst: null, undo: null,
    today: ['no-approval', 'noApproval', 'Not routed'], add: ['wrap'] }));
  if (seen.length) {
    const servers = [...new Set(writes.filter((t) => t.routed).map((t) => t.server))];
    toolRows.push({ name: `${plural(seen.length, 'tool', 'tools')} on ${servers.join(', ')}`, can: null, worst: null, undo: null, today: ['routed', 'routed', 'Through Deedglass'], add: ['–'] });
  }
  rows.unshift(...toolRows);
  const pols = (scan.policies ?? []).slice(0, 3).map((p, i) => {
    const y = p.yaml ?? '';
    const at = y.search(/^(rules|budgets):/m);
    // The rule's core lines; reason, notify, approval and verify lines are left to the full file.
    const core = (at >= 0 ? y.slice(at) : y).split('\n').filter((l) => /^\s*- id:|^\s+(match|when|decision|window|max_calls|per):/.test(l)).map((l) => l.replace(/^  /, ''));
    return { n: i + 1, rule: p.rule, why: p.why, yaml: core.join('\n') };
  });
  const looked = [];
  if (scan.walk) looked.push(`${plural(scan.walk.files ?? 0, 'file', 'files')} in ${scan.dir}${scan.walk.stopped ? ' (stopped early)' : ''}`);
  if (Array.isArray(scan.configs)) looked.push(`${plural(scan.configs.length, 'MCP client config', 'MCP client configs')}, ${plural(sum.mcp_servers ?? 0, 'server', 'servers')} in them`);
  if (pg) looked.push(`Postgres ${String(pg.server_version ?? '').split(' ')[0]} as ${pg.role}: grants, triggers, row security and definer functions, ${plural(tables.length + (pg.tables_read_only ?? 0), 'table', 'tables')}`);
  const notLooked = [];
  if (!pg) notLooked.push('Your database: the scan had no connection string (--postgres-dsn-env).');
  if (!tools.length) notLooked.push('MCP tools: the configs it read list no servers, so no tool was graded.');
  else notLooked.push('Each MCP server\'s own code: tools are graded from a catalog.');
  if (scan.offline) notLooked.push('Anything online: it ran with --offline.');
  notLooked.push('The rows in your tables: it never reads data.');
  notLooked.push('What your agent will do: it grades what the setup allows.');
  return { grade: score?.grade ?? null, points: score?.points ?? null, deductions: score?.deductions ?? [], s, rows, pols, looked, notLooked,
    dir: scan.dir, date: (scan.scanned_at ?? '').slice(0, 10), checked: score?.database_checked ? 'config and database checked' : 'config checked' };
}

/** fill returns the report's HTML for a scan (pure apart from reading the template). */
export function fill(scan, { sample = false, template } = {}) {
  const m = model(scan);
  const html = template ?? readFileSync(join(here, 'report.html'), 'utf8');
  const digest = 'sha256:' + createHash('sha256').update(JSON.stringify(scan)).digest('hex');
  const title = sample ? 'Full scan report · Sample, from the Deedglass demo' : 'Full scan report';
  const slots = {
    title: esc(title),
    label: sample ? '<p class="label">Sample, from the Deedglass demo</p>' : '',
    meta: [esc(m.dir ?? 'not checked'), esc(m.date || 'not checked'), `scan ${esc(short(digest))}`, esc(m.checked),
      m.grade ? `<b>Blast radius: ${esc(m.grade)}</b>` : 'Blast radius: not checked'].join(' · '),
    summary: m.s.map((x) => `<span>${esc(x)}</span>`).join(''),
    grade: m.grade
      ? `<p class="g"><span class="letter">${esc(m.grade)}</span><span class="pts">${esc(m.points)} of 100 · deductions</span></p><ul>` +
        m.deductions.map((d) => `<li><span>${esc(d.subject)}</span><span>−${esc(d.points)}</span></li>`).join('') +
        '</ul><p class="small">A means a small blast radius, not a safe agent.</p>'
      : `<p class="g"><span class="letter">–</span><span class="pts">${NC}</span></p>`,
    rows: m.rows.length
      ? m.rows.map((r) => `<tr><td class="mono" translate="no">${esc(r.name)}</td><td>${r.can ? esc(r.can) : NC}</td><td>${r.worst ? esc(r.worst) : NC}</td>` +
          `<td>${r.undo ? chip(r.undo[0], r.undo[1], r.undo[2]) : NC}</td><td>${r.today ? chip(r.today[0], r.today[1], r.today[2]) : NC}</td>` +
          `<td class="mono" translate="no">${r.add.length ? r.add.map(esc).join(', ') : NC}</td></tr>`).join('')
      : `<tr><td colspan="6">${NC}</td></tr>`,
    policies: m.pols.length ? m.pols.map((p) => `<div class="policy"><p><b>${p.n}</b> ${esc(p.why)}</p><pre translate="no">${esc(p.yaml)}</pre></div>`).join('') : `<div class="policy">${NC}</div>`,
    looked: m.looked.map((x) => `<li>${esc(x)}</li>`).join('') || `<li>${NC}</li>`,
    notlooked: m.notLooked.map((x) => `<li>${esc(x)}</li>`).join(''),
    footer: `<span>Scan ${esc(short(digest))} · sealing as a record: next release</span><span>deedglass scan --json reproduces this page</span>`,
  };
  return html.replace(/\{\{(\w+)\}\}/g, (_, k) => {
    if (!(k in slots)) throw new Error(`report.html has an unknown slot {{${k}}}`);
    return slots[k];
  });
}

function playwright() {
  const req = createRequire(join(here, '../../web/package.json'));
  return req('playwright-core');
}

/** render writes the PDF and returns its size, time and the requests the page made. */
export async function render(scan, { out, html: htmlOut, sample = false } = {}) {
  const t0 = Date.now();
  const html = fill(scan, { sample });
  if (htmlOut) writeFileSync(htmlOut, html);
  if (!out) return { bytes: 0, ms: Date.now() - t0, requests: [] };
  const { chromium } = playwright();
  const browser = await chromium.launch({ channel: 'chrome' });
  const requests = [];
  try {
    const page = await browser.newPage();
    page.on('request', (r) => { if (!/^(data|about):/.test(r.url())) requests.push(r.url()); });
    await page.setContent(html, { waitUntil: 'load' });
    await page.evaluate(() => document.fonts.ready);
    await page.pdf({ path: out, format: 'A4', printBackground: true, tagged: true, outline: true, preferCSSPageSize: true });
  } finally {
    await browser.close();
  }
  return { bytes: statSync(out).size, ms: Date.now() - t0, requests };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const o = parseArgs(process.argv.slice(2));
    const scan = JSON.parse(readFileSync(o.scan, 'utf8'));
    const r = await render(scan, { out: o.out, html: o.html, sample: o.sample });
    if (r.requests.length) throw new Error(`the page asked the network for ${r.requests[0]}; report.html must stay self-contained`);
    if (o.out) console.log(`✓ ${o.out} · ${Math.round(r.bytes / 1024)} KB · ${(r.ms / 1000).toFixed(1)} s · tagged, with an outline`);
  } catch (e) {
    console.error(`✕ ${e.message}`);
    process.exit(1);
  }
}
