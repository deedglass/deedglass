# TypeScript examples

Each example uses only erasable TypeScript, so Node 24 runs it directly. `--scripted` is the
default: no model key is needed, and every tool call is a real Deedglass action.

```sh
make sdk-ts                       # once: installs and builds sdk/typescript
deedglass policy push examples/policies/sdk-demo.yaml
deedglass policy activate sdk-demo --version 1 --mode enforce --reason "SDK examples" --yes
export DEEDGLASS_API_KEY=dg_test_…  # an agent key
make sdk-examples
```

| Example | Shows |
|---|---|
| `quickstart` | `act()` on a connector tool (gateway mode) |
| `wrap` | `wrapTool` on your own functions: allowed, denied, pending |
| `verify` | deeds checked offline with `@deedglass/sdk/verify` |
| `ai-sdk`, `openai-agents`, `langchain` | the same three calls through each framework's adapter |
| `claude-agent-sdk` | the hooks deny `rm -rf /` and a `.env` write (activate `claude-agent-local.yaml`); `--live` runs a real agent |
