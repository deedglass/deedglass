// schemaform.ts: SchemaForm (S26 §3.3). A connector type's config_schema → labelled controls
// (labels above, help below, errors with an icon and aria-describedby), and the server's errors
// back onto the field their param names, verbatim. The field model is lib/schema.ts.

import { formsCopy } from '../lib/copy-forms.ts';
import { fill } from '../lib/copy.ts';
import { h, icon, replace } from '../lib/dom.ts';
import { TABLE_RE, collect, fieldsOf, keyOf, pathOf, rawOf, walk, type Field, type FieldError, type JSONSchema, type RawValue, type RawValues } from '../lib/schema.ts';

const C = formsCopy.schema;

export interface SchemaFormOptions {
  idPrefix: string;
  /** The current config, when editing. */
  initial?: Record<string, unknown> | null;
  readOnly?: boolean;
}

export interface SchemaForm {
  el: HTMLElement;
  fields: Field[];
  /** The config object for the API (empty optional values left out). */
  values(): Record<string, unknown>;
  /** Runs the client-side checks; shows any errors and returns false. */
  validate(): boolean;
  /** Shows each server error under its field, verbatim, and focuses the first invalid field. */
  setErrors(errors: FieldError[], o?: { focus?: boolean }): void;
  clearErrors(): void;
}

interface Control {
  field: Field;
  /** The element that takes aria-invalid and focus. */
  focus: HTMLElement;
  read(): RawValue;
  /** Commits a half-typed chip; returns an error message when it can't. */
  commit?(): string | null;
}

function describe(el: HTMLElement, ids: string[]): void {
  el.setAttribute('aria-describedby', ids.join(' '));
}

function helpText(f: Field): string | null {
  const parts = [f.description, f.hint, f.note, f.kind === 'chips' ? (f.tables ? C.tablesHint : C.chipsHint) : undefined].filter((x): x is string => !!x);
  return parts.length ? parts.join(' · ') : null;
}

function labelRow(f: Field, forId: string | null, tag: 'label' | 'legend' = 'label'): HTMLElement {
  const props: Record<string, string> = { class: tag === 'label' ? 'label sf-label' : 'label sf-label sf-legend' };
  if (forId) props.for = forId;
  return h(tag, props,
    h('span', null, f.label),
    f.label !== f.key ? h('span', { class: 'sf-key mono', translate: 'no' }, f.key) : null,
    f.required ? h('span', { class: 'sf-req' }, C.required) : null);
}

