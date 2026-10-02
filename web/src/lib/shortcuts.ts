// shortcuts.ts: keyboard shortcuts (S24 §3.8). Two-key "g" sequences go to screens; "/" and ⌘K /
// Ctrl+K open the palette; "?" lists everything. They never fire inside inputs, and no shortcut
// changes anything by itself.

export const GO: Record<string, string> = {
  o: '/app', a: '/app/actions', p: '/app/approvals', g: '/app/agents', l: '/app/policies', c: '/app/connectors',
  i: '/app/insights', d: '/app/deeds', s: '/app/settings',
};

export type Command = { kind: 'go'; href: string } | { kind: 'palette' } | { kind: 'help' } | null;

/** typing reports whether a key event happens inside something that takes text. */
export function typing(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.closest !== 'function') return false;
  if (el.isContentEditable) return true;
  return el.closest('input, textarea, select, [contenteditable="true"], [role="combobox"]') !== null;
}

/** Sequencer turns key presses into commands; "g" waits up to 1.2 s for its second key. */
export class Sequencer {
  private pendingG: number | null = null;
  constructor(private readonly now: () => number = () => Date.now()) {}

  key(e: { key: string; metaKey?: boolean; ctrlKey?: boolean; altKey?: boolean; target?: EventTarget | null }): Command {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') return { kind: 'palette' };
    if (e.metaKey || e.ctrlKey || e.altKey) return null;
    if (typing(e.target ?? null)) return null;
    const k = e.key;
    if (this.pendingG !== null && this.now() - this.pendingG < 1200) {
      this.pendingG = null;
      const href = GO[k.toLowerCase()];
      return href ? { kind: 'go', href } : null;
    }
    this.pendingG = null;
    if (k === 'g') {
      this.pendingG = this.now();
      return null;
    }
    if (k === '/') return { kind: 'palette' };
    if (k === '?') return { kind: 'help' };
    return null;
  }
}
