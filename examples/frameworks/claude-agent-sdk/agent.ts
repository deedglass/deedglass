// Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY.
import { query } from '@anthropic-ai/claude-agent-sdk';

for await (const message of query({
  prompt: 'How many rows are in public.orders?',
  options: {
    mcpServers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } },
    allowedTools: ['mcp__deedglass__*'], // Claude Code names them mcp__deedglass__postgres_query and so on
  },
})) {
  if (message.type === 'result' && message.subtype === 'success') console.log(message.result);
}
