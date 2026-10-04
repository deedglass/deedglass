#!/usr/bin/env bash
# scripts/npx_cold.sh (S32 §3.12): times `npx -y deedglass scan` cold, the way a stranger runs it:
# an empty npm cache, the packed wrapper and this machine's platform package served from a
# throwaway local registry on 127.0.0.1 (nothing is published), then `npx -y deedglass scan`.
# Also checks no package has an install script, and that --ignore-scripts works. Budget: < 15 s.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'kill "${REG_PID:-}" 2>/dev/null || true; rm -rf "$WORK"' EXIT
os="$(node -p process.platform)"; arch="$(node -p process.arch)"
plat="cli-$os-$arch"
[ -d "$ROOT/npm/$plat" ] || { echo "✕ No platform package for $os/$arch in npm/." >&2; exit 1; }
goos="$os"; [ "$os" = win32 ] && goos=windows
goarch="$arch"; [ "$arch" = x64 ] && goarch=amd64
(cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "-s -w" -o "npm/$plat/deedglass" ./cmd/deedglass)
mkdir -p "$WORK/reg"
for p in deedglass "$plat"; do
  (cd "$ROOT/npm/$p" && npm pack --silent --pack-destination "$WORK/reg" >/dev/null)
  if node -e "const s=require('$ROOT/npm/$p/package.json').scripts||{};process.exit(s.install||s.postinstall||s.preinstall?1:0)"; then :; else
    echo "✕ npm/$p has an install script" >&2; exit 1; fi
done
for t in "$WORK"/reg/*.tgz; do
  tar -xOzf "$t" package/package.json > "$t.json"
done
node "$ROOT/scripts/npx_registry.mjs" "$WORK/reg" 0 > "$WORK/port" 2>&1 &
REG_PID=$!
for _ in $(seq 1 50); do [ -s "$WORK/port" ] && break; sleep 0.1; done
port="$(head -1 "$WORK/port")"
[ -n "$port" ] || { echo "✕ the local registry didn't start" >&2; exit 1; }
export npm_config_registry="http://127.0.0.1:$port/" npm_config_cache="$WORK/cache" npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
mkdir -p "$WORK/run" && cd "$WORK/run"
t0=$(node -e 'console.log(Date.now())')
npx -y deedglass scan --dir "$ROOT/examples/demo" --ci --no-write >"$WORK/out.txt" 2>&1 || { cat "$WORK/out.txt"; exit 1; }
t1=$(node -e 'console.log(Date.now())')
ms=$((t1 - t0))
head -3 "$WORK/out.txt"
rm -rf "$WORK/cache"
npm_config_ignore_scripts=true npx -y deedglass version -q >/dev/null 2>&1 && ign=ok || ign=failed
echo "npx -y deedglass scan, cold: ${ms} ms on $os/$arch (budget 15000 ms) · --ignore-scripts: $ign"
mkdir -p "$ROOT/bench/results"
printf '{"object":"bench","name":"npx_cold","host":"%s/%s","ms":%d,"budget_ms":15000,"ignore_scripts":"%s","date":"%s"}\n' "$os" "$arch" "$ms" "$ign" "$(date -u +%FT%TZ)" > "$ROOT/bench/results/$(date +%F)-$os-$arch-npx-cold.json"
[ "$ms" -lt 15000 ] && [ "$ign" = ok ]
