"""A LangGraph agent whose SQL tool runs through Deedglass (gateway mode).

The agent tries DROP TABLE public.orders (blocked: the model reads the policy's own sentence) and
then marks order 42 shipped (✓ L2 · VERIFIED: Postgres's change log confirms one row changed).

    pip install "deedglass[langchain]"
    python examples/python/langchain_orders.py --scripted     # a scripted model: no LLM key needed

Without --scripted, set OPENAI_API_KEY or ANTHROPIC_API_KEY and pass --model, for example
--model anthropic:claude-sonnet-5-5 (needs langchain-anthropic).
"""

from __future__ import annotations

import argparse
from typing import Any

from _show import show
from pydantic import BaseModel, Field

from deedglass import Deedglass
from deedglass.langchain import deedglass_tool


class SQL(BaseModel):
    sql: str = Field(description="One SQL statement for the orders database.")


def scripted_model() -> Any:
    from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
    from langchain_core.messages import AIMessage

    class Scripted(GenericFakeChatModel):
        def bind_tools(self, tools: Any, **kw: Any) -> Any:
            return self

    calls = [
        {"name": "postgres__execute", "args": {"sql": "DROP TABLE public.orders"}, "id": "call_drop"},
        {"name": "postgres__execute", "args": {"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"}, "id": "call_ship"},
    ]
    return Scripted(messages=iter([AIMessage(content="", tool_calls=[calls[0]]), AIMessage(content="", tool_calls=[calls[1]]), AIMessage(content="Done.")]))


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--scripted", action="store_true", help="replay a fixed tool-call sequence (no LLM)")
    p.add_argument("--model", default="", help="a LangChain model id, for example anthropic:claude-sonnet-5-5")
    args = p.parse_args()
    from langchain.agents import create_agent

    with Deedglass() as dg:
        sql = deedglass_tool(dg, "postgres.execute", SQL, description="Run one SQL statement on the orders database.",
                             wait_for_approval=0, on_action=show)
        model: Any = scripted_model() if args.scripted or not args.model else args.model
        agent = create_agent(model, tools=[sql], system_prompt="You manage Acme's orders.")
        agent.invoke({"messages": [{"role": "user", "content": "Clean up the orders table, then ship order 42."}]})


if __name__ == "__main__":
    main()
