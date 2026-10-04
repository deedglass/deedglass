// @deedglass/sdk/langchain: LangChain.js (@langchain/core 1.2.x). governTool rebuilds a tool so
// its invoke runs the proxy flow first; deedglassTools passes JSON Schema (LangChain doesn't
// validate it; Deedglass validates arguments server-side).

import { type StructuredToolInterface, tool } from '@langchain/core/tools';
import type { Deedglass } from '../client.js';
import type { JsonObject } from '../types.js';
import { deedglassName, type GovernOptions, governCall, modelNames, resultText } from './shared.js';

export type { GovernOptions } from './shared.js';
export { denialText, pendingText, modelToolName, fromModelToolName } from './shared.js';

/** Puts Deedglass in front of a LangChain tool: t.invoke runs only once authorized. */
export function governTool(t: StructuredToolInterface, o: GovernOptions): StructuredToolInterface {
  const dg = deedglassName(o, t.name);
  return tool(
    async (input: unknown, config: any) =>
      governCall(o, dg, (input ?? {}) as JsonObject, () => t.invoke(input as never, config), { signal: config?.signal, callId: config?.toolCall?.id }),
    { name: t.name, description: t.description, schema: t.schema as never },
  ) as unknown as StructuredToolInterface;
}

/** The org's connector tools from GET /v1/tools, run by Deedglass. */
export async function deedglassTools(client: Deedglass, o: { include?: string[] } = {}): Promise<StructuredToolInterface[]> {
  const list = await client.tools.list(o.include ? { include: o.include } : {});
  return [...modelNames(list)].map(
    ([name, t]) =>
      tool(async (input: unknown, config: any) => resultText(await client.act(t.name, (input ?? {}) as JsonObject, config?.signal ? { signal: config.signal } : {})), {
        name,
        description: t.description || t.title,
        schema: t.input_schema as never,
      }) as unknown as StructuredToolInterface,
  );
}
