# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY (CrewAI's default model).
from crewai import Agent, Crew, Task
from crewai_tools import MCPServerAdapter
from mcp import StdioServerParameters

params = StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"])
with MCPServerAdapter(params, connect_timeout=60) as mcp_tools:  # CrewAI names them postgres_query, ...
    analyst = Agent(role="Orders analyst", goal="Answer questions about orders from the database.",
                    backstory="You read the orders database through Deedglass.", tools=mcp_tools)
    task = Task(description="How many rows are in public.orders?", expected_output="One number.", agent=analyst)
    print(Crew(agents=[analyst], tasks=[task]).kickoff())
