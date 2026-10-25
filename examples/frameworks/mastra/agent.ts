// Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import { Agent } from '@mastra/core/agent';
import { MCPClient } from '@mastra/mcp';

const mcp = new MCPClient({ id: 'deedglass', servers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } } });
try {
  const tools = await mcp.listTools(); // deedglass_postgres_query and so on
  const agent = new Agent({ id: 'orders', name: 'Orders', instructions: 'Answer questions about orders.', model: 'openai/gpt-5.6-sol', tools });
  console.log((await agent.generate('How many rows are in public.orders?')).text);
} finally {
  await mcp.disconnect();
}
