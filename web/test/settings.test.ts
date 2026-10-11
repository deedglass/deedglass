// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest';
import { SETTINGS_SECTIONS } from '../src/lib/nav.ts';
import {
  APPROVAL_DEFAULTS, approvalSettings, approvalsPatch, eventFamilies, filterZones, gates, hostOf, inviteDays, memberActions,
  parseRetention, parseSection, retentionHelp, roleChangeNeedsConfirm, savedToast, SECTION_KEYS, sectionNeeds, sectionPath,
  testLine, WEBHOOK_EVENTS, zoneList,
} from '../src/lib/settings.ts';
import type { Role } from '../src/lib/types.ts';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import members from './fixtures/s26/members.json';
import org from './fixtures/s26/org.json';

describe('settings: sections and URLs', () => {
  it('maps every section key to its URL and back', () => {
    for (const k of SECTION_KEYS) {
      expect(sectionPath(k)).toBe(`/app/settings/${k}`);
      expect(parseSection(sectionPath(k))).toEqual({ key: k, canonical: `/app/settings/${k}` });
      expect(parseSection(`/app/settings/${k}/`).key).toBe(k);
    }
  });
  it('the bare path and unknown sections are the list', () => {
    expect(sectionPath(null)).toBe('/app/settings');
    expect(parseSection('/app/settings')).toEqual({ key: null, canonical: '/app/settings' });
    expect(parseSection('/app/settings/')).toEqual({ key: null, canonical: '/app/settings' });
    expect(parseSection('/app/settings/nope')).toEqual({ key: null, canonical: '/app/settings' });
    expect(parseSection('/app/settings/Org').key).toBeNull(); // the server's ^[a-z][a-z0-9-]{0,31}$
    expect(parseSection('/app/settings/members/x').key).toBeNull();
    expect(parseSection('/app/settingsx').key).toBeNull();
  });
  it('accepts the old key org as an alias for General and rewrites the URL', () => {
    expect(parseSection('/app/settings/org')).toEqual({ key: 'general', canonical: '/app/settings/general' });
  });
  it('the sub-nav and the palette list the same sections in the same order', () => {
    expect(SETTINGS_SECTIONS.map((s) => s.key)).toEqual([...SECTION_KEYS]);
  });
});

describe('settings: role gates (S26 §3.5)', () => {
  const pick = (r: Role | undefined) => gates(r);
  it('viewer: reads, changes nothing', () => {
    const g = pick('viewer');
    expect(g).toMatchObject({ editGeneral: false, editRetention: false, invite: false, changeRoles: false, removeMembers: false, orgKeys: false,
      readApprovals: false, editApprovals: false, webhooks: false, notifications: false, stopAll: false, startAll: false, sealKeys: true, exportDeeds: true });
    expect(g.inviteRoles).toEqual([]);
  });
  it('admin: invites viewers and admins, org keys, webhooks, reads approvals, Stop all; no roles, retention or Start all', () => {
    const g = pick('admin');
    expect(g).toMatchObject({ editGeneral: true, editRetention: false, invite: true, changeRoles: false, removeMembers: true, orgKeys: true,
      readApprovals: true, editApprovals: false, webhooks: true, notifications: true, stopAll: true, startAll: false, sealKeys: true });
    expect(g.inviteRoles).toEqual(['viewer', 'admin']);
  });
  it('owner: everything, and can invite owners', () => {
    const g = pick('owner');
    expect(Object.entries(g).filter(([, v]) => v === false)).toEqual([]);
    expect(g.inviteRoles).toEqual(['viewer', 'admin', 'owner']);
  });
  it('signed out: nothing', () => {
    const g = pick(undefined);
    expect(g.sealKeys).toBe(false);
    expect(g.inviteRoles).toEqual([]);
  });
  it('the sections that need Admin before they show anything', () => {
    expect(SECTION_KEYS.filter((k) => sectionNeeds(k) === 'admin')).toEqual(['keys', 'approvals', 'webhooks', 'notifications']);
  });
  it('member actions: leave yourself (Admin up), remove others, an owner only by an owner, resend invites', () => {
    const [owner, admin, viewer] = members.data as unknown as { id: string; role: Role; status: string; user: { id: string } }[];
    const as = (role: Role, who: { user: { id: string } }) => ({ role, userId: who.user.id });
    expect(memberActions(as('viewer', viewer!), viewer!)).toEqual([]); // the remove route needs Admin
    expect(memberActions(as('viewer', viewer!), admin!)).toEqual([]);
    expect(memberActions(as('admin', admin!), admin!)).toEqual(['leave']);
    expect(memberActions(as('admin', admin!), owner!)).toEqual([]);
    expect(memberActions(as('admin', admin!), viewer!)).toEqual(['remove']);
    expect(memberActions(as('owner', owner!), admin!)).toEqual(['remove']);
    expect(memberActions(as('owner', owner!), owner!)).toEqual(['leave']);
    const invited = { ...viewer!, status: 'invited', user: { id: 'usr_new' } };
    expect(memberActions(as('admin', admin!), invited)).toEqual(['resend', 'remove']);
    expect(memberActions(as('admin', admin!), { ...invited, role: 'owner' })).toEqual([]);
    expect(memberActions(as('owner', owner!), { ...invited, role: 'owner' })).toEqual(['resend', 'remove']);
  });
  it('making or unmaking an owner asks first', () => {
    expect(roleChangeNeedsConfirm('viewer', 'owner')).toBe(true);
    expect(roleChangeNeedsConfirm('owner', 'admin')).toBe(true);
    expect(roleChangeNeedsConfirm('viewer', 'admin')).toBe(false);
    expect(roleChangeNeedsConfirm('owner', 'owner')).toBe(false);
  });
});

