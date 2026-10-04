# deedglass (Python)

Put Deedglass in front of your agent's tools: policy decides, people approve, every call is sealed
as a deed you can check offline.

```sh
pip install deedglass                      # the client: act, tools, errors
pip install "deedglass[crypto]"            # + offline deed checks and signed intents
pip install "deedglass[langchain]"         # + the LangChain / LangGraph adapter
pip install "deedglass[claude-agent-sdk]"  # + the Claude Agent SDK adapter
```

Python 3.10 to 3.14. Python 3.10 reaches its end of life in October 2026; a release after that
date will drop it.

## 60 seconds

Run `deedglass init` first (it starts a local Deedglass with a demo database and writes an agent
key to `.deedglass/quickstart.key`), then:

```python
from deedglass import Deedglass, PolicyDeniedError

dg = Deedglass()   # DEEDGLASS_API_KEY and DEEDGLASS_SERVER, else http://127.0.0.1:8080

action = dg.act("postgres.execute", {"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"},
                wait_for_approval=120)
print(action)      # act_01k6… · postgres.execute · executed · L2 · VERIFIED · deed_01k6…

try:
    dg.act("postgres.execute", {"sql": "DROP TABLE public.orders"})
except PolicyDeniedError as e:
    print(e)       # Blocked by policy "block-destructive-sql": DROP is never allowed for agents. (req_01k6…)
```

## Govern a LangChain agent in three lines

```python
from deedglass import Deedglass
from deedglass.langchain import DeedglassMiddleware

agent = create_agent(model, tools=[ship_order], middleware=[DeedglassMiddleware(Deedglass(), connector="orders")])
```

A denied call never runs; the model reads the policy's own sentence instead, and the run continues.

## Claude Agent SDK

```python
from claude_agent_sdk import ClaudeAgentOptions
from deedglass import AsyncDeedglass
from deedglass.claude_agent_sdk import deedglass_hooks

options = ClaudeAgentOptions(hooks=deedglass_hooks(AsyncDeedglass()))
```

## Your own functions

```python
dg = Deedglass()

@dg.tool("billing.refund", execute="local", on_deny="return")
def refund(customer: str, amount: int) -> dict:
    ...   # runs only after Deedglass authorizes this exact call; the outcome is sealed as a deed
```

## Check deeds offline

```python
from deedglass.verify import KeySet, split_lines, verify_chain

keys = KeySet.fetch("https://deedglass.example/.well-known/deedglass/keys.json")
report = verify_chain(split_lines(open("deeds.jsonl", "rb").read()), keys)
print(report.result, report.intact)   # valid 1204
```

The verifier gives the same answer as `deedglass verify` and the TypeScript SDK on every published
vector and on a 500-deed parity corpus.

## What it doesn't do

- It doesn't evaluate policies locally or cache decisions: Deedglass decides every call.
- The adapters govern tool calls, not model prompts or replies.
- The OpenAI Agents SDK and CrewAI adapters land in the next release; their extras are declared now.

Docs: https://docs.deedglass.com/sdk/python · Licence: Apache-2.0
