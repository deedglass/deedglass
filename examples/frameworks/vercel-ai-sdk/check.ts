// CI check, no model key: the same MCP client and the native adapter, each calling postgres.query once
// through Deedglass with the tool's own execute().
import { createMCPClient } from '@ai-sdk/mcp';
import { Experimental_StdioMCPTransport as StdioClientTransport } from '@ai-sdk/mcp/mcp-stdio';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/ai-sdk';

const sql = 'select count(*) as n from public.orders';
const opts = { toolCallId: 'check', messages: [] };
const mcp = await createMCPClient({ transport: new StdioClientTransport({ command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] }) });
try {
  const tools = Object.fromEntries(Object.entries(await mcp.tools()).map(([name, t]) => [name.replace('.', '_'), t]));
  const out = JSON.stringify(await tools.postgres_query.execute({ sql }, opts));
  console.log('mcp', out.slice(0, 160));
  if (!out.includes('row_count')) throw new Error('MCP route: no rows');
} finally {
  await mcp.close();
}
const native = await deedglassTools(new Deedglass(), { include: ['postgres.*'] });
console.log('native tools', Object.keys(native));
const out = JSON.stringify(await native.postgres__query.execute({ sql }, opts));
console.log('native', out.slice(0, 160));
if (!out.includes('500') && !out.includes('row')) throw new Error('native route: no rows');
