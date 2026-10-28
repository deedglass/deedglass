// connect.ts: the Connect screen (DL-143, Connect v1). A tile per AI tool or system, each with the
// tool's real mark, its status and a sheet with at most three numbered steps. Where the tool keeps
// an MCP settings file on this computer, the sheet finds it and offers one Connect button: the
// server shows exactly the entry it will add, saves a copy of the file first and adds only that
// entry (POST /v1/connect/{id}/install); Undo puts the file back. Everywhere else: a deep link, a
// one-line copy, a QR code for another computer, and Test connection, which turns the pill green
// on the first action. No link, setting or QR code holds a key: lib/connect.ts refuses anything
// key-shaped, and a settings file is written only after the person clicks.

import { api, ApiError } from '../lib/api.ts';
import { brandMark } from '../lib/brandmarks.ts';
import { fill } from '../lib/copy.ts';
import { simpleCopy as sc } from '../lib/copy-simple.ts';
import {
  COMMANDS, CLAUDE_DESKTOP_GUIDE, LINKS, SAMPLE_FHIR, SERVER_JSON, desktopConfig, hasSecret, mcpAddress, serverText, orderedTiles, stateOf,
  type AgentLite, type ConnectItem, type ConnectorLite, type Evidence, type Install, type Status, type TileDef, type TileId, type TileState,
} from '../lib/connect.ts';
import { h, icon, maybe, replace, s, type Child } from '../lib/dom.ts';
import { relative } from '../lib/format.ts';
import { encodeQR, qrPath } from '../lib/qr.ts';
import { getConnect, installConnect, startConnect, undoConnect } from '../lib/simple-api.ts';
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

