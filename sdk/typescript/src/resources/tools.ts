import type { Transport } from '../http.js';
import type { List, ToolInfo } from '../types.js';

/** Glob match for tool names: * matches any run of characters, ? one. */
export function globMatch(pattern: string, name: string): boolean {
  const re = new RegExp('^' + pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.') + '$');
  return re.test(name);
}

export class Tools {
  readonly #t: Transport;
  constructor(t: Transport) {
    this.#t = t;
  }

  /** GET /v1/tools: the org's connector tools this agent may call, every page. `include` filters by glob. */
  async list(o: { include?: string[]; includeHidden?: boolean; signal?: AbortSignal } = {}): Promise<ToolInfo[]> {
    const out: ToolInfo[] = [];
    let cursor: string | null = null;
    for (let page = 0; page < 100; page++) {
      const query: Record<string, string | undefined> = { include_hidden: o.includeHidden ? 'true' : undefined, cursor: cursor ?? undefined };
      const r: { data: List<ToolInfo> } = await this.#t.request<List<ToolInfo>>({ method: 'GET', path: '/v1/tools', query, signal: o.signal });
      out.push(...r.data.data);
      if (!r.data.has_more || !r.data.next_cursor) break;
      cursor = r.data.next_cursor;
    }
    return o.include?.length ? out.filter((t) => o.include!.some((p) => globMatch(p, t.name))) : out;
  }
}
