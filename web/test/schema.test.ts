// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest';
import typesJson from './fixtures/s26/connector-types.json';
import pgJson from './fixtures/s26/connector-postgres.json';
import { collect, fieldsOf, labelOf, pathOf, rawValues, walk, type Field, type JSONSchema } from '../src/lib/schema.ts';
import { schemaForm } from '../src/controllers/schemaform.ts';
import { formsCopy } from '../src/lib/copy-forms.ts';

const types = typesJson as unknown as { data: { type: string; config_schema: JSONSchema }[] };
const schemaOf = (t: string) => types.data.find((x) => x.type === t)!.config_schema;
const pgConnector = pgJson as unknown as { config: Record<string, unknown> };
const byKey = (fs: Field[], path: string) => walk(fs).find((f) => f.path.join('.') === path)!;

describe('fieldsOf: S16 rest', () => {
  const fs = fieldsOf(schemaOf('rest'), 'f');
  it('maps each property in the schema order', () => {
    expect(fs.map((f) => `${f.key}:${f.kind}`)).toEqual([
      'base_url:url', 'auth:group', 'allowed_methods:multi', 'allowed_path_prefixes:chips', 'timeout_ms:integer', 'max_response_bytes:integer',
      'redirects:select', 'send_action_id:switch', 'headers:json', 'test:group', 'tools:json',
    ]);
  });
  it('marks required, carries descriptions and ranges as hints', () => {
    expect(fs.filter((f) => f.required).map((f) => f.key)).toEqual(['base_url', 'allowed_methods', 'allowed_path_prefixes', 'tools']);
    const base = byKey(fs, 'base_url');
    expect(base.description).toBe('https://api.example.com (http in dev only)');
    expect(base.id).toBe('f-base_url');
    expect(base.label).toBe('Base URL');
    expect(byKey(fs, 'timeout_ms').hint).toBe('100 to 60,000');
    expect(byKey(fs, 'timeout_ms').label).toBe('Timeout (ms)');
    expect(byKey(fs, 'allowed_methods').options).toEqual(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE']);
    expect(byKey(fs, 'redirects').options).toEqual(['none', 'same_host']);
  });
  it('one level of object becomes a group; an array of objects and a bare object fall back to JSON with a note', () => {
    expect(byKey(fs, 'auth').children!.map((c) => `${c.key}:${c.kind}`)).toEqual(['kind:select', 'header:text', 'param:text']);
    expect(byKey(fs, 'auth.kind').id).toBe('f-auth-kind');
    expect(byKey(fs, 'tools').note).toBe(formsCopy.schema.jsonNote);
    expect(byKey(fs, 'headers').kind).toBe('json');
  });
});

describe('fieldsOf: S17 postgres', () => {
  const fs = fieldsOf(schemaOf('postgres'), 'pg');
  it('maps every field', () => {
    expect(walk(fs).map((f) => `${f.path.join('.')}:${f.kind}`)).toEqual([
      'mode:select', 'allowed_schemas:chips', 'writable_tables:chips', 'row_limit:integer', 'max_result_bytes:integer', 'max_rows_affected:integer',
      'statement_timeout_ms:integer', 'lock_timeout_ms:integer', 'max_connections:integer', 'witness:group', 'witness.enabled:switch',
      'witness.watched_tables:chips', 'witness.sensitive_columns:chips', 'witness.auto_columns:json', 'witness.max_rows:json',
      'witness.strict_triggers:switch', 'witness.l2_timeout:text', 'witness.diff_rows:integer', 'functions:json', 'throwaway:switch',
    ]);
  });
  it('tables chips check schema.table; other lists do not', () => {
    expect(byKey(fs, 'writable_tables').tables).toBe(true);
    expect(byKey(fs, 'witness.watched_tables').tables).toBe(true);
    expect(byKey(fs, 'allowed_schemas').tables).toBe(false);
    expect(byKey(fs, 'allowed_schemas').required).toBe(true);
  });
  it('round-trips the demo connector config exactly', () => {
    const cfg = pgConnector.config;
    const at = (path: string[]) => path.reduce<unknown>((v, k) => (v && typeof v === 'object' ? (v as Record<string, unknown>)[k] : undefined), cfg);
    const explicit = new Set(walk(fs).filter((f) => f.kind !== 'group' && at(f.path) !== undefined).map((f) => f.path.join('.')));
    const out = collect(fs, rawValues(fs, cfg), explicit);
    expect(out.errors).toEqual([]);
    // Keys the config sets come back equal (JSON fallbacks included); keys it doesn't set stay out.
    expect(out.config).toEqual(cfg);
    // and through the form, untouched
    expect(schemaForm(schemaOf('postgres'), { idPrefix: 'rt', initial: cfg }).values()).toEqual(cfg);
  });
});

describe('the keyword subset', () => {
  const schema: JSONSchema = {
    type: 'object',
    required: ['endpoint', 'level'],
    properties: {
      endpoint: { type: 'string', format: 'uri', maxLength: 200 },
      level: { type: 'string', enum: ['L0', 'L1', 'L2'], default: 'L1' },
      notes: { type: 'string', 'x-deedglass-widget': 'textarea' },
      query: { type: 'string', 'x-deedglass-widget': 'textarea', 'x-deedglass-mono': true, title: 'Probe query' },
      ratio: { type: 'number', minimum: 0, maximum: 1 },
      retries: { type: 'integer', minimum: 0, default: 3 },
      dry: { type: 'boolean', default: true },
      tables: { type: 'array', items: { type: 'string' }, 'x-deedglass-widget': 'tables' },
      deep: { type: 'object', properties: { inner: { type: 'object', properties: { x: { type: 'string' } } } } },
      mixed: { type: ['string', 'integer'] },
      numbers: { type: 'array', items: { type: 'integer' } },
    },
  };
  const fs = fieldsOf(schema, 'k');
  it('format uri, enum with default, textareas, number, integer default, boolean default, tables', () => {
    expect(byKey(fs, 'endpoint')).toMatchObject({ kind: 'url', maxLength: 200, hint: 'Up to 200 characters', required: true });
    expect(byKey(fs, 'level')).toMatchObject({ kind: 'select', default: 'L1', options: ['L0', 'L1', 'L2'] });
    expect(byKey(fs, 'notes')).toMatchObject({ kind: 'textarea', mono: false });
    expect(byKey(fs, 'query')).toMatchObject({ kind: 'textarea', mono: true, label: 'Probe query' });
    expect(byKey(fs, 'ratio')).toMatchObject({ kind: 'number', hint: '0 to 1' });
    expect(byKey(fs, 'retries')).toMatchObject({ kind: 'integer', hint: 'At least 0', default: 3 });
    expect(byKey(fs, 'dry')).toMatchObject({ kind: 'switch', default: true });
    expect(byKey(fs, 'tables')).toMatchObject({ kind: 'chips', tables: true });
  });
  it('anything deeper or outside the subset falls back to JSON with a note', () => {
    expect(byKey(fs, 'deep').kind).toBe('group');
    expect(byKey(fs, 'deep.inner')).toMatchObject({ kind: 'json', note: formsCopy.schema.jsonNote });
    expect(byKey(fs, 'mixed').kind).toBe('json');
    expect(byKey(fs, 'numbers').kind).toBe('json');
    expect(fieldsOf(null)).toEqual([]);
    expect(fieldsOf({ type: 'object' })).toEqual([]);
  });
  it('defaults pre-fill', () => {
    const raw = rawValues(fs);
    expect(raw).toMatchObject({ level: 'L1', retries: '3', dry: true, endpoint: '', tables: [] });
  });
  it('collect omits empty optional values and parses integers exactly', () => {
    const raw = { ...rawValues(fs), endpoint: ' https://x.example.com/ ', retries: '5000', ratio: '0.25' };
    const out = collect(fs, raw);
    expect(out.errors).toEqual([]);
    expect(out.config).toEqual({ endpoint: 'https://x.example.com/', level: 'L1', ratio: 0.25, retries: 5000, dry: true });
    for (const bad of ['5e3', '5000.0', '1,000', '0x10', '9007199254740993', ' ']) {
      const r = collect(fs, { ...raw, retries: bad });
      if (bad.trim() === '') expect(r.config.retries).toBeUndefined();
      else expect(r.errors.map((e) => e.param), bad).toEqual(['config.retries']);
    }
  });
  it('collect reports required, range, tables and JSON problems in the server param form', () => {
    const out = collect(fs, { ...rawValues(fs), level: '', ratio: '2', tables: ['public.orders', 'orders'], 'deep.inner': '{nope' });
    expect(out.errors).toEqual([
      { param: 'config.endpoint', message: formsCopy.schema.errRequired },
      { param: 'config.level', message: formsCopy.schema.errRequired },
      { param: 'config.ratio', message: 'Enter a number from 0 to 1.' },
      { param: 'config.tables', message: formsCopy.schema.errTable },
      { param: 'config.deep.inner', message: formsCopy.schema.errJson },
    ]);
  });
  it('a switch turned off is sent only when it was set before or has a true default', () => {
    expect(collect(fs, { ...rawValues(fs), endpoint: 'https://a.example.com', dry: false }).config.dry).toBe(false);
    const f2 = fieldsOf({ type: 'object', properties: { on: { type: 'boolean' } } });
    expect(collect(f2, { on: false }).config).toEqual({});
    expect(collect(f2, { on: false }, new Set(['on'])).config).toEqual({ on: false });
  });
});

describe('pathOf', () => {
  const fs = fieldsOf(schemaOf('postgres'), 'pg');
  const rest = fieldsOf(schemaOf('rest'), 'r');
  it('maps server params to field ids', () => {
    expect(pathOf('config.row_limit', fs)).toBe('pg-row_limit');
    expect(pathOf('config.witness.watched_tables', fs)).toBe('pg-witness-watched_tables');
    expect(pathOf('config.witness.watched_tables[2]', fs)).toBe('pg-witness-watched_tables');
    expect(pathOf('config.witness.max_rows.public.orders', fs)).toBe('pg-witness-max_rows');
    expect(pathOf('config.witness', fs)).toBe('pg-witness');
    expect(pathOf('config.base_url', rest)).toBe('r-base_url');
    expect(pathOf('config.tools[0].name', rest)).toBe('r-tools');
    expect(pathOf('config.auth.header', rest)).toBe('r-auth-header');
    expect(pathOf('base_url', rest)).toBe('r-base_url');
  });
  it('returns null for params no field shows', () => {
    expect(pathOf('config', fs)).toBeNull();
    expect(pathOf('config.nope', fs)).toBeNull();
    expect(pathOf('name', fs)).toBeNull();
    expect(pathOf('', fs)).toBeNull();
  });
  it('labels', () => {
    expect(labelOf('tools_ttl_ms')).toBe('Tools TTL (ms)');
    expect(labelOf('max_result_bytes')).toBe('Max result (bytes)');
    expect(labelOf('l2_timeout')).toBe('L2 timeout');
  });
});

describe('schemaForm', () => {
  it('renders labelled controls with help, required text and an untouched round trip', () => {
    const form = schemaForm(schemaOf('rest'), { idPrefix: 'rf' });
    document.body.replaceChildren(form.el);
    const base = document.getElementById('rf-base_url') as HTMLInputElement;
    expect(base.type).toBe('url');
    expect(document.querySelector('label[for="rf-base_url"]')!.textContent).toContain('Base URL');
    expect(document.querySelector('label[for="rf-base_url"] .sf-req')!.textContent).toBe('Required');
    expect(base.getAttribute('aria-describedby')).toBe('rf-base_url-h rf-base_url-e');
    expect(document.getElementById('rf-base_url-h')!.textContent).toBe('https://api.example.com (http in dev only)');
    expect(document.querySelector('fieldset#rf-auth legend')!.textContent).toContain('Auth');
    expect(form.values()).toEqual({});
  });
  it('setErrors shows the server message verbatim under its field and focuses the first invalid one', () => {
    const form = schemaForm(schemaOf('rest'), { idPrefix: 'rf' });
    document.body.replaceChildren(form.el);
    const msg = 'base_url resolves to 169.254.169.254, a cloud metadata address. Use the API’s public address.';
    form.setErrors([{ param: 'config.timeout_ms', message: 'timeout_ms must be at most 60000.' }, { param: 'config.base_url', message: msg }, { param: 'name', message: 'name is taken.' }]);
    const base = document.getElementById('rf-base_url')!;
    expect(base.getAttribute('aria-invalid')).toBe('true');
    expect(document.getElementById('rf-base_url-e')!.textContent).toBe(msg);
    expect(document.getElementById('rf-base_url-e')!.hidden).toBe(false);
    expect(document.getElementById('rf-base_url-e')!.querySelector('svg')).not.toBeNull();
    expect(document.activeElement).toBe(base);
    expect(document.getElementById('rf-formerror')!.textContent).toContain('name is taken.');
    form.clearErrors();
    expect(base.getAttribute('aria-invalid')).toBe('false');
    expect(document.getElementById('rf-formerror')!.hidden).toBe(true);
  });
  it('chip input: Enter adds, tables are checked, chips are removable buttons', () => {
    const form = schemaForm(schemaOf('postgres'), { idPrefix: 'pf', initial: { allowed_schemas: ['public'] } });
    document.body.replaceChildren(form.el);
    const input = document.getElementById('pf-writable_tables') as HTMLInputElement;
    const enter = () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    input.value = 'public.orders';
    enter();
    input.value = 'orders';
    enter();
    expect(document.getElementById('pf-writable_tables-e')!.textContent).toBe(formsCopy.schema.errTable);
    expect(input.getAttribute('aria-invalid')).toBe('true');
    input.value = 'public.refunds';
    enter();
    expect(form.values()).toMatchObject({ allowed_schemas: ['public'], writable_tables: ['public.orders', 'public.refunds'] });
    const x = document.querySelector<HTMLButtonElement>('[aria-label="Remove public.orders"]')!;
    expect(x.tagName).toBe('BUTTON');
    x.click();
    expect(form.values()).toMatchObject({ writable_tables: ['public.refunds'] });
  });
  it('validate() reports client problems and blocks a save', () => {
    const form = schemaForm(schemaOf('postgres'), { idPrefix: 'pv' });
    document.body.replaceChildren(form.el);
    (document.getElementById('pv-row_limit') as HTMLInputElement).value = '5e3';
    expect(form.validate()).toBe(false);
    expect(document.getElementById('pv-allowed_schemas')!.getAttribute('aria-invalid')).toBe('true');
    expect(document.getElementById('pv-row_limit-e')!.textContent).toContain('whole number');
    expect(document.activeElement!.id).toBe('pv-allowed_schemas');
  });
  it('read-only shows values and offers no edits', () => {
    const form = schemaForm(schemaOf('postgres'), { idPrefix: 'pr', initial: pgConnector.config, readOnly: true });
    document.body.replaceChildren(form.el);
    expect((document.getElementById('pr-row_limit') as HTMLInputElement).readOnly).toBe(true);
    expect((document.getElementById('pr-throwaway') as HTMLInputElement).disabled).toBe(true);
    expect(document.querySelectorAll('.sf-chip-x').length).toBe(0);
    expect(document.querySelectorAll('.sf-chip').length).toBe(7);
  });
});
