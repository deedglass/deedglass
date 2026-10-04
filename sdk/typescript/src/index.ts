// @deedglass/sdk: put Deedglass in front of your agent's tools. Core exports only: this module
// never imports an adapter, and never imports node:*.

export { Deedglass, type DeedglassOptions, type ActOptions, type ProxyOptions, API_KEY, TOOL_NAME, DEFAULT_BASE_URL } from './client.js';
export {
  DeedglassError,
  AuthenticationError,
  PermissionDeniedError,
  PolicyDeniedError,
  BudgetExceededError,
  RateLimitError,
  InvalidRequestError,
  APIError,
  ConnectionError,
  TimeoutError,
  ApprovalPendingError,
  ToolExecutionError,
  isDeedglassError,
  type ErrorType,
} from './errors.js';
export { wrapTool, type WrapToolOptions, type CallOptions, type ToolFn } from './wrap.js';
export { signIntent, argsDigest, intentKid, generateIntentKey, type IntentKey, type SignedIntent } from './intent.js';
export { API_VERSION, VERSION } from './version.js';
export type { Action, ActionStatus, ActionDecision, ActionApproval, NextAction, ToolInfo, WhoAmI, EdgeResult, DeedEnvelope, JsonValue, JsonObject, ResponseMeta } from './types.js';
export type { SealKeySet } from './verify/keys.js';
export type { DeedResult } from './verify/deed.js';
