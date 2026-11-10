# Melaya.org Integration Kit: Deedglass Connector

This kit provides the integration specification and drop-in code adapters for **Melaya** ([melaya.org](https://melaya.org)) agents, enabling native database verification, pre-call policy enforcement, and tamper-evident deed generation for agent tool calls.

---

## 1. Architectural Overview

When an agent on Melaya executes tools that interact with databases (PostgreSQL, MySQL) or external APIs (Stripe, GitHub, FHIR), Deedglass provides two integration paths:

```mermaid
flowchart LR
    A["Melaya Agent"] -->|Tool Invocation| D["Deedglass Gate<br/>(Policy: Allow / Deny / Hold)"]
    D -->|Executes with Vaulted Creds| DB[("PostgreSQL / Target System")]
    DB -->|Logical Replication / CDC| W["Deedglass Witness<br/>(Verifies Committed Effect)"]
    W -->|Dual Sign (Ed25519 + PQC)| S["Sealed Deed<br/>(Matches / Doesn't Match)"]
    S --> A
```

### Path A: Model Context Protocol (MCP) — Zero Code Changes
If Melaya agents connect to MCP servers via stdio or Streamable HTTP:
1. Melaya launches Deedglass as an MCP server:
   ```json
   {
     "mcpServers": {
       "deedglass": {
         "command": "npx",
         "args": ["-y", "deedglass", "mcp", "serve"]
       }
     }
   }
   ```
2. Or connects over local HTTP (`http://127.0.0.1:4180/v1/mcp`).
3. Deedglass exposes standard database tools (`postgres_query`, `postgres_execute`, `deedglass_check_approval`).
4. Every write is automatically audited against Postgres's logical replication stream.

---

### Path B: TypeScript SDK Adapter (`@deedglass/sdk`)

For Melaya runtimes executing tools in Node.js / TypeScript:

```typescript
import { Deedglass } from '@deedglass/sdk';
import { governCall, deedglassName, type GovernOptions } from '@deedglass/sdk/adapters';

export interface MelayaTool {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
  execute: (args: Record<string, unknown>, context?: unknown) => Promise<unknown>;
}

/**
 * Wraps Melaya agent tools with Deedglass pre-call policy and cryptographic audit deeds.
 */
export function governMelayaTools<T extends Record<string, MelayaTool>>(
  tools: T,
  options: GovernOptions
): T {
  const governed: Record<string, MelayaTool> = {};

  for (const [key, tool] of Object.entries(tools)) {
    const originalExecute = tool.execute;
    const dgToolName = deedglassName(options, tool.name || key);

    governed[key] = {
      ...tool,
      execute: async (args: Record<string, unknown>, context?: unknown) => {
        return governCall(
          options,
          dgToolName,
          args,
          async (signal) => {
            return await originalExecute.call(tool, args, context);
          }
        );
      },
    };
  }

  return governed as T;
}
```

#### Example Usage in Melaya Agent Definition:
```typescript
import { Deedglass } from '@deedglass/sdk';
import { governMelayaTools } from './melaya_adapter';

const dg = new Deedglass({
  baseUrl: process.env.DEEDGLASS_URL || 'http://127.0.0.1:4180',
  apiKey: process.env.DEEDGLASS_API_KEY,
});

// Original Melaya tools
const agentTools = {
  updateUserBalance: {
    name: 'update_user_balance',
    description: 'Updates customer balance in public.accounts',
    parameters: { userId: 'string', amount: 'number' },
    execute: async ({ userId, amount }) => {
      return await db.query('UPDATE accounts SET balance = balance + $1 WHERE id = $2', [amount, userId]);
    },
  },
};

// Wrap with Deedglass
export const safeTools = governMelayaTools(agentTools, {
  client: dg,
  agentId: 'melaya-billing-agent',
});
```

---

### Path C: Python SDK Adapter (`deedglass-python`)

For Python-based Melaya agent runtimes:

```python
from deedglass import DeedglassClient
import functools

client = DeedglassClient(base_url="http://127.0.0.1:4180")

def govern_melaya_tool(tool_name: str, agent_id: str = "melaya-agent"):
    """Decorator to govern Melaya Python agent tool execution with Deedglass."""
    def decorator(func):
        @functools.wraps(func)
        def wrapper(*args, **kwargs):
            payload = kwargs if kwargs else {"args": list(args)}
            
            # Step 1: Pre-call Gate evaluation
            decision = client.gate.evaluate(tool=tool_name, agent_id=agent_id, arguments=payload)
            if decision.outcome == "DENIED":
                raise PermissionError(f"Deedglass Policy Denied action: {decision.reason}")
            elif decision.outcome == "BLOCKED":
                raise PermissionError(f"Action held for human countersignature: {decision.approval_id}")

            # Step 2: Tool execution
            result = func(*args, **kwargs)

            # Step 3: Record and seal deed
            client.deeds.record_effect(
                action_id=decision.action_id,
                reported_effect=result,
                level="L2"  # Proven via database WAL
            )
            return result
        return wrapper
    return decorator
```

---

## 2. Benefits for Melaya & Its Users

1. **Enterprise Grade Guardrails:** Melaya users can safely let agents execute database queries without fear of rogue deletions or cascading trigger corruptions.
2. **Deterministic Rollback:** If an agent updates 300 rows instead of 1, Deedglass provides one-click reverse SQL.
3. **Cryptographic Proof:** Every action generates an offline-verifiable deed dual-signed with Ed25519 and Post-Quantum Cryptography (ML-DSA-65).

