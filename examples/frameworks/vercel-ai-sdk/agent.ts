// Run where you ran `npx deedglass init`. The model goes through the AI Gateway (AI_GATEWAY_API_KEY).
import { generateText, stepCountIs } from 'ai';
import { createMCPClient } from '@ai-sdk/mcp';
import { Experimental_StdioMCPTransport as StdioClientTransport } from '@ai-sdk/mcp/mcp-stdio';

const mcp = await createMCPClient({ transport: new StdioClientTransport({ command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] }) });
try {
  const tools = await mcp.tools(); // postgres_query and so on
  const { text } = await generateText({ model: 'anthropic/claude-sonnet-5.5', tools, stopWhen: stepCountIs(5), prompt: 'How many rows are in public.orders?' });
  console.log(text);
} finally {
  await mcp.close();
}