/** logo is the tile's mark: the real brand marks, a short text mark, or the Lucide icon. */
export function logo(t: TileDef): HTMLElement {
  const box = h('span', { class: `cx-logo cx-logo-${t.marks.length > 1 ? 'many' : 'one'}`, 'aria-hidden': 'true', 'data-marks': t.marks.join(' ') });
  if (t.marks.length) box.append(...t.marks.map((m) => brandMark(m, t.marks.length > 1 ? 20 : 28)));
  else if (t.monogram) box.append(h('span', { class: 'cx-monogram' }, t.monogram));
  else box.append(icon(t.icon));
  return box;
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

/** mono is a file path or address in small monospace, wrapping anywhere. */
function mono(v: string): HTMLElement {
  return h('code', { class: 'cx-path', translate: 'no' }, v);
}

function errorText(e: unknown): string {
  return e instanceof ApiError || e instanceof Error ? e.message : String(e);
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
    const retry = h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => void load(true) }, icon('refresh-cw', 16), c.retry);
    body.replaceChildren(h('div', { class: 'state state-error', role: 'alert' }, h('span', { class: 'well' }, icon('circle-alert', 20)), h('p', null, fill(c.loadFailed, { message: errorText(e) })), h('div', { class: 'actions' }, retry)));
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
    return h('button', { type: 'button', class: 'cx-tile', 'data-tile': t.id, 'data-state': st.status, 'aria-haspopup': 'dialog', 'aria-label': `${d.name}. ${fill(c.statusLabel, { status: c.status[st.status]! })}`, onclick: () => openTile(t) },
      logo(t),
      h('span', { class: 'cx-text' }, h('span', { class: 'name' }, d.name), h('span', { class: 'body' }, d.body)),
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

  const absorb = (item: ConnectItem | null) => {
    if (item && m.ev.server) m.ev.server = [...m.ev.server.filter((x) => x.id !== item.id), item];
    render();
  };

  const markStarted = (id: TileId) => {
    m.started.add(id);
    void startConnect(id).then(absorb).catch(() => render());
  };

  const openTile = (t: TileDef) => {
    const d = T[t.id]!;
    const st = m.states.get(t.id)!;
    const server = m.ev.server?.find((x) => x.id === t.id) ?? null;
    const detailed = getView() === 'detailed';
    let pill = statusPill(st.status);
    const note = h('p', { class: 't-footnote t-2 m-0', role: 'status', 'aria-live': 'polite' }, ...statusNote(t.id, st));
    // The three steps: the one-click story when Connect can write the file here, else the manual one.
    const manual = (d.manual as string[] | undefined) ?? d.steps;
    const canClick = !!st.install && st.install.installed && st.install.status !== 'skip';
    const steps = h('ol', { class: 'cx-steps', 'aria-label': c.stepsLabel }, ...(canClick ? d.steps : manual).slice(0, 3).map((x) => h('li', null, x)));
    const parts: Child[] = [h('div', { class: 'cx-status' }, pill), note, steps];
    const primaryLink = (label: string, href: string, cls = 'btn-primary btn-xl') => h('a', { class: `btn ${cls} cx-primary`, href, onclick: () => markStarted(t.id) }, label);
    const primaryBtn = (label: string, run: () => void, cls = 'btn-primary btn-xl') => h('button', { type: 'button', class: `btn ${cls} cx-primary`, onclick: () => { run(); markStarted(t.id); } }, label);
    const fallback = (summary: string, block: Child, open = false) => {
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
    /** testBtn: Test connection. It only starts listening; the pill turns green on the first action. */
    const testBtn = (who = d.name) => {
      const b = h('button', { type: 'button', class: 'btn btn-secondary cx-test' }, icon('activity', 16), c.test);
      b.addEventListener('click', () => {
        markStarted(t.id);
        b.disabled = true;
        b.replaceChildren(icon('activity', 16), c.testing);
        announce(fill(c.testHelp, { name: who }));
      });
      return h('div', { class: 'cx-testrow' }, b, h('p', { class: 't-footnote t-2 m-0' }, fill(c.testHelp, { name: who })));
    };
    const cursorJSON = JSON.stringify({ mcpServers: { deedglass: JSON.parse(SERVER_JSON) } }, null, 2);

    /**
     * installBlock: the one-click Connect for a tool with a settings file on this computer. Shows
     * the file, exactly what will be added, the Connect button, then the result and Undo.
     */
    const installBlock = (ins: Install, pasteBlock: HTMLElement, pasteLabel: string, name = d.name): HTMLElement => {
      const box = h('div', { class: 'cx-install', 'data-install': ins.status });
      const draw = (cur: Install, just: 'added' | 'updated' | 'undone' | 'removed' | null, err: string | null) => {
        const kids: Child[] = [];
        const dir = cur.file.replace(/[\\/][^\\/]*$/, '');
        if (!cur.installed) {
          kids.push(h('p', { class: 'cx-note-inline' }, icon('circle-dashed', 16), h('span', null, fill(c.notFound, { name, dir }))));
          kids.push(fallback(pasteLabel, pasteBlock, true));
        } else if (cur.status === 'skip') {
          kids.push(h('p', { class: 'cx-note-inline' }, icon('triangle-alert', 16), h('span', null, fill(c.skipped, { name, reason: cur.reason ?? '' }))));
          kids.push(h('p', { class: 'cx-found t-footnote t-2' }, c.foundAt.replace('{file}', ''), mono(cur.file)));
          kids.push(fallback(pasteLabel, pasteBlock, true));
        } else {
          kids.push(h('p', { class: 'cx-found t-footnote t-2' }, icon('check', 16), h('span', null, c.foundAt.replace('{file}', '')), mono(cur.file)));
          if (just === 'added' || just === 'updated' || (cur.status === 'present' && !just)) {
            kids.push(h('div', { class: 'cx-done', role: 'status' }, icon('shield-check', 20),
              h('div', null,
                h('p', { class: 'm-0' }, just === 'updated' ? fill(c.updated, { name }) : just === 'added' ? fill(c.added, { file: cur.file, name }) : fill(c.present, { name })),
                cur.backup ? h('p', { class: 't-footnote t-2 m-0' }, fill(c.backupAt, { backup: cur.backup })) : null)));
            if (cur.can_undo) kids.push(undoBtn());
          } else {
            if (just === 'undone' || just === 'removed') kids.push(h('p', { class: 'cx-note-inline', role: 'status' }, icon('history', 16), h('span', null, fill(just === 'removed' ? c.undoneRemoved : c.undone, { name }))));
            kids.push(h('p', { class: 't-footnote t-2 m-0' }, fill(c.willAdd, { name })));
            kids.push(connectBtn());
            kids.push(fallback(c.whatAdded, codeBlock('json', cur.entry, c.whatAdded), detailed));
          }
          if (err) kids.push(h('p', { class: 'error', role: 'alert' }, icon('circle-alert', 16), err));
          if (cur.status !== 'present' || err) kids.push(fallback(pasteLabel, pasteBlock, !!err));
        }
        replace(box, ...kids);
      };
      const connectBtn = () => {
        const b = h('button', { type: 'button', class: 'btn btn-primary btn-xl cx-primary' }, c.connect);
        b.addEventListener('click', async () => {
          b.disabled = true;
          b.setAttribute('aria-busy', 'true');
          b.textContent = c.connecting;
          m.started.add(t.id);
          try {
            const item = await installConnect(t.id);
            absorb(item);
            const ni = item.install ?? ins;
            draw(ni, ins.status === 'update' ? 'updated' : 'added', null);
            const short = fill(c.added, { file: ni.file.split(/[\\/]/).pop() ?? ni.file, name });
            toast(short, { kind: 'success' });
            announce(short);
          } catch (e) {
            draw(ins, null, errorText(e));
          }
        });
        return b;
      };
      const undoBtn = () => {
        const b = h('button', { type: 'button', class: 'btn btn-secondary' }, icon('history', 16), c.undo);
        b.addEventListener('click', async () => {
          b.disabled = true;
          b.textContent = c.undoing;
          try {
            const item = await undoConnect(t.id);
            absorb(item);
            const ni = item.install ?? { ...ins, status: 'add' as const, can_undo: false };
            const removed = ni.status === 'present';
            draw(ni, removed ? 'removed' : 'undone', null);
            toast(fill(removed ? c.undoneRemoved : c.undone, { name }), { kind: 'success' });
          } catch (e) {
            draw(ins, null, errorText(e));
          }
        });
        return b;
      };
      draw(ins, null, null);
      return box;
    };

    switch (t.id) {
      case 'claude-desktop': {
        const cfg = serverText(server?.config, desktopConfig());
        const paste = h('div', { class: 'grid gap-2' }, codeBlock('claude_desktop_config.json', cfg, c.settingsLabel),
          h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => { void copyText(cfg); markStarted(t.id); } }, icon('copy', 16), d.button));
        if (st.install) parts.push(installBlock(st.install, paste, c.orPaste));
        else parts.push(primaryBtn(d.button, () => void copyText(cfg)), codeBlock('claude_desktop_config.json', cfg, c.settingsLabel));
        parts.push(testBtn(),
          h('a', { href: CLAUDE_DESKTOP_GUIDE, rel: 'noopener', target: '_blank', class: 't-footnote' }, c.guide, ' ', icon('arrow-up-right', 16)), qr(CLAUDE_DESKTOP_GUIDE));
        break;
      }
      case 'cursor':
      case 'vscode': {
        const deep = serverText(server?.link, t.id === 'cursor' ? LINKS.cursor() : LINKS.vscode());
        const web = serverText(server?.web_link, t.id === 'cursor' ? LINKS.cursorWeb() : LINKS.vscodeWeb());
        const cfg = t.id === 'cursor' ? serverText(server?.config, cursorJSON) : serverText(server?.command, `code --add-mcp '{"name":"deedglass",${SERVER_JSON.slice(1)}'`);
        const paste = codeBlock(t.id === 'cursor' ? '~/.cursor/mcp.json' : c.terminalLabel, cfg, t.id === 'cursor' ? c.settingsLabel : c.commandLabel);
        if (st.install && st.install.installed && st.install.status !== 'skip') {
          parts.push(installBlock(st.install, paste, c.orPaste), primaryLink(d.button, deep, 'btn-secondary'));
        } else {
          const fb = fallback(c.didntOpen, paste);
          const link = primaryLink(d.button, deep);
          link.addEventListener('click', () => setTimeout(() => { if (document.hasFocus()) fb.open = true; }, 1500));
          if (st.install) parts.push(installBlock(st.install, paste, c.didntOpen), link);
          else parts.push(link, fb);
        }
        parts.push(testBtn(), qr(web));
        break;
      }
      case 'terminal': {
        const agents = T.terminal!.agents as Record<string, string>;
        // The local app's own command and settings (they run this computer's deedglass with its key file).
        const cmds: typeof COMMANDS = { ...COMMANDS, 'claude-code': serverText(server?.command, COMMANDS['claude-code']), gemini: serverText(server?.config, COMMANDS.gemini) };
        let pick: keyof typeof COMMANDS = 'claude-code';
        const holder = h('div', { class: 'cx-fallback' });
        const geminiPaste = codeBlock('~/.gemini/settings.json', cmds.gemini, c.settingsLabel);
        const show = () => {
          if (pick === 'gemini') {
            replace(holder, st.install ? installBlock(st.install, geminiPaste, c.orPaste, agents.gemini!) : geminiPaste,
              st.install ? null : h('p', { class: 't-footnote t-2 m-0' }, T.terminal!.geminiNote as string));
          } else {
            replace(holder, line(c.commandLabel, cmds[pick]),
              h('button', { type: 'button', class: 'btn btn-primary btn-xl cx-primary', onclick: () => { void copyText(cmds[pick]); markStarted(t.id); } }, d.button));
          }
        };
        const buttons = Object.keys(COMMANDS).map((k) => h('button', { type: 'button', 'aria-pressed': k === pick ? 'true' : 'false', 'data-agent': k },
          k === 'gemini' ? brandMark('gemini', 20) : k === 'codex' ? brandMark('openai', 20) : brandMark('claude', 20), agents[k] ?? k));
        buttons.forEach((b) => b.addEventListener('click', () => {
          pick = b.dataset.agent as keyof typeof COMMANDS;
          buttons.forEach((x) => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          show();
        }));
        show();
        parts.push(h('div', { class: 'segmented cx-picker', role: 'group', 'aria-label': T.terminal!.agentLabel as string }, ...buttons), holder, testBtn(T.terminal!.anyAgent as string));
        const found = server?.detected ?? [];
        if (found.length) {
          parts.push(h('div', { class: 'cx-banner' }, icon('shield-check'), h('p', null, fill(c.detected, { names: found.map((x) => agents[x] ?? x).join(', ') }), ' ', T.terminal!.guardBody as string),
            h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => markStarted('terminal') }, T.terminal!.guard as string)));
        }
        break;
      }
      case 'openai':
      case 'mcp': {
        const addr = serverText(server?.address, mcpAddress(location.origin));
        const py = t.id === 'openai' ? serverText(server?.config, '') : '';
        parts.push(line(c.addressLabel, addr), primaryBtn(d.button, () => void copyText(addr)),
          py ? fallback(T.openai!.sdk as string, codeBlock('agent.py', py, c.settingsLabel)) : null,
          h('a', { href: '/app/agents?new=1', class: 'btn btn-secondary' }, icon('key-round', 16), T.openai!.keys as string), testBtn());
        break;
      }
      case 'scribe': {
        const input = h('input', { class: 'input mono', id: 'cx-fhir', type: 'url', inputmode: 'url', autocomplete: 'off', spellcheck: 'false', placeholder: c.fhirPlaceholder, 'aria-describedby': 'cx-fhir-h cx-fhir-e' });
        const err = h('p', { class: 'error', id: 'cx-fhir-e', hidden: true });
        const sampleNote = h('p', { class: 'cx-sample-note t-footnote t-2 m-0', role: 'status', hidden: true }, c.sampleNote);
        const go = () => {
          const v = input.value.trim();
          if (!/^https:\/\/[^\s/]+/i.test(v) || hasSecret(v)) {
            err.hidden = false;
            replace(err, icon('circle-alert', 16), c.fhirBad);
            input.setAttribute('aria-invalid', 'true');
            input.focus();
            return;
          }
          markStarted(t.id);
          const q = new URLSearchParams({ step: 'configure', type: 'fhir', 'cfg.base_url': v });
          location.assign('/app/connectors/new?' + q.toString());
        };
        input.addEventListener('keydown', (e) => { if ((e as KeyboardEvent).key === 'Enter') { e.preventDefault(); go(); } });
        input.addEventListener('input', () => { err.hidden = true; input.removeAttribute('aria-invalid'); });
        const sample = h('button', { type: 'button', class: 'btn btn-secondary' }, icon('flask-conical', 16), c.sample);
        sample.addEventListener('click', () => {
          input.value = SAMPLE_FHIR;
          err.hidden = true;
          sampleNote.hidden = false;
          input.focus();
        });
        parts.push(h('div', { class: 'field cx-form' },
          h('label', { class: 'label', for: 'cx-fhir' }, c.fhirLabel), input, h('p', { class: 'help', id: 'cx-fhir-h' }, c.fhirHelp), err, sampleNote),
          h('div', { class: 'cx-actions' }, h('button', { type: 'button', class: 'btn btn-primary btn-xl cx-primary', onclick: go }, d.button), sample));
        break;
      }
      case 'database': {
        let kind: 'postgres' | 'mysql' = 'postgres';
        const go = primaryLink(d.button, '/app/connectors/new?step=configure&type=postgres');
        const notYet = h('p', { class: 't-footnote t-2 m-0', role: 'status' });
        const btns = (['postgres', 'mysql'] as const).map((k) => h('button', { type: 'button', 'aria-pressed': k === kind ? 'true' : 'false' }, brandMark(k === 'postgres' ? 'postgresql' : 'mysql', 20), k === 'postgres' ? c.postgres : c.mysql));
        btns.forEach((b, i) => b.addEventListener('click', () => {
          kind = i === 0 ? 'postgres' : 'mysql';
          btns.forEach((x) => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          go.hidden = kind === 'mysql';
          notYet.textContent = kind === 'mysql' ? c.notYet : '';
        }));
        parts.push(h('div', { class: 'segmented cx-picker', role: 'group', 'aria-label': c.dbLabel }, ...btns), h('p', { class: 't-footnote t-2 m-0' }, c.dbHelp), go, notYet);
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
