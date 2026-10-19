#!/usr/bin/env bash
# S05 §3.9 budget for deedglass verify: 100,000 deeds as .jsonl (< 5 s, peak RSS < 256 MiB), the
# same as an export bundle (< 6 s), and one deed from a cold start (< 60 ms). Three runs each; the
# median wall time and the largest RSS are reported. Raw numbers go to
# bench/results/<date>-<os>-<arch>-verify.json (CLAUDE.md rule 8).
#
#   bench/verify_100k.sh            # generates .tmp/deeds-100k.{jsonl,tar.gz} on first use
set -euo pipefail
cd "$(dirname "$0")/.."

GO=${GO:-go}
KEYS=testdata/deeds/keys/test-keys.json
mkdir -p .tmp bench/results
if [ ! -s .tmp/deeds-100k.jsonl ] || [ ! -s .tmp/deeds-100k.tar.gz ]; then
  $GO run ./internal/deed/deedtest/cmd/genchain -n 100000 -keys 2 -out .tmp/deeds-100k.jsonl -bundle .tmp/deeds-100k.tar.gz
fi
# TY-02 (ADR-109): the same chain dual-sealed (Ed25519 + ML-DSA-65) from seq 21, deed v2.
if [ ! -s .tmp/deeds-100k-dual.jsonl ]; then
  $GO run ./internal/deed/deedtest/cmd/genchain -n 100000 -dual -out .tmp/deeds-100k-dual.jsonl
fi
make -s build >/dev/null

# run <label> <args…>: prints "wall_s rss_bytes user_s sys_s exit" for one run (macOS /usr/bin/time -l).
run() {
  local out
  out="$( { /usr/bin/time -l ./bin/deedglass verify "$@" -q; echo "exit=$?" >&2; } 2>&1 >/dev/null || true)"
  local real user sys rss code
  real="$(printf '%s\n' "$out" | awk '/ real /{print $1}')"
  user="$(printf '%s\n' "$out" | awk '/ real /{print $3}')"
  sys="$(printf '%s\n' "$out" | awk '/ real /{print $5}')"
  rss="$(printf '%s\n' "$out" | awk '/maximum resident set size/{print $1}')"
  code="$(printf '%s\n' "$out" | sed -n 's/^exit=//p')"
  echo "$real $rss $user $sys $code"
}

measure() { # measure <name> <args…> → a JSON object
  local name="$1"; shift
  local walls=() rsss=() users=() syss=() codes=()
  for _ in 1 2 3; do
    read -r w r u s c < <(run "$@")
    walls+=("$w"); rsss+=("$r"); users+=("$u"); syss+=("$s"); codes+=("$c")
  done
  local median maxrss
  median="$(printf '%s\n' "${walls[@]}" | sort -n | sed -n 2p)"
  maxrss="$(printf '%s\n' "${rsss[@]}" | sort -n | tail -1)"
  printf '{"name":"%s","args":"%s","runs_wall_s":[%s],"median_wall_s":%s,"peak_rss_bytes":%s,"user_s":[%s],"sys_s":[%s],"exit":[%s]}' \
    "$name" "$*" "$(IFS=,; echo "${walls[*]}")" "$median" "$maxrss" "$(IFS=,; echo "${users[*]}")" "$(IFS=,; echo "${syss[*]}")" "$(IFS=,; echo "${codes[*]}")"
}

jsonl="$(measure verify_100k_jsonl .tmp/deeds-100k.jsonl --keys "$KEYS")"
bundle="$(measure verify_100k_bundle .tmp/deeds-100k.tar.gz --keys "$KEYS")"
dual="$(measure verify_100k_dual_jsonl .tmp/deeds-100k-dual.jsonl --keys testdata/deeds/v2/keys.json)"
single="$(measure verify_one_deed_cold testdata/deeds/valid/01-action-verified-l2.json --keys "$KEYS")"

out="bench/results/$(date +%F)-$($GO env GOOS)-$($GO env GOARCH)-verify.json"
cat > "$out" <<JSON
{
  "object": "benchmark",
  "suite": "S05 deedglass verify",
  "date": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "commit": "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)$( [ -n "$(git status --porcelain 2>/dev/null)" ] && echo "-modified")",
  "go": "$($GO env GOVERSION)",
  "machine": "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || uname -m), $(sysctl -n hw.ncpu 2>/dev/null || echo ?) cores, $(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1073741824 )) GiB",
  "os": "$(uname -sr)",
  "load_average_before": "$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | xargs)",
  "input": {"deeds": 100000, "jsonl_bytes": $(wc -c < .tmp/deeds-100k.jsonl | tr -d ' '), "bundle_bytes": $(wc -c < .tmp/deeds-100k.tar.gz | tr -d ' '), "seal_keys": 2},
  "budget": {"verify_100k_jsonl_wall_s": 5, "verify_100k_bundle_wall_s": 6, "verify_100k_dual_jsonl_wall_s": 5, "peak_rss_bytes": 268435456, "verify_one_deed_cold_wall_s": 0.06},
  "results": [
    $jsonl,
    $bundle,
    $dual,
    $single
  ]
}
JSON
echo "raw: $out"
cat "$out"
