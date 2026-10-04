// @deedglass/sdk/claude-agent-sdk: the Claude Agent SDK (@anthropic-ai/claude-agent-sdk 0.3.x).
// deedglassHooks governs every tool call the SDK routes through hooks (claude.Bash, claude.Write,
// claude.mcp__github__create_issue): PreToolUse asks Deedglass first, PostToolUse and
// PostToolUseFailure report what happened, Stop reports calls that never produced a Post event.
// The hooks fail closed: when Deedglass can't be reached, the call is denied. Deedglass allowing a
// call returns {}, which defers to the host's own permission rules; it never skips a user prompt.

import type { HookCallback, HookCallbackMatcher, HookEvent, McpHttpServerConfig, SdkMcpToolDefinition } from '@anthropic-ai/claude-agent-sdk';
import { apiKeyOf, type Deedglass } from '../client.js';
import { copy } from '../copy.js';
import { isDeedglassError } from '../errors.js';
import { scrub } from '../scrub.js';
import type { Action, JsonObject, JsonValue } from '../types.js';
import { asJson, resultPart } from '../wrap.js';
import { deedglassName, denialText, type GovernOptions, governCall, pendingText } from './shared.js';

export type { GovernOptions } from './shared.js';
export { denialText, pendingText } from './shared.js';

export interface HooksOptions {
  client: Deedglass;
  /** Which tools to govern (the SDK's matcher). Default: every tool. */
  matcher?: string;
  /** The connector the tools are recorded under. Default "claude". */
  connector?: string;
  /** A pending approval: 'deny' with the pending sentence (default), or 'wait' inside the hook. */
  onPending?: 'deny' | 'wait';
  approvalTimeoutMs?: number;
  failureMeans?: 'unknown' | 'not_applied';
}

type Hooks = Partial<Record<HookEvent, HookCallbackMatcher[]>>;

function deny(reason: string): Awaited<ReturnType<HookCallback>> {
  return { hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: reason } };
}

function argsOf(v: unknown): JsonObject {
  const j = asJson(v);
  return j && typeof j === 'object' && !Array.isArray(j) ? j : { input: j ?? null };
}

/** The hooks config for query({ options: { hooks } }). */
export function deedglassHooks(o: HooksOptions): Hooks {
  const client = o.client;
  const connector = o.connector ?? 'claude';
  const timeoutMs = o.approvalTimeoutMs ?? 600_000;
  const open = new Map<string, { action: Action; started: string }>();

  const report = async (a: Action, body: Parameters<Deedglass['actions']['reportResult']>[1]): Promise<void> => {
    try {
      await client.actions.reportResult(a.id, body);
    } catch (err) {
      if (isDeedglassError(err) && err.status === 409) return;
      client.logger.warn(copy.reportLost(a.id));
    }
  };

  const pre: HookCallback = async (input, toolUseID, { signal }) => {
    if (input.hook_event_name !== 'PreToolUse') return {};
    const id = input.tool_use_id ?? toolUseID ?? '';
    try {
      let a = await client.submitProxy(`${connector}.${input.tool_name}`, argsOf(input.tool_input), { waitMs: 0, signal });
      if (a.status === 'pending_approval' && o.onPending === 'wait') a = await client.actions.waitUntilSettled(a.id, { timeoutMs, signal });
      switch (a.status) {
        case 'authorized':
          if (id) open.set(id, { action: a, started: new Date().toISOString() });
          return {};
        case 'pending_approval':
          return deny(pendingText(a));
      }
      return deny(denialText({ message: a.approval?.decision_reason ? `A person denied this action: ${a.approval.decision_reason}` : (a.decision?.message ?? 'Deedglass refused this action.') }));
    } catch (err) {
      if (isDeedglassError(err) && (err.type === 'policy_error' || err.type === 'permission_error' || err.code === 'budget_exceeded')) return deny(denialText(err));
      return deny(copy.outage);
    }
  };

  const post: HookCallback = async (input) => {
    if (input.hook_event_name !== 'PostToolUse') return {};
    const e = open.get(input.tool_use_id);
    if (!e) return {};
    open.delete(input.tool_use_id);
    const j: JsonValue = asJson(input.tool_response) ?? { text: String(input.tool_response).slice(0, 65_536) };
    await report(e.action, { status: 'succeeded', started_at: e.started, finished_at: new Date().toISOString(), ...(await resultPart(j)) });
    return {};
  };

  const failure: HookCallback = async (input) => {
    if (input.hook_event_name !== 'PostToolUseFailure') return {};
    const e = open.get(input.tool_use_id);
    if (!e) return {};
    open.delete(input.tool_use_id);
    await report(e.action, {
      status: o.failureMeans === 'not_applied' ? 'failed' : 'unknown',
      started_at: e.started,
      finished_at: new Date().toISOString(),
      error_code: 'tool_error',
      error_message: scrub(input.error || 'The tool failed.'),
    });
    return {};
  };

  const stop: HookCallback = async (input) => {
    if (input.hook_event_name !== 'Stop') return {};
    const left = [...open.values()];
    open.clear();
    const now = new Date().toISOString();
    await Promise.all(left.map((e) => report(e.action, { status: 'unknown', started_at: e.started, finished_at: now, error_code: 'no_result_observed', error_message: copy.noResultObserved })));
    return {};
  };

  const m = (hooks: HookCallback[], timeout?: number): HookCallbackMatcher => ({ ...(o.matcher ? { matcher: o.matcher } : {}), hooks, ...(timeout ? { timeout } : {}) });
  return {
    PreToolUse: [m([pre], o.onPending === 'wait' ? Math.ceil(timeoutMs / 1000) + 30 : 60)],
    PostToolUse: [m([post])],
    PostToolUseFailure: [m([failure])],
    Stop: [{ hooks: [stop] }],
  };
}

/** Puts Deedglass in front of an SDK MCP tool's handler. A denial is an error result with the sentence. */
export function governSdkTool<T extends SdkMcpToolDefinition<any>>(def: T, o: GovernOptions): T {
  const dg = deedglassName(o, def.name);
  return {
    ...def,
    handler: async (args: any, extra: unknown) => {
      const r = await governCall(o, dg, argsOf(args), () => def.handler(args, extra));
      return typeof r === 'string' ? { content: [{ type: 'text', text: r }], isError: true } : r;
    },
  } as T;
}

/** An MCP HTTP server config for Deedglass's gateway tools (S15): /mcp/native, or /mcp/<connector>. */
export function deedglassMcpServer(client: Deedglass, o: { connector?: string } = {}): McpHttpServerConfig {
  const path = o.connector ? '/mcp/' + encodeURIComponent(o.connector) : '/mcp/native';
  return { type: 'http', url: client.baseUrl + path, headers: { Authorization: 'Bearer ' + apiKeyOf(client) } };
}
