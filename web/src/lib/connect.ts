// connect.ts: the Connect screen's model (DL-143). The tiles, their install links and settings,
// and each tile's status. Links mirror internal/installlinks (one server object: npx runs the npm
// package, which runs mcp serve and finds the key itself), so no link, setting or QR code ever
// carries a key. When the server answers GET /v1/connect, its links and statuses win; until then
// the status comes from what the existing API already shows (agents, connectors, recent actions).
// Pure: no DOM, no fetch (test/connect.test.ts).

import type { BrandMarkId } from '../icons/brandmarks.gen.ts';
import type { Path } from './theme.ts';

export type TileId = 'claude-desktop' | 'cursor' | 'vscode' | 'terminal' | 'openai' | 'scribe' | 'database' | 'mcp';
export type Status = 'not_connected' | 'waiting' | 'connected';
export type Group = 'ai' | 'systems';

export interface TileDef {
  id: TileId;
  group: Group;
  /** the Lucide icon, for a tile without a brand mark */
  icon: string;
  /** the real marks on the tile, left to right (src/icons/brand/marks.json) */
  marks: BrandMarkId[];
  /** a short text mark when there is no drawable one (FHIR's flame isn't published as a vector) */
  monogram?: string;
  /** client names an agent may carry (labels.client or its name) */
  match: string[];
  /** the connector type a system tile adds */
  connectorType?: string;
}

export const TILES: readonly TileDef[] = [
  { id: 'claude-desktop', group: 'ai', icon: 'message-square', marks: ['claude'], match: ['claude-desktop', 'claude desktop', 'claude-ai'] },
  { id: 'cursor', group: 'ai', icon: 'mouse-pointer-2', marks: ['cursor'], match: ['cursor'] },
  { id: 'vscode', group: 'ai', icon: 'code-xml', marks: ['vscode', 'copilot'], match: ['vscode', 'vs-code', 'visual studio code', 'copilot'] },
  { id: 'terminal', group: 'ai', icon: 'square-terminal', marks: ['claude', 'openai', 'gemini'], match: ['claude-code', 'claude code', 'codex', 'gemini', 'coding-agents'] },
  { id: 'openai', group: 'ai', icon: 'bot', marks: ['openai'], match: ['openai', 'chatgpt', 'agents-sdk', 'openai-agents'] },
  { id: 'mcp', group: 'ai', icon: 'plug', marks: [], match: ['mcp'] },
  { id: 'scribe', group: 'systems', icon: 'stethoscope', marks: [], monogram: 'FHIR', match: ['scribe'], connectorType: 'fhir' },
  { id: 'database', group: 'systems', icon: 'database', marks: ['postgresql', 'mysql'], match: [], connectorType: 'postgres' },
];

/** INSTALLABLE are the tiles whose Connect button writes a settings file on this computer (the server says which; this is the fallback). */
export const INSTALLABLE: readonly TileId[] = ['claude-desktop', 'cursor', 'vscode', 'terminal'];

/** SAMPLE_FHIR is a public FHIR test server with made-up records, for "Try with sample data" (never real data). */
export const SAMPLE_FHIR = 'https://hapi.fhir.org/baseR4';

/** ORDER puts the tiles a path needs first; the rest keep their order. */
const ORDER: Record<Path, TileId[]> = {
  clinic: ['scribe', 'claude-desktop', 'openai'],
  build: ['terminal', 'cursor', 'vscode', 'claude-desktop', 'database'],
  it: ['scribe', 'database', 'openai'],
};

export function orderedTiles(path: Path | null): TileDef[] {
  const first = path ? ORDER[path] : [];
  const rank = (t: TileDef) => (first.includes(t.id) ? first.indexOf(t.id) : 100 + TILES.indexOf(t));
  return [...TILES].sort((a, b) => rank(a) - rank(b));
}

// ---------- links and settings (internal/installlinks, kept equal by test/connect.test.ts) ----------

export const SERVER_NAME = 'deedglass';
export const SERVER = { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } as const;
export const SERVER_JSON = JSON.stringify(SERVER);
export const CLAUDE_DESKTOP_GUIDE = 'https://docs.deedglass.com/get-started/claude-desktop/';

function b64(s: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(s)));
}

