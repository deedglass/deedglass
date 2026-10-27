// types.ts: the API shapes the Console reads, hand-written from api/openapi.yaml (S24 §3.9).
// test/types.test.ts loads the Go goldens and asserts every field read here exists there.

export interface ListResponse<T> {
  object: 'list';
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}

export interface ApiErrorBody {
  error: {
    type: string;
    code: string;
    message: string;
    param: string | null;
    request_id: string;
    doc_url: string;
  };
}

export type Role = 'owner' | 'admin' | 'viewer';

export interface MeOrg {
  id: string;
  name: string;
  role: Role;
}

export interface Me {
  object: 'me';
  user: { object: 'user'; id: string; email: string; name: string; created_at: string };
  org: Org;
  membership: { object: 'membership'; id: string; role: Role; status: string };
  orgs: MeOrg[];
  session: { id: string; idle_expires_at: string; expires_at: string };
  csrf_token: string | null;
}

export interface OrgStopState {
  stopped: boolean;
  stopped_at: string | null;
  stopped_by: string | null;
  reason: string | null;
}

export interface Org {
  object: 'org';
  id: string;
  name: string;
  slug: string;
  timezone: string;
  status: string;
  created_at: string;
  stop?: OrgStopState;
}

export interface OrgStop {
  object: 'org_stop';
  org: string;
  stopped: boolean;
  stopped_at: string | null;
  stopped_by: string | null;
  reason: string | null;
  agents_affected: number;
  deed: string | null;
}

export interface AgentRef {
  id: string;
  name: string;
}

export type Verdict = 'VERIFIED' | 'MISMATCH' | 'UNVERIFIABLE';
export type Level = 'L0' | 'L1' | 'L2';

export interface ActionWitness {
  object: 'witness';
  level: Level;
  verdict: Verdict;
  method: string | null;
  required_level: Level | null;
  detail_digest: string | null;
}

export interface ActionDecision {
  outcome: string;
  code: string | null;
  source?: string | null;
  rule_id: string | null;
  reason: string | null;
  message?: string | null;
  policy?: string | null;
  policy_version?: string | null;
  shadow?: { outcome: string; rule_id?: string | null; policy?: string | null; policy_version?: string | null; reason?: string | null } | null;
  /** S27 §3.8: the trace line about the anomaly facts the policy saw ("anomaly facts: volume_spike"); omitted when inactive. */
  anomaly?: string;
}

export interface Action {
  object: 'action';
  id: string;
  created_at: string;
  livemode: boolean;
  agent: AgentRef;
  tool: string;
  connector: string | null;
  mode: string;
  state: string;
  status: string;
  on_behalf_of?: string | null;
  session?: string | null;
  decision: ActionDecision | null;
  policy: { id: string | null; version_id: string | null; mode: string; decision: string | null; rule_id: string | null };
  approval?: { id: string; status: string; expires_at: string; decision_reason?: string | null } | null;
  execution: { status: string | null; started_at?: string | null; finished_at?: string | null; latency_ms: number | null; error_code: string | null; error_message?: string | null } | null;
  witness: ActionWitness | null;
  cost?: { amount: number; currency: string } | null;
  request_digest?: string;
  result_digest?: string | null;
  payload_pruned_at?: string | null;
  deed: string | null;
  undo?: ActionUndo;
}

/** A reason in the Witness's catalog (S19): one sentence per check, already in the brand voice. */
export interface WitnessReason {
  code: string;
  class: 'pass' | 'gap' | 'violation' | 'note';
  level: Level;
  check: string;
  message: string;
  evidence?: Record<string, unknown>;
}

