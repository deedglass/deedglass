import { readdirSync, existsSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parseKeySet, splitLines, Verifier, VerifyError } from '../src/index.js';
import { outcome, readBytes, readText, repo } from './helpers.js';

interface Expected {
  valid: boolean;
  code: string | null;
  seq: number | null;
  field: string | null;
  strict_code: string | null;
  exit: number;
}

async function run(path: string, keysPath: string, strict: boolean) {
  let keys;
  try {
    keys = await parseKeySet(readBytes(keysPath));
  } catch (e) {
    const ve = e as VerifyError;
    return { valid: false, code: ve.code, seq: ve.seq > 0 ? ve.seq : null, field: ve.field || null };
  }
  const v = new Verifier(keys, { strict, keysPinned: true });
  const bytes = readBytes(path);
  if (path.endsWith('.jsonl')) await v.chain(splitLines(bytes), path);
  else await v.loose(bytes, path);
  return outcome(await v.report());
}

const KEYS = 'testdata/deeds/keys/test-keys.json';

describe('testdata/deeds vectors (the same answers as Go)', () => {
  const all = JSON.parse(readText('testdata/deeds/expected.json')) as Record<string, Expected>;
  for (const [p, want] of Object.entries(all)) {
    if (p.endsWith('.cose')) continue; // COSE is CLI-only (S05 stretch)
    it(p, async () => {
      expect(await run('testdata/deeds/' + p, KEYS, false)).toEqual({ valid: want.valid, code: want.code, seq: want.seq, field: want.field });
      if (want.strict_code) {
        const s = await run('testdata/deeds/' + p, KEYS, true);
        expect(s.code).toBe(want.strict_code);
      }
    });
  }

  const dirs = readdirSync(repo('testdata/deeds/invalid'));
  it('has 26 invalid vectors', () => expect(dirs.length).toBe(26));
  for (const d of dirs) {
    it('invalid/' + d, async () => {
      const base = 'testdata/deeds/invalid/' + d + '/';
      const want = JSON.parse(readText(base + 'expected.json')) as Expected;
      const file = existsSync(repo(base + 'deed.json')) ? base + 'deed.json' : base + 'deeds.jsonl';
      const keys = existsSync(repo(base + 'keys.json')) ? base + 'keys.json' : KEYS;
      expect(await run(file, keys, false)).toEqual({ valid: false, code: want.code, seq: want.seq, field: want.field });
    });
  }
});

describe('testdata/deeds/v2 vectors (deed v2, ADR-109)', () => {
  const V2KEYS = 'testdata/deeds/v2/keys.json';
  const all = JSON.parse(readText('testdata/deeds/v2/expected.json')) as Record<string, Expected>;
  for (const [p, want] of Object.entries(all)) {
    it(p, async () => {
      expect(await run('testdata/deeds/' + p, V2KEYS, false)).toEqual({ valid: want.valid, code: want.code, seq: want.seq, field: want.field });
      if (want.strict_code) expect((await run('testdata/deeds/' + p, V2KEYS, true)).code).toBe(want.strict_code);
    });
  }
  const dirs = readdirSync(repo('testdata/deeds/v2/invalid'));
  it('has the v2 invalid vectors', () => expect(dirs.length).toBeGreaterThanOrEqual(15));
  for (const d of dirs) {
    it('v2/invalid/' + d, async () => {
      const base = 'testdata/deeds/v2/invalid/' + d + '/';
      const want = JSON.parse(readText(base + 'expected.json')) as Expected;
      const file = existsSync(repo(base + 'deed.json')) ? base + 'deed.json' : base + 'deeds.jsonl';
      const keys = existsSync(repo(base + 'keys.json')) ? base + 'keys.json' : V2KEYS;
      expect(await run(file, keys, false)).toEqual({ valid: false, code: want.code, seq: want.seq, field: want.field });
    });
  }
});

describe('reports', () => {
  it('reads genesis-20 like the CLI headline', async () => {
    const v = new Verifier(await parseKeySet(readBytes(KEYS)), { keysPinned: true, keySource: 'test-keys.json' });
    await v.chain(splitLines(readBytes('testdata/deeds/chains/genesis-20.jsonl')));
    const r = await v.report();
    expect(r.result).toBe('valid');
    expect(r.intact).toBe(20);
    expect(r.chain.status).toBe('intact');
    expect(r.seal_keys.map((k) => [k.first_seq, k.last_seq])).toEqual([[1, 10], [11, 20]]);
    expect(r.from?.slice(0, 10)).toBe('2026-10-02');
    expect(r.warnings.map((w) => w.code)).toEqual(['test_keys']);
    expect(r.kinds).toEqual({ action: 18, admin: 2 });
  });

  it('says keys_missing without keys', async () => {
    const v = new Verifier(null);
    await v.chain(splitLines(readBytes('testdata/deeds/chains/genesis-20.jsonl')));
    const r = await v.report();
    expect(r.result).toBe('cannot_determine');
    expect(r.failures[0]?.message).toBe('Cannot check deed 1: no seal keys were given.');
  });

  it('gives the exact copy for a changed deed', async () => {
    const lines = splitLines(readBytes('testdata/deeds/chains/genesis-20.jsonl'));
    const l = new TextDecoder().decode(lines[11]).replace('"prev_hash":"sha256:', '"prev_hash":"sha256:0');
    const v = new Verifier(await parseKeySet(readBytes(KEYS)), { keysPinned: true });
    await v.chain([...lines.slice(0, 11), new TextEncoder().encode(l.replace(/(sha256:0)./, '$1'))]);
    const r = await v.report();
    expect(r.failures[0]).toMatchObject({ code: 'body_hash_mismatch', seq: 12, message: 'Deed 12 was changed after it was sealed.' });
    expect(r.failures[0]?.expected).not.toBe(r.failures[0]?.computed);
    expect(r.intact).toBe(11);
  });
});
