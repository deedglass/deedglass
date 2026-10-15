// settings.ts: the pure parts of Settings (S26 §3.4, §3.5): the section ⇄ URL mapping, the role
// gates (the Console hides or disables with the reason; the API refuses regardless), the time-zone
// filter, the retention sentence and the approvals patch. No DOM here, so Vitest runs it in node.

import { fill } from './copy.ts';
import { settingsCopy as c } from './copy-settings.ts';
import type { Role } from './types.ts';

export const SECTION_KEYS = ['general', 'members', 'keys', 'approvals', 'webhooks', 'seal-keys', 'security', 'notifications', 'danger'] as const;
export type SectionKey = (typeof SECTION_KEYS)[number];

/** Old or friendly names for a section. `org` was S24's key for General. */
export const SECTION_ALIASES: Readonly<Record<string, SectionKey>> = { org: 'general', people: 'members', 'api-keys': 'keys', 'admin-keys': 'keys', 'security-review': 'security' };

const BASE = '/app/settings';
const SEGMENT = /^[a-z][a-z0-9-]{0,31}$/; // the server's rule for /app/settings/<section>

export function isSection(k: string): k is SectionKey {
  return (SECTION_KEYS as readonly string[]).includes(k);
}

/** sectionPath is a section's URL; null is the section list (/app/settings). */
export function sectionPath(key: SectionKey | null): string {
  return key ? `${BASE}/${key}` : BASE;
}

export interface ParsedSection {
  key: SectionKey | null; // null: the list (or an unknown section)
  canonical: string; // the URL to show (an alias or an unknown key is rewritten)
}

/** parseSection reads a pathname: `/app/settings` is the list, `/app/settings/org` is General. */
export function parseSection(pathname: string): ParsedSection {
  const rest = pathname.replace(/\/+$/, '');
  if (rest === BASE || !rest.startsWith(BASE + '/')) return { key: null, canonical: BASE };
  const seg = rest.slice(BASE.length + 1);
  if (!SEGMENT.test(seg)) return { key: null, canonical: BASE };
  if (isSection(seg)) return { key: seg, canonical: sectionPath(seg) };
  const alias = SECTION_ALIASES[seg];
  if (alias) return { key: alias, canonical: sectionPath(alias) };
  return { key: null, canonical: BASE };
}

// ---------- roles (S26 §3.5) ----------

const RANK: Record<Role, number> = { viewer: 1, admin: 2, owner: 3 };
const at = (r: Role | undefined, need: Role) => !!r && RANK[r] >= RANK[need];

export interface Gates {
  editGeneral: boolean; // name, time zone: admin
  editRetention: boolean; // owner
  invite: boolean; // admin
  inviteRoles: Role[]; // admin: viewer, admin; owner adds owner
  changeRoles: boolean; // owner
  removeMembers: boolean; // admin (an owner only by an owner)
  orgKeys: boolean; // admin, read and write
  readApprovals: boolean; // admin
  editApprovals: boolean; // owner
  webhooks: boolean; // admin
  notifications: boolean; // admin
  sealKeys: boolean; // viewer
  stopAll: boolean; // admin
  startAll: boolean; // owner
  exportDeeds: boolean; // viewer
}

export function gates(role: Role | undefined): Gates {
  const admin = at(role, 'admin');
  const owner = at(role, 'owner');
  return {
    editGeneral: admin,
    editRetention: owner,
    invite: admin,
    inviteRoles: owner ? ['viewer', 'admin', 'owner'] : admin ? ['viewer', 'admin'] : [],
    changeRoles: owner,
    removeMembers: admin,
    orgKeys: admin,
    readApprovals: admin,
    editApprovals: owner,
    webhooks: admin,
    notifications: admin,
    sealKeys: at(role, 'viewer'),
    stopAll: admin,
    startAll: owner,
    exportDeeds: at(role, 'viewer'),
  };
}

