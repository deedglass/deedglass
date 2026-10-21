// CI check, no model key: Mastra's MCPClient starts `deedglass mcp serve`, lists its tools, and an
// Agent takes them renamed as in agent.ts.
import { Agent } from '@mastra/core/agent';
import { MCPClient } from '@mastra/mcp';

const mcp = new MCPClient({ id: 'deedglass-check', servers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } } });
try {
  const tools = Object.fromEntries(Object.entries(await mcp.listTools()).map(([name, t]) => [name.replace('.', '_'), t]));
  new Agent({ id: 'orders', name: 'Orders', instructions: 'Answer questions about orders.', model: 'openai/gpt-5.6-sol', tools });
  console.log(Object.keys(tools));
  if (!('deedglass_postgres_query' in tools)) throw new Error(`deedglass_postgres_query missing: ${Object.keys(tools)}`);
} finally {
  await mcp.disconnect();
}
