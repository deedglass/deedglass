// S51: Settings › Security review. The status words equal the Go bank's (internal/securitypack/bank.go),
// the Pro sheet's sentence is the API's message, gaps come first, and the copied list is plain text.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { settingsCopy } from '../src/lib/copy-settings.ts';
import { chipClass, gaps, groups, isGap, plainText, summaryParts, type Readiness } from '../src/lib/securitypack.ts';

const c = settingsCopy.security;
const bank = readFileSync(join(process.cwd(), '..', 'internal/securitypack/bank.go'), 'utf8');
const apiGo = readFileSync(join(process.cwd(), '..', 'internal/securitypackapi/api.go'), 'utf8');

const rd: Readiness = {
  object: 'security_pack_readiness', org: 'org_1', org_name: 'Acme', period: { from: '2026-07-20T00:00:00Z', to: '2026-10-18T00:00:00Z' },
  summary: { in_place: 2, partly: 1, not_set_up: 1, not_applicable: 0, cant_tell: 0 },
  rows: [
    { id: 'agents_listed', group: 'inventory', group_title: 'Inventory', question: 'Does every agent have a record?', status: 'in_place', fix: '' },
    { id: 'keys_rotated', group: 'inventory', group_title: 'Inventory', question: 'Are keys replaced every 90 days?', status: 'partly', fix: 'Roll the 2 keys older than 90 days.' },
    { id: 'stop_used', group: 'stop', group_title: 'Stop', question: 'Has Stop been tried?', status: 'not_set_up', fix: 'Try Stop on a test agent.' },
    { id: 'stop_all', group: 'stop', group_title: 'Stop', question: 'Can two people stop every agent?', status: 'in_place', fix: '' },
  ],
};

describe('security review', () => {
  it("uses the Go bank's status words", () => {
    for (const w of Object.values(c.status)) expect(bank).toContain(`return ${JSON.stringify(w)}`);
  });
  it("says what the API's pro_required says about the free list", () => {
    expect(c.proSheet).toContain('The readiness list stays free.');
    expect(apiGo).toContain('The readiness list stays free');
  });
  it('lists gaps first, not set up before partly', () => {
    expect(gaps(rd.rows).map((r) => r.id)).toEqual(['stop_used', 'keys_rotated']);
    expect(isGap('not_applicable')).toBe(false);
    expect(isGap('cant_tell')).toBe(true);
  });
  it('groups rows in the order they arrive', () => {
    expect(groups(rd.rows).map((g) => [g.title, g.rows.length])).toEqual([['Inventory', 2], ['Stop', 2]]);
  });
  it('writes the summary with zero counts left out', () => {
    expect(summaryParts(rd.summary, c.status)).toEqual(['2 in place', '1 partly', '1 not set up']);
  });
  it('gives every status a chip that repeats, never replaces, the word', () => {
    expect(chipClass('in_place')).toContain('chip-verified');
    expect(chipClass('not_set_up')).toContain('chip-mismatch');
    expect(chipClass('partly')).toContain('chip-unverifiable');
    expect(chipClass('not_applicable')).toContain('chip-neutral');
    expect(chipClass('cant_tell')).toContain('chip-pending');
  });
  it('copies plain text with fixes under their question', () => {
    const t = plainText(rd, 'Security review readiness · Acme · last 90 days', c.status);
    expect(t.split('\n').slice(0, 2)).toEqual(['Security review readiness · Acme · last 90 days', '2 in place · 1 partly · 1 not set up']);
    expect(t).toContain('  Partly         Are keys replaced every 90 days?\n                 Roll the 2 keys older than 90 days.');
    expect(t.endsWith('\n')).toBe(true);
  });
  it('never claims a standard', () => {
    const all = JSON.stringify(c) + JSON.stringify(settingsCopy.sections.security);
    expect(all).not.toMatch(/complian|certif|audit-ready|guarante|\bproves?\b|SOC ?2|—/i);
  });
});
