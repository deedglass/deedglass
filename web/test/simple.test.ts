// DL-143: the Simple view's pure model on real captured answers (test/fixtures) and small literals.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { changeOf, decodeText, headlineOf, isHealth, matchesFilter, placeOf, sqlPairs, splitList, opOf, plainAction, plainRef, resultOf, sqlKind, sqlTable, stripTags, targetOf, TONE, ICON, type Result } from '../src/lib/models/simple.ts';
import { simpleCopy } from '../src/lib/copy-simple.ts';
import { iconNames } from '../src/icons/names.gen.ts';
import { choosePath, getPath, getView, hasStarted, setView, viewFor } from '../src/lib/theme.ts';
import { web } from './helpers.ts';

const fx = (p: string) => JSON.parse(readFileSync(web + 'test/fixtures/' + p, 'utf8'));
const base = { state: 'sealed', status: 'executed', witness: null, decision: null, approval: null } as const;

describe('resultOf', () => {
  it('reads results from the server fields on real actions', () => {
    expect(resultOf(fx('action-verified-l2.json'))).toBe('checked');
    expect(resultOf(fx('action-mismatch-l2.json'))).toBe('problem');
    expect(resultOf(fx('action-unverifiable.json'))).toBe('couldnt');
    expect(resultOf(fx('action-denied.json'))).toBe('stopped');
    expect(resultOf(fx('action-failed.json'))).toBe('failed');
  });
  it('covers every state', () => {
    expect(resultOf({ ...base, status: 'pending_approval', state: 'pending_approval' } as never)).toBe('waiting');
    expect(resultOf({ ...base, status: 'denied', approval: { status: 'denied' } } as never)).toBe('sentBack');
    expect(resultOf({ ...base, status: 'expired' } as never)).toBe('expired');
    expect(resultOf({ ...base, status: 'cancelled' } as never)).toBe('cancelled');
    expect(resultOf({ ...base } as never)).toBe('done');
    expect(resultOf({ ...base, state: 'executing' } as never)).toBe('running');
  });
  it('gives every result a word, a tone and a shipped icon', () => {
    for (const r of Object.keys(TONE) as Result[]) {
      expect(simpleCopy.feed.result[r], r).toBeTruthy();
      expect((iconNames as readonly string[]).includes(ICON[r]), ICON[r]).toBe(true);
    }
  });
  it('filters', () => {
    expect(matchesFilter('checked', 'all')).toBe(true);
    expect(matchesFilter('waiting', 'waiting')).toBe(true);
    expect(matchesFilter('couldnt', 'problems')).toBe(true);
    expect(matchesFilter('checked', 'problems')).toBe(false);
    expect(matchesFilter('sentBack', 'stopped')).toBe(true);
    expect(matchesFilter('waiting', 'stopped')).toBe(false);
  });
});

describe('the record and the sentence', () => {
  it('reads FHIR references and SQL', () => {
    expect(plainRef('Patient/0193')).toBe('Patient 0193');
    expect(plainRef('https://ehr.example/fhir/Patient/ab-1/_history/2')).toBe('Patient ab-1');
    expect(sqlKind('UPDATE public.orders SET x = 1')).toBe('update');
    expect(sqlKind('with a as (select 1) delete from t')).toBe('delete');
    expect(sqlKind('vacuum')).toBe('other');
    expect(sqlTable('INSERT INTO "public"."refunds" (id) VALUES (1)')).toBe('public.refunds');
    expect(sqlTable('select 1')).toBeNull();
  });
  it('finds the record a request names', () => {
    expect(targetOf({ resource: { resourceType: 'Observation', subject: { reference: 'Patient/0193' } } })).toEqual({ resource: 'Observation', record: 'Patient 0193', sql: null });
    expect(targetOf({ resourceType: 'Patient', id: '42' }).record).toBe('Patient 42');
    expect(targetOf({ sql: 'delete from public.notes where id = 1' })).toEqual({ resource: null, record: 'public.notes', sql: 'delete from public.notes where id = 1' });
    expect(targetOf(null).record).toBeNull();
    expect(opOf('ehr.update_observation')).toEqual({ verb: 'update', rest: 'observation' });
  });
  it('writes a plain sentence in the right tense', () => {
    expect(plainAction(fx('action-verified-l2.json'), fx('action-verified-l2.json').request).text).toMatch(/public\.orders/);
    const want = plainAction({ ...base, tool: 'ehr.create', status: 'pending_approval', state: 'pending_approval' } as never, { resource: { resourceType: 'Observation', subject: { reference: 'Patient/7' } } });
    expect(want.result).toBe('waiting');
    expect(want.text.startsWith('Wants to')).toBe(true);
    expect(want.text).toContain('Patient 7');
    expect(plainAction(fx('action-denied.json')).text.startsWith('Tried to')).toBe(true);
    expect(plainAction({ ...base, tool: 'postgres.execute' } as never).text).toBe('Ran something in PostgreSQL database');
  });
  it('never writes a dash, an exclamation mark or markup', () => {
    for (const n of ['action-verified-l2.json', 'action-mismatch-l2.json', 'action-denied.json', 'action-failed.json', 'action-unverifiable.json']) {
      const t = plainAction(fx(n)).text;
      expect(/[—–!<>]/.test(t), t).toBe(false);
    }
  });
});

