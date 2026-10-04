// theme.ts: Dark (the default, brand §13.1), Light or System. The choice lives in localStorage
// under deedglass.theme (a UI preference, never anything sensitive); /app/theme.js applies it
// before first paint, and this module changes it at runtime.

export type Theme = 'dark' | 'light' | 'system';
export const THEME_KEY = 'deedglass.theme';
const THEMES: readonly Theme[] = ['dark', 'light', 'system'];

export function isTheme(v: unknown): v is Theme {
  return typeof v === 'string' && (THEMES as readonly string[]).includes(v);
}

export function getTheme(storage: Pick<Storage, 'getItem'> | null = safeStorage()): Theme {
  try {
    const v = storage?.getItem(THEME_KEY);
    return isTheme(v) ? v : 'dark';
  } catch {
    return 'dark';
  }
}

export function setTheme(t: Theme, root: HTMLElement = document.documentElement, storage: Pick<Storage, 'setItem'> | null = safeStorage()): void {
  root.setAttribute('data-theme', t);
  try {
    storage?.setItem(THEME_KEY, t);
  } catch {
    /* private mode: the choice lasts for this page */
  }
  root.dispatchEvent(new CustomEvent('dg:theme', { detail: t, bubbles: true }));
}

/** The theme actually showing: System resolves through prefers-color-scheme. */
export function effectiveTheme(t: Theme = getTheme()): 'dark' | 'light' {
  if (t !== 'system') return t;
  return typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

/** next cycles Dark → Light → System (the palette's Toggle theme). */
export function nextTheme(t: Theme): Theme {
  return t === 'dark' ? 'light' : t === 'light' ? 'system' : 'dark';
}

// S29: whether the last org this browser showed had the simulated banner, so the shell can draw the
// banner (and reserve its space) at boot instead of after GET /v1/org. A display hint only: one
// flag, no org id, no data; GET /v1/org corrects it on every load and sign-out clears it.
export const SIM_KEY = 'deedglass.simulated';

export function getSimHint(storage: Pick<Storage, 'getItem'> | null = safeStorage()): boolean {
  try {
    return storage?.getItem(SIM_KEY) === '1';
  } catch {
    return false;
  }
}

export function setSimHint(on: boolean, storage: Pick<Storage, 'setItem' | 'removeItem'> | null = safeStorage()): void {
  try {
    if (on) storage?.setItem(SIM_KEY, '1');
    else storage?.removeItem(SIM_KEY);
  } catch {
    /* storage blocked: the banner waits for GET /v1/org */
  }
}

function safeStorage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch {
    return null;
  }
}
