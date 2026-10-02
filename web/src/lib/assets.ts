// assets.ts (build time only, used from .astro frontmatter): versioned URLs for the files in
// public/, so the server can cache them as immutable (S24 §3.10).
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

// Astro bundles this file before rendering, so paths resolve from the web/ directory the build runs in.
const pub = join(process.cwd(), 'public') + '/';
const cache = new Map<string, string>();

export function asset(file: string): string {
  let v = cache.get(file);
  if (!v) {
    v = createHash('sha256').update(readFileSync(pub + file)).digest('hex').slice(0, 12);
    cache.set(file, v);
  }
  return `/app/${file}?v=${v}`;
}

let seq = 0;
/** uid: deterministic ids for build-time markup (gradients, labels), so builds are reproducible. */
export function uid(prefix: string): string {
  return `${prefix}${++seq}`;
}