describe('what an approval will change', () => {
  it('shows the exact SQL and the table on a real approval', () => {
    const ap = fx('s26/approval.json');
    const ch = changeOf(ap);
    // Simple view: the plain summary up top, the statement itself only under Technical details.
    expect(ch.body).toBeNull();
    expect(ch.statement).toBe(ap.context.args.sql);
    expect(ch.rows[0]).toEqual([simpleCopy.waiting.fields.table, 'public.refunds']);
    expect(ch.rows).toContainEqual([simpleCopy.waiting.fields.change, 'Add rows']);
    expect(ch.rows).toContainEqual(['Reason', 'Damaged in transit']);
    expect(ch.rows).toContainEqual(['Amount cents', '12000']);
    expect(ch.rows).toContainEqual(['Created at', simpleCopy.waiting.sqlNow]);
    expect(ch.rows.some(([, v]) => /INSERT/i.test(v))).toBe(false);
    expect(headlineOf(ap)).toBe(`${ap.agent.name}: wants to add rows to public.refunds`);
  });
  it('reads a FHIR resource, its note and its narrative as text', () => {
    const ch = changeOf({ tool: 'ehr.create', context: { args: { resource: { resourceType: 'Observation', subject: { reference: 'Patient/7' }, code: { text: 'Heart rate' }, valueQuantity: { value: 72, unit: 'bpm' }, text: { div: '<div>Rate <b>72</b> &amp; steady</div>' } } } } } as never);
    expect(ch.rows).toContainEqual([simpleCopy.waiting.fields.patient, 'Patient 7']);
    expect(ch.rows).toContainEqual([simpleCopy.waiting.fields.value, '72 bpm']);
    expect(ch.body).toBe('Rate 72 & steady');
  });
  it('reads UPDATE pairs, quoted commas and doubled quotes; gives up on shapes it cannot read', () => {
    expect(sqlPairs("UPDATE public.orders SET status = 'held, for now', note = 'it''s late' WHERE id = 7")).toEqual([['status', 'held, for now'], ['note', "it's late"]]);
    expect(sqlPairs('INSERT INTO t (a, b) VALUES (1)')).toEqual([]);
    expect(sqlPairs('DELETE FROM t WHERE id = 1')).toEqual([]);
    expect(splitList("1, 'a,b', f(2, 3)")).toEqual(['1', "'a,b'", 'f(2, 3)']);
  });
  it('names where a change goes in words, never the raw connector id', () => {
    expect(placeOf('postgres', 'postgres')).toBe('PostgreSQL database');
    expect(placeOf('fhir', 'ehr')).toBe('Health record (FHIR)');
    expect(placeOf(null, 'ehr')).toBe('Health record');
    expect(placeOf(null, 'orders_db')).toBe('Orders db');
    expect(placeOf(null, '')).toBe('');
  });
  it('the clinical note belongs to health items only', () => {
    const ap = fx('s26/approval.json');
    expect(isHealth(ap)).toBe(false); // postgres refunds
    expect(isHealth({ tool: 'ehr.create', context: { action: { connector: 'ehr', connector_type: null }, args: {} } } as never)).toBe(true);
    expect(isHealth({ tool: 'fhir.create', context: { action: { connector: 'x', connector_type: 'fhir' }, args: {} } } as never)).toBe(true);
    expect(isHealth({ tool: 'emr.write', context: { args: { resource: { resourceType: 'Observation' } } } } as never)).toBe(true);
    expect(isHealth({ tool: 'stripe.create_refund', context: { action: { connector: 'stripe', connector_type: 'stripe' }, args: {} } } as never)).toBe(false);
  });
  it('reads email and refund previews', () => {
    expect(changeOf({ tool: 'gmail.send', context: { args: {}, sections: { email_preview: { to: 'a@b.test', subject: 'Hi', body: 'Text' } } } } as never)).toEqual({ rows: [[simpleCopy.waiting.fields.to, 'a@b.test'], [simpleCopy.waiting.fields.subject_line, 'Hi']], body: 'Text', bodyMono: false });
    expect(changeOf({ tool: 'stripe.create_refund', context: { args: {}, sections: { stripe: { amount_display: '₹1,200.00' } } } } as never).rows[0]![1]).toBe('₹1,200.00');
  });
  it('strips tags and decodes text attachments only', () => {
    expect(stripTags('<p>a</p><p>b<br/>c</p>')).toBe('a\nb\nc');
    expect(decodeText(btoa('note'), 'text/plain')).toBe('note');
    expect(decodeText(btoa('x'), 'application/pdf')).toBeNull();
    expect(decodeText('%%%', 'text/plain')).toBeNull();
  });
});

describe('view and path preferences', () => {
  const mem = () => {
    const m = new Map<string, string>();
    return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), m };
  };
  it('opens Simple for clinics and hospital IT, Detailed for builders', () => {
    expect(viewFor('clinic')).toBe('simple');
    expect(viewFor('it')).toBe('simple');
    expect(viewFor('build')).toBe('detailed');
  });
  it('saves the first-run choice and the view', () => {
    const s = mem();
    const attrs = new Map<string, string>();
    const root = { setAttribute: (k: string, v: string) => void attrs.set(k, v), getAttribute: (k: string) => attrs.get(k) ?? null } as unknown as HTMLElement;
    expect(hasStarted(s)).toBe(false);
    expect(getView(s)).toBeNull();
    expect(choosePath('clinic', root, s)).toBe('simple');
    expect(root.getAttribute('data-view')).toBe('simple');
    expect([getPath(s), getView(s), hasStarted(s)]).toEqual(['clinic', 'simple', true]);
    setView('detailed', root, s);
    expect(getView(s)).toBe('detailed');
    s.m.set('deedglass.view', 'nonsense');
    expect(getView(s)).toBeNull();
  });
  it('survives blocked storage', () => {
    const bad = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } };
    expect(getView(bad)).toBeNull();
    expect(hasStarted(bad)).toBe(false);
    expect(choosePath('build', null, bad)).toBe('detailed');
  });
});
