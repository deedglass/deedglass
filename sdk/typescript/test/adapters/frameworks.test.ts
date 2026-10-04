import { RunContext, tool as oaTool } from '@openai/agents';
import { tool as lcTool } from '@langchain/core/tools';
import { ToolMessage } from '@langchain/core/messages';
import { createSdkMcpServer, tool as claudeTool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { describe, expect, it, vi } from 'vitest';
import * as oa from '../../src/adapters/openai-agents.js';
import * as lc from '../../src/adapters/langchain.js';
import { deedglassHooks, deedglassMcpServer, governSdkTool } from '../../src/adapters/claude-agent-sdk.js';
import { modelToolName, fromModelToolName, modelNames, resultText } from '../../src/adapters/shared.js';
import { Deedglass } from '../../src/index.js';
import { action, denied, json, KEY, PENDING, scriptedFetch } from '../helpers.js';

function client(...r: Parameters<typeof scriptedFetch>) {
  const s = scriptedFetch(...r);
  return { s, c: new Deedglass({ apiKey: KEY, baseUrl: 'http://127.0.0.1:9', fetch: s.fetch, maxRetries: 0, logger: { warn: vi.fn(), debug: vi.fn() } }) };
}
const sealed = () => json(200, action({ status: 'executed', state: 'sealed' }));
const toolList = (names: string[]) =>
  json(200, { object: 'list', has_more: false, next_cursor: null, data: names.map((n) => ({ object: 'tool', name: n, connector: n.split('.')[0], title: n, description: 'd', input_schema: { type: 'object', properties: {} }, side_effect: 'read', visibility: 'visible', levels: ['L0'], read_back: '' })) });

describe('openai-agents adapter (@openai/agents 0.18)', () => {
  it('invoke(runContext, json) runs the proxy flow, then the original execute once', async () => {
    const { s, c } = client(() => json(200, action()), sealed);
    const exec = vi.fn(async (i: { amount: number }) => `refunded ${i.amount}`);
    const t = oaTool({ name: 'refund_customer', description: 'Refund.', parameters: { type: 'object', properties: { amount: { type: 'integer' } }, required: ['amount'], additionalProperties: false } as never, strict: false, execute: exec as never });
    const g = oa.governTool(t, { client: c, connector: 'billing' });
    expect(await g.invoke(new RunContext(), '{"amount":2000}')).toBe('refunded 2000');
    expect(exec).toHaveBeenCalledTimes(1);
    expect(s.calls[0]!.body).toMatchObject({ tool: 'billing.refund_customer', args: { amount: 2000 } });
  });

  it('denied: the original never runs; the text says why', async () => {
    const { c } = client(() => denied());
    const exec = vi.fn();
    const t = oaTool({ name: 'delete_customer', description: 'x', parameters: { type: 'object', properties: {}, additionalProperties: true } as never, strict: false, execute: exec as never });
    expect(await oa.governTool(t, { client: c, connector: 'billing' }).invoke(new RunContext(), '{}')).toContain('Nothing was run.');
    expect(exec).not.toHaveBeenCalled();
  });

  it('deedglassTools names all match ^[a-zA-Z0-9_-]{1,64}$ and run in gateway mode', async () => {
    const { s, c } = client(() => toolList(['fake.echo', 'github.create_issue', 'stripe.create_refund']), () => json(200, action({ mode: 'gateway', status: 'executed', execution: { status: 'succeeded', result: 'ok' } })));
    const tools = await oa.deedglassTools(c);
    expect(tools.map((t) => t.name)).toEqual(['fake__echo', 'github__create_issue', 'stripe__create_refund']);
    for (const t of tools) expect(t.name).toMatch(/^[a-zA-Z0-9_-]{1,64}$/);
    expect(await tools[0]!.invoke(new RunContext(), '{}')).toBe('ok');
    expect(s.calls[1]!.body).toMatchObject({ tool: 'fake.echo', mode: 'gateway' });
  });
});

describe('langchain adapter (@langchain/core 1.2)', () => {
  const make = (spy: (i: unknown) => unknown) => lcTool(async (i: unknown) => spy(i) as string, { name: 'refund_customer', description: 'Refund.', schema: z.object({ amount: z.number() }) });

  it('denied: the returned content is denialText; the original never called', async () => {
    const { c } = client(() => denied());
    const spy = vi.fn(() => 'x');
    const out = await lc.governTool(make(spy), { client: c, connector: 'billing' }).invoke({ amount: 2000 });
    expect(out).toBe('Blocked by policy "no-deletes": Agents never delete customers. Nothing was run.');
    expect(spy).not.toHaveBeenCalled();
  });

  it('allowed: a ToolMessage round trip carries the tool output', async () => {
    const { c } = client(() => json(200, action()), sealed);
    const spy = vi.fn(() => 'refunded');
    const g = lc.governTool(make(spy), { client: c, connector: 'billing' });
    const msg = await g.invoke({ id: 'call_9', name: 'refund_customer', args: { amount: 2000 }, type: 'tool_call' } as never);
    expect(msg).toBeInstanceOf(ToolMessage);
    expect((msg as ToolMessage).content).toBe('refunded');
    expect((msg as ToolMessage).tool_call_id).toBe('call_9');
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it('deedglassTools passes JSON Schema and runs act()', async () => {
    const { c } = client(() => toolList(['fake.echo']), () => json(200, action({ mode: 'gateway', status: 'executed', execution: { status: 'succeeded', result: { a: 1 } } })));
    const [t] = await lc.deedglassTools(c);
    expect(t!.name).toBe('fake__echo');
    expect(await t!.invoke({})).toBe('{"a":1}');
  });
});

describe('claude-agent-sdk adapter (@anthropic-ai/claude-agent-sdk 0.3)', () => {
  const base = { session_id: 's', transcript_path: '/t', cwd: '/workspace' };
  const signal = new AbortController().signal;

  it('PreToolUse Bash rm -rf / → deny with the policy sentence', async () => {
    const { s, c } = client(() => denied('policy_denied', 'Blocked by policy "claude-agent-local": rm -rf / never runs.'));
    const h = deedglassHooks({ client: c });
    const out = await h.PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command: 'rm -rf /' }, tool_use_id: 'tu_1' }, 'tu_1', { signal });
    expect(out).toEqual({ hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: 'Blocked by policy "claude-agent-local": rm -rf / never runs. Nothing was run.' } });
    expect(s.calls[0]!.body).toMatchObject({ tool: 'claude.Bash', mode: 'proxy', args: { command: 'rm -rf /' } });
  });

  it('pending → deny with the pending sentence (default)', async () => {
    const { c } = client(() => json(202, action(PENDING)));
    const out = (await deedglassHooks({ client: c }).PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Write', tool_input: { file_path: '/etc/hosts' }, tool_use_id: 'tu_2' }, 'tu_2', { signal })) as never as { hookSpecificOutput: { permissionDecisionReason: string } };
    expect(out.hookSpecificOutput.permissionDecisionReason).toContain('Try again after a person approves it.');
  });

  it('authorized → {} (defers to the host), then PostToolUse reports succeeded and clears the mapping', async () => {
    const { s, c } = client(() => json(200, action()), sealed);
    const h = deedglassHooks({ client: c });
    expect(await h.PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Read', tool_input: { file_path: '/workspace/a.ts' }, tool_use_id: 'tu_3' }, 'tu_3', { signal })).toEqual({});
    await h.PostToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PostToolUse', tool_name: 'Read', tool_input: {}, tool_response: { content: 'abc' }, tool_use_id: 'tu_3' }, 'tu_3', { signal });
    expect(s.calls[1]!.body).toMatchObject({ status: 'succeeded', result: { content: 'abc' } });
    await h.PostToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PostToolUse', tool_name: 'Read', tool_input: {}, tool_response: 'again', tool_use_id: 'tu_3' }, 'tu_3', { signal });
    expect(s.calls.length).toBe(2);
  });

  it('PostToolUseFailure reports per failureMeans', async () => {
    const { s, c } = client(() => json(200, action()), sealed);
    const h = deedglassHooks({ client: c, failureMeans: 'not_applied' });
    await h.PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Edit', tool_input: {}, tool_use_id: 'tu_5' }, 'tu_5', { signal });
    await h.PostToolUseFailure![0]!.hooks[0]!({ ...base, hook_event_name: 'PostToolUseFailure', tool_name: 'Edit', tool_input: {}, tool_use_id: 'tu_5', error: 'no such file' }, 'tu_5', { signal });
    expect(s.calls[1]!.body).toMatchObject({ status: 'failed', error_code: 'tool_error', error_message: 'no such file' });
  });

  it('authorized, no Post event, then Stop → unknown no_result_observed', async () => {
    const { s, c } = client(() => json(200, action()), sealed);
    const h = deedglassHooks({ client: c });
    await h.PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command: 'ls' }, tool_use_id: 'tu_4' }, 'tu_4', { signal });
    await h.Stop![0]!.hooks[0]!({ ...base, hook_event_name: 'Stop', stop_hook_active: false }, undefined, { signal });
    expect(s.calls[1]!.body).toMatchObject({ status: 'unknown', error_code: 'no_result_observed' });
  });

  it('Deedglass unreachable → deny with the outage sentence; nothing thrown', async () => {
    const { c } = client(() => 'network');
    const out = await deedglassHooks({ client: c }).PreToolUse![0]!.hooks[0]!({ ...base, hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command: 'ls' }, tool_use_id: 'tu_6' }, 'tu_6', { signal });
    expect(out).toEqual({ hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: "Deedglass couldn't be reached, so this tool call was not run." } });
  });

  it("'wait' sets the matcher timeout from approvalTimeoutMs", () => {
    const { c } = client(() => json(200, {}));
    expect(deedglassHooks({ client: c, onPending: 'wait', approvalTimeoutMs: 120_000, matcher: 'Bash|Write' }).PreToolUse![0]).toMatchObject({ matcher: 'Bash|Write', timeout: 150 });
  });

  it('createSdkMcpServer holds a governed tool; a denial is an error result', async () => {
    const { c } = client(() => denied());
    const handler = vi.fn(async () => ({ content: [{ type: 'text' as const, text: 'sent' }] }));
    const g = governSdkTool(claudeTool('send_report', 'Send the report.', { to: z.string() }, handler), { client: c, connector: 'reports' });
    const server = createSdkMcpServer({ name: 'acme', version: '1.0.0', tools: [g] });
    expect(server.type).toBe('sdk');
    expect(await g.handler({ to: 'ops@example.test' }, {})).toEqual({ content: [{ type: 'text', text: 'Blocked by policy "no-deletes": Agents never delete customers. Nothing was run.' }], isError: true });
    expect(handler).not.toHaveBeenCalled();
  });

  it('deedglassMcpServer points at /mcp/native with the key in a header', () => {
    const { c } = client(() => json(200, {}));
    expect(deedglassMcpServer(c)).toEqual({ type: 'http', url: 'http://127.0.0.1:9/mcp/native', headers: { Authorization: 'Bearer ' + KEY } });
    expect(deedglassMcpServer(c, { connector: 'orders' }).url).toBe('http://127.0.0.1:9/mcp/orders');
  });
});

