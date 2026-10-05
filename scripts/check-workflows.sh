#!/usr/bin/env bash
# scripts/check-workflows.sh [dir] (S35, in make ci): the release workflow's safety rails stay in place.
#   - publish-npm, publish-pypi and deploy-docs run only behind their repository variables and in
#     their GitHub environments (npm, pypi, docs)
#   - release.yml's default is permissions: {} and no workflow anywhere grants write-all
#   - every uses: is pinned to a full commit SHA with a # vX.Y.Z comment
#   - actionlint 1.7.12 passes (the Homebrew binary, else go run at that pin)
# --self-test removes one gate from a copy and checks that this script then fails.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "${1:-}" = "--self-test" ]; then
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  cp .github/workflows/*.yml "$tmp/"
  sed -i.bak "/if: vars.DEEDGLASS_PUBLISH_NPM == 'true'/d" "$tmp/release.yml" && rm -f "$tmp"/*.bak
  if NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then echo "✕ self-test: a removed npm gate wasn't caught" >&2; exit 1; fi
  cp .github/workflows/release.yml "$tmp/release.yml"
  printf '\npermissions: write-all\n' >> "$tmp/release.yml"
  if NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then echo "✕ self-test: write-all wasn't caught" >&2; exit 1; fi
  cp .github/workflows/release.yml "$tmp/release.yml"
  sed -i.bak 's|uses: actions/checkout@[0-9a-f]\{40\} # v6.1.0|uses: actions/checkout@v6|' "$tmp/release.yml" && rm -f "$tmp"/*.bak
  if NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then echo "✕ self-test: an unpinned uses: wasn't caught" >&2; exit 1; fi
  echo "✓ check-workflows self-test · a removed gate, write-all and an unpinned action each fail"
  exit 0
fi
dir="${1:-.github/workflows}"
rel="$dir/release.yml"
fail=0
bad() { echo "✕ $*"; fail=1; }
[ -f "$rel" ] || { echo "✕ $rel is missing"; exit 1; }

# job <name>: the lines of one job in release.yml (from "  name:" to the next two-space key).
job() { awk -v j="  $1:" '$0 == j {on=1; print; next} on && /^  [a-z][a-z-]*:$/ {exit} on {print}' "$rel"; }
gate() { # gate <job> <variable> <environment>
  local body; body="$(job "$1")"
  [ -n "$body" ] || { bad "release.yml has no $1 job"; return; }
  grep -q "if: vars.$2 == 'true'" <<<"$body" || bad "$1 runs without the vars.$2 == 'true' gate"
  grep -q "environment: $3\$" <<<"$body" || bad "$1 isn't in the $3 environment"
}
gate publish-npm DEEDGLASS_PUBLISH_NPM npm
gate publish-pypi DEEDGLASS_PUBLISH_PYPI pypi
gate deploy-docs DEEDGLASS_DEPLOY_DOCS docs
grep -q '^permissions: {}$' "$rel" || bad "release.yml's default permissions aren't {}"
grep -q 'draft: true' .goreleaser.yaml 2>/dev/null || [ "$dir" != ".github/workflows" ] || bad ".goreleaser.yaml doesn't make a draft release"
if grep -nE '^[[:space:]]*permissions:[[:space:]]*write-all' "$dir"/*.yml; then bad "write-all is granted above"; fi
unpinned="$(grep -nE '^[[:space:]]*(-[[:space:]]*)?uses:' "$dir"/*.yml | grep -vE 'uses: [^ ]+@[0-9a-f]{40} # v[0-9]+[.][0-9]+[.][0-9]+$' || true)"
[ -z "$unpinned" ] || bad "not pinned to a commit SHA with its version:"$'\n'"$unpinned"
if [ -z "${NO_ACTIONLINT:-}" ]; then
  # GitHub's service containers accept command: (ci.yml uses it); actionlint 1.7.12 doesn't know it yet.
  ignore='unexpected key "command" for "services" section'
  if command -v actionlint >/dev/null 2>&1; then
    actionlint -ignore "$ignore" "$dir"/*.yml || bad "actionlint found problems"
  else
    go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -ignore "$ignore" "$dir"/*.yml || bad "actionlint found problems"
  fi
fi
if [ "$fail" = 1 ]; then
  echo "  next  Put the gate, environment, permission or pin back; see the header of .github/workflows/release.yml."
  exit 1
fi
echo "✓ workflows · publish jobs gated · permissions {} by default · every action pinned · actionlint clean"
