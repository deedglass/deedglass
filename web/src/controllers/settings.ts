// The Settings stub: the org facts from /v1/org and /v1/me, with the sections as URL-synced tabs
// (/app/settings/<section>). S26 builds each section.
import { copy } from '../lib/copy.ts';
import { h, maybe } from '../lib/dom.ts';
import { date } from '../lib/format.ts';
import { idChip } from '../lib/chips.ts';
import { get } from '../lib/state.ts';
import { emptyState } from './screens.ts';
import { wireTabs } from './tabs.ts';

const SECTION_COPY: Record<string, string> = {
  members: 'People and their roles. Owners change roles; admins invite. S26 builds this section.',
  keys: 'Org admin keys for the CLI and automation. Agent keys live with each agent.',
  webhooks: 'Signed webhooks for every event type, with delivery history.',
  'seal-keys': 'The public keys that seal every deed, published at /.well-known/deedglass/keys.json.',
};

function render(section: string): void {
  const body = maybe('settings-body');
  const s = get();
  if (!body || !s) return;
  body.setAttribute('aria-busy', 'false');
  if (section === 'org') {
    body.replaceChildren(h('dl', { class: 'kv' },
      h('dt', null, 'Org'), h('dd', null, s.org.name),
      h('dt', null, 'Slug'), h('dd', { class: 'mono' }, s.org.slug),
      h('dt', null, 'Org id'), h('dd', null, idChip(s.org.id)),
      h('dt', null, 'Time zone'), h('dd', null, s.org.timezone),
      h('dt', null, 'Created'), h('dd', null, date(s.org.created_at, s.org.timezone)),
      h('dt', null, 'Your role'), h('dd', null, copy.topbar.role[s.me.membership.role] ?? s.me.membership.role)));
  } else {
    body.replaceChildren(emptyState(SECTION_COPY[section] ?? '', section === 'webhooks' ? 'arrow-up-right' : section === 'members' ? 'user' : 'key-round', undefined, 'https://docs.deedglass.com/console'));
  }
}

export function mountSettings(): void {
  let current = 'org';
  const list = maybe('settings-tabs');
  if (list) wireTabs(list, (k) => { current = k; render(k); }, (k) => (k === 'org' ? '/app/settings' : `/app/settings/${k}`));
  document.addEventListener('dg:ready', () => render(current), { once: true });
}
