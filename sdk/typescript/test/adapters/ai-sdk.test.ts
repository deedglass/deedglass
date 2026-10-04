import { generateText, tool, jsonSchema } from 'ai';
import { MockLanguageModelV4 } from 'ai/test';
import { describe, expect, it, vi } from 'vitest';
import { deedglassTools, governTools } from '../../src/adapters/ai-sdk.js';
import { Deedglass } from '../../src/index.js';
import { action, denied, json, KEY, PENDING, scriptedFetch } from '../helpers.js';

const usage = { inputTokens: { total: 1, noCache: 1, cacheRead: 0, cacheWrite: 0 }, outputTokens: { total: 1, text: 1, reasoning: 0 } };

function model(toolName: string, input: unknown) {
  return new MockLanguageModelV4({
    doGenerate: { content: [{ type: 'tool-call', toolCallId: 'call_1', toolName, input: JSON.stringify(input) }], finishReason: { unified: 'tool-calls', raw: 'tool_use' }, usage, warnings: [] } as never,
  });
}

function client(...r: Parameters<typeof scriptedFetch>) {
  const s = scriptedFetch(...r);
  return { s, c: new Deedglass({ apiKey: KEY, baseUrl: 'http://127.0.0.1:9', fetch: s.fetch, logger: { warn: vi.fn(), debug: vi.fn() } }) };
}

function refundTool(spy: (i: unknown) => unknown) {
  return {
    refund_customer: tool({
      description: 'Refund a customer.',
      inputSchema: jsonSchema<{ customer: string; amount: number }>({ type: 'object', properties: { customer: { type: 'string' }, amount: { type: 'integer' } }, required: ['customer', 'amount'] }),
      execute: async (input) => spy(input),
    }),
  };
}

describe('ai-sdk adapter (ai 7, MockLanguageModelV4, no network)', () => {
  it('allowed: the tool runs once and its output reaches the model', async () => {
    const { s, c } = client(() => json(200, action()), () => json(200, action({ status: 'executed', state: 'sealed' })));
    const spy = vi.fn(() => ({ refunded: 2000 }));
    const r = await generateText({ model: model('refund_customer', { customer: 'cus_123', amount: 2000 }), tools: governTools(refundTool(spy), { client: c, connector: 'billing' }), prompt: 'refund' });
    expect(spy).toHaveBeenCalledTimes(1);
    expect(r.toolResults[0]?.output).toEqual({ refunded: 2000 });
    expect(s.calls[0]!.body).toMatchObject({ tool: 'billing.refund_customer', mode: 'proxy', connector_type: 'sdk' });
    expect(Object.keys((s.calls[0]!.body as { context: object }).context)).toEqual(['client', 'transport']);
  });

  it('denied: the model reads the policy sentence; the tool never runs; the run continues', async () => {
    const { c } = client(() => denied());
    const spy = vi.fn();
    const r = await generateText({ model: model('refund_customer', { customer: 'cus_123', amount: 2000 }), tools: governTools(refundTool(spy), { client: c, connector: 'billing' }), prompt: 'refund' });
    expect(spy).not.toHaveBeenCalled();
    expect(r.toolResults[0]?.output).toBe('Blocked by policy "no-deletes": Agents never delete customers. Nothing was run.');
  });

  it('pending: the model reads the pending sentence', async () => {
    const { c } = client(() => json(202, action(PENDING)));
    const spy = vi.fn();
    const r = await generateText({ model: model('refund_customer', { customer: 'cus_123', amount: 12000 }), tools: governTools(refundTool(spy), { client: c, connector: 'billing' }), prompt: 'refund' });
    expect(spy).not.toHaveBeenCalled();
    expect(r.toolResults[0]?.output).toBe(
      'Waiting for approval under policy "large-refunds-need-approval": Refunds over $50.00 wait for a human. Approval apr_01j9zv0000000000000000000a expires at 10:42 UTC. Try again after a person approves it.',
    );
  });

  it('a tool without execute is left alone, with a warning', () => {
    const { c } = client(() => json(200, {}));
    const t = { client_side: tool({ description: 'x', inputSchema: jsonSchema({ type: 'object' }) }) };
    const g = governTools(t, { client: c });
    expect(g.client_side).toBe(t.client_side);
    expect(c.logger.warn).toHaveBeenCalled();
  });

  it('deedglassTools: fixture /v1/tools → fake__echo with a JSON schema; act() in gateway mode', async () => {
    const list = { object: 'list', has_more: false, next_cursor: null, data: [{ object: 'tool', name: 'fake.echo', connector: 'fake', title: 'Echo', description: 'Echo the input.', input_schema: { type: 'object', properties: { text: { type: 'string' } } }, side_effect: 'read', visibility: 'visible', levels: ['L0'], read_back: '' }] };
    const executed = action({ mode: 'gateway', tool: 'fake.echo', status: 'executed', state: 'sealed', execution: { status: 'succeeded', result: { text: 'hi' } }, witness: { object: 'witness', level: 'L0', verdict: 'VERIFIED', method: 'reported' } });
    const { s, c } = client(() => json(200, list), () => json(200, executed));
    const tools = await deedglassTools(c);
    expect(Object.keys(tools)).toEqual(['fake__echo']);
    const r = await generateText({ model: model('fake__echo', { text: 'hi' }), tools, prompt: 'echo' });
    expect(r.toolResults[0]?.output).toBe('{"text":"hi"}');
    expect(s.calls[1]!.body).toMatchObject({ tool: 'fake.echo', mode: 'gateway', args: { text: 'hi' } });
  });
});
