// The OpenAI Agents SDK with Deedglass in front of a FunctionTool. --scripted (the default) invokes
// the tools the way the runner does, so no model key is needed.
//   node examples/ts/openai-agents/index.ts
import { RunContext, tool } from '@openai/agents';
import { Deedglass } from '@deedglass/sdk';
import { governTool } from '@deedglass/sdk/openai-agents';
import { CALLS, checkOffline, printer } from '../_lib/demo.ts';
import { live, note } from '../_lib/print.ts';

const dg = new Deedglass();
const p = printer();
const params = (props: Record<string, unknown>, required: string[]) => ({ type: 'object', properties: props, required, additionalProperties: false }) as never;
const tools = {
  refund_customer: governTool(tool({ name: 'refund_customer', description: 'Refund part of a charge, in cents.', strict: false, parameters: params({ customer: { type: 'string' }, amount: { type: 'integer' } }, ['customer', 'amount']), execute: async (i: { amount: number }) => `refunded ${i.amount}` }), { client: dg, connector: 'billing', onAction: p.onAction }),
  delete_customer: governTool(tool({ name: 'delete_customer', description: 'Delete a customer.', strict: false, parameters: params({ customer: { type: 'string' } }, ['customer']), execute: async () => 'deleted' }), { client: dg, connector: 'billing', onAction: p.onAction }),
};
if (live) note('Live mode: pass these tools to new Agent({ tools }) and run(agent, …) with OPENAI_API_KEY set. This example runs scripted.');
for (const c of CALLS) await tools[c.name].invoke(new RunContext(), JSON.stringify(c.args));
await checkOffline(dg, p.deeds);
