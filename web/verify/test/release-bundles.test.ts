// TY-11, the 2046 test: every release's sealed bundle (testdata/release-bundles/<version>/) verifies
// with this browser verifier. Bundles (.tar.gz) are CLI-only in the page, so the test opens the
// archive itself (node:zlib and a 512-byte tar reader), checks the manifest's file hashes, then
// hands the deeds and the keys trusted out of band (keys.json beside the bundle) to the verifier.
// The CLI half, with the manifest seal, is scripts/release-bundles.sh verify.
import { createHash } from 'node:crypto';
import { readdirSync, statSync } from 'node:fs';
import { gunzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';
import { parseKeySet, splitLines, Verifier } from '../src/index.js';
import { readBytes, repo } from './helpers.js';

const DIR = 'testdata/release-bundles';
const FILES = ['README.txt', 'keys.json', 'deeds.jsonl', 'manifest.json', 'manifest.seal.json'];

/** The regular files of a POSIX tar, in order. */
function untar(tar: Uint8Array): { name: string; data: Uint8Array }[] {
  const out: { name: string; data: Uint8Array }[] = [];
  const text = (b: Uint8Array) => new TextDecoder().decode(b).replace(/\0.*$/s, '');
  for (let off = 0; off + 512 <= tar.length; ) {
    const h = tar.subarray(off, off + 512);
    if (h.every((b) => b === 0)) break;
    const name = text(h.subarray(0, 100));
    const size = parseInt(text(h.subarray(124, 136)).trim(), 8);
    const type = String.fromCharCode(h[156]!);
    off += 512;
    if (type === '0' || type === '\0') out.push({ name, data: tar.subarray(off, off + size) });
    off += Math.ceil(size / 512) * 512;
  }
  return out;
}

const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest('hex');

const versions = readdirSync(repo(DIR))
  .filter((v) => statSync(repo(`${DIR}/${v}`)).isDirectory())
  .sort((a, b) => a.localeCompare(b, 'en', { numeric: true }));

describe('the 2046 test: every release bundle verifies in the browser verifier', () => {
  it('has a bundle for every release since 0.1.0', () => {
    expect(versions.slice(0, 6)).toEqual(['0.1.0', '0.1.1', '0.2.0', '0.2.1', '0.3.0', '0.4.0']);
  });

  for (const v of versions) {
    it(`v${v}`, async () => {
      const files = untar(gunzipSync(readBytes(`${DIR}/${v}/bundle.tar.gz`)));
      expect(files.map((f) => f.name)).toEqual(FILES);
      const get = (n: string) => files.find((f) => f.name === n)!.data;
      const manifest = JSON.parse(new TextDecoder().decode(get('manifest.json')));
      for (const n of ['README.txt', 'keys.json', 'deeds.jsonl']) {
        expect(sha256(get(n)), n).toBe(manifest.files[n].sha256);
        expect(get(n).length, n).toBe(manifest.files[n].bytes);
      }
      const seal = JSON.parse(new TextDecoder().decode(get('manifest.seal.json')));
      expect(seal.manifest_hash).toBe('sha256:' + sha256(get('manifest.json')));

      for (const strict of [false, true]) {
        const keys = await parseKeySet(readBytes(`${DIR}/${v}/keys.json`));
        const ver = new Verifier(keys, { keysPinned: true, strict });
        await ver.chain(splitLines(get('deeds.jsonl')), 'deeds.jsonl');
        const rep = await ver.report();
        expect(rep.failures).toEqual([]);
        expect(rep.result).toBe('valid');
        expect(rep.chain.status).toBe('intact');
        expect(rep.deeds).toBe(manifest.range.count);
        expect(rep.org).toBe(manifest.org);
        expect(rep.first_seq).toBe(manifest.range.first_seq);
        expect(rep.last_seq).toBe(manifest.chain.head.seq);
        expect(rep.warnings.map((w) => w.code)).toEqual(['test_keys']);
      }
    });
  }

  it('fails a changed deed, so the check can fail', async () => {
    const last = versions[versions.length - 1]!;
    const files = untar(gunzipSync(readBytes(`${DIR}/${last}/bundle.tar.gz`)));
    const deeds = new TextDecoder().decode(files.find((f) => f.name === 'deeds.jsonl')!.data);
    const changed = deeds.replace('"seq":3,', '"seq":4,');
    expect(changed).not.toBe(deeds);
    const ver = new Verifier(await parseKeySet(readBytes(`${DIR}/${last}/keys.json`)), { keysPinned: true });
    await ver.chain(splitLines(new TextEncoder().encode(changed)), 'deeds.jsonl');
    const rep = await ver.report();
    expect(rep.result).toBe('invalid');
    expect(rep.failures.length).toBeGreaterThan(0);
  });
});
