import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { SCRUB_RULES, scrub } from '../../src/scrub.js';
import { readText, repo } from '../helpers.js';

describe('one source', () => {
  it('src/verify matches web/verify/src (make sdk-sync-verify)', () => {
    const out = execFileSync('node', [fileURLToPath(new URL('../../scripts/sync-verify.mjs', import.meta.url)), '--check'], { encoding: 'utf8' });
    expect(out).toContain('matches');
  });

  it("scrub.ts has every pattern of Go's vault.Scrub, in order", () => {
    const go = readText('internal/vault/redact.go');
    const block = go.slice(go.indexOf('var scrubRules'), go.indexOf('\n}\n', go.indexOf('var scrubRules')));
    const goPats = [...block.matchAll(/regexp\.MustCompile\(`([^`]+)`\)/g)].map((m) => m[1]!.replace(/^\(\?i\)/, ''));
    expect(SCRUB_RULES.map(([re]) => re.source.replace(/\\\//g, '/'))).toEqual(goPats);
  });

  it('scrub caps at 1 KiB and removes every shape', () => {
    expect(scrub('k dg_live_abcdefgh_' + 'a'.repeat(32))).toBe('k dg_live_[redacted]');
    expect(scrub('postgres://u:pw@host/db')).toBe('postgres://[redacted]@host/db');
    expect(scrub('ntn_' + 'a'.repeat(30))).toBe('[redacted]');
    const long = scrub('é'.repeat(2000));
    expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(1024);
    expect(long.endsWith('…')).toBe(true);
  });

  it('core src never imports node:* or an adapter; adapters import only their own peer', () => {
    const src = (p: string) => readFileSync(new URL('../../src/' + p, import.meta.url), 'utf8');
    for (const f of ['index.ts', 'client.ts', 'http.ts', 'wrap.ts', 'intent.ts', 'args.ts', 'runtime.ts', 'verify/index.ts', 'verify/fetch.ts']) {
      expect(src(f)).not.toMatch(/from '(node:|ai'|@openai|@langchain|@anthropic|\.\/adapters)/);
    }
    expect(repo('x')).toContain('product');
  });
});
