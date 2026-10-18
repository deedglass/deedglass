# OpenAI Agents SDK adapter for Deedglass

Import it from `@deedglass/sdk/openai-agents`. Tested with `@openai/agents` 0.18.0 (2026-10-04), invoking tools the way the runner does
(`invoke(runContext, inputJson)`); the peer range is `>=0.18 <1`.

```ts
import { Agent, run, tool } from '@openai/agents';
import { Deedglass } from '@deedglass/sdk';
import { governTool, deedglassTools } from '@deedglass/sdk/openai-agents';

const dg = new Deedglass();
const refund = governTool(tool({ name: 'refund_customer', parameters, execute }), { client: dg, connector: 'billing' });
const agent = new Agent({ name: 'support', tools: [refund, ...(await deedglassTools(dg))] });
```

- **`governTool(tool, options)`** returns a new `FunctionTool` whose `invoke` parses the JSON input,
  runs the proxy flow, then calls the original `invoke` once, only when authorized.
- **`deedglassTools(client)`** builds `tool({ name, parameters: input_schema, strict: false, execute })`
  for each connector tool. Names fit `^[a-zA-Z0-9_-]{1,64}$` (`github.create_issue` becomes
  `github__create_issue`); a collision or a longer name is refused.
- **Pending and denied** return their sentence to the model by default.
- **The Agents SDK's `needsApproval`** interrupts the run for your own UI; Deedglass approvals go to
  the org's approvers and are sealed. They compose.
