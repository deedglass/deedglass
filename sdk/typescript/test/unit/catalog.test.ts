import { describe, expect, it } from 'vitest';
import { fromEnvelope } from '../../src/errors.js';
import { readText } from '../helpers.js';

// S32: sdk/errors.json is written from internal/httpx/errors.go by gensdkerrors; the Python SDK's
// test reads the same file, so both SDKs map every code to the same class.
describe('the shared error catalog (sdk/errors.json)', () => {
  const doc = JSON.parse(readText('sdk/errors.json')) as { object: string; codes: { code: string; status: number; type: string; class: string }[] };
  it('is the generated catalog', () => {
    expect(doc.object).toBe('sdk_error_catalog');
    expect(doc.codes.length).toBeGreaterThan(70);
  });
  for (const e of doc.codes) {
    it(`${e.code} → ${e.class}`, () => {
      const err = fromEnvelope(e.status, { error: { type: e.type, code: e.code, message: 'm.' } }, null, 0);
      expect(err.constructor.name).toBe(e.class);
    });
  }
});
