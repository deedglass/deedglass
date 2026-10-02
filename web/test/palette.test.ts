import { describe, expect, it } from 'vitest';
import { idTarget, rank, score, type Item } from '../src/lib/palette.ts';
import { GO, Sequencer } from '../src/lib/shortcuts.ts';

describe('palette', () => {
  it('jumps to pasted ids', () => {
    expect(idTarget('act_01k6h7mz3c9d8e7f6g5h4jq3m8')).toBe('/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8');
    expect(idTarget('  DEED_01K6H7MZ3C9D8E7F6G5H4JQ3M8 ')).toBe('/app/deeds/deed_01k6h7mz3c9d8e7f6g5h4jq3m8');
    expect(idTarget('key_01k6h7mz3c9d8e7f6g5h4jq3m8')).toBe('/app/agents?id=key_01k6h7mz3c9d8e7f6g5h4jq3m8');
    expect(idTarget('act_01k6')).toBeNull();
    expect(idTarget('act_01k6h7mz3c9d8e7f6g5h4jq3mu')).toBeNull(); // u is not Crockford
    expect(idTarget('org_01k6h7mz3c9d8e7f6g5h4jq3m8')).toBeNull();
  });
  it('scores subsequences with word-start and run bonuses', () => {
    expect(score('xyz', 'Approvals')).toBe(-1);
    expect(score('appr', 'Approvals')).toBeGreaterThan(score('appr', 'Stop an agent… approve'));
    expect(score('sa', 'Stop all agents')).toBeGreaterThan(score('sa', 'Settings › Seal keys') - 100);
    expect(score('ac', 'Actions')).toBeGreaterThan(score('ac', 'Policies › seal accounts'));
  });
  it('ranks', () => {
    const items: Item[] = ['Overview', 'Actions', 'Approvals', 'Agents', 'Stop all agents', 'Toggle theme'].map((l) => ({ id: l, label: l, group: 'goto' }));
    expect(rank('ag', items)[0]!.label).toBe('Agents');
    expect(rank('stop', items)[0]!.label).toBe('Stop all agents');
    expect(rank('a', items).length).toBeGreaterThan(3);
    expect(rank('', items).map((i) => i.label)).toEqual(items.map((i) => i.label));
    expect(rank('zzz', items)).toEqual([]);
  });
  it('shortcuts go, never act, and stay quiet in inputs', () => {
    let t = 0;
    const s = new Sequencer(() => t);
    expect(s.key({ key: 'g' })).toBeNull();
    t += 200;
    expect(s.key({ key: 'a' })).toEqual({ kind: 'go', href: GO.a });
    expect(s.key({ key: 'g' })).toBeNull();
    t += 5000;
    expect(s.key({ key: 'a' })).toBeNull(); // too late
    expect(s.key({ key: 'k', metaKey: true })).toEqual({ kind: 'palette' });
    expect(s.key({ key: '/' })).toEqual({ kind: 'palette' });
    expect(s.key({ key: '?' })).toEqual({ kind: 'help' });
    const input = { closest: () => ({}), isContentEditable: false } as unknown as EventTarget;
    expect(s.key({ key: '/', target: input })).toBeNull();
    for (const href of Object.values(GO)) expect(href.startsWith('/app')).toBe(true);
  });
});
