# Vercel AI SDK adapter for Deedglass

Import it from `@deedglass/sdk/ai-sdk`. Tested with `ai` 7.0.127 (2026-10-04) using AI SDK's own `MockLanguageModelV4`; the peer range is
`ai >=7 <8`.

```ts
import { generateText } from 'ai';
import { Deedglass } from '@deedglass/sdk';
import { governTools, deedglassTools } from '@deedglass/sdk/ai-sdk';

const dg = new Deedglass();
const tools = governTools({ refund_customer, send_report }, { client: dg, connector: 'billing' });
const connectorTools = await deedglassTools(dg, { include: ['stripe.*'] });
await generateText({ model, tools: { ...tools, ...connectorTools }, prompt });
```

- **`governTools(tools, options)`** (proxy mode): each tool's `execute` runs only after Deedglass
  authorizes the call, recorded as `<connector>.<name>` (`connector` defaults to `app`; `names` maps
  one tool to any Deedglass name). Tools without `execute` are left alone, with a warning. The
  `abortSignal` passes through.
- **`deedglassTools(client)`** (gateway mode): the org's connector tools from `GET /v1/tools`, named
  `stripe__create_refund`, with their JSON Schema (`jsonSchema(...)`), run by Deedglass with the
  connector's credentials, which never leave it.
- **Pending and denied:** by default the model reads the sentence (`Blocked by policy "no-deletes":
  Agents never delete customers. Nothing was run.`, or `Waiting for approval in Console › Approvals
  (apr_…). Try again after someone decides.`, the same sentence the Python SDK writes), and the run continues.
  `onDenied: 'throw'`, `onPending: 'throw' | 'wait'` change that. `onAction` sees every decided action.
- **AI SDK's `toolApproval` and Deedglass's approvals are different people:** `toolApproval` asks the
  chat user in your UI; a Deedglass rule asks the org's approvers (Console, email, Slack) and seals
  their answer. Use both when a call needs both.
