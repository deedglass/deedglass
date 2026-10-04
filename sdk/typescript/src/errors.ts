// Typed errors (S31 §3.4). Classes are chosen by the envelope's type first, then its code; an
// unknown code still produces a DeedglassError, so older SDKs keep working against newer servers.
// Server messages pass through verbatim. Error identity uses a Symbol.for brand, so two copies of
// the package never break `isDeedglassError`.

import type { Action } from './types.js';

const BRAND: unique symbol = Symbol.for('deedglass.error') as never;

export type ErrorType =
  | 'invalid_request_error'
  | 'authentication_error'
  | 'permission_error'
  | 'policy_error'
  | 'rate_limit_error'
  | 'api_error'
  | 'client_error';

export interface ErrorInit {
  type: ErrorType;
  code: string;
  message: string;
  param?: string | null;
  status?: number | null;
  requestId?: string | null;
  docUrl?: string | null;
  action?: Action;
  cause?: unknown;
}

/** Every error the SDK throws. */
export class DeedglassError extends Error {
  readonly type: ErrorType;
  readonly code: string;
  readonly param: string | null;
  readonly status: number | null;
  readonly requestId: string | null;
  readonly docUrl: string | null;
  /** The sealed action, when the server embedded it (denials, Stop, budgets). */
  readonly action?: Action;

  constructor(init: ErrorInit) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = new.target.name;
    this.type = init.type;
    this.code = init.code;
    this.param = init.param ?? null;
    this.status = init.status ?? null;
    this.requestId = init.requestId ?? null;
    this.docUrl = init.docUrl ?? null;
    if (init.action) this.action = init.action;
    Object.defineProperty(this, BRAND, { value: true });
  }
}

/** invalid_api_key, api_key_revoked, api_key_expired. */
export class AuthenticationError extends DeedglassError {}
/** agent_stopped, agent_suspended, org_stopped, insufficient_scope, intent_*. */
export class PermissionDeniedError extends DeedglassError {}
/** policy_denied (and a recorded tool_unknown): the message is the policy's sentence verbatim. */
export class PolicyDeniedError extends DeedglassError {}
/** 429 budget_exceeded: a policy limit that resets on its own schedule. Never retried. */
export class BudgetExceededError extends DeedglassError {
  readonly retryAfterMs: number;
  constructor(init: ErrorInit & { retryAfterMs: number }) {
    super(init);
    this.retryAfterMs = init.retryAfterMs;
  }
}
/** 429 rate_limited, after the retries the SDK allows. */
export class RateLimitError extends DeedglassError {
  readonly retryAfterMs: number;
  constructor(init: ErrorInit & { retryAfterMs: number }) {
    super(init);
    this.retryAfterMs = init.retryAfterMs;
  }
}
/** validation_failed, not_found, conflict, idempotency_conflict, … */
export class InvalidRequestError extends DeedglassError {}
/** 5xx, internal_error. */
export class APIError extends DeedglassError {}
/** The network failed before an answer arrived. */
export class ConnectionError extends DeedglassError {}
/** No answer within the timeout. The action may still be running. */
export class TimeoutError extends DeedglassError {
  readonly actionId?: string;
  constructor(init: ErrorInit & { actionId?: string }) {
    super(init);
    if (init.actionId) this.actionId = init.actionId;
  }
}
/** A person must approve the action first (wrapTool and act with onPending 'throw'). */
export class ApprovalPendingError extends DeedglassError {
  readonly approvalId: string;
  readonly actionId: string;
  readonly expiresAt: string;
  constructor(init: ErrorInit & { approvalId: string; actionId: string; expiresAt: string }) {
    super(init);
    this.approvalId = init.approvalId;
    this.actionId = init.actionId;
    this.expiresAt = init.expiresAt;
  }
}
/** wrapTool: your function threw after Deedglass authorized it. `cause` is the original error. */
export class ToolExecutionError extends DeedglassError {
  readonly actionId: string;
  constructor(init: ErrorInit & { actionId: string }) {
    super(init);
    this.actionId = init.actionId;
  }
}

/** True for any error this SDK (any copy of it) threw. */
export function isDeedglassError(e: unknown): e is DeedglassError {
  return typeof e === 'object' && e !== null && (e as Record<symbol, unknown>)[BRAND] === true;
}

/** Builds the typed error for an API error envelope. */
export function fromEnvelope(
  status: number,
  body: unknown,
  requestId: string | null,
  retryAfterMs: number,
  action?: Action,
): DeedglassError {
  const e = (body as { error?: Record<string, unknown> } | null)?.error ?? {};
  const str = (k: string): string | null => (typeof e[k] === 'string' ? (e[k] as string) : null);
  const type = (str('type') ?? (status >= 500 ? 'api_error' : 'invalid_request_error')) as ErrorType;
  const code = str('code') ?? (status >= 500 ? 'internal_error' : 'unknown_error');
  const init: ErrorInit = {
    type,
    code,
    message: str('message') ?? `Deedglass answered ${status}.`,
    param: str('param'),
    status,
    requestId: str('request_id') ?? requestId,
    docUrl: str('doc_url'),
  };
  if (action) init.action = action;
  switch (type) {
    case 'authentication_error':
      return new AuthenticationError(init);
    case 'permission_error':
      return new PermissionDeniedError(init);
    case 'policy_error':
      return new PolicyDeniedError(init);
    case 'rate_limit_error':
      return code === 'budget_exceeded' ? new BudgetExceededError({ ...init, retryAfterMs }) : new RateLimitError({ ...init, retryAfterMs });
    case 'api_error':
      return new APIError(init);
    case 'invalid_request_error':
      if (code === 'tool_unknown' && action) return new PolicyDeniedError(init);
      return new InvalidRequestError(init);
  }
  return new DeedglassError(init);
}
