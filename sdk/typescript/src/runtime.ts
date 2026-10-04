// What the SDK learns about the runtime, without importing node:* (the core runs on Node, Bun,
// Deno, Cloudflare Workers and Vercel Edge).

type G = typeof globalThis & {
  process?: { env?: Record<string, string | undefined>; versions?: Record<string, string | undefined> };
  Deno?: { env?: { get(k: string): string | undefined }; version?: { deno?: string } };
  Bun?: { version?: string };
  window?: unknown;
  document?: unknown;
  navigator?: { userAgent?: string };
  EdgeRuntime?: string;
  WorkerGlobalScope?: unknown;
};

const g = globalThis as G;

/** An environment variable, when the runtime has a process environment (never in a browser). */
export function env(name: string): string | undefined {
  try {
    if (g.process?.env) return g.process.env[name] || undefined;
    if (g.Deno?.env) return g.Deno.env.get(name) || undefined;
  } catch {
    // Deno without --allow-env, a sandbox: no environment
  }
  return undefined;
}

/** True in a browser page (window and document, not a worker). */
export function isBrowser(): boolean {
  return typeof g.window !== 'undefined' && typeof g.document !== 'undefined';
}

/** "node/24.9.0", "bun/1.3.0", "deno/2.5.0", "edge", "workerd" or "unknown". */
export function runtime(): string {
  if (g.Bun?.version) return `bun/${g.Bun.version}`;
  if (g.Deno?.version?.deno) return `deno/${g.Deno.version.deno}`;
  if (typeof g.EdgeRuntime === 'string') return 'edge';
  if (g.navigator?.userAgent === 'Cloudflare-Workers') return 'workerd';
  if (g.process?.versions?.node) return `node/${g.process.versions.node}`;
  return 'unknown';
}
