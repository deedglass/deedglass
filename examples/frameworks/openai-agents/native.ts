// The native route: @deedglass/sdk's OpenAI Agents adapter. Needs DEEDGLASS_API_KEY, DEEDGLASS_SERVER
// and OPENAI_API_KEY.
import { Agent, run } from '@openai/agents';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/openai-agents';

// The org's connector tools, named postgres__query and so on; Deedglass runs each one.
const tools = await deedglassTools(new Deedglass(), { include: ['postgres.*'] });
const agent = new Agent({ name: 'orders', instructions: 'Answer questions about orders.', tools });
const result = await run(agent, 'How many rows are in public.orders?');
console.log(result.finalOutput);
