// The native route: @deedglass/sdk's hooks ask Deedglass before every tool Claude runs (Bash, Write,
// MCP tools), and Deedglass's own tools come over HTTP. Needs DEEDGLASS_API_KEY, DEEDGLASS_SERVER
// and ANTHROPIC_API_KEY.
import { query } from '@anthropic-ai/claude-agent-sdk';
import { Deedglass } from '@deedglass/sdk';
import { deedglassHooks, deedglassMcpServer } from '@deedglass/sdk/claude-agent-sdk';

const dg = new Deedglass();
const options = { hooks: deedglassHooks({ client: dg }), mcpServers: { deedglass: deedglassMcpServer(dg) }, allowedTools: ['mcp__deedglass__*'] };
for await (const message of query({ prompt: 'How many rows are in public.orders?', options })) {
  if (message.type === 'result' && message.subtype === 'success') console.log(message.result);
}
