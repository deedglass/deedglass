import { describe, expect, it } from 'vitest';
import { denialText } from '../../src/adapters/shared.js';
import { trimSlashes } from '../../src/http.js';
import { pemToDer } from '../../src/intent.js';

// Regression for code-scanning js/polynomial-redos #2–5: each input below took seconds or more
// with the old regexes; the replacements are linear.
const N = 200_000;

describe('linear-time string helpers', () => {
  it('trimSlashes strips trailing slashes only, fast on long runs', () => {
    expect(trimSlashes('https://x.test///')).toBe('https://x.test');
    expect(trimSlashes('https://x.test')).toBe('https://x.test');
    expect(trimSlashes('///')).toBe('');
    const t = performance.now();
    expect(trimSlashes('/'.repeat(N) + 'x')).toHaveLength(N + 1);
    expect(performance.now() - t).toBeLessThan(500);
  });

  it('denialText drops a trailing "Nothing was run." once, fast on long space runs', () => {
    expect(denialText({ message: 'Blocked by policy.  Nothing was run.' })).toBe('Blocked by policy. Nothing was run.');
    expect(denialText({ message: 'Blocked by policy' })).toBe('Blocked by policy. Nothing was run.');
    const t = performance.now();
    denialText({ message: 'a' + ' '.repeat(N) + 'b' });
    expect(performance.now() - t).toBeLessThan(500);
  });

  it('pemToDer reads the body between markers and rejects bad shapes, fast on many BEGIN lines', () => {
    const b64 = Buffer.from([1, 2, 3, 4]).toString('base64');
    expect([...pemToDer(`-----BEGIN PRIVATE KEY-----\n${b64}\n-----END PRIVATE KEY-----\n`)]).toEqual([1, 2, 3, 4]);
    expect(() => pemToDer('-----BEGIN PRIVATE KEY----------END PRIVATE KEY-----')).toThrow();
    expect(() => pemToDer('no key here')).toThrow();
    const t = performance.now();
    expect(() => pemToDer('-----BEGIN PRIVATE KEY-----'.repeat(N / 20))).toThrow();
    expect(performance.now() - t).toBeLessThan(500);
  });
});
