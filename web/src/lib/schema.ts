// schema.ts: the JSON Schema subset in a connector type's config_schema → a field model for
// SchemaForm (S26 §3.3), and back: collect() turns the raw control values into the config object
// the API takes, and pathOf() maps a server error's param (`config.witness.watched_tables`) to the
// field that shows it. Pure: no DOM, so it's tested directly.
//
// Supported: string (format uri → url; enum → select; maxLength), x-deedglass-widget: textarea
// (mono with x-deedglass-mono), integer and number (minimum/maximum as a hint), boolean (switch),
// array of string (chips; enum items → checkboxes; x-deedglass-widget: tables checks schema.table),
// object one level deep (a fieldset). Anything else becomes a JSON textarea with a note.
//
// The server's schemas (internal/connector) don't carry format or x-deedglass-* keywords today, so
// two conventions stand in for them: a string named `url` or `*_url` is a URL input, and an array
// of strings named `*_tables` is a tables chip input. An explicit keyword always wins.

import { formsCopy } from './copy-forms.ts';
import { fill } from './copy.ts';

export interface JSONSchema {
  type?: string | string[];
  title?: string;
  description?: string;
  default?: unknown;
  enum?: unknown[];
  format?: string;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  pattern?: string;
  items?: JSONSchema;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  additionalProperties?: boolean | JSONSchema;
  'x-deedglass-widget'?: string;
  'x-deedglass-mono'?: boolean;
  [k: string]: unknown;
}

export type FieldKind = 'text' | 'url' | 'select' | 'textarea' | 'integer' | 'number' | 'switch' | 'chips' | 'multi' | 'group' | 'json';

export interface Field {
  /** DOM id: `${prefix}-${path joined by -}`. */
  id: string;
  key: string;
  /** Property names from the config root, like ['witness', 'watched_tables']. */
  path: string[];
  kind: FieldKind;
  label: string;
  required: boolean;
  description?: string;
  /** min/max or maxLength as words, shown after the description. */
  hint?: string;
  default?: unknown;
  /** select options (enum), or the allowed items for `multi`. */
  options?: string[];
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  mono: boolean;
  /** chips that must each be schema.table. */
  tables: boolean;
  children?: Field[];
  /** Why a field fell back to JSON. */
  note?: string;
}

/** A control's raw value: text for inputs, selects and JSON; boolean for switches; a list for chips. */
export type RawValue = string | boolean | string[];
export type RawValues = Record<string, RawValue>;
export interface FieldError { param: string; message: string }
export interface Collected { config: Record<string, unknown>; errors: FieldError[] }

const C = formsCopy.schema;
export const TABLE_RE = /^[A-Za-z_][A-Za-z0-9_$]*\.[A-Za-z_][A-Za-z0-9_$]*$/;
const INT_RE = /^-?\d+$/;
const NUM_RE = /^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/;

const WORDS: Record<string, string> = { url: 'URL', urls: 'URLs', id: 'ID', ids: 'IDs', ttl: 'TTL', sql: 'SQL', dsn: 'DSN', tls: 'TLS', mcp: 'MCP', l0: 'L0', l1: 'L1', l2: 'L2', api: 'API', http: 'HTTP', json: 'JSON', acl: 'ACL' };

/** label: base_url → "Base URL"; timeout_ms → "Timeout (ms)"; tools_ttl_ms → "Tools TTL (ms)". */
export function labelOf(key: string): string {
  const parts = key.split(/[_\s-]+/).filter(Boolean);
  let unit = '';
  if (parts.length > 1 && (parts[parts.length - 1] === 'ms' || parts[parts.length - 1] === 'bytes')) {
    unit = parts[parts.length - 1] === 'ms' ? ' (ms)' : ' (bytes)';
    parts.pop();
  }
  const words = parts.map((p, i) => WORDS[p.toLowerCase()] ?? (i === 0 ? p.charAt(0).toUpperCase() + p.slice(1) : p));
  return (words.join(' ') || key) + unit;
}

const typeOf = (s: JSONSchema): string | undefined => (Array.isArray(s.type) ? (s.type.length === 1 ? s.type[0] : undefined) : s.type);
const isStringEnum = (e: unknown[] | undefined): e is string[] => Array.isArray(e) && e.length > 0 && e.every((x) => typeof x === 'string');
const fmt = (n: number) => n.toLocaleString('en-US');
const safeId = (s: string) => s.replace(/[^A-Za-z0-9_-]/g, '_');

