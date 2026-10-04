// @vitest-environment happy-dom
// S29: the simulated-org banner is drawn at shell boot from the last org's flag (theme.ts
// getSimHint), before GET /v1/org answers, and GET /v1/org's settings.simulated then confirms or
// removes it and updates the flag. Nothing waits for Insights' settings any more.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { copy } from '../src/lib/copy.ts';
import { getSimHint, setSimHint, SIM_KEY } from '../src/lib/theme.ts';

const me = {
  object: 'me', user: { object: 'user', id: 'usr_1', email: 'priya@acme.example', name: 'Priya Raman', created_at: '2026-10-01T00:00:00Z' },
  org: { object: 'org', id: 'org_1', name: 'Acme', slug: 'acme', timezone: 'Asia/Kolkata', status: 'active', created_at: '2026-10-01T00:00:00Z' },
  membership: { object: 'membership', id: 'mem_1', role: 'owner', status: 'active' },
  orgs: [{ id: 'org_1', name: 'Acme', slug: 'acme', role: 'owner' }],
  session: { id: 'ses_1', idle_expires_at: '2026-10-05T00:00:00Z', expires_at: '2026-10-05T00:00:00Z' },
  csrf_token: 'c',
};
const org = (simulated: boolean) => ({
  ...me.org, retention_days: 90, settings: { approvals: { email_links: 'decide', email_detail: 'summary', self_countersign: 'deny' }, ...(simulated ? { simulated: true } : {}) },
  stop: { stopped: false, stopped_at: null, stopped_by: null, reason: null },
});

class FakeES {
  readyState = 0;
  onerror = null;
  onopen = null;
  addEventListener() {}
  close() {}
}

/** boot mounts a fresh shell (fresh module state) against a stubbed API; resolve() lets GET /v1/org answer. */
async function boot(simulated: boolean) {
  vi.resetModules();
  document.body.replaceChildren();
  const host = document.createElement('div');
  host.id = 'banners';
  document.body.append(host);
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  vi.stubGlobal('EventSource', FakeES);
  vi.stubGlobal('fetch', async (u: string) => {
    const path = new URL(u, 'http://x').pathname;
    if (path === '/v1/org') await gate;
    const body = path === '/v1/me' ? me : path === '/v1/org' ? org(simulated) : { object: 'list', data: [], has_more: false };
    return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
  });
  const { bootShell } = await import('../src/controllers/shell.ts');
  bootShell({ title: 'Insights' });
  const ready = new Promise((r) => document.addEventListener('dg:ready', r, { once: true }));
  return { resolve: async () => (release(), await ready, await new Promise((r) => setTimeout(r, 0))) };
}
const banner = () => document.getElementById('banner-simulated');

describe('simulated banner (S29)', () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.unstubAllGlobals());

  it('the hint reads and writes one flag, and survives blocked storage', () => {
    expect(getSimHint()).toBe(false);
    setSimHint(true);
    expect(localStorage.getItem(SIM_KEY)).toBe('1');
    expect(getSimHint()).toBe(true);
    setSimHint(false);
    expect(localStorage.getItem(SIM_KEY)).toBeNull();
    const blocked = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); }, removeItem: () => { throw new Error('blocked'); } };
    expect(getSimHint(blocked)).toBe(false);
    expect(() => setSimHint(true, blocked)).not.toThrow();
    expect(getSimHint(null)).toBe(false);
  });

  it('a remembered simulated org: the banner is there at boot, before GET /v1/org, and stays', async () => {
    setSimHint(true);
    const s = await boot(true);
    expect(banner()?.textContent).toBe(copy.states.simulatedBanner);
    expect(banner()?.getAttribute('role')).toBe('note');
    await s.resolve();
    expect(banner()).not.toBeNull();
    expect(getSimHint()).toBe(true);
  });

  it('a first visit: GET /v1/org settings.simulated draws it and remembers it', async () => {
    const s = await boot(true);
    expect(banner()).toBeNull();
    await s.resolve();
    expect(banner()?.textContent).toBe(copy.states.simulatedBanner);
    expect(getSimHint()).toBe(true);
  });

  it('a real org after a simulated one: GET /v1/org removes it and clears the flag', async () => {
    setSimHint(true);
    const s = await boot(false);
    expect(banner()).not.toBeNull();
    await s.resolve();
    expect(banner()).toBeNull();
    expect(getSimHint()).toBe(false);
  });
});
