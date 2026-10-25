// CI check, no model key: MCPServerStdio connects to `deedglass mcp serve`, an Agent takes it with
// mcpConfig, and the native adapter lists the org's connector tools.
import { Agent, MCPServerStdio } from '@openai/agents';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/openai-agents';

const server = new MCPServerStdio({ name: 'deedglass', fullCommand: 'npx -y deedglass mcp serve' });
await server.connect();
try {
  new Agent({ name: 'orders', instructions: 'Answer questions about orders.', mcpServers: [server], mcpConfig: { includeServerInToolNames: true } });
  console.log('mcp connected');
} finally {
  await server.close();
}
const names = (await deedglassTools(new Deedglass(), { include: ['postgres.*'] })).map((t) => t.name);
console.log('native tools', names);
if (!names.includes('postgres__query')) throw new Error(`postgres__query missing: ${names}`);
