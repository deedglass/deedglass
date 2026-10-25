#!/usr/bin/env bash
# scripts/check-versions.sh <tag> (S35): a release tag, the CHANGELOG.md heading, the TypeScript SDK,
# the Python SDK (PEP 440 form) and the npm CLI wrapper must all name the same version. Prints each
# value it compared; exit 1 on any disagreement, 2 on a tag that isn't vX.Y.Z or vX.Y.Z-rc.N.
set -euo pipefail
cd "$(dirname "$0")/.."
tag="${1:-}"
if ! [[ "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)(-rc\.([0-9]+))?$ ]]; then
  echo "✕ \"$tag\" isn't a release tag. Use vX.Y.Z or vX.Y.Z-rc.N, for example v0.1.0-rc.1." >&2
  exit 2
fi
semver="${tag#v}"
pep440="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}${BASH_REMATCH[5]:+rc${BASH_REMATCH[5]}}"
fail=0
check() { # check <what> <found> <want>
  if [ "$2" = "$3" ]; then echo "✓ $1 · $2"; else echo "✕ $1 · $2, but the tag says $3"; fail=1; fi
}
heading="$(sed -n 's/^## \[\([0-9][^]]*\)\].*/\1/p' CHANGELOG.md | head -1)"
check "CHANGELOG.md newest heading" "${heading:-none}" "$semver"
check "sdk/typescript/package.json" "$(node -p 'require("./sdk/typescript/package.json").version' 2>/dev/null || echo none)" "$semver"
py="$(sed -n 's/^__version__ = "\(.*\)"/\1/p' sdk/python/src/deedglass/_version.py)"
check "sdk/python (PEP 440)" "${py:-none}" "$pep440"
for p in npm/*/package.json; do
  check "$p" "$(node -p "require('./$p').version" 2>/dev/null || echo none)" "$semver"
done
# The Claude Code plugin pins its npx command to the release (scripts/pin-plugin.sh).
check "integrations/claude-code plugin.json" "$(node -p "require('./integrations/claude-code/.claude-plugin/plugin.json').version" 2>/dev/null || echo none)" "$semver"
check "integrations/claude-code .mcp.json npx pin" "$(node -p "require('./integrations/claude-code/.mcp.json').mcpServers.deedglass.args.find((a) => a.startsWith('deedglass@')) || 'unpinned'" 2>/dev/null || echo none)" "deedglass@$semver"
if [ "$fail" = 1 ]; then
  echo "  next  Set every version above to $semver ($pep440 for Python), run scripts/pin-plugin.sh $semver, add a ## [$semver] section to CHANGELOG.md, then tag again."
  exit 1
fi