export interface L2Cell { column: string; before: string | null; after: string | null }
export interface L2Row { table: string; op: 'insert' | 'update' | 'delete'; key: Record<string, string>; expected: boolean; cells: L2Cell[] }
export interface L2ExpectedTable { table: string; ops: string[]; rows?: number; max_rows?: number; keys?: Record<string, string>[]; columns?: string[] }
export interface L2Evidence {
  transaction?: { system_id: string; timeline: number; commit_lsn: string; xid: number; committed_at: string; marker_lsn?: string; changes_digest?: string };
  expected?: { derivation: string; why?: string; tables: L2ExpectedTable[] };
  observed?: { total: number; tables: { table: string; insert: number; update: number; delete: number; truncate?: number }[] };
  rows: L2Row[];
  rows_shown: number;
  rows_more: number;
  columns_more: number;
  redacted: string[];
  frames: number;
}
export interface LevelDetail extends Partial<L2Evidence> {
  verdict: Verdict;
  method: string;
  readback?: { tool: string; target: string; attempts: number; latency_ms: number };
}
export interface WitnessDetail {
  object: 'witness_detail';
  version: number;
  claim: { level: Level; verdict: Verdict; method: string; required_level: Level };
  summary: string;
  levels: { L0: LevelDetail | null; L1: LevelDetail | null; L2: LevelDetail | null };
  reasons: WitnessReason[];
  witnessed_at: string;
  truncated?: boolean;
}

export interface ActionUndo { undoes: string | null; undone_by: string | null; pending?: string | null; eligible: boolean }

export interface ActionEvent { object: 'action_event'; id: string; seq: number; from: string | null; to: string; at: string; actor: string; data: Record<string, unknown> }

/** GET /v1/actions/{id}: the action plus its events, deeds, payloads and witness detail. */
export interface ActionDetail extends Action {
  witness: (ActionWitness & { summary?: string; reasons?: WitnessReason[]; witnessed_at?: string; detail?: WitnessDetail; history?: { deed: string; kind: string; level: Level; verdict: Verdict; at: string; reason?: string }[] }) | null;
  request?: unknown;
  result?: unknown;
  events?: ListResponse<ActionEvent>;
  deeds?: { id: string; kind: string; seq: number; issued_at: string }[];
  undo: ActionUndo;
}

export interface AgentUsage {
  calls_minute: number;
  calls_hour: number;
  calls_today: number;
  spend_today: { amount: number; currency: string } | null;
}

export interface AgentBudgets {
  calls_per_minute?: number;
  calls_per_hour?: number;
  calls_per_day?: number;
  spend_per_day?: { amount: number; currency: string };
}

export interface Agent {
  object: 'agent';
  id: string;
  name: string;
  description: string;
  labels?: Record<string, string>;
  budgets?: AgentBudgets;
  status: 'active' | 'suspended' | 'stopped';
  status_reason?: string | null;
  status_changed_at?: string | null;
  intent_mode?: 'off' | 'optional' | 'required';
  active_keys: number;
  last_used_at: string | null;
  created_at: string;
  usage?: AgentUsage;
  last_action_at?: string | null;
}

export interface ApiKey {
  object: 'api_key';
  id: string;
  agent: string | null;
  name: string;
  mode: 'live' | 'test';
  prefix: string;
  scopes: string[];
  secret?: string | null;
  expires_at: string | null;
  last_used_at: string | null;
  revoked_at: string | null;
  rolled_from?: string | null;
  created_at: string;
  deed?: string;
}

export interface IntentKey { kid: string; key: string; created_at: string; created_by?: string | null }

export interface Approval {
  object: 'approval';
  id: string;
  action: string;
  agent: AgentRef;
  tool: string;
  status: 'pending' | 'approved' | 'denied' | 'expired' | 'cancelled';
  rule_id: string | null;
  required: number;
  summary: string;
  created_at: string;
  expires_at: string;
}

export interface Countersign { user: { id: string; name: string }; at: string; via: string; reason: string | null; deed: string }

export interface ApprovalDetail extends Approval {
  reason?: string | null;
  approvers?: string[];
  countersigned: Countersign[] | number;
  denied_by?: { id: string; name: string };
  decision_reason: string | null;
  decided_at?: string | null;
  deed?: string | null;
}

