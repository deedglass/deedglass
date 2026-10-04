import { existsSync, readdirSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { parseKeySet, splitLines, Verifier, type VerifyError, verifyDeeds } from '../../src/verify/index.js';
import { outcome, readBytes, readText, repo } from '../helpers.js';

interface Expected {
  valid: boolean;
  code: string | null;
  seq: number | null;
  field: string | null;
  strict_code: string | null;
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

describe('@deedglass/sdk/verify: testdata/deeds vectors (the same answers as Go)', () => {
  const all = JSON.parse(readText('testdata/deeds/expected.json')) as Record<string, Expected>;
  for (const [p, want] of Object.entries(all)) {
    if (p.endsWith('.cose')) continue; // COSE is CLI-only
    it(p, async () => {
      expect(await run('testdata/deeds/' + p, KEYS, false)).toEqual({ valid: want.valid, code: want.code, seq: want.seq, field: want.field });
      if (want.strict_code) expect((await run('testdata/deeds/' + p, KEYS, true)).code).toBe(want.strict_code);
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
  it('verifyDeeds reads genesis-20 like the CLI', async () => {
    const r = await verifyDeeds(splitLines(readBytes('testdata/deeds/chains/genesis-20.jsonl')), await parseKeySet(readBytes(KEYS)), { keysPinned: true });
    expect(r.result).toBe('valid');
    expect(r.intact).toBe(20);
    expect(r.chain.status).toBe('intact');
  });
});

interface Case {
  case: number;
  kind: string;
  chain: boolean;
  strict: boolean;
  deeds: string[];
}

describe('parity corpus (testdata/deeds/parity)', () => {
  it('matches Go on all 500 cases', async () => {
    const cases = readText('testdata/deeds/parity/corpus-500.jsonl').trim().split('\n').map((l) => JSON.parse(l) as Case);
    const expected = JSON.parse(readText('testdata/deeds/parity/corpus-500.expected.json')) as unknown[];
    expect(cases.length).toBe(500);
    const keys = await parseKeySet(readBytes(KEYS));
    const diffs: string[] = [];
    for (const c of cases) {
      const deeds = c.deeds.map((d) => new Uint8Array(Buffer.from(d, 'base64')));
      const v = new Verifier(keys, { strict: c.strict, keysPinned: true });
      if (c.chain) await v.chain(deeds, 'corpus');
      else await v.loose(deeds[0]!, 'corpus');
      const rep = await v.report();
      const got = { case: c.case, ...outcome(rep), deeds: rep.deeds };
      if (JSON.stringify(got) !== JSON.stringify(expected[c.case - 1])) diffs.push(`#${c.case} (${c.kind})`);
    }
    expect(diffs, diffs.slice(0, 20).join('\n')).toEqual([]);
  });
});
