// node --test scripts/report/render.test.mjs (S37 §6 "Report"): the renderer takes the scan JSON
// only, is deterministic, makes no network request, and writes a tagged one-page PDF.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs, fill, model, render } from './render.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const scan = JSON.parse(readFileSync(join(here, 'testdata/scan-demo.json'), 'utf8'));

test('refuses a notes flag and unknown flags', () => {
  assert.throws(() => parseArgs(['--scan', 'x.json', '--out', 'y.pdf', '--notes', 'hi']), /doesn't take --notes/);
  assert.throws(() => parseArgs(['--scan', 'x.json', '--out', 'y.pdf', '--founder', 'x']), /doesn't take --founder/);
  assert.deepEqual(parseArgs(['--scan', 'a', '--out', 'b', '--sample']), { scan: 'a', out: 'b', sample: true });
});

test('refuses anything but a scan_report v1', () => {
  assert.throws(() => model({ object: 'deed' }), /scan_report v1/);
});

test('the same scan gives the same page', () => {
  assert.equal(fill(scan, { sample: true }), fill(scan, { sample: true }));
});

test('the page carries the sample label, the grade with its deductions and the small print', () => {
  const html = fill(scan, { sample: true });
  for (const want of ['Sample, from the Deedglass demo', 'Blast radius: C', '72 of 100', 'A means a small blast radius, not a safe agent.',
    'orders_ship_open_batch', "Can't be undone", 'No approval', 'What it didn&#39;t look at'.replace('&#39;', "'")]) {
    assert.ok(html.includes(want), `missing ${want}`);
  }
  for (const d of scan.score.deductions) assert.ok(html.includes(d.subject), `deduction ${d.subject} missing`);
  assert.ok(!/\{\{\w+\}\}/.test(html), 'an unfilled slot');
  assert.ok(!/<script/i.test(html), 'the report runs no script');
  assert.ok(!/https?:\/\//.test(html.replace(/xmlns="http:\/\/www\.w3\.org\/2000\/svg"/g, '')), 'the report links nothing');
});

test('a field the scan did not fill says not checked', () => {
  const s = structuredClone(scan);
  delete s.postgres;
  delete s.score;
  const html = fill(s, {});
  assert.ok(html.includes('not checked'));
  assert.ok(html.includes('Blast radius: not checked'));
  assert.ok(!html.includes('Sample, from the Deedglass demo'));
});

test('the PDF is tagged, one page, at most 300 KB, in at most 3 s, with no network request', { timeout: 60_000 }, async () => {
  const out = join(mkdtempSync(join(tmpdir(), 'dg-report-')), 'r.pdf');
  const r = await render(scan, { out, sample: true });
  assert.deepEqual(r.requests, []);
  assert.ok(r.bytes <= 300 * 1024, `${r.bytes} bytes`);
  assert.ok(r.ms <= 3000, `${r.ms} ms`);
  const pdf = readFileSync(out, 'latin1');
  assert.ok(pdf.includes('/StructTreeRoot'), 'not tagged');
  assert.ok(pdf.includes('/Outlines'), 'no outline');
  assert.equal((pdf.match(/\/Type\s*\/Page[^s]/g) ?? []).length, 1, 'more than one page');
});