export interface UndoCheck { id: string; title: string; status: 'pass' | 'warn' | 'fail' | 'skip'; detail?: string; message?: string; fix?: string; fix_sql?: string }
export interface UndoStep { op: 'insert' | 'update' | 'delete'; table: string; key: Record<string, string>; set?: Record<string, string | null>; expect?: Record<string, string | null> }
export interface UndoPreview {
  object: 'undo_preview';
  action: string;
  tool: string;
  available: boolean;
  summary: string;
  headline: string;
  plan_digest: string | null;
  counts: { update: number; insert: number; delete: number };
  rows: number;
  tables: string[];
  steps: { object: 'list'; total: number; has_more: boolean; data: UndoStep[] };
  checks: UndoCheck[];
  notes: string[];
  redacted: string[];
  policy: { decision: string; rule_id: string | null; reason: string | null } | null;
  undone_by: string | null;
}

/** GET /v1/ledger/check (S06). */
export interface LedgerCheck {
  object: 'ledger_check';
  status: 'intact' | 'broken' | 'incomplete';
  checked: number;
  first_seq: number | null;
  last_seq: number | null;
  head: { seq: number; deed: string; body_hash: string; issued_at: string } | null;
  seal_keys: string[];
  break: { seq: number | null; deed: string | null; code: string; message: string; next: string } | null;
  next_from_seq: number | null;
  checked_at: string;
}

/** A deed as GET /v1/deeds returns it: the envelope with its body. */
export interface DeedBody {
  v: number;
  kind: 'action' | 'admin' | 'amendment';
  deed_id: string;
  org: string;
  seq: number;
  prev_hash: string;
  issued_at: string;
  agent: { id: string; name?: string } | null;
  on_behalf_of: string | null;
  session: string | null;
  action: { id: string; tool: string; connector: string | null; mode: string; request_digest: string; intent_hash: string | null; intent_signed: boolean } | null;
  policy: { id: string | null; version_id: string | null; version_hash: string | null; mode: string; decision: string; rule_id: string | null } | null;
  approval: Record<string, unknown> | null;
  execution: { status: string; started_at: string | null; finished_at: string | null; latency_ms: number | null; result_digest: string | null; error_code: string | null } | null;
  witness: { level: Level; verdict: Verdict; method: string | null; detail_digest: string | null } | null;
  cost: { amount: number; currency: string } | null;
  admin: { action: string; actor: { id: string; type: string; name?: string }; target: { id: string; type: string } | null; data_digest: string | null; event: string; reason: string | null } | null;
  amends: string | null;
  seal_key_id: string;
}
export interface DeedEnvelope { object: 'deed'; id: string; body: DeedBody; body_hash: string; seal: string; seal_key_id: string }

/** GET /v1/overview (S25 §3.2). The server sends kinds and data; copy.ts writes the sentences. */
export interface AttentionItem {
  object: 'attention_item';
  kind: 'chain_broken' | 'org_stopped' | 'slot_invalidated' | 'slot_lagging' | 'mismatch' | 'approval_expiring' | 'budget_near';
  severity: 'high' | 'medium' | 'low';
  at: string;
  ref: string | null;
  data: Record<string, unknown>;
}
export interface OverviewSeries { total: number; previous: number; series: number[] }
export interface Overview {
  object: 'overview';
  window: '24h' | '7d';
  from: string;
  to: string;
  timezone: string;
  generated_at: string;
  actions: OverviewSeries;
  blocked: OverviewSeries;
  /** console-v1: the window's actions by decision (blocked is blocked.total); absent on older servers. */
  decisions?: { allowed: number; held: number };
  approvals: { pending: number; expiring_soon: number; next_expires_at: string | null };
  verification: { verified: number; mismatch: number; unverifiable: number; not_witnessed: number; by_level: Record<Level, number> };
  agents: { object: 'list'; has_more: boolean; data: { id: string; name: string; status: Agent['status']; last_action_at: string | null; usage: { calls_today: number; spend_today: { amount: number; currency: string } | null }; budgets: AgentBudgets }[] };
  attention: AttentionItem[];
}

/** The event object: S22's webhook envelope plus hydrated (contract §8, S24 §3.11). */
export interface DeedglassEvent<T = Record<string, unknown>> {
  object: 'event';
  id: string;
  type: string;
  api_version: string;
  created_at: string;
  livemode: boolean;
  org: string;
  hydrated: boolean;
  data: { object: T };
}

export interface StreamHello {
  object: 'stream';
  instance: string;
  resumed: boolean;
  heartbeat_s: number;
  server_time: string;
}
