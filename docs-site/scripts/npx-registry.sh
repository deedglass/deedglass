#!/usr/bin/env bash
# npx-registry.sh start|stop: serves the packed npm wrapper (npm/deedglass) and this machine's
# platform package from a throwaway registry on 127.0.0.1, so docs snippets can run
# `npx -y deedglass …` exactly as a newcomer will, before anything is published (S32's
# scripts/npx_cold.sh does the same for its timing). `start` prints the port.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE="$ROOT/docs-site/.cache/npx-registry"
case "${1:-}" in
  start)
    rm -rf "$STATE" && mkdir -p "$STATE/reg"
    os="$(node -p process.platform)"; arch="$(node -p process.arch)"
    plat="cli-$os-$arch"
    [ -d "$ROOT/npm/$plat" ] || { echo "✕ No platform package for $os/$arch in npm/." >&2; exit 1; }
    goarch="$arch"; [ "$arch" = x64 ] && goarch=amd64
    (cd "$ROOT" && CGO_ENABLED=0 GOOS="$os" GOARCH="$goarch" go build -trimpath -ldflags "-s -w" -o "npm/$plat/deedglass" ./cmd/deedglass)
    for p in deedglass "$plat"; do (cd "$ROOT/npm/$p" && npm pack --silent --pack-destination "$STATE/reg" >/dev/null); done
    rm -f "$ROOT/npm/$plat/deedglass"
    for t in "$STATE"/reg/*.tgz; do tar -xOzf "$t" package/package.json > "$t.json"; done
    nohup node "$ROOT/scripts/npx_registry.mjs" "$STATE/reg" 0 > "$STATE/port" 2>"$STATE/log" &
    echo $! > "$STATE/pid"
    for _ in $(seq 1 50); do [ -s "$STATE/port" ] && break; sleep 0.1; done
    head -1 "$STATE/port"
    ;;
  stop)
    [ -f "$STATE/pid" ] && kill "$(cat "$STATE/pid")" 2>/dev/null || true
    rm -rf "$STATE"
    ;;
  *) echo "usage: npx-registry.sh start|stop" >&2; exit 2 ;;
esac
