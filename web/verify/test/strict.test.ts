import { describe, expect, it } from 'vitest';
import { canonicalize, parseStrict, VerifyError } from '../src/index.js';

const BS = String.fromCharCode(92); // one backslash: escapes are built at run time
const u = (hex: string) => BS + 'u' + hex;

function code(f: () => unknown): string {
  try {
    f();
    return 'ok';
  } catch (e) {
    return (e as VerifyError).code + ': ' + (e as VerifyError).detail;
  }
}

describe('strict JSON profile', () => {
  const bad: [string, string | Uint8Array, string, boolean?][] = [
    ['duplicate key', '{"a":1,"a":2}', 'duplicate object key at a'],
    ['duplicate key after unescaping', `{"a":1,"${u('0061')}":2}`, 'duplicate object key at a'],
    ['fraction', '{"a":1.0}', 'only integers are allowed (no fraction or exponent) at a'],
    ['exponent', '{"a":1e2}', 'only integers are allowed (no fraction or exponent) at a'],
    ['2^53', '9007199254740992', 'integer magnitude exceeds 2^53-1'],
    ['17 digits', '10000000000000000', 'integer magnitude exceeds 2^53-1'],
    ['-0 (deed bodies)', '{"a":-0}', 'negative numbers and -0 are not allowed at a', true],
    ['negative (deed bodies)', '[-1]', 'negative numbers and -0 are not allowed at [0]', true],
    ['leading zero', '[01]', 'leading zero in number at [0]'],
    ['lone high surrogate', `["${u('d800')}"]`, 'high surrogate not followed by a low surrogate at [0]'],
    ['lone low surrogate', `["${u('dc00')}"]`, 'lone low surrogate at [0]'],
    ['high then non-low', `["${u('d800')}${u('0041')}"]`, 'high surrogate not followed by a low surrogate at [0]'],
    ['depth 17', '['.repeat(17) + ']'.repeat(17), 'nesting too deep at ' + '[0]'.repeat(16)],
    ['trailing data', '{} {}', 'trailing data after the top-level value'],
    ['control character', '["a' + String.fromCharCode(1) + '"]', 'unescaped control character in string at [0]'],
    ['bad escape', '["' + BS + 'x"]', 'invalid escape ' + BS + 'x at [0]'],
    ['invalid UTF-8 (bytes)', new Uint8Array([0x22, 0xff, 0x22]), 'input is not valid UTF-8'],
    ['overlong UTF-8', new Uint8Array([0x22, 0xc0, 0xaf, 0x22]), 'input is not valid UTF-8'],
    ['UTF-8 surrogate', new Uint8Array([0x22, 0xed, 0xa0, 0x80, 0x22]), 'input is not valid UTF-8'],
    ['raw lone surrogate (string)', '"' + String.fromCharCode(0xd800) + '"', 'input is not valid UTF-8'],
    ['empty', '', 'unexpected end of input'],
    ['NaN', '[NaN]', "unexpected character 'N' at [0]"],
    ['unterminated', '{"a":1', 'unterminated object'],
  ];
  for (const [name, input, detail, nonNegative] of bad) {
    it(name + ' → malformed', () => {
      expect(code(() => parseStrict(input, { nonNegative: nonNegative ?? false }))).toBe('malformed: ' + detail);
    });
  }

  it('accepts what the profile allows', () => {
    expect(parseStrict('9007199254740991')).toBe(9007199254740991);
    expect(parseStrict('[-1]')).toEqual([-1]);
    expect(parseStrict(` {"b":[true,false,null],"a":"${u('00e9')}${u('d83d')}${u('de00')}"} `)).toEqual({ b: [true, false, null], a: 'é😀' });
    expect(parseStrict('['.repeat(16) + ']'.repeat(16))).toBeTruthy();
    const proto = parseStrict('{"__proto__":1,"constructor":2}') as Record<string, number>;
    expect(Object.keys(proto)).toEqual(['__proto__', 'constructor']);
    expect(canonicalize(proto)).toBe('{"__proto__":1,"constructor":2}');
  });

  it('never uses JSON.parse rounding', () => {
    expect(code(() => parseStrict('9007199254740993'))).toBe('malformed: integer magnitude exceeds 2^53-1');
  });
});
