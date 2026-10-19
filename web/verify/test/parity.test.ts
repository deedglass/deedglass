import { describe, expect, it } from 'vitest';
import { parseKeySet, Verifier } from '../src/index.js';
import { outcome, readBytes, readText } from './helpers.js';

interface Case {
  case: number;
  kind: string;
  chain: boolean;
  strict: boolean;
  deeds: string[];
}
interface Expected {
  case: number;
  valid: boolean;
  code: string | null;
  seq: number | null;
  field: string | null;
  deeds: number;
}

describe('parity corpus (testdata/deeds/parity)', () => {
  it('matches Go on all 500 cases', async () => {
    const cases = readText('testdata/deeds/parity/corpus-500.jsonl').trim().split('\n').map((l) => JSON.parse(l) as Case);
    const expected = JSON.parse(readText('testdata/deeds/parity/corpus-500.expected.json')) as Expected[];
    expect(cases.length).toBe(500);
    const keys = await parseKeySet(readBytes('testdata/deeds/keys/test-keys.json'));
    const diffs: string[] = [];
    for (const c of cases) {
      const deeds = c.deeds.map((d) => new Uint8Array(Buffer.from(d, 'base64')));
      const v = new Verifier(keys, { strict: c.strict, keysPinned: true });
      if (c.chain) await v.chain(deeds, 'corpus');
      else await v.loose(deeds[0]!, 'corpus');
      const rep = await v.report();
      const got = { case: c.case, ...outcome(rep), deeds: rep.deeds };
      const want = expected[c.case - 1]!;
      if (JSON.stringify(got) !== JSON.stringify(want)) diffs.push(`#${c.case} (${c.kind}): got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
    expect(diffs, diffs.slice(0, 20).join('\n')).toEqual([]);
  });
});

describe('v2 parity corpus (testdata/deeds/v2/parity, ADR-109)', () => {
  it('matches Go on all 200 cases', async () => {
    const cases = readText('testdata/deeds/v2/parity/corpus-200.jsonl').trim().split('\n').map((l) => JSON.parse(l) as Case);
    const expected = JSON.parse(readText('testdata/deeds/v2/parity/corpus-200.expected.json')) as Expected[];
    expect(cases.length).toBe(200);
    const keys = await parseKeySet(readBytes('testdata/deeds/v2/keys.json'));
    const diffs: string[] = [];
    for (const c of cases) {
      const deeds = c.deeds.map((d) => new Uint8Array(Buffer.from(d, 'base64')));
      const v = new Verifier(keys, { strict: c.strict, keysPinned: true });
      if (c.chain) await v.chain(deeds, 'corpus');
      else await v.loose(deeds[0]!, 'corpus');
      const rep = await v.report();
      const got = { case: c.case, ...outcome(rep), deeds: rep.deeds };
      const want = expected[c.case - 1]!;
      if (JSON.stringify(got) !== JSON.stringify(want)) diffs.push(`#${c.case} (${c.kind}): got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
    expect(diffs, diffs.slice(0, 20).join('\n')).toEqual([]);
  });
});
