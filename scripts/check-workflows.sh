#!/usr/bin/env bash
# scripts/check-workflows.sh [dir] (S35, in make ci): the release workflow's safety rails stay in place.
#   - publish-npm, publish-pypi and deploy-docs run only behind their repository variables and in
#     their GitHub environments (npm, pypi, docs)
#   - release.yml's default is permissions: {} and no workflow anywhere grants write-all
#   - in release.yml only release, publish-npm and publish-pypi may mint an OIDC token (id-token: write,
#     block or flow style, any job name); npm-stage, which stages the npm packages, stays read-only,
#     and publish-npm publishes only what it staged; no YAML anchors or aliases hide a job's keys
#   - verify refuses a tag whose commit isn't on main; deploy-docs checks out the full history (lastmod)
#   - every uses: is pinned to a full commit SHA with a # vX.Y.Z comment
#   - actionlint 1.7.12 passes (the Homebrew binary, else go run at that pin)
#   - .github/required-checks.txt (REQUIRED_CHECKS) names exactly ci.yml's jobs, release-bundles (TY-11,
#     the 2046 test) among them, and that job runs scripts/release-bundles.sh verify on every PR
# --self-test breaks a copy one way at a time (a removed gate, write-all, an unpinned action, an extra
# OIDC token in npm-stage, in a job named with a digit and an underscore, in flow style, after a
# column-0 comment, a removed on-main check, a shallow docs checkout, release-bundles dropped from the
# required checks or from ci.yml) and checks that this script then fails.
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
  sed -i.bak 's|uses: actions/checkout@[0-9a-f]\{40\} # v[0-9.]*|uses: actions/checkout@v6|' "$tmp/release.yml" && rm -f "$tmp"/*.bak
  if NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then echo "✕ self-test: an unpinned uses: wasn't caught" >&2; exit 1; fi
  # broken <what> <awk program>: release.yml through the awk program must fail the check.
  broken() {
    awk "$2" .github/workflows/release.yml > "$tmp/release.yml"
    if cmp -s .github/workflows/release.yml "$tmp/release.yml"; then echo "✕ self-test: the edit for $1 changed nothing" >&2; exit 1; fi
    if NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then echo "✕ self-test: $1 wasn't caught" >&2; exit 1; fi
  }
  broken "an OIDC token in npm-stage" \
    '{print} $0 == "  npm-stage:" {on=1} on && $0 == "      contents: read" {print "      id-token: write"; on=0}'
  broken "an OIDC token in a job named with a digit and an underscore, after release" \
    '$0 == "  sdk-build:" {print "  sbom_2:\n    runs-on: ubuntu-latest\n    permissions:\n      id-token: write  # for the SBOM\n    steps:\n      - run: \"true\"\n"} {print}'
  broken "an OIDC token in flow style" \
    '$0 == "  sdk-build:" {print "  npm2:\n    runs-on: ubuntu-latest\n    permissions: { contents: read, id-token: write }\n    steps:\n      - run: \"true\"\n"} {print}'
  broken "an OIDC token after a column-0 comment" \
    '$0 == "  npm-stage:" {print "# staging"; on=1} {print} on && $0 == "      contents: read" {print "      id-token: write"; on=0}'
  broken "a removed on-main check" '!/merge-base --is-ancestor/'
  broken "a shallow docs checkout" '$0 == "  deploy-docs:" {on=1} on && /fetch-depth: 0/ {on=0; next} {print}'
  cp .github/workflows/release.yml "$tmp/release.yml"
  grep -vx 'release-bundles' .github/required-checks.txt > "$tmp/required-checks.txt"
  if REQUIRED_CHECKS="$tmp/required-checks.txt" NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then
    echo "✕ self-test: release-bundles dropped from the required checks wasn't caught" >&2; exit 1; fi
  awk '$0 == "  release-bundles:" {skip=1; next} skip && /^  [^ #]/ {skip=0} !skip {print}' .github/workflows/ci.yml > "$tmp/ci.yml"
  if REQUIRED_CHECKS=.github/required-checks.txt NO_ACTIONLINT=1 "$0" "$tmp" >/dev/null 2>&1; then
    echo "✕ self-test: the release-bundles job removed from ci.yml wasn't caught" >&2; exit 1; fi
  echo "✓ check-workflows self-test · a removed gate, write-all, an unpinned action, extra OIDC tokens (any name, flow style, after a comment), a removed on-main check, a shallow docs checkout and a dropped 2046 test each fail"
  exit 0
fi
dir="${1:-.github/workflows}"
rel="$dir/release.yml"
fail=0
bad() { echo "✕ $*"; fail=1; }
[ -f "$rel" ] || { echo "✕ $rel is missing"; exit 1; }

# job <name>: the lines of one job in release.yml (from "  name:" to the next key at two spaces or none;
# comments don't end it).
job() { awk -v j="  $1:" '$0 == j {on=1; print; next} on && /^(  )?[^ #]/ {exit} on {print}' "$rel"; }
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
# Every job of release.yml that asks for id-token: write, in block or flow style, with or without a
# comment after it. The jobs are the keys at two spaces under jobs: (any GitHub job id); a two-space
# line that isn't a plain job key, or an id-token line outside a known job, counts as "?" and fails.
oidc="$(awk '
  /^jobs:[[:space:]]*(#.*)?$/ {on=1; j=""; next}
  on && /^[^ #]/ {on=0}
  !on {next}
  /^  [^ #]/ { if ($0 ~ /^  [A-Za-z_][A-Za-z0-9_-]*:[[:space:]]*(#.*)?$/) { j=$0; sub(/^  /, "", j); sub(/:.*/, "", j) } else { j="?" }; next }
  /id-token["\047]?[[:space:]]*:[[:space:]]*["\047]?write/ { print (j == "" ? "?" : j) }