/** queryEscape matches Go's url.QueryEscape for the characters these links hold. */
export function queryEscape(s: string): string {
  return encodeURIComponent(s).replace(/%20/g, '+').replace(/[!'()*]/g, (ch) => '%' + ch.charCodeAt(0).toString(16).toUpperCase());
}

const withName = () => `{"name":"${SERVER_NAME}",` + SERVER_JSON.slice(1);

export const LINKS = {
  cursor: () => `cursor://anysphere.cursor-deeplink/mcp/install?name=${SERVER_NAME}&config=${queryEscape(b64(SERVER_JSON))}`,
  cursorWeb: () => `https://cursor.com/en/install-mcp?name=${SERVER_NAME}&config=${queryEscape(b64(SERVER_JSON))}`,
  vscode: () => `vscode:mcp/install?${queryEscape(withName())}`,
  // url.Values.Encode sorts the keys: config before name.
  vscodeWeb: () => `https://vscode.dev/redirect/mcp/install?config=${queryEscape(SERVER_JSON)}&name=${SERVER_NAME}`,
};

export const COMMANDS: Record<'claude-code' | 'codex' | 'gemini', string> = {
  'claude-code': 'claude mcp add --scope user deedglass -- npx -y deedglass mcp serve',
  codex: 'codex mcp add deedglass -- npx -y deedglass mcp serve',
  gemini: JSON.stringify({ mcpServers: { deedglass: SERVER } }, null, 2),
};

/** desktopConfig is what Claude Desktop's config file needs. */
export function desktopConfig(): string {
  return JSON.stringify({ mcpServers: { [SERVER_NAME]: SERVER } }, null, 2);
}

/** mcpAddress is the gateway's native MCP endpoint on this server (S15); the key goes in a header. */
export function mcpAddress(origin: string): string {
  return origin.replace(/\/+$/, '') + '/mcp/native';
}

/** hasSecret refuses anything that looks like a key, so no link or QR code can carry one. */
export function hasSecret(s: string): boolean {
  return /\b(?:sk|dg|ak|key)_(?:[a-z]+_)?[A-Za-z0-9]{8,}/.test(s) || /[?&](?:key|token|secret|password|api_key)=/i.test(s) || /:\/\/[^/@\s]+:[^/@\s]+@/.test(s);
}

// ---------- status ----------

/** The server's answer to GET /v1/connect (DL-143; the CTO's endpoint). */
/** serverText: a link, config or command from GET /v1/connect, used only when it holds no key; else the fallback. */
export function serverText(s: string | null | undefined, fallback: string): string {
  return typeof s === 'string' && s.trim() !== '' && !hasSecret(s) ? s : fallback;
}

/** Install is the tool's settings file on this computer and what the Connect button would do to it. */
export interface Install {
  tool: 'claude-desktop' | 'cursor' | 'vscode' | 'gemini';
  title: string;
  file: string;
  installed: boolean;
  status: 'add' | 'update' | 'present' | 'skip' | 'absent';
  reason?: string;
  entry: string;
  backup?: string;
  can_undo: boolean;
}

export interface ConnectItem {
  id: TileId;
  name?: string;
  status: Status;
  agent?: string | null;
  last_action_at?: string | null;
  detected?: string[];
  link?: string | null;
  web_link?: string | null;
  config?: string | null;
  command?: string | null;
  address?: string | null;
  install?: Install | null;
}

export interface AgentLite { id: string; name: string; labels?: Record<string, string>; last_used_at?: string | null; last_action_at?: string | null }
export interface ConnectorLite { type: string; name: string; health?: { status: string } | null }

export interface Evidence {
  server?: ConnectItem[] | null;
  agents?: AgentLite[];
  connectors?: ConnectorLite[];
  /** tools of recent actions ("ehr.create"), to see a system's first use */
  recentTools?: string[];
  /** tiles the person started in this page (pressed the primary button) */
  started?: ReadonlySet<TileId>;
}

export interface TileState { status: Status; lastActionAt: string | null; detected: string[]; install: Install | null }

function agentMatches(a: AgentLite, t: TileDef): boolean {
  const client = (a.labels?.client ?? a.labels?.['deedglass.client'] ?? '').toLowerCase();
  const name = a.name.toLowerCase();
  return t.match.some((m) => client === m || name === m || name.includes(m));
}

/** stateOf decides one tile's status from the evidence. The server's answer wins when present. */
export function stateOf(t: TileDef, e: Evidence): TileState {
  const s = e.server?.find((x) => x.id === t.id);
  if (s) return { status: s.status, lastActionAt: s.last_action_at ?? null, detected: s.detected ?? [], install: s.install ?? null };
  if (t.connectorType) {
    const mine = (e.connectors ?? []).filter((x) => x.type === t.connectorType);
    if (mine.length === 0) return { status: e.started?.has(t.id) ? 'waiting' : 'not_connected', lastActionAt: null, detected: [], install: null };
    const used = mine.some((x) => (e.recentTools ?? []).some((tool) => tool.startsWith(x.name + '.')));
    return { status: used ? 'connected' : 'waiting', lastActionAt: null, detected: [], install: null };
  }
  // The "Any MCP tool" tile owns every agent no named tile claims.
  const agents = t.id === 'mcp'
    ? (e.agents ?? []).filter((a) => !TILES.some((o) => o.id !== 'mcp' && agentMatches(a, o)))
    : (e.agents ?? []).filter((a) => agentMatches(a, t));
  const last = agents.map((a) => a.last_action_at ?? a.last_used_at ?? null).filter((x): x is string => !!x).sort().pop() ?? null;
  if (last) return { status: 'connected', lastActionAt: last, detected: [], install: null };
  if (agents.length || e.started?.has(t.id)) return { status: 'waiting', lastActionAt: null, detected: [], install: null };
  return { status: 'not_connected', lastActionAt: null, detected: [], install: null };
}
