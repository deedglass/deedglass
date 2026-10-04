#!/usr/bin/env bash
# make sdk-examples (S31): every examples/ts example against DEEDGLASS_SERVER with DEEDGLASS_API_KEY
# (an agent key) and sdk-demo.yaml active. The claude-agent-sdk example runs when
# SDK_EXAMPLES_CLAUDE=1 (activate claude-agent-local.yaml first). Links the built SDK and its dev
# frameworks into examples/ts/node_modules (git-ignored); installs nothing.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
ex="$root/examples/ts"
sdk="$root/sdk/typescript"
[ -n "${DEEDGLASS_API_KEY:-}" ] || { echo "✕ Set DEEDGLASS_API_KEY to an agent key (dg_test_…), then run make sdk-examples again." >&2; exit 3; }
mkdir -p "$ex/node_modules/@deedglass" "$ex/node_modules/@openai" "$ex/node_modules/@langchain" "$ex/node_modules/@anthropic-ai"
ln -sfn "$sdk" "$ex/node_modules/@deedglass/sdk"
for m in ai @openai/agents @langchain/core @anthropic-ai/claude-agent-sdk; do ln -sfn "$sdk/node_modules/$m" "$ex/node_modules/$m"; done
names="${SDK_EXAMPLES:-quickstart wrap verify ai-sdk openai-agents langchain}"
[ "${SDK_EXAMPLES_CLAUDE:-0}" = 1 ] && [ -z "${SDK_EXAMPLES:-}" ] && names="$names claude-agent-sdk"
for n in $names; do
  echo "$ node examples/ts/$n/index.ts --scripted"
  (cd "$root" && NO_COLOR=1 node --no-warnings "examples/ts/$n/index.ts" --scripted)
done
