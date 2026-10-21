#!/usr/bin/env bash
# examples/frameworks/run.sh <framework> [--agent]
#
# Runs one framework recipe against a local stack started by `npx deedglass init`:
#   1. installs the pinned packages (package.json / requirements.txt) into a throwaway directory,
#   2. runs check.ts / check.py, which need no model key: they start `npx -y deedglass mcp serve`
#      through the framework's own MCP client and, where the framework lets a tool run without a
#      model, send one postgres.query through Deedglass,
#   3. with --agent, also runs agent.* and native.ts when the model key the file names is set.
#
# STACK_DIR is the folder where init ran (default: the current directory). DEEDGLASS_API_KEY and
# DEEDGLASS_SERVER are taken from its .deedglass/ when unset. PYTHON picks the interpreter
# (default python3.13, else python3). An install that fails is reported as "skipped" and exits 0,
# so a framework's broken release never fails the run; STRICT=1 makes it fail instead.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
fw="${1:-}"; mode="${2:-}"
if [ -z "$fw" ] || [ ! -d "$here/$fw" ]; then
  echo "usage: examples/frameworks/run.sh <$(cd "$here" && ls -d */ | tr -d / | paste -sd'|' -)> [--agent]" >&2
  exit 2
fi
stack="$(cd "${STACK_DIR:-.}" && pwd)"
[ -f "$stack/.deedglass/quickstart.key" ] || { echo "✕ no .deedglass/quickstart.key in $stack: run npx deedglass init there first, or set STACK_DIR" >&2; exit 1; }
export DEEDGLASS_API_KEY="${DEEDGLASS_API_KEY:-$(cat "$stack/.deedglass/quickstart.key")}"
if [ -z "${DEEDGLASS_SERVER:-}" ]; then
  listen="$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")).listen || "127.0.0.1:8080")' "$stack/.deedglass/stack.json")"
  export DEEDGLASS_SERVER="http://$listen"
fi
export PYDANTIC_AI_NO_BANNER=1
# Each recipe starts `npx -y deedglass mcp serve`. Fetch it once now, then let npx use its cache, so the
# first start stays inside the frameworks' MCP connect timeouts (some are a few seconds).
(cd "$stack" && npx -y deedglass version >/dev/null)
export npm_config_prefer_offline=true
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
src="$here/$fw"
skip() { if [ -n "${STRICT:-}" ]; then echo "✕ $fw: $1" >&2; exit 1; fi; echo "◐ $fw skipped: $1"; exit 0; }

# again <cmd…>: run a check, and once more if it fails. Several MCP clients start the server with a
# trimmed environment, so npx may still ask the registry first; on a slow network that can outlast a
# framework's connect timeout (a few seconds in some). A real failure fails twice.
again() { "$@" || { echo "◐ $fw: the check failed once; running it again"; "$@"; }; }

# needs <file>: the model key a recipe names in its first line (ANTHROPIC_API_KEY and so on), if any.
needs() { grep -oE '[A-Z][A-Z_]*_API_KEY' "$1" | grep -v DEEDGLASS | head -1 || true; }

ran=0
if [ -f "$src/package.json" ]; then
  cp "$src"/*.ts "$src/package.json" "$work/"
  (cd "$work" && npm install --no-audit --no-fund --loglevel=error >"$work/npm.log" 2>&1) || { tail -20 "$work/npm.log"; skip "npm install failed"; }
  echo "▸ $fw · check.ts"
  again bash -c 'cd "$1" && node "$2"' _ "$stack" "$work/check.ts"
  ran=1
  if [ "$mode" = "--agent" ]; then
    for f in agent native; do
      [ -f "$work/$f.ts" ] || continue
      key="$(needs "$work/$f.ts")"
      if [ -n "$key" ] && [ -z "${!key:-}" ]; then echo "◐ $fw · $f.ts skipped: $key isn't set"; continue; fi
      echo "▸ $fw · $f.ts"; (cd "$stack" && node "$work/$f.ts")
    done
  fi
fi
if [ -f "$src/requirements.txt" ]; then
  py="${PYTHON:-$(command -v python3.13 || command -v python3)}"
  "$py" -m venv "$work/venv"
  "$work/venv/bin/pip" install -q --disable-pip-version-check -r "$src/requirements.txt" >"$work/pip.log" 2>&1 || { tail -20 "$work/pip.log"; skip "pip install failed"; }
  echo "▸ $fw · check.py"
  again bash -c 'cd "$1" && "$2" "$3"' _ "$stack" "$work/venv/bin/python" "$src/check.py"
  ran=1
  if [ "$mode" = "--agent" ] && [ -f "$src/agent.py" ]; then
    key="$(needs "$src/agent.py")"
    if [ -n "$key" ] && [ -z "${!key:-}" ]; then echo "◐ $fw · agent.py skipped: $key isn't set"
    elif [ "$fw" = google-adk ]; then echo "▸ $fw · adk run (type a question, then exit)"; (cd "$stack" && "$work/venv/bin/adk" run "$src")
    else echo "▸ $fw · agent.py"; (cd "$stack" && "$work/venv/bin/python" "$src/agent.py"); fi
  fi
fi
[ "$ran" = 1 ] || { echo "✕ $fw has no package.json or requirements.txt" >&2; exit 1; }
echo "✓ $fw"
