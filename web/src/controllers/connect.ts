// connect.ts: the Connect screen (DL-143). Tiles for the AI tools and systems people use; each
// opens a sheet with at most three numbered steps, one primary action (a deep link where the app
// has one), a copy-the-settings fallback, an optional QR code for another computer and a live
// status (Not connected → Waiting for first action → Connected). No link, setting or QR code holds
// a key: lib/connect.ts builds them from the public server object and refuses anything key-shaped.

import { api } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { simpleCopy as sc } from '../lib/copy-simple.ts';
import {
  COMMANDS, CLAUDE_DESKTOP_GUIDE, LINKS, SERVER_JSON, desktopConfig, hasSecret, mcpAddress, serverText, orderedTiles, stateOf,
  type AgentLite, type ConnectItem, type ConnectorLite, type Evidence, type Status, type TileDef, type TileId, type TileState,
} from '../lib/connect.ts';
import { h, icon, maybe, replace, s, type Child } from '../lib/dom.ts';
import { relative } from '../lib/format.ts';
import { encodeQR, qrPath } from '../lib/qr.ts';
import { getConnect, startConnect } from '../lib/simple-api.ts';
import { stream } from '../lib/stream.ts';
import { getPath, getView } from '../lib/theme.ts';
import type { Action, ListResponse } from '../lib/types.ts';
import { codeBlock } from './components.ts';
import { openSheet, type Overlay } from './overlay.ts';
import { announce } from './shell.ts';
import { toast } from './toast.ts';

const c = sc.connect;
const T = sc.tiles;

const STATUS_CHIP: Record<Status, string> = { not_connected: 'chip-outline', waiting: 'chip-pending', connected: 'chip-verified' };

/** pill is a status: a dot and the word (colour never carries it alone). */
export function statusPill(st: Status): HTMLElement {
  return h('span', { class: `chip sx-pill ${STATUS_CHIP[st]}`, 'data-status': st }, h('span', { class: 'dot', 'aria-hidden': 'true' }), c.status[st]!);
}

function qrCode(text: string, name: string): SVGSVGElement | null {
  if (hasSecret(text)) return null; // never put a key in a picture
  const { d, box } = qrPath(encodeQR(text));
  const svg = s('svg', { viewBox: `0 0 ${box} ${box}`, role: 'img', 'aria-label': fill(c.qrLabel, { name }), 'data-qr': text });
  svg.append(s('path', { d }));
  return svg;
}

function line(label: string, value: string): HTMLElement {
  return h('div', { class: 'cx-line' },
    h('code', { translate: 'no', 'aria-label': label }, value),
    h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'data-copy': value, 'aria-label': `${c.copy} ${label.toLowerCase()}` }, icon('copy', 16), c.copy));
}

async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(c.copied, { kind: 'success' });
  } catch {
    /* clipboard blocked: the text stays on screen to select */
  }
}

interface Model {
  ev: Evidence;
  started: Set<TileId>;
  states: Map<TileId, TileState>;
  open: { id: TileId; ov: Overlay; pill: HTMLElement; note: HTMLElement } | null;
}

