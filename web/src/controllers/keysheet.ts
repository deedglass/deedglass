// keysheet.ts: S08's "Copy your new key" sheet, verbatim (docs/COPY_CATALOG.md). The secret comes
// from the create response only; it is shown once, never stored (no state, storage or URL), and
// the sheet's DOM, with the value, is gone when it closes. A replayed create answers secret: null,
// and the sheet says Deedglass doesn't show a key twice.

import { copy } from '../lib/copy.ts';
import { h, icon } from '../lib/dom.ts';
import type { ApiKey } from '../lib/types.ts';
import { openSheet } from './overlay.ts';

export function showKeySheet(k: ApiKey, onDone?: () => void): void {
  const secret = k.secret ?? null;
  const box = h('label', { class: 'check' }, h('input', { type: 'checkbox', name: 'stored' }), h('span', null, copy.agents.newKeyStored));
  const done = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, copy.agents.done);
  const input = box.querySelector('input')!;
  input.addEventListener('change', () => (done.disabled = !input.checked));
  const value = secret
    ? h('div', { class: 'secret' },
        h('input', { class: 'input mono', name: 'new_key', readonly: true, value: secret, 'aria-label': `${k.name} key`, autocomplete: 'off', spellcheck: 'false', 'data-secret': '' }),
        h('button', { type: 'button', class: 'btn btn-secondary', 'data-copy': secret, 'aria-label': 'Copy key' }, icon('copy', 16), 'Copy'))
    : h('p', { class: 'banner banner-warn' }, icon('triangle-alert', 16), copy.agentsPage.keyReplayed);
  // wipe drops the secret from the page the moment the sheet starts closing (Done, ×, Escape).
  let wiped = false;
  const wipe = () => {
    if (wiped) return;
    wiped = true;
    for (const el of sheet.el.querySelectorAll('[data-secret], [data-copy]')) {
      if (el instanceof HTMLInputElement) el.value = '';
      el.removeAttribute('data-copy');
      el.removeAttribute('value');
    }
    sheet.el.querySelector('.secret')?.replaceChildren();
    sheet.el.dispatchEvent(new Event('dg:wiped'));
  };
  const sheet = openSheet({
    title: copy.agents.newKeyTitle,
    body: [h('p', null, copy.agents.newKeyBody), value, h('p', { class: 't-footnote t-2' }, `${k.prefix}… · ${k.mode === 'live' ? copy.agentsPage.keyLive : copy.agentsPage.keyTest} · ${k.scopes.join(', ')}`), secret ? box : null],
    foot: [secret ? done : h('button', { type: 'button', class: 'btn btn-primary', 'data-close': '' }, copy.agents.done)],
    initialFocus: () => sheet.el.querySelector<HTMLInputElement>('[data-secret]'),
    onClose: () => {
      wipe();
      sheet.el.replaceChildren();
      onDone?.();
    },
  });
  const close = () => {
    wipe();
    sheet.close();
  };
  done.addEventListener('click', close);
  sheet.el.querySelector('[data-close]')?.addEventListener('click', close);
  sheet.el.querySelector('.sheet-head .icon-btn')?.addEventListener('click', wipe, { capture: true });
  const onEsc = (e: KeyboardEvent) => {
    if (e.key === 'Escape') wipe();
  };
  window.addEventListener('keydown', onEsc, { capture: true }); // before the sheet's own Escape handler
  sheet.el.addEventListener('dg:wiped', () => window.removeEventListener('keydown', onEsc, { capture: true }));
}
