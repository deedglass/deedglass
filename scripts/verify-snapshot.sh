#!/usr/bin/env bash
# scripts/verify-snapshot.sh (S35, make release-check): builds a GoReleaser snapshot twice from the
# same tree and checks what a release would ship, without pushing or signing anything:
#   - the linux_amd64 binary is byte-identical across the two builds (reproducible)
#   - five archives, each with LICENSE, README.md, CHANGELOG.md and three completions
#   - checksums.txt covers every archive, and every archive has an SPDX SBOM
#   - the release image builds for linux/amd64 and linux/arm64 from Dockerfile.release (an OCI
#     tarball, no push, no emulation), is under 30 MB per platform, runs as 65532:65532 and
#     carries the five labels, including the MCP Registry name
# Needs goreleaser 2.18 and syft (ADR-067) and docker buildx. Output in .tmp/release/.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$PWD"
OUT="$ROOT/.tmp/release"
for t in goreleaser syft docker; do
  command -v "$t" >/dev/null || { echo "✕ $t isn't installed." >&2; echo "  next  brew install goreleaser syft (ADR-067), and start Docker." >&2; exit 2; }
done
rm -rf "$OUT" && mkdir -p "$OUT"
fail=0
bad() { echo "✕ $*"; fail=1; }

build() { # build <n>: a snapshot into .tmp/release/run<n>
  goreleaser release --snapshot --clean --skip=publish,sign,docker,homebrew --parallelism 4 >"$OUT/goreleaser-$1.log" 2>&1 ||
    { tail -30 "$OUT/goreleaser-$1.log" >&2; echo "✕ goreleaser snapshot $1 failed (log: .tmp/release/goreleaser-$1.log)" >&2; exit 1; }
  mv dist "$OUT/run$1"
}
build 1
build 2

R="$OUT/run1"
sha() { shasum -a 256 "$1" | awk '{print $1}'; }
b1="$(ls "$R"/deedglass_linux_amd64*/deedglass)"; b2="$(ls "$OUT"/run2/deedglass_linux_amd64*/deedglass)"
h1="$(sha "$b1")"; h2="$(sha "$b2")"
[ "$h1" = "$h2" ] || bad "linux/amd64 binary differs between builds: $h1 vs $h2"

archives=()
while IFS= read -r a; do archives+=("$a"); done < <(cd "$R" && ls deedglass_*.tar.gz deedglass_*.zip 2>/dev/null | sort)
[ "${#archives[@]}" = 5 ] || bad "${#archives[@]} archives, want 5"
for a in "${archives[@]}"; do
  case "$a" in
    *.zip) list="$(unzip -Z1 "$R/$a")" ;;
    *) list="$(tar -tzf "$R/$a")" ;;
  esac
  for f in LICENSE README.md CHANGELOG.md completions/deedglass.bash completions/deedglass.zsh completions/deedglass.fish; do
    grep -qx "$f" <<<"$list" || bad "$a has no $f"
  done
  grep -q "  $a\$" "$R/checksums.txt" || bad "checksums.txt doesn't list $a"
  [ "$(grep "  $a\$" "$R/checksums.txt" | awk '{print $1}')" = "$(sha "$R/$a")" ] || bad "checksums.txt has the wrong sha256 for $a"
  [ -s "$R/$a.sbom.json" ] || bad "$a has no SBOM"
  grep -q '"spdxVersion"' "$R/$a.sbom.json" 2>/dev/null || bad "$a.sbom.json isn't SPDX JSON"
done
sbom_pkgs="$(jq '[.packages | length] | add' "$R/${archives[0]}.sbom.json" 2>/dev/null || echo 0)"

# The image: a context laid out as linux/<arch>/deedglass, like GoReleaser's dockers_v2.
ctx="$OUT/image-context"; mkdir -p "$ctx/linux/amd64" "$ctx/linux/arm64"
cp "$(ls "$R"/deedglass_linux_amd64*/deedglass)" "$ctx/linux/amd64/deedglass"
cp "$(ls "$R"/deedglass_linux_arm64*/deedglass)" "$ctx/linux/arm64/deedglass"
cp Dockerfile.release "$ctx/Dockerfile"
builder="deedglass-release-check"
docker buildx inspect "$builder" >/dev/null 2>&1 || docker buildx create --name "$builder" --driver docker-container >/dev/null
docker buildx build --builder "$builder" --platform linux/amd64,linux/arm64 -f "$ctx/Dockerfile" \
  --output "type=oci,dest=$OUT/image.oci.tar" "$ctx" >"$OUT/buildx.log" 2>&1 || { tail -20 "$OUT/buildx.log" >&2; bad "the image didn't build"; }
docker buildx rm "$builder" >/dev/null 2>&1 || true
img="$OUT/image"; mkdir -p "$img" && tar -xf "$OUT/image.oci.tar" -C "$img"
blob() { cat "$img/blobs/sha256/${1#sha256:}"; }
index="$(cat "$img/index.json")"
top="$(jq -r '.manifests[0].digest' <<<"$index")"
mt="$(blob "$top" | jq -r '.mediaType // ""')"
manifests="$(blob "$top" | jq -c '.manifests[]? | select(.platform.os == "linux")')"
[ -n "$manifests" ] || manifests="$(jq -c '.manifests[] | select(.platform.os == "linux")' <<<"$index")"
sizes=""
while IFS= read -r m; do
  [ -n "$m" ] || continue
  arch="$(jq -r '.platform.architecture' <<<"$m")"
  man="$(blob "$(jq -r .digest <<<"$m")")"
  size="$(jq '[.layers[].size] | add' <<<"$man")"
  cfg="$(blob "$(jq -r .config.digest <<<"$man")")"
  user="$(jq -r .config.User <<<"$cfg")"
  mib="$(awk -v s="$size" 'BEGIN{printf "%.1f", s/1048576}')"
  sizes="$sizes linux/$arch $mib MiB"
  awk -v s="$size" 'BEGIN{exit !(s < 30*1048576)}' || bad "linux/$arch image is $mib MiB, over 30 MB"
  [ "$user" = "65532:65532" ] || bad "linux/$arch runs as $user"
  for l in org.opencontainers.image.title org.opencontainers.image.description org.opencontainers.image.source org.opencontainers.image.licenses io.modelcontextprotocol.server.name; do
    [ "$(jq -r --arg l "$l" '.config.Labels[$l] // ""' <<<"$cfg")" != "" ] || bad "linux/$arch has no $l label"
  done
done <<<"$manifests"
[ "$(grep -c 'linux/' <<<"$(tr ' ' '\n' <<<"$sizes")")" = 2 ] || bad "the image index doesn't hold linux/amd64 and linux/arm64 ($mt)"

{
  echo "linux_amd64 sha256 run1 $h1"
  echo "linux_amd64 sha256 run2 $h2"
  echo "image$sizes"
  echo "sbom packages (first archive) $sbom_pkgs"
} >"$OUT/summary.txt"
if [ "$fail" = 1 ]; then
  echo "  next  Fix the lines above; the logs are in .tmp/release/."
  exit 1
fi
echo "✓ 5 binaries · 5 archives · checksums · 5 SBOMs · image linux/amd64 + linux/arm64 · reproducible"
sed 's/^/  /' "$OUT/summary.txt"
