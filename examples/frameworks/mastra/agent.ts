// Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import { Agent } from '@mastra/core/agent';
import { MCPClient } from '@mastra/mcp';

const mcp = new MCPClient({ id: 'deedglass', servers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } } });
try {
  // deedglass_postgres.query -> deedglass_postgres_query: model APIs reject dots in tool names.
  const tools = Object.fromEntries(Object.entries(await mcp.listTools()).map(([name, t]) => [name.replace('.', '_'), t]));
  const agent = new Agent({ id: 'orders', name: 'Orders', instructions: 'Answer questions about orders.', model: 'openai/gpt-5.6-sol', tools });
  console.log((await agent.generate('How many rows are in public.orders?')).text);
} finally {
  await mcp.disconnect();
}
