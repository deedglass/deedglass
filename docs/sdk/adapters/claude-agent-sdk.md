# Claude Agent SDK adapter (`@deedglass/sdk/claude-agent-sdk`)

Tested with `@anthropic-ai/claude-agent-sdk` 0.3.289 (2026-10-04) by calling the hook callbacks with
the SDK's own input types, and with `createSdkMcpServer` holding a governed tool; no model call. The
peer range is `>=0.3 <0.4`.

```ts
import { query } from '@anthropic-ai/claude-agent-sdk';
import { Deedglass } from '@deedglass/sdk';
import { deedglassHooks, deedglassMcpServer } from '@deedglass/sdk/claude-agent-sdk';

const dg = new Deedglass();
for await (const m of query({ prompt, options: { hooks: deedglassHooks({ client: dg }), mcpServers: { deedglass: deedglassMcpServer(dg) } } })) { … }
```

- **`deedglassHooks({ client, matcher, connector = 'claude', onPending = 'deny', approvalTimeoutMs, failureMeans })`**
  - `PreToolUse` records a proxy action `claude.<tool_name>` (`claude.Bash`, `claude.Write`,
    `claude.mcp__github__create_issue`) with `tool_input` as its arguments.
    Denied → `permissionDecision: 'deny'` with the policy sentence. Pending → `deny` with the pending
    sentence, or with `onPending: 'wait'` a long poll inside the hook (the matcher's `timeout` becomes
    `approvalTimeoutMs / 1000 + 30`). Authorized → `{}`, which defers to the host's own permission
    rules: Deedglass allowing a call never skips your prompts.
  - `PostToolUse` reports `succeeded` with `tool_response` (256 KiB, else its digest);
    `PostToolUseFailure` reports `unknown` (or `failed` with `failureMeans: 'not_applied'`).
  - `Stop` reports `unknown` (`no_result_observed`) for authorized calls that never produced a Post event.
  - **It fails closed:** when Deedglass can't be reached the hook denies with "Deedglass couldn't be
    reached, so this tool call was not run." It never throws, and there is no fail-open option.
- **`governSdkTool(def, options)`** wraps an SDK MCP tool's handler; a denial is
  `{ content: [{ type: 'text', text }], isError: true }`.
- **`deedglassMcpServer(client, { connector })`** returns `{ type: 'http', url: '<server>/mcp/native', headers: { Authorization } }`
  for Deedglass's gateway tools (S15).
- **Policy:** `examples/policies/claude-agent-local.yaml` (15 cases) lets reads run, never runs
  `rm -rf /`, curl into a shell, force pushes or `.env` writes, and sends writes outside `/workspace/`
  and every MCP tool to a person.
- **What it doesn't see:** tools the Agent SDK doesn't route through hooks.
