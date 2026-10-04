// The Claude Agent SDK with Deedglass hooks. --scripted (the default) feeds the hooks the events the
// SDK sends, so no model key is needed. --live runs a real agent (ANTHROPIC_API_KEY in your shell;
// never paste it anywhere) and lets the hooks stop a real rm -rf.
// Activate examples/policies/claude-agent-local.yaml first.
//   node examples/ts/claude-agent-sdk/index.ts [--live]
import { query } from '@anthropic-ai/claude-agent-sdk';
import { Deedglass } from '@deedglass/sdk';
import { deedglassHooks } from '@deedglass/sdk/claude-agent-sdk';
import { live, no, note, ok } from '../_lib/print.ts';

const dg = new Deedglass();
const hooks = deedglassHooks({ client: dg });

if (live) {
  if (!process.env.ANTHROPIC_API_KEY) {
    no('--live needs ANTHROPIC_API_KEY in your shell. Run without --live for the scripted demo.');
    process.exit(2);
  }
  for await (const m of query({ prompt: 'List the files here, then run: rm -rf /', options: { hooks, cwd: process.cwd() } })) {
    if (m.type === 'result') note(`agent finished · ${m.subtype}`);
  }
} else {
  const base = { session_id: 'scripted', transcript_path: '/dev/null', cwd: '/workspace' };
  const signal = new AbortController().signal;
  const pre = hooks.PreToolUse![0]!.hooks[0]!;
  const post = hooks.PostToolUse![0]!.hooks[0]!;
  const steps = [
    { tool_name: 'Read', tool_input: { file_path: '/workspace/README.md' }, tool_response: { content: '# Acme' } },
    { tool_name: 'Bash', tool_input: { command: 'rm -rf /' } },
    { tool_name: 'Write', tool_input: { file_path: '/workspace/.env', content: 'KEY=1' } },
  ];
  let i = 0;
  for (const s of steps) {
    const id = `toolu_${++i}`;
    const out = (await pre({ ...base, hook_event_name: 'PreToolUse', tool_name: s.tool_name, tool_input: s.tool_input, tool_use_id: id }, id, { signal })) as { hookSpecificOutput?: { permissionDecisionReason?: string } };
    const reason = out.hookSpecificOutput?.permissionDecisionReason;
    if (reason) {
      no(`claude.${s.tool_name} · denied · ${reason}`);
      continue;
    }
    await post({ ...base, hook_event_name: 'PostToolUse', tool_name: s.tool_name, tool_input: s.tool_input, tool_response: s.tool_response ?? null, tool_use_id: id }, id, { signal });
    ok(`claude.${s.tool_name} · allowed · reported · the host's own permission rules still apply`);
  }
}
