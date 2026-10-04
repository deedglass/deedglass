// wrapTool (S31 §3.5): proxy mode for your own functions. Deedglass decides; your function runs
// only after the action is authorized, at most once per action; the outcome is reported and sealed
// (L0, edge-reported). A thrown error means "it may have run" unless you say otherwise.

import { canonicalArgs } from './args.js';
import type { Deedglass } from './client.js';
import { TOOL_NAME } from './client.js';
import { copy } from './copy.js';
import {
  ApprovalPendingError,
  type DeedglassError,
  InvalidRequestError,
  isDeedglassError,
  PermissionDeniedError,
  PolicyDeniedError,
  ToolExecutionError,
} from './errors.js';
import { scrub } from './scrub.js';
import type { Action, EdgeResult, JsonObject, JsonValue } from './types.js';
import { canonicalize } from './verify/jcs.js';
import { digest } from './verify/sha256.js';

export const MAX_RESULT_BYTES: number = 256 * 1024;
export const MAX_TEXT_RESULT: number = 65_536;
const REPORT_ATTEMPTS = 5;

export interface WrapToolOptions<R> {
  client: Deedglass;
  /** "<connector>.<tool>", for example "billing.refund_customer". */
  tool: string;
  /** What a thrown error means. 'unknown' (default): it may have run. 'not_applied': it failed before any side effect. */
  failureMeans?: 'unknown' | 'not_applied';
  /** A pending approval: 'throw' ApprovalPendingError (default), or 'wait' for a person to decide. */
  onPending?: 'throw' | 'wait';
  /** How long 'wait' waits, default 600,000 ms. */
  approvalTimeoutMs?: number;
  /** What is reported as the result (default: the value if it is JSON, else { text: String(value) }). */
  toResult?: (r: R) => JsonValue;
  onSealed?: (a: Action) => void;
  /** The connector_type declared to policy. Default "sdk". */
  connectorType?: string;
}

export interface CallOptions {
  signal?: AbortSignal;
  onBehalfOf?: string;
  session?: string;
  /** The framework's tool call id, sent as context.call_id (S32). */
  callId?: string;
}

export type ToolFn<A, R> = (args: A, ctx: { signal: AbortSignal; actionId: string }) => Promise<R> | R;

/** The JSON value of r, or undefined when r isn't plain JSON (undefined, a function, a BigInt, a cycle, NaN). */
export function asJson(v: unknown, seen: Set<object> = new Set(), depth = 0): JsonValue | undefined {
  if (depth > 64) return undefined;
  if (v === null) return null;
  switch (typeof v) {
    case 'string':
    case 'boolean':
      return v;
    case 'number':
      return Number.isFinite(v) ? v : undefined;
    case 'object': {
      if (seen.has(v)) return undefined;
      seen.add(v);
      try {
        if (Array.isArray(v)) {
          const out: JsonValue[] = [];
          for (const x of v) {
            const j = asJson(x, seen, depth + 1);
            if (j === undefined) return undefined;
            out.push(j);
          }
          return out;
        }
        const proto = Object.getPrototypeOf(v);
        if (proto !== Object.prototype && proto !== null) return undefined;
        const out: Record<string, JsonValue> = {};
        for (const [k, x] of Object.entries(v)) {
          if (x === undefined) continue;
          const j = asJson(x, seen, depth + 1);
          if (j === undefined) return undefined;
          out[k] = j;
        }
        return out;
      } finally {
        seen.delete(v);
      }
    }
  }
  return undefined;
}

function textOf(r: unknown): string {
  let s: string;
  try {
    s = typeof r === 'bigint' ? r.toString() : String(r);
  } catch {
    s = Object.prototype.toString.call(r);
  }
  return s.slice(0, MAX_TEXT_RESULT);
}

