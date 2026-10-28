// DL-143 Connect: the links equal internal/installlinks' generated set, no secret ever reaches a
// link or a QR code, and each tile's status follows the evidence.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { COMMANDS, LINKS, SERVER, TILES, desktopConfig, hasSecret, mcpAddress, orderedTiles, queryEscape, serverText, stateOf } from '../src/lib/connect.ts';
import { safeHref } from '../src/lib/dom.ts';
import { repo } from './helpers.ts';

const gen = JSON.parse(readFileSync(repo('examples/play/install-links.json'), 'utf8')) as { links: { id: string; url: string }[] };
const url = (id: string) => gen.links.find((l) => l.id === id)!.url;
const tile = (id: string) => TILES.find((t) => t.id === id)!;

describe('install links', () => {
  it('equal the Go generator byte for byte', () => {
    expect(LINKS.cursor()).toBe(url('cursor'));
    expect(LINKS.cursorWeb()).toBe(url('cursor-web'));
    expect(LINKS.vscode()).toBe(url('vs-code'));
    expect(LINKS.vscodeWeb()).toBe(url('vs-code-web'));
  });
  it('decode to the one server object and pass safeHref', () => {
    const cfg = new URL(LINKS.cursor().replace('cursor://', 'https://')).searchParams.get('config')!;
    expect(JSON.parse(atob(cfg))).toEqual(SERVER);
    for (const l of Object.values(LINKS)) expect(safeHref(l())).not.toBeNull();
    expect(JSON.parse(desktopConfig()).mcpServers.deedglass).toEqual(SERVER);
    expect(COMMANDS['claude-code']).toContain('npx -y deedglass mcp serve');
  });
  it('escape like url.QueryEscape', () => {
    expect(queryEscape("a b!'()*~")).toBe('a+b%21%27%28%29%2A~');
  });
  it('carry no secret, and the guard catches one', () => {
    for (const l of Object.values(LINKS)) expect(hasSecret(l())).toBe(false);
    expect(hasSecret(mcpAddress('http://127.0.0.1:8080/'))).toBe(false);
    expect(mcpAddress('http://127.0.0.1:8080/')).toBe('http://127.0.0.1:8080/mcp/native');
    expect(hasSecret('dg_live_ABCDEFGH1234')).toBe(true);
    expect(hasSecret('dg_test_ABCDEFGH1234')).toBe(true);
    expect(hasSecret('sk_ABCDEFGH1234')).toBe(true);
    expect(hasSecret('https://x.test/?token=abc')).toBe(true);
    expect(hasSecret('postgres://u:pw@db/x')).toBe(true);
  });
});

describe('tiles', () => {
  it('put the path first', () => {
    expect(orderedTiles('clinic')[0]!.id).toBe('scribe');
    expect(orderedTiles('build')[0]!.id).toBe('terminal');
    expect(orderedTiles(null).map((t) => t.id)).toEqual(TILES.map((t) => t.id));
    expect(new Set(orderedTiles('it').map((t) => t.id)).size).toBe(TILES.length);
  });
  it('go Not connected → Waiting → Connected', () => {
    const t = tile('cursor');
    expect(stateOf(t, {}).status).toBe('not_connected');
    expect(stateOf(t, { started: new Set(['cursor']) }).status).toBe('waiting');
    expect(stateOf(t, { agents: [{ id: 'agt_1', name: 'cursor', labels: {} }] }).status).toBe('waiting');
    const s = stateOf(t, { agents: [{ id: 'agt_1', name: 'my agent', labels: { client: 'cursor' }, last_action_at: '2026-10-28T04:00:00Z' }] });
    expect(s).toEqual({ status: 'connected', lastActionAt: '2026-10-28T04:00:00Z', detected: [] });
  });
  it('read systems from connectors and their first use', () => {
    const db = tile('database');
    expect(stateOf(db, {}).status).toBe('not_connected');
    expect(stateOf(db, { connectors: [{ type: 'postgres', name: 'clinicdb' }] }).status).toBe('waiting');
    expect(stateOf(db, { connectors: [{ type: 'postgres', name: 'clinicdb' }], recentTools: ['clinicdb.query'] }).status).toBe('connected');
  });
  it('let the server answer win', () => {
    expect(stateOf(tile('vscode'), { server: [{ id: 'vscode', status: 'connected', last_action_at: null }], started: new Set() }).status).toBe('connected');
  });
});

describe('serverText', () => {
  it('uses the local app\'s own text only when it holds no key', () => {
    expect(serverText("claude mcp add --scope user deedglass -- '/opt/deedglass' mcp serve --client terminal", 'x')).toContain('--client terminal');
    expect(serverText('cursor://anysphere.cursor-deeplink/mcp/install?name=deedglass&config=e30%3D', 'x')).toMatch(/^cursor:/);
    expect(serverText('https://x.test/?token=abc', 'fallback')).toBe('fallback');
    expect(serverText('{"env":{"DEEDGLASS_API_KEY":"dg_live_ABCDEFGH1234"}}', 'fallback')).toBe('fallback');
    expect(serverText(null, 'fallback')).toBe('fallback');
    expect(serverText('  ', 'fallback')).toBe('fallback');
  });
});

