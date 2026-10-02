import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const web = fileURLToPath(new URL('..', import.meta.url));
export const repo = (p: string) => join(web, '..', p);
export const read = (p: string) => readFileSync(p, 'utf8');
export function files(dir: string, re: RegExp): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f);
    return statSync(p).isDirectory() ? files(p, re) : re.test(p) ? [p] : [];
  });
}