export function schemaForm(schema: JSONSchema | null | undefined, o: SchemaFormOptions): SchemaForm {
  const fields = fieldsOf(schema, o.idPrefix);
  const controls = new Map<string, Control>();
  const explicit = new Set<string>();
  const ro = o.readOnly === true;
  const formErr = h('div', { class: 'sf-formerror', id: `${o.idPrefix}-formerror`, role: 'alert', tabindex: -1, hidden: true });

  // Keys set before (editing) are always sent, so turning a switch off or emptying a list sticks.
  if (o.initial) {
    for (const f of walk(fields)) {
      const has = f.path.reduce<unknown>((x, k) => (x && typeof x === 'object' && !Array.isArray(x) ? (x as Record<string, unknown>)[k] : undefined), o.initial);
      if (f.kind !== 'group' && has !== undefined) explicit.add(keyOf(f));
    }
  }

  const errorEl = (id: string) => h('p', { class: 'error', id: `${id}-e`, hidden: true });
  const helpEl = (f: Field) => {
    const t = helpText(f);
    return t ? h('p', { class: 'help', id: `${f.id}-h` }, t) : null;
  };
  const touched = (f: Field) => () => explicit.add(keyOf(f));

  function build(f: Field): HTMLElement {
    const help = helpEl(f);
    const err = errorEl(f.id);
    const ids = [help ? `${f.id}-h` : '', `${f.id}-e`].filter(Boolean);
    const raw = rawOf(f, o.initial);

    if (f.kind === 'group') {
      const fs = h('fieldset', { class: 'sf-group', id: f.id, 'data-field': f.id, tabindex: -1 }, labelRow(f, null, 'legend'), help, err,
        h('div', { class: 'sf-fields' }, ...(f.children ?? []).map(build)));
      describe(fs, ids);
      controls.set(f.id, { field: f, focus: fs, read: () => '' });
      return fs;
    }

    if (f.kind === 'switch') {
      const input = h('input', { type: 'checkbox', role: 'switch', id: f.id, name: f.id, ...(raw === true ? { checked: true } : {}), ...(ro ? { disabled: true } : {}) });
      input.addEventListener('change', touched(f));
      describe(input, ids);
      controls.set(f.id, { field: f, focus: input, read: () => input.checked });
      return h('div', { class: 'field sf-field sf-switch', 'data-field': f.id },
        h('label', { class: 'switch', for: f.id }, input, h('span', { class: 'sf-switch-text' }, h('span', null, f.label), f.label !== f.key ? h('span', { class: 'sf-key mono', translate: 'no' }, f.key) : null)),
        help, err);
    }

    if (f.kind === 'multi') {
      const chosen = new Set(Array.isArray(raw) ? raw : []);
      const boxes = (f.options ?? []).map((opt, i) => {
        const b = h('input', { type: 'checkbox', id: `${f.id}-${i}`, name: f.id, value: opt, ...(chosen.has(opt) ? { checked: true } : {}), ...(ro ? { disabled: true } : {}) });
        b.addEventListener('change', touched(f));
        return b;
      });
      const fs = h('fieldset', { class: 'sf-multi', id: f.id, 'data-field': f.id, tabindex: -1 }, labelRow(f, null, 'legend'),
        h('div', { class: 'sf-multi-opts' }, ...boxes.map((b, i) => h('label', { class: 'check', for: `${f.id}-${i}` }, b, h('span', { class: 'mono', translate: 'no' }, f.options![i]!)))),
        help, err);
      describe(fs, ids);
      controls.set(f.id, { field: f, focus: boxes[0] ?? fs, read: () => boxes.filter((b) => b.checked).map((b) => b.value) });
      return fs;
    }

    if (f.kind === 'chips') return chips(f, Array.isArray(raw) ? raw : [], help, err, ids);

    let input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
    let control: HTMLElement;
    const common = { id: f.id, name: f.id, ...(f.required ? { 'aria-required': 'true' } : {}), ...(ro ? { readonly: true } : {}) };
    if (f.kind === 'select') {
      const sel = h('select', { class: 'select', id: f.id, name: f.id, ...(f.required ? { 'aria-required': 'true' } : {}), ...(ro ? { disabled: true } : {}) },
        !f.required || raw === '' ? h('option', { value: '' }, C.notSet) : null,
        ...(f.options ?? []).map((v) => h('option', { value: v, ...(v === raw ? { selected: true } : {}) }, v)));
      sel.value = typeof raw === 'string' ? raw : '';
      input = sel;
      control = h('span', { class: 'select-wrap' }, sel, icon('chevron-down', 16));
    } else if (f.kind === 'textarea' || f.kind === 'json') {
      input = h('textarea', { class: `textarea${f.mono ? ' mono' : ''}`, ...common, rows: f.kind === 'json' ? 4 : 3, spellcheck: 'false', autocomplete: 'off', ...(f.maxLength !== undefined ? { maxlength: f.maxLength } : {}) });
      input.value = typeof raw === 'string' ? raw : '';
      control = input;
    } else {
      const numeric = f.kind === 'integer' || f.kind === 'number';
      input = h('input', {
        class: `input${f.mono ? ' mono' : ''}${numeric ? ' num sf-num' : ''}`,
        ...common,
        type: f.kind === 'url' ? 'url' : 'text',
        ...(numeric ? { inputmode: f.kind === 'integer' ? 'numeric' : 'decimal' } : {}),
        ...(f.kind === 'url' ? { inputmode: 'url' } : {}),
        autocomplete: 'off',
        spellcheck: 'false',
        ...(f.maxLength !== undefined ? { maxlength: f.maxLength } : {}),
      });
      input.value = typeof raw === 'string' ? raw : '';
      control = input;
    }
    input.addEventListener('input', touched(f));
    input.addEventListener('change', touched(f));
    describe(input, ids);
    controls.set(f.id, { field: f, focus: input, read: () => input.value });
    return h('div', { class: 'field sf-field', 'data-field': f.id }, labelRow(f, f.id), control, help, err);
  }

  function chips(f: Field, initial: string[], help: HTMLElement | null, err: HTMLElement, ids: string[]): HTMLElement {
    const items: string[] = [...initial];
    const list = h('ul', { class: 'sf-chips', role: 'list', 'aria-label': f.label });
    const live = h('span', { class: 'sr-only', 'aria-live': 'polite' });
    const input = h('input', { class: 'input mono sf-chip-input', id: f.id, name: f.id, type: 'text', autocomplete: 'off', spellcheck: 'false', autocapitalize: 'off', enterkeyhint: 'done', ...(f.required ? { 'aria-required': 'true' } : {}), ...(f.maxLength !== undefined ? { maxlength: f.maxLength } : {}), ...(ro ? { hidden: true } : {}) });
    describe(input, ids);
    const draw = () => {
      replace(list, ...items.map((v, i) => h('li', { class: 'sf-chip' },
        h('span', { class: 'mono', translate: 'no' }, v),
        ro ? null : h('button', { type: 'button', class: 'sf-chip-x', 'aria-label': fill(C.remove, { value: v }), onclick: () => remove(i) }, icon('x', 16)))));
      list.hidden = items.length === 0;
    };
    const showErr = (msg: string | null) => {
      err.hidden = !msg;
      replace(err, ...(msg ? [icon('circle-alert', 16), msg] : []));
      input.setAttribute('aria-invalid', msg ? 'true' : 'false');
    };
    const remove = (i: number) => {
      const [v] = items.splice(i, 1);
      explicit.add(keyOf(f));
      draw();
      if (v !== undefined) live.textContent = fill(C.removed, { value: v });
      const next = list.querySelectorAll<HTMLButtonElement>('.sf-chip-x')[Math.min(i, items.length - 1)];
      (next ?? input).focus();
    };
    const add = (): string | null => {
      const parts = input.value.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
      if (parts.length === 0) return null;
      for (const p of parts) {
        if (f.tables && !TABLE_RE.test(p)) return C.errTable;
        if (f.maxLength !== undefined && [...p].length > f.maxLength) return fill(C.errMaxLength, { n: f.maxLength });
      }
      const dup = parts.find((p) => items.includes(p));
      if (dup !== undefined) return fill(C.errDuplicate, { value: dup });
      items.push(...parts);
      explicit.add(keyOf(f));
      input.value = '';
      draw();
      live.textContent = fill(C.added, { value: parts.join(', ') });
      return null;
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        showErr(add());
      } else if (e.key === 'Backspace' && input.value === '' && items.length > 0) {
        e.preventDefault();
        const last = list.querySelector<HTMLButtonElement>('.sf-chip:last-child .sf-chip-x');
        last?.focus();
      }
    });
    input.addEventListener('input', () => {
      if (!err.hidden) showErr(null);
    });
    draw();
    controls.set(f.id, {
      field: f,
      focus: input,
      read: () => [...items],
      commit: () => {
        const e = add();
        if (e) showErr(e);
        return e;
      },
    });
    return h('div', { class: 'field sf-field sf-chipfield', 'data-field': f.id }, labelRow(f, f.id), h('div', { class: 'sf-chipbox' }, list, input), help, err, live);
  }

  const fieldsEl = h('div', { class: 'sf-fields' }, ...fields.map(build));
  const el = h('div', { class: `sf${ro ? ' sf-readonly' : ''}`, id: o.idPrefix }, formErr, fieldsEl);

  function raws(): RawValues {
    const out: RawValues = {};
    for (const c of controls.values()) if (c.field.kind !== 'group') out[keyOf(c.field)] = c.read();
    return out;
  }

  function show(id: string, msg: string | null): void {
    const c = controls.get(id);
    const e = el.querySelector<HTMLElement>(`#${CSS.escape(id)}-e`);
    if (!c || !e) return;
    e.hidden = !msg;
    replace(e, ...(msg ? [icon('circle-alert', 16), msg] : []));
    c.focus.setAttribute('aria-invalid', msg ? 'true' : 'false');
  }

  function clearErrors(): void {
    for (const id of controls.keys()) show(id, null);
    formErr.hidden = true;
    replace(formErr);
  }

  function setErrors(errors: FieldError[], o: { focus?: boolean } = {}): void {
    clearErrors();
    const loose: string[] = [];
    for (const e of errors) {
      const id = pathOf(e.param, fields);
      if (id && controls.has(id)) {
        const cur = el.querySelector<HTMLElement>(`#${CSS.escape(id)}-e`);
        // A second message for the same field joins the first rather than replacing it.
        const prev = cur && !cur.hidden ? cur.textContent ?? '' : '';
        show(id, prev ? `${prev} ${e.message}` : e.message);
      } else loose.push(e.message);
    }
    if (loose.length) {
      formErr.hidden = false;
      replace(formErr, icon('circle-alert', 16), h('div', null, h('p', { class: 'sf-formerror-title' }, C.formError), ...loose.map((m) => h('p', null, m))));
    }
    const first = el.querySelector<HTMLElement>('[aria-invalid="true"]');
    if (o.focus === false) return;
    if (first) first.focus();
    else if (loose.length) formErr.focus();
  }

  return {
    el,
    fields,
    values: () => collect(fields, raws(), explicit).config,
    validate: () => {
      const commitErrs: FieldError[] = [];
      for (const c of controls.values()) {
        const m = c.commit?.();
        if (m) commitErrs.push({ param: `config.${keyOf(c.field)}`, message: m });
      }
      const { errors } = collect(fields, raws(), explicit);
      const all = [...commitErrs, ...errors.filter((e) => !commitErrs.some((c) => c.param === e.param))];
      if (all.length) setErrors(all);
      else clearErrors();
      return all.length === 0;
    },
    setErrors,
    clearErrors,
  };
}