/** The result part of an edge report: the JSON up to 256 KiB, else only its digest. */
export async function resultPart(v: JsonValue): Promise<Pick<EdgeResult, 'result' | 'result_digest'>> {
  const c = canonicalize(v);
  if (new TextEncoder().encode(c).length <= MAX_RESULT_BYTES) return { result: v };
  return { result_digest: await digest(c) };
}

function deniedError(a: Action): DeedglassError {
  const msg = a.approval?.decision_reason ? `A person denied this action: ${a.approval.decision_reason}` : a.decision?.message ?? 'Deedglass refused this action.';
  const init = { type: 'policy_error' as const, code: a.status === 'expired' ? 'approval_expired' : 'policy_denied', message: msg, action: a, requestId: a.meta?.requestId ?? null };
  if (a.status === 'expired') return new InvalidRequestError({ ...init, type: 'invalid_request_error', message: `Approval ${a.approval?.id ?? ''} expired before a person decided. Nothing was run.`.replace('  ', ' ') });
  if (a.status === 'cancelled') return new PermissionDeniedError({ ...init, type: 'permission_error', code: 'cancelled', message: 'The action was cancelled before it ran. Nothing was run.' });
  return new PolicyDeniedError(init);
}

function pendingError(a: Action): ApprovalPendingError {
  const apr = a.approval?.id ?? a.next_action?.approval ?? '';
  const exp = a.approval?.expires_at ?? a.next_action?.expires_at ?? '';
  return new ApprovalPendingError({
    type: 'policy_error',
    code: 'approval_required',
    message: a.decision?.message ? `${a.decision.message.replace(/\.?$/, '.')} ${copy.approvalPending(apr, clock(exp))}` : copy.approvalPending(apr, clock(exp)),
    action: a,
    approvalId: apr,
    actionId: a.id,
    expiresAt: exp,
    requestId: a.meta?.requestId ?? null,
  });
}

/** "10:42 UTC" from an RFC 3339 time. */
export function clock(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return 'its expiry';
  const d = new Date(t);
  return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')} UTC`;
}

/** The pending registry and per-action locks, shared by every wrapped tool of one client. */
interface Registry {
  pending: Map<string, string>;
  running: Map<string, Promise<unknown>>;
}
const registries = new WeakMap<Deedglass, Registry>();
function registryOf(c: Deedglass): Registry {
  let r = registries.get(c);
  if (!r) {
    r = { pending: new Map(), running: new Map() };
    registries.set(c, r);
  }
  return r;
}

/**
 * Puts Deedglass in front of fn. The wrapped function submits a proxy-mode action; fn runs only
 * after the action is authorized, then its outcome is reported and sealed.
 */
