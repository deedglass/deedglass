// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest';
import { h, safeHref, text } from '../src/lib/dom.ts';

describe('dom', () => {
  it('renders agent strings as text, never markup', () => {
    const evil = '<img src=x onerror=alert(1)>';
    const el = h('div', null, evil, h('span', { title: evil }, evil));
    expect(el.querySelector('img')).toBeNull();
    expect(el.textContent).toBe(evil + evil);
    expect(el.childNodes[0]!.nodeType).toBe(Node.TEXT_NODE);
    const t = h('p');
    text(t, '<b>bold</b>');
    expect(t.querySelector('b')).toBeNull();
  });
  it('refuses string event handlers and inline styles', () => {
    expect(() => h('div', { onclick: 'alert(1)' as never })).toThrow();
    expect(() => h('div', { style: 'color:red' })).toThrow();
    let n = 0;
    h('button', { onclick: () => n++ }).click();
    expect(n).toBe(1);
  });
  it('drops unsafe hrefs', () => {
    expect(h('a', { href: 'javascript:alert(1)' }).hasAttribute('href')).toBe(false);
    expect(h('a', { href: '/app/actions' }).getAttribute('href')).toBe('/app/actions');
  });
  it('safeHref allows only the Console, the API and the docs', () => {
    for (const ok of ['/app', '/app/actions', '/app/actions?verdict=MISMATCH', '/v1/deeds/export', '/login?next=%2Fapp', '#main', 'https://docs.deedglass.com/errors/rate_limited']) {
      expect(safeHref(ok), ok).not.toBeNull();
    }
    for (const bad of ['javascript:alert(1)', 'JavaScript:alert(1)', '//evil.com', '/\\evil.com', 'data:text/html,x', 'https://evil.com', 'https://docs.deedglass.com.evil.com/',
      'https://user@docs.deedglass.com/', 'http://docs.deedglass.com/', '/app/../v1/me', '/apps', '/etc/passwd', ' /app', '/app\n', '']) {
      expect(safeHref(bad), bad).toBeNull();
    }
  });
});
