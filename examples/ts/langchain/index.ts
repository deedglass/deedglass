// LangChain.js with Deedglass in front of a tool. --scripted (the default) invokes the tools with
// tool calls the way an agent executor does, so no model key is needed.
//   node examples/ts/langchain/index.ts
import { tool } from '@langchain/core/tools';
import { Deedglass } from '@deedglass/sdk';
import { governTool } from '@deedglass/sdk/langchain';
import { CALLS, checkOffline, printer } from '../_lib/demo.ts';
import { live, note } from '../_lib/print.ts';

const dg = new Deedglass();
const p = printer();
const schema = (props: Record<string, unknown>, required: string[]) => ({ type: 'object', properties: props, required }) as never;
const tools = {
  refund_customer: governTool(tool(async (i: { amount: number }) => `refunded ${i.amount}`, { name: 'refund_customer', description: 'Refund part of a charge, in cents.', schema: schema({ customer: { type: 'string' }, amount: { type: 'integer' } }, ['customer', 'amount']) }), { client: dg, connector: 'billing', onAction: p.onAction }),
  delete_customer: governTool(tool(async () => 'deleted', { name: 'delete_customer', description: 'Delete a customer.', schema: schema({ customer: { type: 'string' } }, ['customer']) }), { client: dg, connector: 'billing', onAction: p.onAction }),
};
if (live) note('Live mode: bind these tools to a chat model with OPENAI_API_KEY or ANTHROPIC_API_KEY set. This example runs scripted.');
let i = 0;
for (const c of CALLS) await tools[c.name].invoke({ id: `call_${i++}`, name: c.name, args: c.args, type: 'tool_call' } as never);
await checkOffline(dg, p.deeds);