function rangeHint(s: JSONSchema): string | undefined {
  const lo = typeof s.minimum === 'number' ? fmt(s.minimum) : null;
  const hi = typeof s.maximum === 'number' ? fmt(s.maximum) : null;
  if (lo !== null && hi !== null) return fill(C.range, { min: lo, max: hi });
  if (lo !== null) return fill(C.atLeast, { min: lo });
  if (hi !== null) return fill(C.atMost, { max: hi });
  return undefined;
}

function one(key: string, s: JSONSchema, path: string[], required: boolean, prefix: string, depth: number): Field {
  const t = typeOf(s);
  const widget = s['x-deedglass-widget'];
  const base: Field = {
    id: `${prefix}-${path.map(safeId).join('-')}`,
    key,
    path,
    kind: 'json',
    label: typeof s.title === 'string' && s.title ? s.title : labelOf(key),
    required,
    mono: s['x-deedglass-mono'] === true,
    tables: false,
  };
  if (typeof s.description === 'string' && s.description) base.description = s.description;
  if (s.default !== undefined) base.default = s.default;
  const fallback = (): Field => ({ ...base, kind: 'json', mono: true, note: C.jsonNote });

  if (t === 'string') {
    if (typeof s.maxLength === 'number') {
      base.maxLength = s.maxLength;
      base.hint = fill(C.maxLength, { n: fmt(s.maxLength) });
    }
    if (isStringEnum(s.enum)) return { ...base, kind: 'select', options: s.enum };
    if (Array.isArray(s.enum)) return fallback();
    if (widget === 'textarea') return { ...base, kind: 'textarea' };
    if (s.format === 'uri' || s.format === 'url' || (s.format === undefined && (key === 'url' || key.endsWith('_url')))) return { ...base, kind: 'url', mono: true };
    return { ...base, kind: 'text' };
  }
  if (t === 'integer' || t === 'number') {
    if (typeof s.minimum === 'number') base.minimum = s.minimum;
    if (typeof s.maximum === 'number') base.maximum = s.maximum;
    const hint = rangeHint(s);
    if (hint) base.hint = hint;
    return { ...base, kind: t };
  }
  if (t === 'boolean') return { ...base, kind: 'switch' };
  if (t === 'array') {
    const it = s.items;
    if (!it || typeOf(it) !== 'string') return fallback();
    if (isStringEnum(it.enum)) return { ...base, kind: 'multi', options: it.enum };
    if (it.enum !== undefined) return fallback();
    const tables = widget === 'tables' || it['x-deedglass-widget'] === 'tables' || (widget === undefined && key.endsWith('_tables'));
    return { ...base, kind: 'chips', mono: true, tables, ...(typeof it.maxLength === 'number' ? { maxLength: it.maxLength } : {}) };
  }
  if (t === 'object' && depth === 0 && s.properties && Object.keys(s.properties).length > 0) {
    const req = new Set(Array.isArray(s.required) ? s.required : []);
    const children = Object.entries(s.properties).map(([k, sub]) => one(k, sub ?? {}, [...path, k], req.has(k), prefix, depth + 1));
    return { ...base, kind: 'group', children };
  }
  return fallback();
}

/** fieldsOf: the root object schema → its fields, in the schema's property order. */
export function fieldsOf(schema: JSONSchema | null | undefined, prefix = 'sf'): Field[] {
  if (!schema || typeof schema !== 'object' || !schema.properties) return [];
  const req = new Set(Array.isArray(schema.required) ? schema.required : []);
  return Object.entries(schema.properties).map(([k, s]) => one(k, s ?? {}, [k], req.has(k), prefix, 0));
}

/** walk: every field, groups before their children. */
export function walk(fields: Field[]): Field[] {
  return fields.flatMap((f) => [f, ...(f.children ? walk(f.children) : [])]);
}

/** keyOf: the RawValues key for a field ('witness.watched_tables'). */
export const keyOf = (f: Field): string => f.path.join('.');

const get = (o: unknown, path: string[]): unknown => path.reduce<unknown>((v, k) => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>)[k] : undefined), o);

/** rawOf: the control value for a field from a config (editing) or from its default (adding). */
export function rawOf(f: Field, initial?: Record<string, unknown> | null): RawValue {
  const has = initial ? get(initial, f.path) : undefined;
  const v = has !== undefined ? has : f.default;
  switch (f.kind) {
    case 'switch':
      return v === true;
    case 'chips':
    case 'multi':
      return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
    case 'json':
      return v === undefined ? '' : JSON.stringify(v, null, 2);
    case 'group':
      return '';
    default:
      return v === undefined || v === null ? '' : String(v);
  }
}

