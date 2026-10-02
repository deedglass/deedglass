// tabs.ts: keyboard-driven tabs (Left/Right, Home/End), URL-synced through a path segment or ?tab=.
export function wireTabs(list: HTMLElement, onSelect: (key: string) => void, sync: (key: string) => string | null): void {
  const tabs = [...list.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
  const select = (t: HTMLButtonElement, focus: boolean, push: boolean) => {
    for (const o of tabs) {
      const on = o === t;
      o.setAttribute('aria-selected', on ? 'true' : 'false');
      o.tabIndex = on ? 0 : -1;
    }
    if (focus) t.focus();
    const key = t.dataset.section ?? t.id;
    const panel = document.getElementById(t.getAttribute('aria-controls') ?? '');
    panel?.setAttribute('aria-labelledby', t.id);
    if (push) {
      const url = sync(key);
      if (url && url !== location.pathname + location.search) history.replaceState(null, '', url);
    }
    onSelect(key);
  };
  tabs.forEach((t, i) => {
    t.addEventListener('click', () => select(t, false, true));
    t.addEventListener('keydown', (e) => {
      const n = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? tabs.length - 1 : -2;
      if (n === -2) return;
      e.preventDefault();
      select(tabs[(n + tabs.length) % tabs.length]!, true, true);
    });
  });
  const want = tabs.find((t) => location.pathname.endsWith('/' + (t.dataset.section ?? '')));
  if (want) select(want, false, false);
}
