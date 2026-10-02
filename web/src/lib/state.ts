// state.ts: who is signed in, in which org, with which role (S24 §3.7). GET /v1/me loads once;
// GET /v1/org adds the Stop all state, and org.stopped / org.started refresh it. Roles only shape
// affordances: the API enforces every rule regardless.

import { api, configure } from './api.ts';
import type { Me, Org, Role } from './types.ts';

export interface State {
  me: Me;
  org: Org;
}

let current: State | null = null;
let loading: Promise<State> | null = null;
const listeners = new Set<(s: State) => void>();

const RANK: Record<Role, number> = { viewer: 1, admin: 2, owner: 3 };

export function atLeast(role: Role | undefined, need: Role): boolean {
  return !!role && RANK[role] >= RANK[need];
}

export function get(): State | null {
  return current;
}

export function onChange(fn: (s: State) => void): () => void {
  listeners.add(fn);
  if (current) fn(current);
  return () => listeners.delete(fn);
}

function publish(s: State): void {
  current = s;
  for (const fn of listeners) fn(s);
}

async function fetchMe(): Promise<Me> {
  return api<Me>('GET', '/v1/me');
}

/** load reads /v1/me and /v1/org once; later callers share the answer. */
export function load(): Promise<State> {
  if (loading) return loading;
  configure({
    csrf: () => current?.me.csrf_token ?? null,
    refreshCsrf: async () => {
      const me = await fetchMe();
      if (current) publish({ ...current, me });
    },
  });
  loading = (async () => {
    const [me, org] = await Promise.all([fetchMe(), api<Org>('GET', '/v1/org')]);
    const s = { me, org };
    publish(s);
    return s;
  })();
  loading.catch(() => {
    loading = null;
  });
  return loading;
}

/** refreshOrg rereads /v1/org (the Stop all banner). */
export async function refreshOrg(): Promise<void> {
  if (!current) return;
  const org = await api<Org>('GET', '/v1/org');
  publish({ ...current, org });
}

export function role(): Role | undefined {
  return current?.me.membership.role;
}
