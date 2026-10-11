// models/connector.ts: the Console's view of S16's connector objects, S17's readiness checks and
// S20's decoder (S26 §3.3). Names are the API's as shipped (test/fixtures/s26/connector-*.json,
// decoder-postgres.json); this file adapts, the API never changes.
//
// Adapters (recorded in the S26 report):
// - The witness card reads GET /v1/connectors/{id}/decoder (S20's decoder object, viewer+), which
//   already carries status, health, lag and the last commit from deedglass.decoder_slots. No new
//   `witness` block was added to the connector object, and nothing is read live from the customer
//   database on page load.
// - The Postgres readiness panel is S17's connector_test for a postgres connector (POST …/test);
//   "Ready for Proven checks" when no check fails.
// - The connector's `health` is {status: unknown|pass|warn|fail, tested_at}.

import { connectorsCopy as c } from '../copy-connectors.ts';
import { fill } from '../copy.ts';
import { number } from '../format.ts';

export interface ConnectorTool { object: 'tool'; name: string; description?: string; side_effect: 'read' | 'write' | 'destructive' | 'unknown' | string; idempotent?: boolean; levels?: string[] }
export interface Credential { object: 'credential'; connector: string; set: boolean; kind: string | null; fields?: string[]; hint: string | null; version: number | null; kek_id?: string | null; updated_at: string | null; updated_by?: string | null }
export interface Connector {
  object: 'connector';
  id: string;
  type: string;
  name: string;
  description: string | null;
  status: string;
  config: Record<string, unknown>;
  config_hash: string;
  tools: ConnectorTool[];
  credentials: Credential | null;
  health: { status: 'unknown' | 'pass' | 'warn' | 'fail' | string; tested_at: string | null } | null;
  created_at: string;
  updated_at: string;
}
export interface CredentialKind { kind: string; fields: { name: string; secret: boolean }[] }
export interface ConnectorType {
  object: 'connector_type';
  type: string;
  title: string;
  description: string;
  levels: string[];
  credential_kinds: CredentialKind[];
  config_schema: Record<string, unknown>;
  docs_url?: string;
}
export interface Check { id: string; title: string; status: 'pass' | 'warn' | 'fail' | 'skip'; detail?: string; fix?: string; fix_sql?: string; doc_url?: string }
export interface ConnectorTest { object: 'connector_test'; connector: string; status: 'pass' | 'warn' | 'fail'; duration_ms: number; checks: Check[] }
export interface DecoderView {
  object: 'decoder';
  connector: string;
  name: string;
  slot: string;
  status: string;
  health: string;
  reason: string | null;
  detail: Record<string, unknown>;
  since: string;
  positions: { restart_lsn: string | null; confirmed_flush_lsn: string | null };
  lag: { bytes: number | null; seconds: number | null; retained_bytes: number | null };
  wal_status: string | null;
  watching: string[];
  last_commit: { lsn: string; at: string | null } | null;
  last_heartbeat_at: string | null;
}

export const TYPE_ICON: Record<string, string> = {
  postgres: 'database', rest: 'globe', github: 'git-pull-request', stripe: 'credit-card', slack: 'message-square',
  gmail: 'mail', notion: 'file-text', fake: 'flask-conical', mcp_http: 'plug',
};

export function typeIcon(t: string): string {
  return TYPE_ICON[t] ?? 'plug';
}

export type Health = 'pass' | 'warn' | 'fail' | 'unknown';

export function healthOf(x: Pick<Connector, 'health'>): Health {
  const s = x.health?.status;
  return s === 'pass' || s === 'warn' || s === 'fail' ? s : 'unknown';
}

export function healthLabel(h: Health): { glyph: string; text: string } {
  switch (h) {
    case 'pass': return { glyph: '✓', text: c.health.pass };
    case 'warn': return { glyph: '○', text: c.health.warn };
    case 'fail': return { glyph: '✕', text: c.health.fail };
    default: return { glyph: '–', text: c.health.unknown };
  }
}

/** levelsOf: the levels the connector's tools reach (the union), in L0, L1, L2 order. */
export function levelsOf(x: Pick<Connector, 'tools'>, typeLevels?: string[]): string[] {
  const set = new Set<string>(typeLevels ?? []);
  for (const t of x.tools ?? []) for (const l of t.levels ?? []) set.add(l);
  return ['L0', 'L1', 'L2'].filter((l) => set.has(l));
}

/** bytes: 432013312 → "412 MB" (binary units, written as MB like the CLI). */
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '·';
  if (n < 1024) return `${number(n)} B`;
  if (n < 1024 ** 2) return `${number(Math.round(n / 1024))} KB`;
  if (n < 1024 ** 3) return `${number(Math.round(n / 1024 ** 2))} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

export type WitnessState = 'healthy' | 'lagging' | 'inactive' | 'invalidated' | 'not_created';

/** witnessState maps S20's decoder object onto the card's five states (S26 §3.3). */
export function witnessState(d: DecoderView | null): WitnessState {
  if (!d) return 'not_created';
  if (d.wal_status === 'lost' || d.health === 'halted' || d.status === 'dropped') return 'invalidated';
  if (d.health === 'healthy') return 'healthy';
  if (d.health === 'degraded') return 'lagging';
  if (d.health === 'disabled') return 'not_created';
  return 'inactive'; // pending, connecting, waiting
}

/** witnessLine: `Witness · healthy · 12 MB behind · last commit 4s ago`. */
export function witnessLine(d: DecoderView | null, now = new Date()): string {
  const st = witnessState(d);
  if (!d || st === 'not_created') return c.witness.notCreated;
  const parts: string[] = [c.witness.word, c.witness.state[st] ?? st];
  parts.push(fill(c.witness.behind, { size: bytes(d.lag.bytes ?? 0) }));
  if (d.last_commit?.at) parts.push(fill(c.witness.lastCommit, { ago: ago(d.last_commit.at, now) }));
  return parts.join(' · ');
}

export function ago(iso: string, now = new Date()): string {
  const s = Math.max(0, Math.round((now.getTime() - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

/** credentialLine: S16's row copy, `Set · bearer · …9f2c · updated 2h ago`, or `Not set`. */
export function credentialLine(cr: Credential | null, now = new Date()): string {
  if (!cr || !cr.set) return c.credentials.notSet;
  const parts: string[] = [c.credentials.set];
  if (cr.kind) parts.push(cr.kind);
  if (cr.hint) parts.push(cr.hint);
  if (cr.updated_at) parts.push(fill(c.credentials.updated, { ago: ago(cr.updated_at, now) }));
  return parts.join(' · ');
}

/** sideEffect: read and write neutral, destructive in a red outline (the one allowed red). */
export function sideEffectClass(s: string): string {
  return s === 'destructive' ? 'chip chip-denied' : s === 'write' ? 'chip chip-neutral side-write' : 'chip chip-outline';
}
