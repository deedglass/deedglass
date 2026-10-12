#!/usr/bin/env bash
# S47 §3.6: build the three Claude Desktop bundles and check them. Nothing is published.
#   scripts/mcpb.sh            # .tmp/mcpb/deedglass-<version>-{darwin-arm64,darwin-x64,win32-x64}.mcpb
# Each binary is built like make build (CGO off, trimmed, stripped); cmd/mcpbgen writes the bundles;
# the pinned @anthropic-ai/mcpb CLI (deploy/mcpb, ADR-090) validates every manifest and reads every
# bundle back. A failure exits 1.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=${MCPB_OUT:-.tmp/mcpb}
GO=${GO:-go}
VERSION=$($GO run ./cmd/mcpbgen -h 2>&1 | sed -n 's/.*bundle version (default "\(.*\)").*/\1/p')
rm -rf "$OUT" && mkdir -p "$OUT/bin"
args=()
for t in darwin-arm64:darwin:arm64 darwin-x64:darwin:amd64 win32-x64:windows:amd64; do
  IFS=: read -r id goos goarch <<<"$t"
  exe=deedglass; [ "$goos" = windows ] && exe=deedglass.exe
  mkdir -p "$OUT/bin/$id"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch $GO build -trimpath -ldflags "-s -w -X github.com/deedglass/deedglass/internal/buildinfo.Version=$VERSION" -o "$OUT/bin/$id/$exe" ./cmd/deedglass
  args+=(-bin "$id=$OUT/bin/$id/$exe")
done
$GO run ./cmd/mcpbgen -out "$OUT" "${args[@]}"
M=deploy/mcpb/node_modules/.bin/mcpb
if [ ! -x "$M" ]; then
  (cd deploy/mcpb && npm ci --ignore-scripts --no-audit --no-fund >/dev/null)
fi
for f in "$OUT"/*.mcpb; do
  d="$OUT/check/$(basename "$f" .mcpb)"
  mkdir -p "$d" && (cd "$d" && unzip -q "$OLDPWD/$f")
  "$M" validate "$d/manifest.json" >/dev/null
  "$M" info "$f" | grep -q "^File: $(basename "$f")$"
  echo "✓ $(basename "$f") · manifest valid · $(du -h "$f" | cut -f1)"
done
