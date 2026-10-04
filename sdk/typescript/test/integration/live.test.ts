// S31 §4 step 14: the SDK against a real serve (started by cmd/deedglass TestSDKTypeScriptLive,
// make sdk-ts-it). Skipped without DEEDGLASS_SERVER and the test hook.
import { readdirSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { ApprovalPendingError, Deedglass, generateIntentKey, PolicyDeniedError, signIntent, wrapTool } from '../../src/index.js';
import { fetchKeys } from '../../src/verify/index.js';
import { readText, repo } from '../helpers.js';

const server = process.env.DEEDGLASS_SERVER;
const hook = process.env.DEEDGLASS_IT_HOOK;
const admin = process.env.DEEDGLASS_IT_ADMIN_KEY;
const agentId = process.env.DEEDGLASS_IT_AGENT_ID;
const live = describe.runIf(Boolean(server && hook && admin && agentId));

async function adminCall(method: string, path: string, body?: unknown): Promise<Response> {
  return fetch(server + path, {
    method,
    headers: { Authorization: 'Bearer ' + admin, 'Content-Type': 'application/json', 'Deedglass-Version': '2026-10-01', 'Idempotency-Key': crypto.randomUUID() },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

live('@deedglass/sdk against a real Deedglass', () => {
  const dg = new Deedglass();

  it('whoami names the agent', async () => {
    const w = await dg.whoami();
    expect(w.agent?.id).toBe(agentId);
  });

  it('act (gateway) → executed and sealed; deeds.get → verifies offline with the server keys', async () => {
    const a = await dg.act('fake.echo', { message: 'hello' }, { waitMs: 10_000 });
    expect(a.status, JSON.stringify(a.execution)).toBe('executed');
    expect(a.deed).toMatch(/^deed_/);
    let deedId = a.deed!;
    const d = await dg.deeds.get(deedId);
    expect(d.id).toBe(deedId);
    const { keys, pinned } = await fetchKeys(server + '/.well-known/deedglass/keys.json');
    expect(pinned).toBe(true);
    const r = await dg.deeds.verify(d, keys);
    expect(r.envelope.id).toBe(deedId);
    // The same through the client's own key fetch.
    expect((await dg.deeds.verify(JSON.stringify(d))).envelope.id).toBe(deedId);
    deedId = '';
    expect(deedId).toBe('');
  });

  it('a denial carries the sealed action', async () => {
    const del = wrapTool(async () => 'never', { client: dg, tool: 'billing.delete_customer' });
    const err = await del({ customer: 'cus_123' }).catch((e) => e);
    expect(err).toBeInstanceOf(PolicyDeniedError);
    expect(err.code).toBe('policy_denied');
    expect(err.message).toBe('Blocked by policy "no-deletes": Agents never delete customers.');
    expect(err.action?.deed).toMatch(/^deed_/);
    expect(err.requestId).toMatch(/^req_/);
  });

  it('wrapTool proxy → authorized → fn runs once → reported L0 (connector_type sdk)', async () => {
    let runs = 0;
    let sealedDeed: string | null = null;
    const refund = wrapTool(async (args: { customer: string; amount: number }) => {
      runs++;
      return { refunded: args.amount };
    }, { client: dg, tool: 'billing.refund_customer', onSealed: (a) => (sealedDeed = a.deed) });
    expect(await refund({ customer: 'cus_123', amount: 2000 })).toEqual({ refunded: 2000 });
    expect(runs).toBe(1);
    expect(sealedDeed).toMatch(/^deed_/);
  });

  it('pending → a person approves → the same call runs once; waitUntilSettled sees it', async () => {
    let runs = 0;
    const refund = wrapTool(async () => {
      runs++;
      return 'refunded';
    }, { client: dg, tool: 'billing.refund_customer' });
    const err = await refund({ customer: 'cus_123', amount: 12000 }).catch((e) => e);
    expect(err).toBeInstanceOf(ApprovalPendingError);
    expect(err.message).toContain('Refunds over $50.00 wait for a human.');
    const h = await fetch(hook + '/approve?id=' + err.approvalId, { method: 'POST' });
    expect(h.status, await h.text()).toBe(204);
    const settled = await dg.actions.waitUntilSettled(err.actionId, { timeoutMs: 30_000 });
    expect(settled.status).toBe('authorized');
    expect(await refund({ customer: 'cus_123', amount: 12000 })).toBe('refunded');
    expect(runs).toBe(1);
    const done = await dg.actions.get(err.actionId, { waitMs: 5_000 });
    expect(done.status).toBe('executed');
  });

  it('a function that throws seals UNVERIFIABLE', async () => {
    const t = wrapTool(async () => {
      throw new Error('declined');
    }, { client: dg, tool: 'billing.send_report' });
    const err = await t({ to: 'ops@example.test' }).catch((e) => e);
    expect(err.name).toBe('ToolExecutionError');
    const a = await dg.actions.get(err.actionId, { waitMs: 5_000 });
    expect(a.execution?.status).toBe('unknown');
    expect(a.witness?.verdict).toBe('UNVERIFIABLE');
  });

  it('intents: every valid vector shape verifies when signed by the SDK for this agent', async () => {
    const w = await dg.whoami();
    const keys = JSON.parse(readText('testdata/intents/keys.json')) as { keys: Record<string, { pem: string; public_key: string }> };
    let res = await adminCall('POST', `/v1/agents/${agentId}/intent-keys`, { public_key: keys.keys.test1!.public_key });
    expect(res.status, await res.clone().text()).toBe(201);
    res = await adminCall('PATCH', `/v1/agents/${agentId}`, { intent_mode: 'optional' });
    expect(res.status, await res.clone().text()).toBe(200);
    const pem = readText('testdata/intents/' + keys.keys.test1!.pem);
    const files = readdirSync(repo('testdata/intents/valid'));
    for (const f of files) {
      const v = JSON.parse(readText('testdata/intents/valid/' + f)) as { sign: { tool: string; args: string; on_behalf_of?: string; session?: string } };
      const args = JSON.parse(v.sign.args);
      const s = await signIntent({ key: { privateKeyPem: pem }, org: w.org.id, agent: w.agent!.id, tool: v.sign.tool, args, ...(v.sign.on_behalf_of ? { onBehalfOf: v.sign.on_behalf_of } : {}), ...(v.sign.session ? { session: v.sign.session } : {}) });
      const r = await fetch(server + '/v1/intents/verify', {
        method: 'POST',
        headers: {
          Authorization: 'Bearer ' + process.env.DEEDGLASS_API_KEY,
          'Content-Type': 'application/json',
          'Deedglass-Version': '2026-10-01',
          'Deedglass-Intent': s.header,
          'Idempotency-Key': crypto.randomUUID(),
          ...(v.sign.on_behalf_of ? { 'Deedglass-On-Behalf-Of': v.sign.on_behalf_of } : {}),
          ...(v.sign.session ? { 'Deedglass-Session': v.sign.session } : {}),
        },
        body: JSON.stringify({ tool: v.sign.tool, args }),
      });
      expect(r.status, f + ': ' + (await r.clone().text())).toBe(200);
    }
    // A generated key registers (Go parses its public key) and signs a call the Gate accepts.
    const g = await generateIntentKey();
    res = await adminCall('POST', `/v1/agents/${agentId}/intent-keys`, { public_key: g.publicKey });
    expect(res.status, await res.clone().text()).toBe(201);
    expect(((await res.json()) as { kid: string }).kid).toBe(g.kid);
    const signed = new Deedglass({ intentKey: { privateKeyPem: g.privateKeyPem } });
    const a = await signed.act('fake.echo', { message: 'signed' });
    expect(a.status, JSON.stringify(a.execution)).toBe('executed');
  });
});
