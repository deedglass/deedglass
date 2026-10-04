// secretfield.ts: SecretField (S26 §3.3, §7). A write-only credential input. The value lives only
// in the input element: this module keeps no copy, never pre-fills, never logs, and clear() swaps
// in a fresh input so neither the value, a value attribute nor the browser's undo history keeps it.
// withSecrets() reads the fields, runs the save, and clears them in a finally block (success or error).

import { formsCopy } from '../lib/copy-forms.ts';
import { fill } from '../lib/copy.ts';
import { h, icon, replace } from '../lib/dom.ts';

const C = formsCopy.secret;

export interface SecretFieldOptions {
  id: string;
  label: string;
  help?: string;
  required?: boolean;
}

export interface SecretField {
  el: HTMLElement;
  /** The current value, read from the input on each call. */
  value(): string;
  /** Empties the field and drops anything that could hold the value. */
  clear(): void;
  /** Hides the value again (the Show toggle off). */
  hide(): void;
  focus(): void;
  /** Shows a message under the field (the server's, verbatim), or clears it. */
  setError(message: string | null): void;
}

// The attributes a password manager or the browser reads; set on every input this module makes.
const QUIET = { autocomplete: 'off', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off', 'data-1p-ignore': 'true', 'data-lpignore': 'true', 'data-bwignore': 'true', 'data-form-type': 'other' } as const;

export function secretField(o: SecretFieldOptions): SecretField {
  const help = o.help ?? C.hiddenNote;
  const helpId = `${o.id}-h`;
  const errId = `${o.id}-e`;
  const toggle = h('button', { type: 'button', class: 'btn btn-quiet btn-sm secret-toggle', 'aria-pressed': 'false', 'aria-controls': o.id, 'aria-label': fill(C.showLabel, { label: o.label }) }, C.show);
  let invalid = false;
  const make = () => {
    const i = h('input', { class: 'input mono secret-input', type: 'password', id: o.id, ...QUIET, ...(invalid ? { 'aria-invalid': 'true' } : {}), 'aria-describedby': `${helpId} ${errId}`, ...(o.required ? { 'aria-required': 'true' } : {}) });
    i.addEventListener('keydown', (e) => {
      // Escape hides a shown value without clearing it.
      if (e.key === 'Escape' && i.type === 'text') hide();
    });
    return i;
  };
  let input = make();
  const row = h('div', { class: 'secret-row' }, input, toggle);
  const el = h('div', { class: 'field secretfield', 'data-field': o.id },
    h('label', { class: 'label', for: o.id }, o.label),
    row,
    h('p', { class: 'help', id: helpId }, help),
    h('p', { class: 'error', id: errId, hidden: true }));

  function hide(): void {
    input.type = 'password';
    toggle.setAttribute('aria-pressed', 'false');
  }
  // Keep focus in the input when the toggle is pressed with a pointer, so the blur reset below
  // doesn't fire between pointer down and click (Safari doesn't focus buttons on click).
  toggle.addEventListener('mousedown', (e) => e.preventDefault());
  toggle.addEventListener('click', () => {
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    toggle.setAttribute('aria-pressed', show ? 'true' : 'false');
  });
  // Off again whenever focus leaves the field and its toggle.
  el.addEventListener('focusout', (e) => {
    const to = (e as FocusEvent).relatedTarget as Node | null;
    if (!to || !row.contains(to)) hide();
  });

  return {
    el,
    value: () => input.value,
    clear: () => {
      const had = document.activeElement === input;
      input.value = '';
      input.defaultValue = '';
      input.removeAttribute('value');
      const fresh = make();
      input.replaceWith(fresh);
      input = fresh;
      hide();
      if (had) fresh.focus();
    },
    hide,
    focus: () => input.focus(),
    setError: (message) => {
      invalid = !!message;
      const e = el.querySelector<HTMLElement>('.error');
      if (e) {
        e.hidden = !message;
        replace(e, ...(message ? [icon('circle-alert', 16), message] : []));
      }
      input.setAttribute('aria-invalid', invalid ? 'true' : 'false');
    },
  };
}

/**
 * withSecrets reads each field once, awaits fn(values), then clears every field and blanks the
 * values object, whether fn resolves or throws. Nothing else should hold the values.
 */
export async function withSecrets<T>(fields: Record<string, SecretField>, fn: (values: Record<string, string>) => Promise<T>): Promise<T> {
  const values: Record<string, string> = {};
  for (const [k, f] of Object.entries(fields)) values[k] = f.value();
  try {
    return await fn(values);
  } finally {
    for (const k of Object.keys(values)) values[k] = '';
    for (const f of Object.values(fields)) f.clear();
  }
}
