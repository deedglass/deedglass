// DL-143: the dependency-free QR encoder (byte mode, ECC M). Structure here; the e2e decodes a real
// code with the browser's BarcodeDetector.
import { describe, expect, it } from 'vitest';
import { alignmentPositions, dataCodewords, encodeQR, qrPath, rawModules } from '../src/lib/qr.ts';

const finder = (g: boolean[][], r: number, c: number) => {
  for (let y = 0; y < 7; y++) for (let x = 0; x < 7; x++) {
    const ring = Math.max(Math.abs(y - 3), Math.abs(x - 3));
    expect(g[r + y]![c + x], `${r + y},${c + x}`).toBe(ring !== 2);
  }
};

describe('qr', () => {
  it('matches the standard capacity tables', () => {
    expect(rawModules(1)).toBe(208);
    expect(rawModules(40)).toBe(29648);
    expect(dataCodewords(1)).toBe(16);
    expect(dataCodewords(10)).toBe(216);
    expect(alignmentPositions(1)).toEqual([]);
    expect(alignmentPositions(7)).toEqual([6, 22, 38]);
  });
  it('draws finders, timing and the dark module, sized to the text', () => {
    for (const text of ['http://127.0.0.1:8080/mcp/native', 'x'.repeat(300)]) {
      const g = encodeQR(text);
      const n = g.length;
      expect((n - 17) % 4).toBe(0);
      for (const row of g) expect(row.length).toBe(n);
      finder(g, 0, 0); finder(g, 0, n - 7); finder(g, n - 7, 0);
      for (let i = 8; i < n - 8; i++) { expect(g[6]![i]).toBe(i % 2 === 0); expect(g[i]![6]).toBe(i % 2 === 0); }
      expect(g[n - 8]![8]).toBe(true);
    }
    expect(encodeQR('a').length).toBe(21);
  });
  it('is deterministic and refuses what does not fit', () => {
    expect(encodeQR('same')).toEqual(encodeQR('same'));
    expect(() => encodeQR('x'.repeat(3000))).toThrow();
  });
  it('paths the dark modules inside a quiet zone', () => {
    const g = encodeQR('a');
    const { d, box } = qrPath(g);
    expect(box).toBe(21 + 8);
    expect(d.startsWith('M4 4')).toBe(true);
    expect(/^[MhvHVz0-9 .-]+$/.test(d)).toBe(true);
  });
});
