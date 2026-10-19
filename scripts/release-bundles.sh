#!/usr/bin/env bash
# scripts/release-bundles.sh (TY-11, the 2046 test): one sealed export bundle from every release,
# kept forever in testdata/release-bundles/<version>/ and checked by today's verifiers.
#
#   scripts/release-bundles.sh verify [deedglass]   every committed bundle: its SHA256SUMS, then
#       `deedglass verify --strict` against the keys beside it (the CLI given, else one built from
#       this tree), then a tampered copy that must fail; with git tags present, every stable release
#       tag merged into HEAD (other than one on HEAD itself) must have its bundle. In make ci and
#       the CI job release-bundles; the browser verifier's half is web/verify/test/release-bundles.test.ts.
#   scripts/release-bundles.sh add <tag> [--out <dir>]   make <tag>'s bundle from <tag>'s own code:
#       a throwaway worktree at the tag, internal/deed/deedtest/cmd/genrelease copied in and run
#       there (so the writer and sealer are that release's), then the tag's own deedglass verifies
#       it. Writes bundle.tar.gz, keys.json, origin.json and SHA256SUMS into
#       testdata/release-bundles/<version>/ and adds their lines to testdata/SHA256SUMS (with --out,
#       only the directory). Refuses a directory that exists: a committed bundle is never made again.
#
# The bundles are sealed by the published test keys (testdata/deeds/keys/test-keys.json), so they
# mean nothing outside tests; verify --strict passes them with the warning test_keys.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"
DIR="testdata/release-bundles"
GEN="internal/deed/deedtest/cmd/genrelease/main.go"
BI="github.com/deedglass/deedglass/internal/buildinfo"
TMP="$(mktemp -d)"
cleanup() {
  [ ! -d "$TMP/src" ] || git -C "$ROOT" worktree remove --force "$TMP/src" >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

ok() { printf '✓ %s\n' "$*"; }
fail() { printf '✕ %s\n' "$*" >&2; exit 1; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

# versions lists the bundle directories in version order.
versions() { find "$DIR" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; | sort -t. -k1,1n -k2,2n -k3,3n; }

verify() {
  local bin="${1:-}" tmp="$TMP" v n=0
  if [ -z "$bin" ]; then
    go build -o "$tmp/deedglass" ./cmd/deedglass
    bin="$tmp/deedglass"
  fi
  [ -d "$DIR" ] || fail "$DIR is missing"
  for v in $(versions); do
    local d="$DIR/$v"
    [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "$d isn't named for a stable release (X.Y.Z)"
    for f in bundle.tar.gz keys.json origin.json SHA256SUMS; do [ -f "$d/$f" ] || fail "$d/$f is missing"; done
    (cd "$d" && sha256 -c SHA256SUMS >/dev/null) || fail "$d changed since it was committed (SHA256SUMS)"
    local out
    if ! out="$("$bin" verify "$d/bundle.tar.gz" --keys "$d/keys.json" --strict --json 2>&1)"; then
      printf '%s\n' "$out" >&2
      fail "$v: this deedglass doesn't verify the bundle v$v sealed"
    fi
    grep -q '"result": "valid"' <<<"$out" || fail "$v: verify didn't report valid"
    grep -q '"manifest": "ok"' <<<"$out" || fail "$v: the bundle manifest didn't match"
    grep -q '"manifest_seal": "ok"' <<<"$out" || fail "$v: the bundle manifest's seal didn't verify"
    local deeds; deeds="$(sed -n 's/^  "deeds": \([0-9]*\),$/\1/p' <<<"$out")"
    ok "v$v · $deeds deeds · chain intact · manifest sealed"
    n=$((n + 1))
  done
  [ "$n" -gt 0 ] || fail "no release bundles in $DIR"

  # The check can fail: one changed byte in the newest bundle must not verify.
  local last; last="$(versions | tail -1)"
  cp "$DIR/$last/bundle.tar.gz" "$tmp/tampered.tar.gz"
  printf 'x' | dd of="$tmp/tampered.tar.gz" bs=1 seek=200 conv=notrunc 2>/dev/null
  if "$bin" verify "$tmp/tampered.tar.gz" --keys "$DIR/$last/keys.json" --strict -q >/dev/null 2>&1; then
    fail "a tampered copy of v$last's bundle verified; the check is broken"
  fi
  ok "a tampered copy of v$last's bundle fails, as it must"

  # Every stable release merged into HEAD has its bundle (the release's follow-up PR adds it).
  if git rev-parse -q --verify HEAD >/dev/null 2>&1 && [ -n "$(git tag -l 'v*' 2>/dev/null)" ]; then
    local head t missing=""
    head="$(git rev-parse HEAD)"
    for t in $(git tag -l 'v[0-9]*.[0-9]*.[0-9]*'); do
      [[ "$t" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
      [ "$(git rev-parse "$t^{commit}")" = "$head" ] && continue
      git merge-base --is-ancestor "$t" HEAD 2>/dev/null || continue
      [ -d "$DIR/${t#v}" ] || missing="$missing $t"
    done
    [ -z "$missing" ] || fail "no release bundle for:$missing. Next: scripts/release-bundles.sh add <tag> for each, then commit $DIR (docs/RELEASING.md)."
    ok "every release tag in this history has its bundle"
  else
    printf '○ no git tags here, so which releases need a bundle wasn'"'"'t checked\n'
  fi
  ok "the 2046 test · $n release bundles verify with $("$bin" version 2>/dev/null | head -1 | awk -F' · ' '{print $1 " · " $2}')"
}

add() {
  local tag="${1:-}" out=""
  shift || true
  [ "${1:-}" = "--out" ] && out="${2:-}"
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "usage: scripts/release-bundles.sh add vX.Y.Z [--out <dir>] (a stable release tag)"
  local version="${tag#v}"
  [ -n "$out" ] || out="$DIR/$version"
  case "$out" in /*) ;; *) out="$ROOT/$out" ;; esac
  [ ! -e "$out" ] || fail "$out exists; a committed bundle is never made again"
  git rev-parse -q --verify "refs/tags/$tag" >/dev/null || fail "no tag $tag here (git fetch --tags)"
  local commit full tmp="$TMP"
  full="$(git rev-parse "$tag^{commit}")"
  commit="$(git rev-parse --short=7 "$tag^{commit}")"
  git worktree add --detach -q "$tmp/src" "$tag"
  # The tag's own CLI first, from the untouched tree; then the generator, copied in.
  (cd "$tmp/src" && GOWORK=off go build -trimpath -ldflags "-X $BI.Version=$version -X $BI.Commit=$commit" -o "$tmp/deedglass-$version" ./cmd/deedglass)
  mkdir -p "$tmp/src/$(dirname "$GEN")"
  cp "$GEN" "$tmp/src/$GEN"
  (cd "$tmp/src" && GOWORK=off go run "./$(dirname "$GEN")" -version "$version" -commit "$commit" -out "$out")
  local said
  said="$("$tmp/deedglass-$version" verify "$out/bundle.tar.gz" --keys "$out/keys.json" --strict -q 2>&1)" || {
    printf '%s\n' "$said" >&2; rm -rf "$out"; fail "$tag's own deedglass doesn't verify the bundle it made"; }
  local by; by="$("$tmp/deedglass-$version" version | head -1)"
  printf '{"release":"%s","commit":"%s","generator":"%s, run in a checkout of %s","verified_at_release_by":"%s verify --strict"}\n' \
    "$tag" "$full" "$GEN" "$tag" "${by//\"/}" > "$out/origin.json"
  (cd "$out" && sha256 bundle.tar.gz keys.json origin.json > SHA256SUMS)
  if [ "$out" = "$ROOT/$DIR/$version" ]; then # testdata/SHA256SUMS pins every vector too (make testdata-check)
    (cd testdata && sha256 "release-bundles/$version/bundle.tar.gz" "release-bundles/$version/keys.json" \
      "release-bundles/$version/origin.json" >> SHA256SUMS)
  fi
  ok "$tag · ${out#"$ROOT/"} · sealed by $tag's code, verified by $by"
}

case "${1:-}" in
  verify) shift; verify "$@" ;;
  add) shift; add "$@" ;;
  *) echo "usage: scripts/release-bundles.sh verify [deedglass] | add vX.Y.Z [--out <dir>]" >&2; exit 2 ;;
esac