export function mountConnect(): void {
  const body = maybe('cx-body');
  if (!body) return;
  const m: Model = { ev: {}, started: new Set(), states: new Map(), open: null };
  const params = new URLSearchParams(location.search);
  if (params.get('from') === 'start') banner();

  let busy = false;
  const load = async (first = false) => {
    if (busy) return;
    busy = true;
    try {
      const [server, agents, connectors, actions] = await Promise.all([
        getConnect().catch(() => null),
        api<ListResponse<AgentLite>>('GET', '/v1/agents', { query: { limit: '100' } }).then((r) => r.data).catch(() => null),
        api<ListResponse<ConnectorLite>>('GET', '/v1/connectors', { query: { limit: '100' } }).then((r) => r.data).catch(() => null),
        api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: '100' } }).then((r) => r.data).catch(() => null),
      ]);
      if (first && server === null && agents === null && connectors === null) throw new Error(c.noAnswer);
      m.ev = { server, agents: agents ?? [], connectors: connectors ?? [], recentTools: (actions ?? []).map((a) => a.tool), started: m.started };
      render();
    } catch (e) {
      if (first) showError(e);
    } finally {
      busy = false;
      body.setAttribute('aria-busy', 'false');
    }
  };

  const showError = (e: unknown) => {
    const msg = e instanceof Error ? e.message : String(e);
    const retry = h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => void load(true) }, icon('refresh-cw', 16), c.retry);
    body.replaceChildren(h('div', { class: 'state state-error', role: 'alert' }, h('span', { class: 'well' }, icon('circle-alert', 20)), h('p', null, fill(c.loadFailed, { message: msg })), h('div', { class: 'actions' }, retry)));
  };

  const render = () => {
    const tiles = orderedTiles(getPath());
    const changed: TileId[] = [];
    for (const t of tiles) {
      const st = stateOf(t, m.ev);
      const before = m.states.get(t.id)?.status;
      if (before && before !== 'connected' && st.status === 'connected') changed.push(t.id);
      m.states.set(t.id, st);
    }
    const group = (g: 'ai' | 'systems', title: string) => [
      h('h2', { class: 'sx-section-title', id: `cx-${g}` }, title),
      h('div', { class: 'cx-grid', role: 'list', 'aria-labelledby': `cx-${g}` }, ...tiles.filter((t) => t.group === g).map((t) => h('div', { role: 'listitem' }, tile(t)))),
    ];
    const focusId = (document.activeElement as HTMLElement | null)?.dataset?.tile;
    // The path's first tile leads, so a clinic sees its record system before the AI tools.
    const ai = group('ai', c.groupAi);
    const sys = group('systems', c.groupSystems);
    body.replaceChildren(...(tiles[0]?.group === 'systems' ? [...sys, ...ai] : [...ai, ...sys]));
    if (focusId) body.querySelector<HTMLElement>(`[data-tile="${focusId}"]`)?.focus();
    for (const id of changed) {
      const name = T[id]!.name;
      toast(fill(c.connectedNote, { name }), { kind: 'success' });
      announce(fill(c.connectedNote, { name }));
    }
    if (m.open) {
      const st = m.states.get(m.open.id)!;
      m.open.pill.replaceWith((m.open.pill = statusPill(st.status)));
      replace(m.open.note, ...statusNote(m.open.id, st));
    }
  };

  const tile = (t: TileDef): HTMLElement => {
    const st = m.states.get(t.id)!;
    const d = T[t.id]!;
    return h('button', { type: 'button', class: 'cx-tile', 'data-tile': t.id, 'aria-haspopup': 'dialog', onclick: () => openTile(t) },
      h('span', { class: 'top' }, h('span', { class: 'cx-ico', 'aria-hidden': 'true' }, icon(t.icon)), h('span', { class: 'name' }, d.name)),
      h('p', null, d.body),
      h('span', { class: 'cx-foot' }, statusPill(st.status), h('span', { class: 'go', 'aria-hidden': 'true' }, icon('chevron-right', 16))));
  };

  const statusNote = (id: TileId, st: TileState): Child[] => {
    const name = T[id]!.name;
    if (st.status === 'connected') {
      return [h('span', null, fill(c.connectedNote, { name }), st.lastActionAt ? ` ${fill(c.connectedAt, { when: relative(st.lastActionAt) })}` : ''), ' ',
        h('a', { href: '/app/simple' }, c.seeActivity)];
    }
    if (st.status === 'waiting') return [fill(c.waitingNote, { name })];
    return [];
  };

  const markStarted = (id: TileId) => {
    m.started.add(id);
    void startConnect(id).then((item: ConnectItem | null) => {
      if (item && m.ev.server) m.ev.server = [...m.ev.server.filter((x) => x.id !== id), item];
      render();
    }).catch(() => render());
  };

  const openTile = (t: TileDef) => {
    const d = T[t.id]!;
    const st = m.states.get(t.id)!;
    const server = m.ev.server?.find((x) => x.id === t.id) ?? null;
    let pill = statusPill(st.status);
    const note = h('p', { class: 't-footnote t-2 m-0', role: 'status', 'aria-live': 'polite' }, ...statusNote(t.id, st));
    const steps = h('ol', { class: 'cx-steps', 'aria-label': c.stepsLabel }, ...d.steps.slice(0, 3).map((x) => h('li', null, x)));
    const parts: Child[] = [h('div', { class: 'cx-status' }, pill), note, steps];
    const primaryLink = (label: string, href: string) => h('a', { class: 'btn btn-primary btn-xl cx-primary', href, onclick: () => markStarted(t.id) }, label);
    const primaryBtn = (label: string, run: () => void) => h('button', { type: 'button', class: 'btn btn-primary btn-xl cx-primary', onclick: () => { run(); markStarted(t.id); } }, label);
    const fallback = (summary: string, block: HTMLElement, open = false) => {
      const det = h('details', { class: 'cx-fallback sv-tech' }, h('summary', null, summary), block);
      det.open = open;
      return det;
    };
    const qr = (text: string) => {
      const code = qrCode(text, d.name);
      if (!code) return null;
      return h('details', { class: 'cx-qr sv-tech' }, h('summary', null, c.otherComputer),
        h('div', { class: 'cx-qr-body' }, code, h('p', { class: 't-footnote t-2 m-0' }, fill(c.qrHelp, { name: d.name }))));
    };
    const cursorJSON = JSON.stringify({ mcpServers: { deedglass: JSON.parse(SERVER_JSON) } }, null, 2);

    switch (t.id) {
      case 'claude-desktop': {
        const cfg = serverText(server?.config, desktopConfig());
        parts.push(primaryBtn(d.button, () => void copyText(cfg)), codeBlock('claude_desktop_config.json', cfg, c.settingsLabel),
          h('a', { href: CLAUDE_DESKTOP_GUIDE, rel: 'noopener', target: '_blank', class: 't-footnote' }, c.guide, ' ', icon('arrow-up-right', 16)), qr(CLAUDE_DESKTOP_GUIDE));
        break;
      }
      case 'cursor':
      case 'vscode': {
        const deep = serverText(server?.link, t.id === 'cursor' ? LINKS.cursor() : LINKS.vscode());
        const web = serverText(server?.web_link, t.id === 'cursor' ? LINKS.cursorWeb() : LINKS.vscodeWeb());
        const cfg = t.id === 'cursor' ? serverText(server?.config, cursorJSON) : serverText(server?.command, `code --add-mcp '{"name":"deedglass",${SERVER_JSON.slice(1)}'`);
        const fb = fallback(c.didntOpen, codeBlock(t.id === 'cursor' ? '~/.cursor/mcp.json' : c.terminalLabel, cfg, t.id === 'cursor' ? c.settingsLabel : c.commandLabel));
        const link = primaryLink(d.button, deep);
        link.addEventListener('click', () => setTimeout(() => { if (document.hasFocus()) fb.open = true; }, 1500));
        parts.push(link, fb, qr(web));
        break;
      }
      case 'terminal': {
        const agents = T.terminal!.agents as Record<string, string>;
        // The local app's own command and settings (they run this computer's deedglass with its key file).
        const cmds: typeof COMMANDS = { ...COMMANDS, 'claude-code': serverText(server?.command, COMMANDS['claude-code']), gemini: serverText(server?.config, COMMANDS.gemini) };
        let pick: keyof typeof COMMANDS = 'claude-code';
        const holder = h('div', { class: 'cx-fallback' });
        const show = () => {
          replace(holder, pick === 'gemini' ? codeBlock('settings.json', cmds.gemini, c.settingsLabel) : line(c.commandLabel, cmds[pick]),
            pick === 'gemini' ? h('p', { class: 't-footnote t-2 m-0' }, T.terminal!.geminiNote as string) : null);
        };
        const buttons = Object.keys(COMMANDS).map((k) => h('button', { type: 'button', 'aria-pressed': k === pick ? 'true' : 'false', 'data-agent': k }, agents[k] ?? k));
        buttons.forEach((b) => b.addEventListener('click', () => {
          pick = b.dataset.agent as keyof typeof COMMANDS;
          buttons.forEach((x) => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          show();
        }));
        show();
        parts.push(h('div', { class: 'segmented cx-picker', role: 'group', 'aria-label': T.terminal!.agentLabel as string }, ...buttons), holder,
          primaryBtn(d.button, () => void copyText(cmds[pick])));
        const found = server?.detected ?? [];
        if (found.length) {
          parts.push(h('div', { class: 'cx-banner' }, icon('shield-check'), h('p', null, fill(c.detected, { names: found.map((x) => agents[x] ?? x).join(', ') }), ' ', T.terminal!.guardBody as string),
            h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => markStarted('terminal') }, T.terminal!.guard as string)));
        }
        break;
      }
      case 'openai': {
        const addr = serverText(server?.address, mcpAddress(location.origin));
        const py = serverText(server?.config, '');
        parts.push(line(c.addressLabel, addr), primaryBtn(d.button, () => void copyText(addr)),
          py ? fallback(T.openai!.sdk as string, codeBlock('agent.py', py, c.settingsLabel)) : null,
          h('a', { href: '/app/agents?new=1', class: 'btn btn-secondary' }, icon('key-round', 16), T.openai!.keys as string));
        break;
      }
      case 'scribe':
        parts.push(primaryLink(d.button, '/app/connectors/new?step=configure&type=fhir'));
        break;
      case 'database': {
        let kind: 'postgres' | 'mysql' = 'postgres';
        const go = primaryLink(d.button, '/app/connectors/new?step=configure&type=postgres');
        const notYet = h('p', { class: 't-footnote t-2 m-0', role: 'status' });
        const btns = (['postgres', 'mysql'] as const).map((k) => h('button', { type: 'button', 'aria-pressed': k === kind ? 'true' : 'false' }, k === 'postgres' ? c.postgres : c.mysql));
        btns.forEach((b, i) => b.addEventListener('click', () => {
          kind = i === 0 ? 'postgres' : 'mysql';
          btns.forEach((x) => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          go.hidden = kind === 'mysql';
          notYet.textContent = kind === 'mysql' ? c.notYet : '';
        }));
        parts.push(h('div', { class: 'segmented cx-picker', role: 'group', 'aria-label': d.name }, ...btns), go, notYet);
        break;
      }
    }
    parts.push(h('p', { class: 'cx-secret' }, icon('lock', 16), c.noSecrets));
    const ov = openSheet({
      title: d.name,
      body: parts,
      className: 'cx-sheet',
      onClose: () => {
        m.open = null;
      },
    });
    m.open = { id: t.id, ov, pill, note };
    pill = m.open.pill;
  };

  void load(true);
  const live = stream();
  let timer = 0;
  const soon = () => {
    clearTimeout(timer);
    timer = window.setTimeout(() => void load(), 400);
  };
  live.on('action.*', soon);
  live.on('agent.*', soon);
  live.on('connector.*', soon);
  live.onResync(soon);
  // While something waits for its first action, look again every 5 s (the stream also wakes it).
  window.setInterval(() => {
    if ([...m.states.values()].some((x) => x.status === 'waiting') && !document.hidden) void load();
  }, 5000);
}

function banner(): void {
  const host = maybe('cx-banner');
  if (!host) return;
  const home = getView() === 'detailed' ? '/app' : '/app/simple';
  host.replaceChildren(h('div', { class: 'cx-banner', role: 'note' },
    icon('info'),
    h('p', null, c.fromStart),
    h('a', { class: 'btn btn-secondary', href: home }, c.skipToApp)));
}
