// @deedglass/sdk/openai-agents: the OpenAI Agents SDK (@openai/agents 0.18.x). governTool returns a
// FunctionTool whose invoke runs the proxy flow first; deedglassTools exposes the org's connector
// tools (gateway mode) with names that fit ^[a-zA-Z0-9_-]{1,64}$.

import { type FunctionTool, tool } from '@openai/agents';
import type { Deedglass } from '../client.js';
import type { JsonObject } from '../types.js';
import { deedglassName, type GovernOptions, governCall, modelNames, resultText } from './shared.js';

export type { GovernOptions } from './shared.js';
export { denialText, pendingText, modelToolName, fromModelToolName } from './shared.js';

function parse(input: string): JsonObject {
  try {
    const v = JSON.parse(input || '{}') as unknown;
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as JsonObject) : {};
  } catch {
    return {};
  }
}

/** Puts Deedglass in front of a FunctionTool: the original invoke runs only once authorized. */
export function governTool<T extends FunctionTool<any, any, any>>(t: T, o: GovernOptions): T {
  const dg = deedglassName(o, t.name);
  const invoke = t.invoke.bind(t);
  return {
    ...t,
    invoke: (runContext: any, input: string, details?: any) =>
      governCall(o, dg, parse(input), () => invoke(runContext, input, details), { signal: details?.signal }),
  } as T;
}

/** The org's connector tools from GET /v1/tools, run by Deedglass. */
export async function deedglassTools(client: Deedglass, o: { include?: string[] } = {}): Promise<FunctionTool<any, any, any>[]> {
  const list = await client.tools.list(o.include ? { include: o.include } : {});
  return [...modelNames(list)].map(([name, t]) =>
    tool({
      name,
      description: t.description || t.title,
      parameters: t.input_schema as never,
      strict: false,
      execute: async (input: unknown) => resultText(await client.act(t.name, (input ?? {}) as JsonObject)),
    } as never) as FunctionTool<any, any, any>,
  );
}