describe('shared helpers', () => {
  it('model tool names round-trip and refuse collisions and long names', () => {
    expect(modelToolName('github.create_issue')).toBe('github__create_issue');
    expect(fromModelToolName('github__create_issue')).toBe('github.create_issue');
    expect(() => modelToolName('x.' + 'y'.repeat(70))).toThrow('64');
    expect(() => modelNames([{ name: 'a.b_c' }, { name: 'a.b.c' }] as never)).toThrow('Two Deedglass tools');
  });

  it('resultText covers every status', () => {
    expect(resultText(action({ status: 'failed', execution: { status: 'failed', error_message: 'Stripe refused it' } }) as never)).toBe('Stripe refused it. Nothing was applied.');
    expect(resultText(action({ status: 'executed', execution: { status: 'succeeded', result: 'x' }, witness: { level: 'L1', verdict: 'MISMATCH', summary: 'amount differs' } }) as never)).toBe('x\nDeedglass witness · L1 · MISMATCH · amount differs');
    expect(resultText(action({ status: 'denied', decision: { message: 'Blocked.' } }) as never)).toBe('Blocked. Nothing was run.');
    expect(resultText(action(PENDING) as never)).toContain('Try again after');
    expect(resultText(action({ status: 'cancelled' }) as never)).toBe('Deedglass recorded this action as cancelled.');
  });
});
