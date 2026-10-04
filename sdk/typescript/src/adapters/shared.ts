// What every adapter shares (S31 §3.7): model-facing names and sentences, and the govern call that
// runs a framework's own tool function behind Deedglass (proxy mode).

import type { Deedglass } from '../client.js';
import { copy } from '../copy.js';
import { ApprovalPendingError, type DeedglassError, isDeedglassError } from '../errors.js';
import type { Action, JsonObject, ToolInfo } from '../types.js';
import { clock, wrapTool } from '../wrap.js';

export interface GovernOptions {
  client: Deedglass;
  /** The connector name your tools are recorded under: "<connector>.<tool>". Default "app". */
  connector?: string;
  /** Deedglass tool names by framework tool name, when the default "<connector>.<name>" doesn't fit. */
  names?: Record<string, string>;
  /** A denial: 'message' (default) returns the sentence to the model; 'throw' raises the error. */
  onDenied?: 'message' | 'throw';
  /** A pending approval: 'message' (default), 'throw' ApprovalPendingError, or 'wait' for a person. */
  onPending?: 'message' | 'throw' | 'wait';
  approvalTimeoutMs?: number;
  failureMeans?: 'unknown' | 'not_applied';
  /** Called with each action Deedglass decided: the sealed action after a run, a denial, a pending approval. */
  onAction?: (a: Action) => void;
}

const MODEL_NAME = /^[a-zA-Z0-9_-]{1,64}$/;

/** "github.create_issue" → "github__create_issue": the name a model sees. Refuses names over 64 characters. */
export function modelToolName(tool: string): string {
  const i = tool.indexOf('.');
  const out = i < 0 ? tool : tool.slice(0, i) + '__' + tool.slice(i + 1).replace(/\./g, '_');
  if (!MODEL_NAME.test(out)) throw new TypeError(`${JSON.stringify(tool)} can't be a model tool name: it must fit ^[a-zA-Z0-9_-]{1,64}$ after the dot becomes __.`);
  return out;
}

/** "github__create_issue" → "github.create_issue". */
export function fromModelToolName(name: string): string {
  const i = name.indexOf('__');
  return i < 0 ? name : name.slice(0, i) + '.' + name.slice(i + 2);
}

/** Model names for tools, refusing collisions. */
export function modelNames(tools: ToolInfo[]): Map<string, ToolInfo> {
  const out = new Map<string, ToolInfo>();
  for (const t of tools) {
    const n = modelToolName(t.name);
    if (out.has(n)) throw new TypeError(`Two Deedglass tools map to the model name ${n}: ${out.get(n)!.name} and ${t.name}.`);
    out.set(n, t);
  }
  return out;
}

/** S11's sentence, then " Nothing was run." (Stop gets its own sentence.) */
export function denialText(err: DeedglassError | { message: string; code?: string }): string {
  if (err.code === 'agent_stopped' || err.code === 'org_stopped') return copy.stopped;
  const m = err.message.trim().replace(/\s*Nothing was run\.$/, '');
  return `${m.replace(/\.?$/, '.')} ${copy.nothingRan}`;
}

/** S14's MCP wording for a pending approval. */
export function pendingText(a: Pick<Action, 'decision' | 'approval' | 'next_action'>): string {
  const msg = (a.decision?.message ?? 'Waiting for a person to approve this action.').replace(/\.?$/, '.');
  const apr = a.approval?.id ?? a.next_action?.approval ?? 'apr_…';
  const exp = a.approval?.expires_at ?? a.next_action?.expires_at ?? '';
  return `${msg} Approval ${apr} expires at ${clock(exp)}. Try again after a person approves it.`;
}

/** What a gateway tool hands back to the model: the result, or the sentence about why there is none. */
export function resultText(a: Action): string {
  switch (a.status) {
    case 'executed': {
      const r = a.execution?.result;
      const out = r === undefined ? '' : typeof r === 'string' ? r : JSON.stringify(r);
      const w = a.witness;
      const note = w && w.verdict && w.verdict !== 'VERIFIED' ? `\nDeedglass witness · ${w.level} · ${w.verdict}${w.summary ? ' · ' + w.summary : ''}` : '';
      return (out || 'Done.') + note;
    }
    case 'failed':
      return `${(a.execution?.error_message ?? 'The tool failed.').replace(/\.?$/, '.')} Nothing was applied.`;
    case 'pending_approval':
      return pendingText(a);
    case 'denied':
      return denialText({ message: a.decision?.message ?? 'Deedglass refused this action.' });
  }
  return `Deedglass recorded this action as ${a.status}.`;
}

/** The Deedglass tool name of a framework tool. */
export function deedglassName(o: GovernOptions, name: string): string {
  return o.names?.[name] ?? `${o.connector ?? 'app'}.${name}`;
}

/**
 * Runs `run` behind Deedglass in proxy mode. A denial or a pending approval becomes model text
 * (by default), so the framework's run continues; the function itself runs only once authorized.
 */
export async function governCall<R>(
  o: GovernOptions,
  tool: string,
  args: JsonObject,
  run: (signal: AbortSignal) => Promise<R> | R,
  call: { signal?: AbortSignal | undefined } = {},
): Promise<R | string> {
  const w = wrapTool<JsonObject, R>((_a, ctx) => run(ctx.signal), {
    client: o.client,
    tool,
    ...(o.failureMeans ? { failureMeans: o.failureMeans } : {}),
    onPending: o.onPending === 'wait' ? 'wait' : 'throw',
    ...(o.approvalTimeoutMs !== undefined ? { approvalTimeoutMs: o.approvalTimeoutMs } : {}),
    ...(o.onAction ? { onSealed: o.onAction } : {}),
  });
  try {
    return await w(args, call.signal ? { signal: call.signal } : {});
  } catch (err) {
    if (err instanceof ApprovalPendingError || (isDeedglassError(err) && err.code === 'approval_required')) {
      if (o.onPending === 'throw') throw err;
      const a = (err as DeedglassError).action;
      if (a) o.onAction?.(a);
      return a ? pendingText(a) : err.message;
    }
    if (isDeedglassError(err) && (err.type === 'policy_error' || err.type === 'permission_error' || err.code === 'approval_expired' || err.code === 'budget_exceeded')) {
      if (err.action) o.onAction?.(err.action);
      if (o.onDenied === 'throw') throw err;
      return denialText(err);
    }
    throw err;
  }
}