' "$rel")"
for j in $oidc; do
  case "$j" in release|publish-npm|publish-pypi) ;; *) bad "job $j asks for id-token: write; only release, publish-npm and publish-pypi may" ;; esac
done
if grep -nE '^[[:space:]]*(-[[:space:]]+)?[^#[:space:]][^:]*:[[:space:]]+[&*][A-Za-z0-9_-]+|^[[:space:]]*<<:' "$rel"; then
  bad "release.yml uses a YAML anchor, alias or merge key above; spell each job's keys out (this check reads them line by line)"
fi
job verify | grep -q 'merge-base --is-ancestor HEAD refs/remotes/origin/main' || bad "verify doesn't refuse a tag whose commit isn't on main"
job deploy-docs | grep -qE '^ +fetch-depth: 0( |$)' || bad "deploy-docs checks out without fetch-depth: 0 (each page's lastmod comes from git history; a shallow clone can't give it)"
job npm-stage | grep -q 'scripts/npm-publish.mjs stage ' || bad "npm-stage doesn't stage the npm packages with scripts/npm-publish.mjs"
job publish-npm | grep -q 'needs: \[release, npm-stage\]' || bad "publish-npm doesn't need npm-stage (it publishes only what npm-stage staged)"
if job publish-npm | grep -qE '^ +run: .*npm publish '; then bad "publish-npm runs npm publish itself; scripts/npm-publish.mjs publish keeps the order and the skips"; fi
grep -q 'draft: true' .goreleaser.yaml 2>/dev/null || [ "$dir" != ".github/workflows" ] || bad ".goreleaser.yaml doesn't make a draft release"
if grep -nE '^[[:space:]]*permissions:[[:space:]]*write-all' "$dir"/*.yml; then bad "write-all is granted above"; fi
unpinned="$(grep -nE '^[[:space:]]*(-[[:space:]]*)?uses:' "$dir"/*.yml | grep -vE 'uses: [^ ]+@[0-9a-f]{40} # v[0-9]+[.][0-9]+[.][0-9]+$' || true)"
[ -z "$unpinned" ] || bad "not pinned to a commit SHA with its version:"$'\n'"$unpinned"
# The required checks (TY-11): the list names exactly ci.yml's jobs, release-bundles among them, and
# that job runs on pull requests and verifies the release bundles.
req="${REQUIRED_CHECKS:-.github/required-checks.txt}"
ci="$dir/ci.yml"
if [ ! -f "$req" ] || [ ! -f "$ci" ]; then
  bad "$req or $ci is missing"
else
  want="$(grep -vE '^[[:space:]]*(#|$)' "$req" | sort)"
  have="$(awk '/^jobs:/ {on=1; next} on && /^[^ #]/ {on=0} on && /^  [A-Za-z0-9_-]+:[[:space:]]*(#.*)?$/ {j=$1; sub(/:$/, "", j); print j}' "$ci" | sort)"
  [ "$want" = "$have" ] || bad "$req doesn't name exactly ci.yml's jobs:"$'\n'"$(diff <(echo "$want") <(echo "$have") || true)"
  grep -qx 'release-bundles' <<<"$want" || bad "$req doesn't require release-bundles (TY-11, the 2046 test)"
  rb="$(awk '$0 == "  release-bundles:" {on=1; print; next} on && /^  [^ #]/ {exit} on {print}' "$ci")"
  grep -q 'scripts/release-bundles.sh verify' <<<"$rb" || bad "ci.yml's release-bundles job doesn't run scripts/release-bundles.sh verify"
  grep -q 'test/release-bundles.test.ts' <<<"$rb" || bad "ci.yml's release-bundles job doesn't run the browser verifier's release-bundles test"
  grep -qE '^  pull_request:' "$ci" || bad "ci.yml doesn't run on pull requests"
fi
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
echo "✓ workflows · publish jobs gated · permissions {} by default · every action pinned · required checks match ci.yml with the 2046 test · actionlint clean"
