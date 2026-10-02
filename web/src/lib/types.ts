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
  decision: { outcome: string; code: string | null; rule_id: string | null; reason: string | null } | null;
  policy: { id: string | null; version_id: string | null; mode: string; decision: string | null; rule_id: string | null };
  execution: { status: string | null; latency_ms: number | null; error_code: string | null } | null;
  witness: ActionWitness | null;
  deed: string | null;
}

export interface Agent {
  object: 'agent';
  id: string;
  name: string;
  description: string;
  status: 'active' | 'suspended' | 'stopped';
  active_keys: number;
  last_used_at: string | null;
  created_at: string;
}

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
