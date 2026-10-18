# LangChain.js adapter for Deedglass

Import it from `@deedglass/sdk/langchain`. Tested with `@langchain/core` 1.2.14 (2026-10-04), including a `ToolMessage` round trip; the peer
range is `>=1.2 <2`.

```ts
import { tool } from '@langchain/core/tools';
import { Deedglass } from '@deedglass/sdk';
import { governTool, deedglassTools } from '@deedglass/sdk/langchain';

const dg = new Deedglass();
const refund = governTool(tool(refundCustomer, { name: 'refund_customer', schema }), { client: dg, connector: 'billing' });
const tools = [refund, ...(await deedglassTools(dg))];
```

- **`governTool(tool, options)`** rebuilds the tool with the same name, description and schema; its
  function calls the original `invoke` only after Deedglass authorizes the call. Invoked with a tool
  call, it answers a `ToolMessage` as LangChain does.
- **`deedglassTools(client)`** passes each connector tool's JSON Schema. LangChain doesn't validate
  JSON Schema; Deedglass validates arguments server-side before anything runs.
- **Pending and denied** return their sentence as the tool output by default.
- **LangGraph's `interrupt`** pauses a graph for your own reviewer; Deedglass approvals go to the
  org's approvers and are sealed. They compose.