/** rawValues: every field's control value. */
export function rawValues(fields: Field[], initial?: Record<string, unknown> | null): RawValues {
  const out: RawValues = {};
  for (const f of walk(fields)) if (f.kind !== 'group') out[keyOf(f)] = rawOf(f, initial);
  return out;
}

function parseOne(f: Field, raw: RawValue | undefined, explicit: ReadonlySet<string>): { value?: unknown; error?: string } {
  const k = keyOf(f);
  if (f.kind === 'switch') {
    const on = raw === true;
    if (on || f.required || f.default === true || explicit.has(k)) return { value: on };
    return {};
  }
  if (f.kind === 'chips' || f.kind === 'multi') {
    const list = Array.isArray(raw) ? raw : [];
    if (list.length === 0) return f.required ? { error: C.errRequired } : explicit.has(k) ? { value: [] } : {};
    if (f.tables) {
      const bad = list.find((x) => !TABLE_RE.test(x));
      if (bad !== undefined) return { error: C.errTable };
    }
    return { value: [...list] };
  }
  const text = typeof raw === 'string' ? raw : '';
  const t = f.kind === 'json' || f.kind === 'textarea' ? text : text.trim();
  if (t.trim() === '') return f.required ? { error: C.errRequired } : {};
  if (f.kind === 'integer' || f.kind === 'number') {
    const example = fmt(f.minimum ?? 0).replace(/,/g, '');
    if (f.kind === 'integer' ? !INT_RE.test(t) : !NUM_RE.test(t)) return { error: fill(f.kind === 'integer' ? C.errInteger : C.errNumber, { example: f.default !== undefined ? String(f.default) : example }) };
    const n = Number(t);
    if (!Number.isFinite(n) || (f.kind === 'integer' && !Number.isSafeInteger(n))) return { error: fill(C.errInteger, { example }) };
    const lo = f.minimum, hi = f.maximum;
    if ((lo !== undefined && n < lo) || (hi !== undefined && n > hi)) {
      if (lo !== undefined && hi !== undefined) return { error: fill(C.errRange, { min: fmt(lo), max: fmt(hi) }) };
      return { error: lo !== undefined ? fill(C.errMin, { min: fmt(lo) }) : fill(C.errMax, { max: fmt(hi!) }) };
    }
    return { value: n };
  }
  if (f.kind === 'json') {
    try {
      return { value: JSON.parse(t) };
    } catch {
      return { error: C.errJson };
    }
  }
  if (f.maxLength !== undefined && [...t].length > f.maxLength) return { error: fill(C.errMaxLength, { n: fmt(f.maxLength) }) };
  if (f.kind === 'select' && f.options && !f.options.includes(t)) return { error: C.errRequired };
  return { value: t };
}

/**
 * collect: the control values → the config object, plus client-side errors in the server's
 * param form (`config.row_limit`). Empty optional values are left out; integers are parsed exactly
 * (`5000`, never `5e3` or `5000.0`). `explicit` lists keys that were set before (editing) or that
 * the person changed, so turning a switch off or emptying a list is sent rather than dropped.
 */
export function collect(fields: Field[], values: RawValues, explicit: ReadonlySet<string> = new Set()): Collected {
  const errors: FieldError[] = [];
  const build = (list: Field[]): Record<string, unknown> => {
    const out: Record<string, unknown> = {};
    for (const f of list) {
      if (f.kind === 'group') {
        const sub = build(f.children ?? []);
        if (Object.keys(sub).length > 0 || f.required) out[f.key] = sub;
        continue;
      }
      const r = parseOne(f, values[keyOf(f)], explicit);
      if (r.error) errors.push({ param: `config.${keyOf(f)}`, message: r.error });
      else if (r.value !== undefined) out[f.key] = r.value;
    }
    return out;
  };
  const config = build(fields);
  return { config, errors };
}

/**
 * pathOf: a server error's param → the id of the field that shows it, or null when no field
 * matches (the form shows it at the top). `config.tools[0].name` maps to the `tools` field;
 * `config.witness.max_rows.public.orders` maps to `witness.max_rows`.
 */
export function pathOf(param: string, fields: Field[]): string | null {
  if (typeof param !== 'string' || !param) return null;
  const segs = param.replace(/\[\d+\]/g, '').split('.').filter(Boolean);
  if (segs[0] === 'config') segs.shift();
  let level: Field[] | undefined = fields;
  let hit: Field | null = null;
  for (const s of segs) {
    const f: Field | undefined = level?.find((x) => x.key === s);
    if (!f) break;
    hit = f;
    level = f.children;
    if (!level) break;
  }
  return hit ? hit.id : null;
}