describe('settings: time zones', () => {
  const zones = ['UTC', 'America/New_York', 'America/Argentina/Buenos_Aires', 'Asia/Kolkata', 'Europe/London', 'Pacific/Port_Moresby'];
  it('matches every word, ignoring case, underscores and slashes', () => {
    expect(filterZones(zones, 'new york')).toEqual(['America/New_York']);
    expect(filterZones(zones, 'NEW_YORK')).toEqual(['America/New_York']);
    expect(filterZones(zones, 'kolk')).toEqual(['Asia/Kolkata']);
    expect(filterZones(zones, 'america')).toEqual(['America/New_York', 'America/Argentina/Buenos_Aires']);
    expect(filterZones(zones, 'buenos argentina')).toEqual(['America/Argentina/Buenos_Aires']);
    expect(filterZones(zones, 'port moresby')).toEqual(['Pacific/Port_Moresby']);
    expect(filterZones(zones, 'utc')).toEqual(['UTC']);
  });
  it('an empty query keeps every zone; no match is empty', () => {
    expect(filterZones(zones, '   ')).toEqual(zones);
    expect(filterZones(zones, 'atlantis')).toEqual([]);
  });
  it('the list comes from Intl, with UTC first and the current zone always in it', () => {
    const all = zoneList('Etc/GMT+5');
    expect(all[0]).toBe('UTC');
    expect(all).toContain('Etc/GMT+5');
    expect(all).toContain('Asia/Kolkata');
    expect(new Set(all).size).toBe(all.length);
  });
});

describe('settings: values and copy', () => {
  it('retention help is S26 §5 verbatim with the number filled in', () => {
    expect(retentionHelp(90)).toBe('After 90 days, request and result bodies are removed. Records hold digests only, so they keep verifying.');
    expect(retentionHelp(1)).toBe('After 1 day, request and result bodies are removed. Records hold digests only, so they keep verifying.');
    expect(retentionHelp(3650)).toContain('After 3,650 days,');
  });
  it('retention is a whole number from 1 to 3650', () => {
    expect(parseRetention('90')).toBe(90);
    expect(parseRetention(' 1 ')).toBe(1);
    expect(parseRetention('3650')).toBe(3650);
    for (const bad of ['0', '3651', '-1', '1.5', '', 'abc', '1e3']) expect(parseRetention(bad), bad).toBeNull();
  });
  it('approval settings default to decide, summary, deny and patch only what changed', () => {
    expect(approvalSettings(undefined)).toEqual(APPROVAL_DEFAULTS);
    expect(approvalSettings({ email_links: 'review' })).toEqual({ ...APPROVAL_DEFAULTS, email_links: 'review' });
    const before = approvalSettings(null);
    expect(approvalsPatch(before, { ...before })).toBeNull();
    expect(approvalsPatch(before, { ...before, self_countersign: 'allow', email_detail: 'full' })).toEqual({ self_countersign: 'allow', email_detail: 'full' });
  });
  it('saving says whether a deed was sealed', () => {
    expect(savedToast({ deed: 'deed_01' })).toBe('Saved · record sealed');
    expect(savedToast({ deed: null })).toBe('Saved');
    expect(savedToast(undefined)).toBe('Saved');
  });
  it('invites last 7 days unless the answer says otherwise', () => {
    expect(inviteDays(members.data[0] as unknown as { expires_at?: string })).toBe(7); // S07 answers no expiry
    const now = Date.parse('2026-10-04T00:00:00Z');
    expect(inviteDays({ expires_at: '2026-10-07T00:00:00Z' }, now)).toBe(3);
  });
  it('a webhook test reads like S22: delivered with status and time, or the failure', () => {
    expect(testLine({ delivered: true, response_status: 200, response_ms: 184, error: null })).toEqual({ ok: true, text: '✓ Delivered webhook.test · 200 in 184 ms' });
    expect(testLine({ delivered: false, response_status: 500, response_ms: 1200, error: null })).toEqual({ ok: false, text: '✕ Not delivered · 500 in 1,200 ms' });
    expect(testLine({ delivered: false, response_status: 0, response_ms: 0, error: 'connection refused' }).text).toBe('✕ Not delivered · connection refused');
  });
  it('the delete confirmation types the host', () => {
    expect(hostOf('https://hooks.acme.com/deedglass?x=1')).toBe('hooks.acme.com');
    expect(hostOf('http://127.0.0.1:9000/h')).toBe('127.0.0.1:9000');
    expect(hostOf('not a url')).toBe('not a url');
  });
  it('the event picker offers every event the server accepts, grouped by family', () => {
    const flat = eventFamilies().flatMap(([, e]) => e);
    expect(flat.sort()).toEqual([...WEBHOOK_EVENTS].sort());
    expect(eventFamilies().map(([f]) => f)).toEqual(['action', 'approval', 'deed', 'agent', 'org', 'policy', 'decoder', 'anomaly', 'suggestion', 'webhook']);
    expect(org.slug).toBe('acme');
  });
  it('the event list equals internal/ledger WebhookEvents', () => {
    const go = readFileSync(join(process.cwd(), '..', 'internal/ledger/events.go'), 'utf8'); // vitest runs in web/
    const consts = new Map([...go.matchAll(/(\w+)\s*=\s*"([a-z]+\.[a-z_]+)"/g)].map((m) => [m[1]!, m[2]!]));
    const block = /var WebhookEvents = \[\]string\{([^}]*)\}/.exec(go)?.[1] ?? '';
    const server = block.split(',').map((t) => t.trim()).filter(Boolean).map((t) => (t.startsWith('"') ? t.slice(1, -1) : consts.get(t) ?? t));
    expect([...server].sort()).toEqual([...WEBHOOK_EVENTS].sort());
  });
});

