#!/usr/bin/env bash
# The fuzz schedule (S34 §3.6). Every parser that reads untrusted bytes, timed, a few at a time.
#
#   scripts/fuzz_all.sh                 the full schedule (make fuzz-all): S34's times, 3 targets at a time
#   scripts/fuzz_all.sh regress         every target for 10 s (make fuzz-regress, CI): the committed
#                                       corpus under testdata/fuzz/ replays, then a short burst
#   FUZZ_SCALE=0.1 scripts/fuzz_all.sh  the same schedule at a tenth of the time
#   FUZZ_PARALLEL=3 FUZZ_JOBS=3         workers per target, targets at a time (defaults 2 and 3)
#
# Logs go to .tmp/fuzz/<target>.log. The full schedule writes bench/results/<date>-<os>-<arch>-fuzz.json.
# A crasher is written by go test under <pkg>/testdata/fuzz/<Target>/; minimize it, fix the code,
# and commit the file so every later `go test` replays it.
set -euo pipefail
cd "$(dirname "$0")/.."

mode="${1:-all}"
GO="${GO:-go}"
jobs="${FUZZ_JOBS:-3}"
par="${FUZZ_PARALLEL:-2}"
scale="${FUZZ_SCALE:-1}"
out=".tmp/fuzz"
mkdir -p "$out"

# target package minutes (S34 §3.6, longest first so the batches stay full)
schedule='FuzzReader ./internal/mcp/jsonrpc 30
FuzzFrontEnvelope ./internal/mcp/front 30
FuzzCompile ./internal/policy 30
FuzzYAMLReader ./internal/policy 30
FuzzParseEnvelope ./internal/deed 30
FuzzVerifyEnvelope ./internal/deed 30
FuzzIntentHeader ./internal/identity 30
FuzzTokenize ./internal/sqlguard 30
FuzzAnalyze ./internal/sqlguard 30
FuzzSafeParse ./internal/decoder 15
FuzzArchiveOpen ./internal/deed/archive 10
FuzzVerifyStream ./internal/deed 10
FuzzCheckURL ./internal/connector/egress 10
FuzzRedactor ./internal/vault 10
FuzzTemplatePath ./internal/connector/rest 10
FuzzSlackVerify ./internal/notify/slack 10
FuzzWebhookSignature ./pkg/deedglass/webhook 10
FuzzApprovalToken ./internal/approve 10
FuzzParseAPIKey ./internal/identity 5
FuzzNormalize ./internal/gate 5
FuzzHeaderDecode ./internal/mcp/httpgw 5'

# Every other fuzz target in the repo replays its corpus in regress mode too.
extra='FuzzEmailNormalize ./internal/identity
FuzzHashToken ./internal/identity
FuzzParseKeySet ./internal/deed
FuzzVerifyCOSE ./internal/deed
FuzzCanonical ./internal/deed
FuzzParse ./internal/ids
FuzzParse ./internal/intent
FuzzArgsDigest ./internal/intent
FuzzShape ./internal/sqlguard
FuzzEvaluate ./internal/policy
FuzzApprovalLinkBody ./internal/approve
FuzzSlackEscape ./internal/notify/slack
FuzzSlackVerify ./internal/connector/slack
FuzzSignatureHeader ./pkg/deedglass/webhook
FuzzGitHubPath ./internal/connector/github
FuzzMIMEHeaders ./internal/connector/gmail
FuzzStripeForm ./internal/connector/stripe
FuzzLivemodeScan ./internal/connector/stripe
FuzzTypeName ./internal/witness/undo
FuzzNetEffect ./internal/witness/undo'

run_one() { # target pkg duration log
	local target="$1" pkg="$2" dur="$3" log="$4"
	if "$GO" test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$dur" -parallel "$par" >"$log" 2>&1; then
		echo "ok   $target ($pkg, $dur)"
	else
		echo "FAIL $target ($pkg, $dur): see $log"
		return 1
	fi
}

if [ "$mode" = "regress" ]; then
	fail=0
	while read -r target pkg _; do
		[ -n "$target" ] || continue
		run_one "$target" "$pkg" 10s "$out/regress-${target}-$(basename "$pkg").log" || fail=1
	done <<EOF
$schedule
$extra
EOF
	exit $fail
fi

[ "$mode" = "all" ] || { echo "usage: scripts/fuzz_all.sh [all|regress]" >&2; exit 2; }

# Batches of $jobs, each target with its own log; a failure doesn't stop the schedule.
started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
pids=() names=() fails=0
flush() {
	local i
	for i in "${!pids[@]}"; do
		wait "${pids[$i]}" || fails=$((fails + 1))
	done
	pids=() names=()
}
while read -r target pkg mins; do
	[ -n "$target" ] || continue
	secs=$(awk -v m="$mins" -v s="$scale" 'BEGIN { v = int(m * 60 * s); if (v < 5) v = 5; print v }')
	run_one "$target" "$pkg" "${secs}s" "$out/${target}-$(basename "$pkg").log" &
	pids+=($!) names+=("$target")
	if [ "${#pids[@]}" -ge "$jobs" ]; then flush; fi
done <<EOF
$schedule
EOF
flush

# The record: one row per target, read from its log's last progress line.
date_s=$(date +%Y-%m-%d)
dest="bench/results/${date_s}-$("$GO" env GOOS)-$("$GO" env GOARCH)-fuzz.json"
{
	printf '{\n  "object": "fuzz_run",\n  "started_at": "%s",\n  "finished_at": "%s",\n' "$started" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	printf '  "go": "%s",\n  "cpu": "%s",\n  "parallel": %s,\n  "at_a_time": %s,\n  "scale": %s,\n  "targets": [\n' \
		"$("$GO" env GOVERSION)" "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || uname -m)" "$par" "$jobs" "$scale"
	first=1
	while read -r target pkg mins; do
		[ -n "$target" ] || continue
		log="$out/${target}-$(basename "$pkg").log"
		last=$(grep -E 'execs: [0-9]+' "$log" | tail -1 || true)
		execs=$(printf '%s' "$last" | sed -nE 's/.*execs: ([0-9]+).*/\1/p')
		newi=$(printf '%s' "$last" | sed -nE 's/.*new interesting: ([0-9]+).*/\1/p')
		crash=0
		if grep -q -E '^(--- FAIL|FAIL)' "$log"; then crash=1; fi
		secs=$(awk -v m="$mins" -v s="$scale" 'BEGIN { v = int(m * 60 * s); if (v < 5) v = 5; print v }')
		[ $first = 1 ] || printf ',\n'
		first=0
		printf '    {"target": "%s", "pkg": "%s", "fuzztime": "%ss", "execs": %s, "new_interesting": %s, "crashers": %s}' \
			"$target" "${pkg#./}" "$secs" "${execs:-0}" "${newi:-0}" "$crash"
	done <<EOF
$schedule
EOF
	printf '\n  ]\n}\n'
} >"$dest"
echo "wrote $dest ($fails failing)"
[ "$fails" = 0 ]
