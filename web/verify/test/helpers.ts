import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

/** A path under the repo root (web/verify/test → ../../..). */
export function repo(rel: string): string {
  return fileURLToPath(new URL('../../../' + rel, import.meta.url));
}

export function readBytes(rel: string): Uint8Array {
  return new Uint8Array(readFileSync(repo(rel)));
}

export function readText(rel: string): string {
  return readFileSync(repo(rel), 'utf8');
}

/** The first failure of a report as {valid, code, seq, field}, the shape expected.json uses. */
export function outcome(rep: { failures: { code: string; seq: number | null; field: string | null }[] }) {
  const f = rep.failures[0];
  return f ? { valid: false, code: f.code, seq: f.seq, field: f.field } : { valid: true, code: null, seq: null, field: null };
}
