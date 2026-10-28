// simple-api.ts: the typed client for the DL-143 local-app endpoints the CTO adds (WAR_ROOM API line
// of 2026-10-28). Each call answers null when the server doesn't have the route yet (404 or 405), so
// the screens fall back to what the existing API shows and nothing breaks on an older server.

import { api, ApiError } from './api.ts';
import type { ConnectItem, TileId } from './connect.ts';
import type { ListResponse } from './types.ts';
import type { Path } from './theme.ts';

export interface LocalInfo {
  object: 'local';
  local: boolean;
  address: string;
  first_run: boolean;
  path: Path | null;
}

const missing = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 405 || e.code === 'not_found' || e.code === 'route_not_found');

async function orNull<T>(p: Promise<T>): Promise<T | null> {
  try {
    return await p;
  } catch (e) {
    if (missing(e)) return null;
    throw e;
  }
}

/** getLocal: GET /v1/local, or null on a server that isn't the local app. */
export function getLocal(): Promise<LocalInfo | null> {
  return orNull(api<LocalInfo>('GET', '/v1/local'));
}

/** saveLocal: PATCH /v1/local with the first-run choice; null when the route is missing. */
export function saveLocal(body: { path?: Path; first_run?: boolean }): Promise<LocalInfo | null> {
  return orNull(api<LocalInfo>('PATCH', '/v1/local', { body }));
}

/** getConnect: GET /v1/connect, or null until the route lands. */
export async function getConnect(): Promise<ConnectItem[] | null> {
  const r = await orNull(api<ListResponse<ConnectItem>>('GET', '/v1/connect'));
  return r ? r.data : null;
}

/** startConnect: POST /v1/connect/{id}/start (waiting; for terminal agents, guard them). */
export function startConnect(id: TileId): Promise<ConnectItem | null> {
  return orNull(api<ConnectItem>('POST', `/v1/connect/${id}/start`, { body: {} }));
}
