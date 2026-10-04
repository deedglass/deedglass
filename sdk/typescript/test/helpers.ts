import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

/** A path under the repo root (sdk/typescript/test → ../../..). */
export function repo(rel: string): string {
  return fileURLToPath(new URL('../../../' + rel, import.meta.url));
}
export function readBytes(rel: string): Uint8Array {
  return new Uint8Array(readFileSync(repo(rel)));
}
export function readText(rel: string): string {
  return readFileSync(repo(rel), 'utf8');
}
export function outcome(rep: { failures: { code: string; seq: number | null; field: string | null }[] }) {
  const f = rep.failures[0];
  return f ? { valid: false, code: f.code, seq: f.seq, field: f.field } : { valid: true, code: null, seq: null, field: null };
}

/** A sentinel API key shape built at run time; it is no credential. */
export const KEY = 'dg_test_' + 'Ab12Cd34' + '_' + 'SENTINELsentinel'.repeat(2);

export interface Recorded {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

export type Responder = (r: Recorded, init: RequestInit) => Response | Promise<Response> | 'network' | 'hang';

/** A scripted fetch: each call takes the next responder (the last one repeats). */
export function scriptedFetch(...responders: Responder[]) {
  const calls: Recorded[] = [];
  const f = (async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const h: Record<string, string> = {};
    for (const [k, v] of Object.entries((init.headers ?? {}) as Record<string, string>)) h[k.toLowerCase()] = v;
    const rec: Recorded = { url: String(input), method: init.method ?? 'GET', headers: h, body: init.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(rec);
    const r = responders[Math.min(calls.length - 1, responders.length - 1)]!;
    const out = await r(rec, init);
    if (out === 'network') throw new TypeError('fetch failed');
    if (out === 'hang') {
      return new Promise<Response>((_, reject) => {
        init.signal?.addEventListener('abort', () => reject(init.signal!.reason), { once: true });
      });
    }
    return out;
  }) as typeof fetch;
  return { fetch: f, calls };
}

export function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', 'deedglass-request-id': 'req_test', ...headers } });
}

export function action(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    object: 'action',
    id: 'act_01j9zw0000000000000000000a',
    created_at: '2026-10-10T10:00:00.000Z',
    livemode: false,
    agent: { id: 'agt_01k633rrmh3bcz2p39gg4vp46q', name: 'support-bot' },
    tool: 'billing.refund_customer',
    connector: null,
    mode: 'proxy',
    state: 'executing',
    status: 'authorized',
    on_behalf_of: null,
    session: null,
    decision: { outcome: 'allow', code: null, source: 'rule', rule_id: 'small-refunds', reason: null, message: null, policy: 'sdk-demo', policy_version: 'pv_x' },
    approval: null,
    execution: null,
    witness: null,
    cost: null,
    request_digest: 'sha256:' + '0'.repeat(64),
    result_digest: null,
    deed: null,
    next_action: { type: 'execute_at_edge', report_url: '/v1/actions/act_01j9zw0000000000000000000a/result', deadline: new Date(Date.now() + 60_000).toISOString() },
    ...over,
  };
}

export const PENDING = {
  status: 'pending_approval',
  state: 'pending_approval',
  decision: { outcome: 'require_approval', code: 'approval_required', source: 'rule', rule_id: 'large-refunds-need-approval', reason: 'Refunds over $50.00 wait for a human.', message: 'Waiting for approval under policy "large-refunds-need-approval": Refunds over $50.00 wait for a human.', policy: 'sdk-demo', policy_version: 'pv_x' },
  approval: { id: 'apr_01j9zv0000000000000000000a', status: 'pending', expires_at: '2026-10-10T10:42:00.000Z' },
  next_action: { type: 'await_approval', approval: 'apr_01j9zv0000000000000000000a', expires_at: '2026-10-10T10:42:00.000Z', poll_url: '/v1/actions/act_01j9zw0000000000000000000a?wait=25' },
};

export function denied(code = 'policy_denied', message = 'Blocked by policy "no-deletes": Agents never delete customers.', type = 'policy_error', status = 403) {
  return json(status, { error: { type, code, message, param: null, request_id: 'req_test', doc_url: `https://docs.deedglass.com/errors/${code}`, action: action({ status: 'denied', state: 'sealed', deed: 'deed_01j9zw0000000000000000000a', next_action: null }) } });
}
