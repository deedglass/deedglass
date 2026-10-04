// @vitest-environment happy-dom
import { beforeEach, describe, expect, it } from 'vitest';
import { secretField, withSecrets, type SecretField } from '../src/controllers/secretfield.ts';

const SECRET = 'pg-s3cr3t-9f2c-DO-NOT-LEAK';

/** Every string the document holds: text, attribute values, and each control's live value. */
function domStrings(root: Node = document): string[] {
  const out: string[] = [];
  const walk = (n: Node) => {
    if (n.nodeType === Node.TEXT_NODE || n.nodeType === Node.COMMENT_NODE) out.push(n.nodeValue ?? '');
    if (n instanceof Element) {
      for (const a of Array.from(n.attributes)) out.push(a.name, a.value);
      if (n instanceof HTMLInputElement || n instanceof HTMLTextAreaElement) out.push(n.value, n.defaultValue);
    }
    for (const c of Array.from(n.childNodes)) walk(c);
  };
  walk(root);
  return out;
}

/** Every string reachable from a plain object, descending into DOM nodes with domStrings. */
function objStrings(o: unknown, seen = new Set<unknown>()): string[] {
  if (typeof o === 'string') return [o];
  if (!o || typeof o !== 'object' || seen.has(o)) return [];
  seen.add(o);
  if (o instanceof Node) return domStrings(o);
  return Object.entries(o).flatMap(([k, v]) => [k, ...objStrings(v, seen)]);
}

function type(f: SecretField, v: string) {
  const i = f.el.querySelector('input')!;
  i.value = v;
  i.dispatchEvent(new Event('input', { bubbles: true }));
}

const leaks = (f: SecretField) => [...domStrings(), ...objStrings(f)].filter((s) => s.includes(SECRET));

describe('SecretField', () => {
  beforeEach(() => document.body.replaceChildren());

  it('is a write-only password input, never pre-filled', () => {
    const f = secretField({ id: 'dsn', label: 'dsn' });
    document.body.append(f.el);
    const i = f.el.querySelector('input')!;
    expect(i.type).toBe('password');
    expect(i.getAttribute('autocomplete')).toBe('off');
    expect(i.getAttribute('spellcheck')).toBe('false');
    expect(i.getAttribute('autocapitalize')).toBe('off');
    expect(i.getAttribute('data-1p-ignore')).not.toBeNull();
    expect(i.getAttribute('data-lpignore')).toBe('true');
    expect(i.value).toBe('');
    expect(i.hasAttribute('value')).toBe(false);
    expect(document.querySelector('label[for="dsn"]')!.textContent).toBe('dsn');
  });

  it('Show is off by default, toggles with aria-pressed, and resets when focus leaves', () => {
    const f = secretField({ id: 'tok', label: 'token' });
    const other = document.createElement('button');
    document.body.append(f.el, other);
    const i = f.el.querySelector('input')!;
    const t = f.el.querySelector<HTMLButtonElement>('.secret-toggle')!;
    expect(t.getAttribute('aria-pressed')).toBe('false');
    t.click();
    expect(i.type).toBe('text');
    expect(t.getAttribute('aria-pressed')).toBe('true');
    // focus moving from the input to its own toggle keeps it shown
    i.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: t }));
    expect(i.type).toBe('text');
    // leaving the field hides it
    i.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: other }));
    expect(i.type).toBe('password');
    expect(t.getAttribute('aria-pressed')).toBe('false');
    t.click();
    t.click();
    expect(i.type).toBe('password');
  });

  it('clear() empties the value, the attribute and swaps the element', () => {
    const f = secretField({ id: 'pw', label: 'password' });
    document.body.append(f.el);
    type(f, SECRET);
    const before = f.el.querySelector('input')!;
    before.setAttribute('value', SECRET); // as if something had reflected it
    expect(f.value()).toBe(SECRET);
    f.clear();
    expect(f.value()).toBe('');
    expect(before.value).toBe('');
    expect(before.hasAttribute('value')).toBe(false);
    expect(f.el.querySelector('input')).not.toBe(before);
    expect(leaks(f)).toEqual([]);
  });

  it('leak test: nothing in the document or the component holds the secret after a successful save', async () => {
    const a = secretField({ id: 'dsn', label: 'dsn' });
    const b = secretField({ id: 'wdsn', label: 'witness_dsn' });
    document.body.append(a.el, b.el);
    type(a, SECRET);
    type(b, SECRET + '-w');
    a.el.querySelector<HTMLButtonElement>('.secret-toggle')!.click(); // shown at save time
    let sent: Record<string, string> | null = null;
    const res = await withSecrets({ dsn: a, witness_dsn: b }, async (v) => {
      sent = { ...v };
      expect(v.dsn).toBe(SECRET);
      return { ok: true };
    });
    expect(res).toEqual({ ok: true });
    expect(sent!.witness_dsn).toBe(SECRET + '-w');
    sent = null;
    expect(leaks(a)).toEqual([]);
    expect(leaks(b)).toEqual([]);
    expect(a.el.querySelector('input')!.type).toBe('password');
  });

  it('leak test: the same after a failed save, and the error still surfaces', async () => {
    const a = secretField({ id: 'dsn', label: 'dsn' });
    document.body.append(a.el);
    type(a, SECRET);
    let seen: Record<string, string> | null = null;
    await expect(withSecrets({ dsn: a }, async (v) => {
      seen = v;
      throw new Error('credential_invalid');
    })).rejects.toThrow('credential_invalid');
    // the values object handed to the save is blanked too
    expect(seen!.dsn).toBe('');
    a.setError('The credential was refused. Check it, then save again.');
    expect(leaks(a)).toEqual([]);
    expect(a.el.querySelector('input')!.getAttribute('aria-invalid')).toBe('true');
  });
});
