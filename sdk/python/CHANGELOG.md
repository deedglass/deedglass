# Changelog

## 0.1.0 (unreleased)

- `Deedglass` and `AsyncDeedglass` over one request core: `act`, `actions`, `deeds`, `approvals`,
  `tools`, `whoami`; retries with the same Idempotency-Key; typed errors from the API catalog.
- `wrap_tool`, `wrap_tool_async` and the `@dg.tool` decorator (gateway and local execution).
- `deedglass.verify`: offline deed and chain checks (crypto extra), equal to Go on every vector.
- `sign_intent` and `generate_intent_key` (crypto extra), byte-equal to S09's vectors.
- Adapters: `deedglass.langchain` and `deedglass.claude_agent_sdk`. `deedglass.openai_agents` and
  `deedglass.crewai` land in the next release.
