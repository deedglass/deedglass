// The Vercel AI SDK with Deedglass in front of its tools. --scripted (the default) uses AI SDK's
// own mock model, so no model key is needed; the tool calls are real Deedglass actions.
//   node examples/ts/ai-sdk/index.ts
import { generateText, jsonSchema, tool } from 'ai';
import { MockLanguageModelV4 } from 'ai/test';
import { Deedglass } from '@deedglass/sdk';
import { governTools } from '@deedglass/sdk/ai-sdk';
import { CALLS, checkOffline, printer } from '../_lib/demo.ts';
import { live, note } from '../_lib/print.ts';

const dg = new Deedglass();
const p = printer();

// Your tools, unchanged.
const tools = {
  refund_customer: tool({
    description: 'Refund part of a charge, in cents.',
    inputSchema: jsonSchema<{ customer: string; amount: number }>({ type: 'object', properties: { customer: { type: 'string' }, amount: { type: 'integer' } }, required: ['customer', 'amount'] }),
    execute: async ({ amount }) => ({ refunded: amount }),
  }),
  delete_customer: tool({
    description: 'Delete a customer.',
    inputSchema: jsonSchema<{ customer: string }>({ type: 'object', properties: { customer: { type: 'string' } }, required: ['customer'] }),
    execute: async () => ({ deleted: true }),
  }),
};

if (live) note('Live mode needs a provider package (for example @ai-sdk/anthropic); this example runs scripted.');
const usage = { inputTokens: { total: 1, noCache: 1, cacheRead: 0, cacheWrite: 0 }, outputTokens: { total: 1, text: 1, reasoning: 0 } };
const model = new MockLanguageModelV4({
  doGenerate: {
    content: CALLS.map((c, i) => ({ type: 'tool-call' as const, toolCallId: `call_${i}`, toolName: c.name, input: JSON.stringify(c.args) })),
    finishReason: { unified: 'tool-calls', raw: 'tool_use' },
    usage,
    warnings: [],
  } as never,
});
await generateText({ model, tools: governTools(tools, { client: dg, connector: 'billing', onAction: p.onAction }), prompt: 'Refund cus_123 and clean up.' });
await checkOffline(dg, p.deeds);
