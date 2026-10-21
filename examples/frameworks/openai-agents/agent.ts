// Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import { Agent, MCPServerStdio, run } from '@openai/agents';

const server = new MCPServerStdio({ name: 'deedglass', fullCommand: 'npx -y deedglass mcp serve' });
await server.connect();
try {
  // Names become mcp_deedglass__postgres_query: OpenAI function names can't hold a dot.
  const agent = new Agent({ name: 'orders', instructions: 'Answer questions about orders.', mcpServers: [server], mcpConfig: { includeServerInToolNames: true } });
  const result = await run(agent, 'How many rows are in public.orders?');
  console.log(result.finalOutput);
} finally {
  await server.close();
}
