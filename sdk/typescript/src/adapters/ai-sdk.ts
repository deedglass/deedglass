// @deedglass/sdk/ai-sdk: the Vercel AI SDK (ai 7.x). governTools wraps your tools' execute in
// proxy mode; deedglassTools exposes the org's connector tools (gateway mode). AI SDK's own
// toolApproval asks the chat user; Deedglass asks the org's approvers. Use either or both.

import { jsonSchema, type Tool, tool } from 'ai';
import type { Deedglass } from '../client.js';
import type { JsonObject } from '../types.js';
import { deedglassName, type GovernOptions, governCall, modelNames, resultText } from './shared.js';

export type { GovernOptions } from './shared.js';
export { denialText, pendingText, modelToolName, fromModelToolName } from './shared.js';

type AnyTool = Tool<any, any, any>;

/** Puts Deedglass in front of each tool's execute. Tools without execute are left alone. */
export function governTools<T extends Record<string, object>>(tools: T, o: GovernOptions): T {
  const out: Record<string, object> = {};
  for (const [name, t] of Object.entries(tools)) {
    const exec = (t as { execute?: (input: unknown, options: any) => unknown }).execute;
    if (typeof exec !== 'function') {
      o.client.logger.warn(`@deedglass/sdk/ai-sdk: the tool ${name} has no execute, so Deedglass leaves it alone.`);
      out[name] = t;
      continue;
    }
    const dg = deedglassName(o, name);
    out[name] = {
      ...t,
      execute: (input: unknown, options: { toolCallId?: string; abortSignal?: AbortSignal }) =>
        governCall(o, dg, (input ?? {}) as JsonObject, (signal) => exec.call(t, input, { ...options, abortSignal: signal }), { signal: options?.abortSignal, callId: options?.toolCallId }),
    } as AnyTool;
  }
  return out as T;
}

/** The org's connector tools from GET /v1/tools, run by Deedglass (credentials never leave it). */
export async function deedglassTools(client: Deedglass, o: { include?: string[] } = {}): Promise<Record<string, AnyTool>> {
  const list = await client.tools.list(o.include ? { include: o.include } : {});
  const out: Record<string, AnyTool> = {};
  for (const [name, t] of modelNames(list)) {
    out[name] = tool({
      description: t.description || t.title,
      inputSchema: jsonSchema(t.input_schema as never),
      execute: async (input: unknown, options: { abortSignal?: AbortSignal }) =>
        resultText(await client.act(t.name, (input ?? {}) as JsonObject, options?.abortSignal ? { signal: options.abortSignal } : {})),
    } as never) as AnyTool;
  }
  return out;
}
