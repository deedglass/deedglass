// dom.ts: the only way the Console builds DOM from data (S24 §3.9). Strings become Text nodes;
// nothing is ever parsed as HTML. The HTML-string sinks (listed in test/sinks.test.ts) are
// banned in web/src and in the built bundles (test/sinks.test.ts, scripts/check-dist.mjs), and the
// CSP's Trusted Types 'none' makes them throw in Chromium anyway.

export type Child = Node | string | number | null | undefined | false;
export type Handler = (e: Event) => void;
export type PropValue = string | number | boolean | null | undefined | Handler;
export type Props = Record<string, PropValue>;

const SVG_NS = 'http://www.w3.org/2000/svg';

// SR-016 (S35): the bidi overrides and isolates (U+202A–U+202E, U+2066–U+2069) in a string an
// agent supplied (a tool name, an agent name, an error message) could reorder the text around it.
// Every string the Console renders passes through bidiSafe, which shows them as \uXXXX escapes,
// exactly as the CLI's clean() does. Nothing the Console's own copy holds contains them.
const BIDI = /[\u202a-\u202e\u2066-\u2069]/g;

/** bidiSafe shows bidi override and isolate characters as visible \uXXXX escapes. */
export function bidiSafe(v: string): string {
  return v.replace(BIDI, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}

/** Appends children: strings and numbers as Text nodes, falsy values skipped. */
export function append(parent: Node, children: readonly Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    parent.appendChild(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(bidiSafe(String(c))) : c);
  }
}

function setProp(el: Element, key: string, v: PropValue): void {
  if (v === null || v === undefined || v === false) return;
  if (key.startsWith('on')) {
    if (typeof v !== 'function') throw new Error(`dom: ${key} must be a function, never a string`);
    el.addEventListener(key.slice(2).toLowerCase(), v);
    return;
  }
  if (typeof v === 'function') throw new Error(`dom: ${key} can't take a function`);
  if (key === 'style') throw new Error('dom: no style attributes; use a class (the CSP refuses inline styles)');
  if (key === 'href' || key === 'src' || key === 'action' || key === 'formaction') {
    const safe = safeHref(String(v));
    if (safe === null) return;
    el.setAttribute(key, safe);
    return;
  }
  if (key === 'class') {
    el.setAttribute('class', String(v));
    return;
  }
  el.setAttribute(key, v === true ? '' : bidiSafe(String(v)));
}

/** h builds an HTML element. Props become attributes (on* must be functions); children become nodes. */
export function h<K extends keyof HTMLElementTagNameMap>(tag: K, props?: Props | null, ...children: Child[]): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (props) for (const [k, v] of Object.entries(props)) setProp(el, k, v);
  append(el, children);
  return el;
}

/** s builds an SVG element (icons, the Mirror, sparklines). */
export function s<K extends keyof SVGElementTagNameMap>(tag: K, props?: Record<string, string | number> | null, ...children: Child[]): SVGElementTagNameMap[K] {
  const el = document.createElementNS(SVG_NS, tag);
  if (props) for (const [k, v] of Object.entries(props)) el.setAttribute(k, String(v));
  append(el, children);
  return el;
}

/** text replaces an element's content with one Text node. */
export function text(el: Element, value: string): void {
  el.textContent = bidiSafe(value);
}

/** clear removes every child. */
export function clear(el: Element): void {
  el.replaceChildren();
}

/** replace sets an element's children. */
export function replace(el: Element, ...children: Child[]): void {
  el.replaceChildren();
  append(el, children);
}

let spriteURL: string | null = null;

/** The sprite URL (versioned; the layout writes it into <meta name="dg-icons">). */
export function sprite(): string {
  if (spriteURL === null) {
    const m = typeof document !== 'undefined' ? document.querySelector('meta[name="dg-icons"]') : null;
    spriteURL = m?.getAttribute('content') ?? '/app/icons.svg';
  }
  return spriteURL;
}

/** icon renders a sprite icon: aria-hidden, 16 or 20 px, a 1.5 px stroke at 20 px (brand §11). */
export function icon(name: string, size: 16 | 20 = 20, cls = ''): SVGSVGElement {
  const svg = s('svg', { class: `icon${cls ? ' ' + cls : ''}`, width: size, height: size, viewBox: '0 0 24 24', 'aria-hidden': 'true', focusable: 'false' });
  svg.appendChild(s('use', { href: `${sprite()}#i-${name}` }));
  return svg;
}

/**
 * safeHref allows only same-origin Console and API paths and the docs site. Everything else
 * (javascript:, data:, //evil.com, other hosts, backslashes, control characters) is null.
 */
export const INSTALL_PREFIXES: readonly string[] = [
  'cursor://anysphere.cursor-deeplink/mcp/install?',
  'vscode:mcp/install?',
  'https://cursor.com/en/install-mcp?',
  'https://vscode.dev/redirect/mcp/install?',
];

export function safeHref(u: string): string | null {
  if (typeof u !== 'string' || u.length === 0 || u.length > 2048) return null;
  if (/[\u0000-\u001f\u007f\\\s]/.test(u)) return null;
  if (u.startsWith('/')) {
    if (u.startsWith('//')) return null;
    if (u === '/app' || u.startsWith('/app/') || u.startsWith('/app?') || u.startsWith('/app#') || u.startsWith('/v1/') || u === '/login' || u.startsWith('/login?')) {
      if (/(^|\/)\.\.?(\/|$|\?|#)/.test(u.split(/[?#]/)[0] ?? '')) return null;
      return u;
    }
    return null;
  }
  if (u.startsWith('#')) return u;
  // DL-143: the Connect screen's one-click install links (internal/installlinks). Exact prefixes
  // only; their query carries the public server object, never a key.
  for (const p of INSTALL_PREFIXES) if (u.startsWith(p) && u.length > p.length) return u;
  try {
    const url = new URL(u);
    if (url.protocol === 'https:' && (url.hostname === 'docs.deedglass.com' || url.hostname === 'deedglass.com') && !url.username && !url.password && (url.port === '' || url.port === '443')) {
      return url.toString();
    }
  } catch {
    return null;
  }
  return null;
}

/** byId returns an element or throws: the page's static markup is part of the contract. */
export function byId<T extends HTMLElement = HTMLElement>(id: string): T {
  const el = document.getElementById(id);
  if (!el) throw new Error(`dom: #${id} is missing`);
  return el as T;
}

/** maybe returns an element or null. */
export function maybe<T extends HTMLElement = HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}