export function wrapTool<A extends JsonObject, R>(fn: ToolFn<A, R>, opts: WrapToolOptions<R>): (args: A, call?: CallOptions) => Promise<R> {
  if (typeof opts.tool !== 'string' || opts.tool.length > 200 || !TOOL_NAME.test(opts.tool)) {
    throw new InvalidRequestError({ type: 'client_error', code: 'validation_failed', message: copy.badToolName(String(opts.tool)), param: 'tool' });
  }
  const client = opts.client;
  const reg = registryOf(client);
  const failureMeans = opts.failureMeans ?? 'unknown';

  const report = async (a: Action, r: EdgeResult): Promise<void> => {
    const deadline = Date.parse(a.next_action?.deadline ?? '') || Date.now() + 60_000;
    for (let i = 0; i < REPORT_ATTEMPTS; i++) {
      try {
        const sealed = await client.actions.reportResult(a.id, r);
        opts.onSealed?.(sealed);
        return;
      } catch (err) {
        if (isDeedglassError(err) && err.status === 409) return; // already reported
        if (isDeedglassError(err) && err.status !== null && err.status < 500 && err.status !== 408 && err.status !== 429) break;
        const left = deadline - Date.now();
        if (left <= 0) break;
        await new Promise((res) => setTimeout(res, Math.min(left, 250 * 2 ** i)));
      }
    }
    client.logger.warn(copy.reportLost(a.id));
  };

  const run = (a: Action, args: A, call: CallOptions): Promise<R> => {
    const existing = reg.running.get(a.id);
    if (existing) return existing as Promise<R>;
    const p = (async (): Promise<R> => {
      const deadlineAt = Date.parse(a.next_action?.deadline ?? '');
      const sigs: AbortSignal[] = [];
      if (Number.isFinite(deadlineAt)) sigs.push(AbortSignal.timeout(Math.max(0, deadlineAt - Date.now())));
      if (call.signal) sigs.push(call.signal);
      const signal = sigs.length ? AbortSignal.any(sigs) : new AbortController().signal;
      const started = new Date().toISOString();
      let value: R;
      try {
        value = await fn(args, { signal, actionId: a.id });
      } catch (err) {
        const finished = new Date().toISOString();
        const m = err instanceof Error ? err.message : textOf(err);
        await report(a, {
          status: failureMeans === 'not_applied' ? 'failed' : 'unknown',
          started_at: started,
          finished_at: finished,
          error_code: 'tool_error',
          error_message: scrub(m || 'The function threw.'),
        });
        throw new ToolExecutionError({
          type: 'client_error',
          code: 'tool_error',
          message: failureMeans === 'not_applied' ? copy.toolFailedNotApplied(opts.tool) : copy.toolExecution(opts.tool),
          actionId: a.id,
          cause: err,
        });
      }
      const finished = new Date().toISOString();
      let j: JsonValue | undefined;
      try {
        j = opts.toResult ? opts.toResult(value) : asJson(value);
      } catch {
        j = undefined;
      }
      if (j === undefined) j = { text: textOf(value) };
      await report(a, { status: 'succeeded', started_at: started, finished_at: finished, ...(await resultPart(j)) });
      return value;
    })();
    reg.running.set(a.id, p);
    p.then(
      () => reg.running.delete(a.id),
      () => reg.running.delete(a.id),
    );
    return p;
  };

  return async (args: A, call: CallOptions = {}): Promise<R> => {
    const regKey = opts.tool + '\n' + (await digest(canonicalArgs(args))) + '\n' + (call.onBehalfOf ?? '');
    const decide = async (a: Action): Promise<R> => {
      switch (a.status) {
        case 'authorized':
          try {
            return await run(a, args, call);
          } finally {
            reg.pending.delete(regKey);
          }
        case 'pending_approval':
          reg.pending.set(regKey, a.id);
          if (opts.onPending === 'wait') {
            const settled = await client.actions.waitUntilSettled(a.id, { timeoutMs: opts.approvalTimeoutMs ?? 600_000, ...(call.signal ? { signal: call.signal } : {}) });
            if (settled.status === 'pending_approval') throw pendingError(settled);
            return decide(settled);
          }
          throw pendingError(a);
        case 'denied':
        case 'expired':
        case 'cancelled':
          reg.pending.delete(regKey);
          throw deniedError(a);
      }
      reg.pending.delete(regKey);
      throw new InvalidRequestError({ type: 'client_error', code: 'unexpected_status', message: `Deedglass answered status ${a.status} for a proxy action. Nothing was run.`, action: a });
    };
    const pendingId = reg.pending.get(regKey);
    if (pendingId) {
      const a = await client.actions.get(pendingId, call.signal ? { signal: call.signal } : {});
      if (a.status !== 'executed' && a.status !== 'failed') return decide(a);
      reg.pending.delete(regKey); // it finished: a repeat is a new action
    }
    const a = await client.submitProxy(opts.tool, args, {
      ...(opts.connectorType ? { connectorType: opts.connectorType } : {}),
      ...(call.onBehalfOf !== undefined ? { onBehalfOf: call.onBehalfOf } : {}),
      ...(call.session !== undefined ? { session: call.session } : {}),
      ...(call.signal ? { signal: call.signal } : {}),
      ...(call.callId !== undefined ? { callId: call.callId } : {}),
      waitMs: 0,
    });
    return decide(a);
  };
}
