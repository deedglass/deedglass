// The kit page's demos: the Mirror player, tabs, the segmented control, the table, overlays and toasts.
import { mountMirror, type MirrorHandle, type MirrorSize } from '@verify/ui/mirror.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, maybe } from '../lib/dom.ts';
import { confirmTyped, menuButton, openPopover, openSheet } from './overlay.ts';
import { listKeys, wireTable } from './table.ts';
import { wireTabs } from './tabs.ts';
import { toast } from './toast.ts';

export function mountKit(): void {
  // The Mirror player: take over the build-time marks.
  const handles: MirrorHandle[] = [];
  for (const host of document.querySelectorAll<HTMLElement>('#kit-player .play-target')) {
    handles.push(mountMirror(host, { size: Number(host.dataset.size ?? 48) as MirrorSize, state: 'open' }));
  }
  const play = async (s: 'sealed' | 'mismatch') => {
    for (const m of handles) await m.set('open');
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
    await Promise.all(handles.map((m) => m.set(s, { animate: true })));
  };
  maybe('play-sealed')?.addEventListener('click', () => void play('sealed'));
  maybe('play-mismatch')?.addEventListener('click', () => void play('mismatch'));
  maybe('play-reset')?.addEventListener('click', () => handles.forEach((m) => void m.set('open')));

  const tabs = maybe('kit-tabs');
  if (tabs) wireTabs(tabs, () => {}, () => null);
  const seg = maybe('kit-seg');
  if (seg) {
    const radios = [...seg.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
    const pick = (b: HTMLButtonElement, focus: boolean) => {
      for (const r of radios) {
        r.setAttribute('aria-checked', r === b ? 'true' : 'false');
        r.tabIndex = r === b ? 0 : -1;
      }
      if (focus) b.focus();
    };
    radios.forEach((b, i) => {
      b.addEventListener('click', () => pick(b, false));
      b.addEventListener('keydown', (e) => {
        if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
        e.preventDefault();
        pick(radios[(i + (e.key === 'ArrowRight' ? 1 : -1) + radios.length) % radios.length]!, true);
      });
    });
  }
  const table = document.getElementById('kit-table') as HTMLTableElement | null;
  if (table) wireTable(table);
  listKeys();

  maybe('kit-sheet')?.addEventListener('click', () => {
    const done = h('button', { type: 'button', class: 'btn btn-primary' }, copy.agents.done);
    const ov = openSheet({
      title: copy.agents.newKeyTitle,
      body: [h('p', null, copy.agents.newKeyBody), h('div', { class: 'field' }, h('span', { class: 'label' }, 'Key'), h('input', { class: 'input mono', readonly: true, value: 'dg_test_aB3dE5fG_••••••••••••••••••••••••••••••••', 'aria-label': 'Key' })),
        h('label', { class: 'check' }, h('input', { type: 'checkbox' }), copy.agents.newKeyStored)],
      foot: [done],
    });
    done.addEventListener('click', ov.close);
  });
  maybe('kit-dialog')?.addEventListener('click', () => {
    const ok = h('button', { type: 'button', class: 'btn btn-primary' }, 'Sign in');
    const ov = openSheet({ kind: 'dialog', title: copy.states.sessionEnded, body: [], foot: [ok] });
    ok.addEventListener('click', ov.close);
  });
  maybe('kit-confirm')?.addEventListener('click', () => {
    confirmTyped({
      title: fill(copy.stop.agentTitle, { name: 'support-bot' }),
      body: fill(copy.stop.agentBody, { name: 'support-bot' }),
      expected: 'support-bot',
      reasonLabel: copy.stop.reasonOptional,
      confirmLabel: copy.stop.agentButton,
      danger: true,
      onConfirm: async () => {
        toast(fill(copy.stop.agentToast, { name: 'support-bot' }), { kind: 'success', action: { label: copy.stop.viewDeed, href: '/app/deeds' } });
      },
    });
  });
  const pop = maybe('kit-popover');
  pop?.addEventListener('click', () => openPopover(pop, [h('p', { class: 'm-0' }, fill(copy.stop.viewer, { org: 'Acme' }))]));
  const mb = maybe('kit-menu-btn');
  if (mb) menuButton(mb, 'Row actions', () => [
    { label: 'Copy action id', icon: 'copy', run: () => toast(copy.toast.copied, { kind: 'success' }) },
    { label: 'View in Ledger', icon: 'deed' },
    { label: 'Stop agent…', icon: 'stop', divider: true },
  ]);
  maybe('kit-toast-neutral')?.addEventListener('click', () => toast(copy.states.resync));
  maybe('kit-toast-success')?.addEventListener('click', () => toast('Deed sealed. Chain intact.', { kind: 'success', action: { label: copy.stop.viewDeed, href: '/app/deeds' } }));
  maybe('kit-toast-error')?.addEventListener('click', () => toast("The API key has been revoked. Create a new key in Console › Agents.", { kind: 'error', requestId: 'req_01k6h7s9t0u1v2w3x4y5z6a7b8' }));
}