describe('settings: the webhook secret is shown once', () => {
  const SECRET = 'whsec_FAKEtestSECRETvalue0123456789abcdefghijklm';
  const leaks = (): string[] => {
    const out: string[] = [];
    const walk = (n: Node) => {
      if (n.nodeType === Node.TEXT_NODE && (n.nodeValue ?? '').includes(SECRET)) out.push('text');
      if (n instanceof Element) {
        for (const a of [...n.attributes]) if (a.value.includes(SECRET)) out.push(`${n.tagName}[${a.name}]`);
        if ((n instanceof HTMLInputElement || n instanceof HTMLTextAreaElement) && n.value.includes(SECRET)) out.push(`${n.tagName}.value`);
      }
      n.childNodes.forEach(walk);
    };
    walk(document.documentElement);
    return out;
  };

  it('Done is gated on "I stored it", and after Done no node, attribute or value keeps the secret', async () => {
    const { showWebhookSecretSheet } = await import('../src/controllers/settings.ts');
    let doneCalls = 0;
    const ov = showWebhookSecretSheet({ url: 'https://hooks.acme.com/deedglass', secret: SECRET }, () => doneCalls++);
    expect(leaks().length).toBeGreaterThan(0); // shown once
    expect(ov.el.textContent).toContain('Signing secret (shown once)');
    expect(ov.el.textContent).toContain("Store it now. Deedglass keeps only an encrypted copy and can't show it again.");
    const done = [...ov.el.querySelectorAll('button')].find((b) => b.textContent === 'Done')!;
    expect(done.disabled).toBe(true);
    const box = ov.el.querySelector<HTMLInputElement>('input[name="stored"]')!;
    box.checked = true;
    box.dispatchEvent(new Event('change'));
    expect(done.disabled).toBe(false);
    done.click();
    expect(leaks()).toEqual([]);
    await new Promise((r) => setTimeout(r, 250));
    expect(leaks()).toEqual([]);
    expect(doneCalls).toBe(1);
    expect(document.querySelector('[data-secret]')).toBeNull();
  });

  it('Escape wipes the secret too', async () => {
    const { showWebhookSecretSheet } = await import('../src/controllers/settings.ts');
    showWebhookSecretSheet({ url: 'https://hooks.acme.com/deedglass', secret: SECRET });
    expect(leaks().length).toBeGreaterThan(0);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(leaks()).toEqual([]);
    await new Promise((r) => setTimeout(r, 250));
    expect(leaks()).toEqual([]);
  });

  it('a replayed create (secret null) shows no value and says why', async () => {
    const { showWebhookSecretSheet } = await import('../src/controllers/settings.ts');
    const ov = showWebhookSecretSheet({ url: 'https://hooks.acme.com/deedglass', secret: null });
    expect(ov.el.querySelector('[data-secret]')).toBeNull();
    expect(ov.el.textContent).toContain("the secret isn't shown again");
    ov.close();
  });
});
