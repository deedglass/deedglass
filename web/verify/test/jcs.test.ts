import { readdirSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { canonicalize, parseStrict } from '../src/index.js';
import { readBytes, readText, repo } from './helpers.js';

describe('RFC 8785 reference pairs (testdata/jcs)', () => {
  for (const f of readdirSync(repo('testdata/jcs/input'))) {
    it(f, () => {
      const got = canonicalize(JSON.parse(readText('testdata/jcs/input/' + f)));
      expect(new TextEncoder().encode(got)).toEqual(readBytes('testdata/jcs/output/' + f));
    });
  }
});

describe('deed bodies', () => {
  it('re-canonicalize to the exact stored bytes', () => {
    for (const f of readdirSync(repo('testdata/deeds/valid')).filter((x) => x.endsWith('.json'))) {
      const raw = readText('testdata/deeds/valid/' + f);
      expect(canonicalize(parseStrict(raw, { nonNegative: true }))).toBe(raw);
    }
  });
  it('sorts keys by UTF-16 code units', () => {
    const k1 = String.fromCodePoint(0xe000);
    const k2 = String.fromCodePoint(0x1f600);
    const obj: Record<string, number> = { [k1]: 1, [k2]: 2, a: 3 };
    expect(canonicalize(obj)).toBe(`{"a":3,"${k2}":2,"${k1}":1}`);
  });
});