/** The role a section needs before it shows anything (null: every member). */
export function sectionNeeds(key: SectionKey): Role | null {
  return key === 'keys' || key === 'approvals' || key === 'webhooks' || key === 'notifications' ? 'admin' : null;
}

export type MemberAction = 'leave' | 'remove' | 'resend';

/**
 * memberActions: what the signed-in person may do to one membership. Removing yourself is
 * leaving, which the API's remove route allows from Admin up; an owner is removed only by an owner.
 */
export function memberActions(me: { role: Role | undefined; userId: string }, m: { role: Role; status: string; user: { id: string } }): MemberAction[] {
  const out: MemberAction[] = [];
  const self = m.user.id === me.userId;
  if (self) {
    if (at(me.role, 'admin')) out.push('leave');
    return out;
  }
  if (m.status === 'invited' && at(me.role, 'admin') && (m.role !== 'owner' || at(me.role, 'owner'))) out.push('resend');
  if (at(me.role, 'admin') && (m.role !== 'owner' || at(me.role, 'owner'))) out.push('remove');
  return out;
}

/** A role change that makes or unmakes an owner gets a confirmation first. */
export function roleChangeNeedsConfirm(from: Role, to: Role): boolean {
  return from !== to && (from === 'owner' || to === 'owner');
}

// ---------- time zones ----------

/**
 * ICU still lists some zones under their old IANA names (Asia/Calcutta, Europe/Kiev). The Console
 * offers today's names, which the server's tz database also knows, so a search for Kolkata works.
 */
export const LEGACY_ZONES: Readonly<Record<string, string>> = {
  'Africa/Asmera': 'Africa/Asmara',
  'America/Buenos_Aires': 'America/Argentina/Buenos_Aires',
  'America/Godthab': 'America/Nuuk',
  'America/Indianapolis': 'America/Indiana/Indianapolis',
  'America/Louisville': 'America/Kentucky/Louisville',
  'Asia/Calcutta': 'Asia/Kolkata',
  'Asia/Katmandu': 'Asia/Kathmandu',
  'Asia/Rangoon': 'Asia/Yangon',
  'Asia/Saigon': 'Asia/Ho_Chi_Minh',
  'Atlantic/Faeroe': 'Atlantic/Faroe',
  'Europe/Kiev': 'Europe/Kyiv',
};

/** zoneList: the engine's IANA zones (today's names), with UTC and the org's current zone always present. */
export function zoneList(current?: string): string[] {
  let zones: string[] = [];
  try {
    zones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? [];
  } catch {
    zones = [];
  }
  const set = new Set(zones.map((z) => LEGACY_ZONES[z] ?? z));
  set.add('UTC');
  if (current) set.add(current);
  return [...set].sort((a, b) => (a === 'UTC' ? -1 : b === 'UTC' ? 1 : a.localeCompare(b)));
}

const norm = (s: string) => s.toLowerCase().replace(/[_\s/-]+/g, ' ').trim();

/** filterZones: every word of the query appears in the zone (case, `_`, `/` and spaces ignored). */
export function filterZones(zones: readonly string[], query: string): string[] {
  const words = norm(query).split(' ').filter(Boolean);
  if (words.length === 0) return [...zones];
  return zones.filter((z) => {
    const n = norm(z);
    return words.every((w) => n.includes(w));
  });
}

// ---------- retention ----------

export const DEFAULT_RETENTION = 90;
export const MAX_RETENTION = 3650;

export function retentionSpan(days: number): string {
  return days === 1 ? '1 day' : `${days.toLocaleString('en-US')} days`;
}

/** retentionHelp: S26 §5's sentence with the org's number in it. */
export function retentionHelp(days: number): string {
  return fill(c.general.retentionHelp, { span: retentionSpan(days) });
}

/** parseRetention: a whole number of days from 1 to 3650, or null. */
export function parseRetention(v: string): number | null {
  const t = v.trim();
  if (!/^\d{1,4}$/.test(t)) return null;
  const n = Number(t);
  return n >= 1 && n <= MAX_RETENTION ? n : null;
}

