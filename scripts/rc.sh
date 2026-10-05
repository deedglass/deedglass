#!/usr/bin/env bash
# rc.sh (S37 §3.1): the release-candidate regression. Every gate from S01–S37 on one tree, logs in
# .tmp/rc/<gate>.log, one summary at the end. Exit 0 only when every gate that ran is green.
#
#   scripts/rc.sh            every gate (about 90 minutes; matrix and numbers run in the background)
#   scripts/rc.sh --quick    the launch-morning subset: ci, integration, attack, demo, site, release
#
# Env: RC_ONLY="ci attack"  run only these gates      RC_SKIP="site cloud"  skip these (said in the summary)
#      RC_DRY=1             replace every command with true (the self-test)
#      RC_FAIL=<gate>       force that gate to fail (the self-test)
#      DG_SITE_DIR          the site repo (default ../site)
# Blockers (S37 §3.1, decided before the run): any red gate, a P0 attack case that differs, an open
# high or medium security finding, the quickstart over 5 minutes, a doc_url that 404s, a demo scene
# that fails, a banned word in public copy, a secret in history. Everything else is KNOWN_ISSUES.md.
set -uo pipefail
export LC_ALL="${LC_ALL:-en_US.UTF-8}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SITE="${DG_SITE_DIR:-$ROOT/../site}"
OPS="${DG_OPS_DIR:-$ROOT/../ops}"
OUT="${RC_OUT:-$ROOT/.tmp/rc}"
mkdir -p "$OUT"
cd "$ROOT"

QUICK=0
[ "${1:-}" = "--quick" ] && QUICK=1
ALL="ci integration matrix attack fuzz-audit sdks console docs site numbers release cloud quickstart demo words history"
QUICKSET="ci integration attack demo site release"
GATES="$ALL"
[ "$QUICK" = 1 ] && GATES="$QUICKSET"
[ -n "${RC_ONLY:-}" ] && GATES="$RC_ONLY"

TEAL="" RED="" AMBER="" DIM="" RESET=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then TEAL=$'\033[36m' RED=$'\033[31m' AMBER=$'\033[33m' DIM=$'\033[2m' RESET=$'\033[0m'; fi

dur() { local s=$1; if [ "$s" -lt 60 ]; then printf '%d s' "$s"; else printf '%d m %02d s' $((s / 60)) $((s % 60)); fi; }

# cmd_<gate> prints the gate's shell command. Every one is a fixed string: nothing comes from input.
cmd() {
  case "$1" in
    ci) echo "make ci" ;;
    integration) echo "make integration" ;;
    matrix) echo "make matrix" ;;
    attack) echo "make attack" ;;
    fuzz-audit) echo "make fuzz-regress && make audit" ;;
    sdks) echo "make sdk-ts && make py-ci && make py-build" ;;
    console) echo "make web-e2e" ;;
    docs) echo "make docs-check && make docs-snippets" ;;
    site) echo "cd '$SITE' && npm run build && npm run check && npm test && npm run e2e" ;;
    numbers) echo "pmset -g batt | head -1 | grep -q 'AC Power' && make bench-quick && BENCH_QUICK=1 make bench-check" ;;
    release) echo "make release-check" ;;
    cloud) echo "make cloud-smoke" ;;
    quickstart) echo "scripts/quickstart_timed.sh" ;;
    demo) echo "make demo-launch-check && make demo-smoke && node --test scripts/report/render.test.mjs" ;;
    words) echo "scripts/banned_words.sh && scripts/words_over.sh '$ROOT/docs-site/src' '$SITE/src' '$OPS/launch' '$OPS/gtm'" ;;
    history) echo "scripts/prepublic_audit.sh" ;;
    *) return 1 ;;
  esac
}

run_gate() {
  local g=$1 c log start st
  c="$(cmd "$g")" || { echo "unknown gate $g" > "$OUT/$g.log"; echo 2 > "$OUT/$g.status"; return; }
  [ "${RC_DRY:-0}" = 1 ] && c="true"
  [ "${RC_FAIL:-}" = "$g" ] && c="false"
  log="$OUT/$g.log"
  start=$(date +%s)
  ( eval "$c" ) > "$log" 2>&1
  st=$?
  echo "$st $(( $(date +%s) - start ))" > "$OUT/$g.status"
}

rm -f "$OUT"/*.status
begin=$(date +%s)
bg=""
for g in $GATES; do
  case " ${RC_SKIP:-} " in *" $g "*) echo "skip 0" > "$OUT/$g.status"; continue ;; esac
  case "$g" in
    matrix|numbers) run_gate "$g" & bg="$bg $!" ;;   # long and independent: in the background
    *) run_gate "$g" ;;
  esac
done
for p in $bg; do wait "$p"; done

ver="$(git describe --tags --always 2>/dev/null || echo unknown)"
printf 'Deedglass %s · release candidate · %s\n' "$ver" "$(TZ=Asia/Kolkata date '+%Y-%m-%d %H:%M IST')"
fails=0 skipped=0
for g in $GATES; do
  read -r st secs < "$OUT/$g.status" 2>/dev/null || { st=2; secs=0; }
  label="$(printf '%-14s' "$g")"
  if [ "$st" = "skip" ]; then
    printf '%s○%s %s skipped (RC_SKIP)\n' "$AMBER" "$RESET" "$label"; skipped=$((skipped + 1))
  elif [ "$st" = 0 ]; then
    printf '%s✓%s %s %s\n' "$TEAL" "$RESET" "$label" "$(dur "$secs")"
  else
    printf "%s✕%s %s %s · log %s\n" "$RED" "$RESET" "$label" "$(dur "$secs")" "$OUT/$g.log"; fails=$((fails + 1))
  fi
done
ki=0
[ -f KNOWN_ISSUES.md ] && ki=$(grep -cE '^\| KI-[0-9]+' KNOWN_ISSUES.md)
total="$(dur $(( $(date +%s) - begin )))"
if [ "$fails" -eq 0 ]; then
  msg="Ready for the founder. 0 blockers · $ki known issues (KNOWN_ISSUES.md) · $total"
  [ "$skipped" -gt 0 ] && msg="$msg · $skipped skipped"
  printf '%s\n' "$msg"
  exit 0
fi
printf '%sNot ready.%s %d gate(s) red: each is a blocker (S37 §3.1). Open the log, fix, and run scripts/rc.sh again.\n' "$RED" "$RESET" "$fails"
exit 1
