// nav.ts: the nine destinations in brand §13.1 order, with their icons and go-to keys (S24 §3.5, §3.8).
import { copy } from './copy.ts';

export interface Dest {
  key: string;
  href: string;
  label: string;
  icon: string;
  go: string;
}

export const DESTS: readonly Dest[] = [
  { key: 'overview', href: '/app', label: copy.nav.overview, icon: 'layout-grid', go: 'g o' },
  { key: 'actions', href: '/app/actions', label: copy.nav.actions, icon: 'activity', go: 'g a' },
  { key: 'approvals', href: '/app/approvals', label: copy.nav.approvals, icon: 'stamp', go: 'g p' },
  { key: 'agents', href: '/app/agents', label: copy.nav.agents, icon: 'bot', go: 'g g' },
  { key: 'policies', href: '/app/policies', label: copy.nav.policies, icon: 'scroll-text', go: 'g l' },
  { key: 'connectors', href: '/app/connectors', label: copy.nav.connectors, icon: 'plug', go: 'g c' },
  { key: 'insights', href: '/app/insights', label: copy.nav.insights, icon: 'chart-line', go: 'g i' },
  { key: 'deeds', href: '/app/deeds', label: copy.nav.deeds, icon: 'deed', go: 'g d' },
  { key: 'settings', href: '/app/settings', label: copy.nav.settings, icon: 'settings', go: 'g s' },
];

/** The bottom bar under 768 px: four destinations and More. */
export const BOTTOM = ['overview', 'actions', 'approvals', 'agents'] as const;

/** Settings sections the palette can jump to. */
export const SETTINGS_SECTIONS: readonly { key: string; label: string }[] = [
  { key: 'org', label: 'Org' },
  { key: 'members', label: 'People' },
  { key: 'keys', label: 'Admin keys' },
  { key: 'webhooks', label: 'Webhooks' },
  { key: 'seal-keys', label: 'Seal keys' },
];
