// CI check, no model key: the recipe's imports resolve and the native adapter builds its hooks and its
// MCP server entry against the local stack. A model run needs ANTHROPIC_API_KEY (run.sh --agent).
import { query } from '@anthropic-ai/claude-agent-sdk';
import { Deedglass } from '@deedglass/sdk';
import { deedglassHooks, deedglassMcpServer } from '@deedglass/sdk/claude-agent-sdk';

if (typeof query !== 'function') throw new Error('query() missing');
const dg = new Deedglass();
const hooks = deedglassHooks({ client: dg });
const server = deedglassMcpServer(dg);
console.log('hooks', Object.keys(hooks), 'server', server.type, server.url);
if (!hooks.PreToolUse || server.type !== 'http') throw new Error('adapter shape changed');
