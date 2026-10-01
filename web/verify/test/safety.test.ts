import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { repo } from './helpers.js';

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? files(p) : [p];
  });
}

describe('page safety', () => {
  const src = files(repo('web/verify/src')).filter((f) => /\.(ts|css)$/.test(f));
  const page = [...src, repo('web/verify/index.html')];

  it('never writes HTML from strings (textContent only)', () => {
    for (const f of page) {
      const s = readFileSync(f, 'utf8');
      for (const banned of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'eval(', 'new Function']) {
        expect(s.includes(banned), `${f} uses ${banned}`).toBe(false);
      }
    }
  });

  it('has a strict CSP and no inline script or style', () => {
    const html = readFileSync(repo('web/verify/index.html'), 'utf8');
    expect(html).toContain(
      `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src https: http://localhost:* http://127.0.0.1:*; base-uri 'none'; form-action 'none'`,
    );
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)[^>]*>/);
    expect(html).not.toMatch(/\sstyle=/);
    expect(html).not.toMatch(/<style/);
  });

  it('stores nothing and sends nothing but the explicit key fetch', () => {
    for (const f of src) {
      const s = readFileSync(f, 'utf8');
      for (const banned of ['localStorage', 'sessionStorage', 'indexedDB', 'document.cookie', 'sendBeacon', 'XMLHttpRequest']) {
        expect(s.includes(banned), `${f} uses ${banned}`).toBe(false);
      }
      if (s.includes('fetch(')) expect(f.endsWith('fetchkeys.ts'), `${f} calls fetch`).toBe(true);
    }
  });
});