// ---------- approval settings ----------

export interface ApprovalSettings {
  email_links: 'decide' | 'review';
  email_detail: 'summary' | 'full';
  self_countersign: 'deny' | 'allow';
}

export const APPROVAL_DEFAULTS: Readonly<ApprovalSettings> = { email_links: 'decide', email_detail: 'summary', self_countersign: 'deny' };

/** approvalSettings fills what the server left out with the documented defaults. */
export function approvalSettings(raw: Partial<ApprovalSettings> | null | undefined): ApprovalSettings {
  const r = raw ?? {};
  return {
    email_links: r.email_links === 'review' ? 'review' : 'decide',
    email_detail: r.email_detail === 'full' ? 'full' : 'summary',
    self_countersign: r.self_countersign === 'allow' ? 'allow' : 'deny',
  };
}

/** approvalsPatch: only the keys that changed, or null when nothing did. */
export function approvalsPatch(before: ApprovalSettings, after: ApprovalSettings): Partial<ApprovalSettings> | null {
  const out: Partial<ApprovalSettings> = {};
  for (const k of Object.keys(after) as (keyof ApprovalSettings)[]) {
    if (before[k] !== after[k]) (out as Record<string, string>)[k] = after[k];
  }
  return Object.keys(out).length ? out : null;
}

// ---------- webhooks ----------

/** The event types an endpoint may subscribe to (internal/ledger WebhookEvents; contract §8). */
export const WEBHOOK_EVENTS = [
  'action.created', 'action.decided', 'action.executed', 'action.witnessed',
  'approval.requested', 'approval.decided',
  'deed.sealed',
  'agent.stopped', 'agent.resumed', 'agent.suspended',
  'org.stopped', 'org.started',
  'policy.activated',
  'decoder.degraded', 'decoder.recovered', 'decoder.alert',
  'anomaly.detected', 'suggestion.created',
  'webhook.test',
] as const;

/** eventFamilies groups the event types by their first word, in list order. */
export function eventFamilies(events: readonly string[] = WEBHOOK_EVENTS): [string, string[]][] {
  const out = new Map<string, string[]>();
  for (const e of events) {
    const f = e.split('.')[0] ?? e;
    out.set(f, [...(out.get(f) ?? []), e]);
  }
  return [...out];
}

/** hostOf: a URL's host for the typed delete confirmation (the URL itself when it doesn't parse). */
export function hostOf(u: string): string {
  try {
    return new URL(u).host || u;
  } catch {
    return u;
  }
}

export interface WebhookTestResult {
  delivered: boolean;
  response_status: number;
  response_ms: number;
  error: string | null;
}

/** testLine: `✓ Delivered webhook.test · 200 in 184 ms`, or the failure with its status. */
export function testLine(r: WebhookTestResult): { ok: boolean; text: string } {
  const ms = Math.max(0, Math.round(r.response_ms)).toLocaleString('en-US');
  if (r.delivered) return { ok: true, text: fill(c.webhooks.testOk, { status: r.response_status, ms }) };
  if (r.response_status > 0) return { ok: false, text: fill(c.webhooks.testFail, { status: r.response_status, ms }) + (r.error ? ` · ${r.error}` : '') };
  return { ok: false, text: fill(c.webhooks.testNoAnswer, { error: r.error ?? c.webhooks.noAnswer }) };
}

/** savedToast: `Saved · deed sealed` when the answer names a deed, otherwise `Saved`. */
export function savedToast(answer: { deed?: string | null } | null | undefined): string {
  return answer?.deed ? c.savedDeed : c.saved;
}

/** inviteDays: the invite's lifetime in days (S07: 7), from the answer's expiry when it has one. */
export function inviteDays(answer: { expires_at?: string | null } | null | undefined, now = Date.now()): number {
  const t = answer?.expires_at ? Date.parse(answer.expires_at) : NaN;
  if (!Number.isFinite(t)) return 7;
  return Math.max(1, Math.round((t - now) / 86_400_000));
}
