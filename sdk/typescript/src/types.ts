// The shapes the Deedglass API answers with (contract §8). Fields the SDK doesn't read are kept as
// the server sent them; every object is additive across versions.

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [k: string]: JsonValue };
export type JsonObject = { [k: string]: JsonValue };

/** An action's status: the one field an agent needs (S13). */
export type ActionStatus = 'processing' | 'authorized' | 'pending_approval' | 'denied' | 'executed' | 'failed' | 'expired' | 'cancelled';

export interface ActionDecision {
  outcome: 'allow' | 'deny' | 'require_approval' | string;
  code: string | null;
  source: string | null;
  rule_id: string | null;
  reason: string | null;
  message: string | null;
  policy: string | null;
  policy_version: string | null;
}

export interface ActionApproval {
  id: string;
  status: 'pending' | 'approved' | 'denied' | 'expired' | 'cancelled' | string;
  expires_at: string;
  decision_reason?: string;
}

export interface NextAction {
  type: 'await_approval' | 'execute_at_edge' | string;
  approval?: string;
  expires_at?: string;
  poll_url?: string;
  report_url?: string;
  deadline?: string;
}

export interface ActionExecution {
  status: 'succeeded' | 'failed' | 'unknown' | string;
  started_at: string | null;
  finished_at: string | null;
  latency_ms: number | null;
  error_code: string | null;
  error_message?: string | null;
  result?: JsonValue;
}

export interface ActionWitness {
  object: 'witness';
  level: 'L0' | 'L1' | 'L2' | string;
  verdict: 'VERIFIED' | 'MISMATCH' | 'UNVERIFIABLE' | string | null;
  method: string | null;
  required_level?: string;
  summary?: string | null;
  reasons?: { code: string; class: string; level: string; message: string }[];
}

/** Response metadata the SDK adds: never sent by the server in the body. */
export interface ResponseMeta {
  requestId: string | null;
  /** The server answered this POST from its idempotency store (Deedglass-Idempotent-Replayed). */
  replayed: boolean;
  status: number;
}

/** The action object (contract §8). `R` types `execution.result` for gateway calls. */
export interface Action<R = unknown> {
  object: 'action';
  id: string;
  created_at: string;
  livemode: boolean;
  agent: { id: string; name: string };
  tool: string;
  connector: string | null;
  mode: 'gateway' | 'proxy';
  state: string;
  status: ActionStatus;
  on_behalf_of: string | null;
  session: string | null;
  decision: ActionDecision | null;
  approval: ActionApproval | null;
  execution: (Omit<ActionExecution, 'result'> & { result?: R }) | null;
  witness: ActionWitness | null;
  cost: { amount: number; currency: string } | null;
  request_digest: string;
  result_digest: string | null;
  deed: string | null;
  next_action: NextAction | null;
  /** Added by the SDK (not part of the JSON). */
  meta: ResponseMeta;
  [k: string]: unknown;
}

/** One tool from GET /v1/tools. */
export interface ToolInfo {
  object: 'tool';
  name: string;
  connector: string;
  title: string;
  description: string;
  input_schema: JsonObject;
  side_effect: 'read' | 'write' | 'destructive' | 'unknown' | string;
  visibility: 'visible' | 'hidden' | string;
  levels: string[];
  read_back: string;
}

/** GET /v1/whoami. */
export interface WhoAmI {
  object: 'whoami';
  org: { id: string; name: string; slug: string; stopped: boolean };
  agent: { id: string; name: string; status: string; labels: Record<string, string> } | null;
  key: { id?: string; prefix?: string; mode?: 'live' | 'test' | string; scopes?: string[]; [k: string]: unknown };
  budgets?: unknown;
}

/** What an edge reports after running a tool in proxy mode (POST /v1/actions/{id}/result). */
export interface EdgeResult {
  status: 'succeeded' | 'failed' | 'unknown';
  started_at: string;
  finished_at: string;
  result?: JsonValue;
  result_digest?: string;
  error_code?: string;
  error_message?: string;
}

/** A deed envelope (contract §7). */
export interface DeedEnvelope {
  object: 'deed';
  id: string;
  body: JsonObject;
  body_hash: string;
  seal: string;
  seal_key_id: string;
}

export interface List<T> {
  object: 'list';
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}
