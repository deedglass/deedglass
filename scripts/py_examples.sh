#!/usr/bin/env bash
# make py-examples (S32): every examples/python example in scripted mode against DEEDGLASS_SERVER,
# with DEEDGLASS_API_KEY an agent key. Each must print one ✕ line and one "L2 · VERIFIED" line.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PY="$ROOT/sdk/python/.venv/bin/python"
[ -x "$PY" ] || { echo "✕ sdk/python/.venv is missing. Run make py-ci first." >&2; exit 1; }
: "${DEEDGLASS_API_KEY:?is empty. Export an agent key (deedglass init writes .deedglass/quickstart.key).}"
export NO_COLOR=1 PYTHONPATH="$ROOT/examples/python"
fail=0
for ex in quickstart langchain_orders claude_agent_sdk_orders; do
  echo "== $ex"
  out="$("$PY" "$ROOT/examples/python/$ex.py" --scripted 2>&1)" || { echo "$out"; echo "✕ $ex exited non-zero"; fail=1; continue; }
  echo "$out"
  grep -q '^✕ ' <<<"$out" || { echo "✕ $ex printed no ✕ line"; fail=1; }
  grep -q 'L2 · VERIFIED' <<<"$out" || { echo "✕ $ex printed no L2 · VERIFIED line"; fail=1; }
done
exit $fail
