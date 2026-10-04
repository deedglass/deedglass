# Changelog

## 0.1.0 (unreleased)

- The `Deedglass` client: `act`, `actions.get|waitUntilSettled|cancel|reportResult`, `tools.list`,
  `deeds.get|verify`, `keys.fetch`, `whoami`, with typed errors, retries on a stable idempotency
  key, a timeout per request, the browser guard and key redaction.
- `wrapTool`: proxy mode for your own functions, with the pending registry and honest failures.
- Signed intents: `signIntent`, `argsDigest`, `intentKid`, `generateIntentKey` (`deedglass.intent/1`).
- `@deedglass/sdk/verify`: offline deed checks with the same answers as `deedglass verify`.
- Adapters: `/ai-sdk`, `/openai-agents`, `/langchain`, `/claude-agent-sdk`.
