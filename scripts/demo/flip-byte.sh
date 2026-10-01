#!/usr/bin/env bash
# flip-byte.sh <deeds.jsonl> <seq>: change exactly one digit of latency_ms in deed <seq> (or, when
# that deed never ran a tool, in the next action deed that did), in place. It prints what changed:
#
#   deed 812: "latency_ms":41 → "latency_ms":47
#
# This is the tamper in tonight's clip (S06): the deed's seal no longer matches its body, and
# `deedglass verify` names it. Only ever run it on an export, never on a database.
set -euo pipefail
[ $# -eq 2 ] || { echo "usage: $0 <deeds.jsonl> <seq>" >&2; exit 2; }
file="$1" seq="$2"
[ -f "$file" ] || { echo "✕ No file $file." >&2; exit 1; }
case "$seq" in ''|*[!0-9]*) echo "✕ seq must be a number." >&2; exit 2 ;; esac
tmp="$(mktemp "${file}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
awk -v want="$seq" '
  done != 1 && match($0, /"seq":[0-9]+,/) {
    s = substr($0, RSTART + 6, RLENGTH - 7) + 0
    if (s >= want && match($0, /"latency_ms":[0-9]+/)) {
      old = substr($0, RSTART, RLENGTH)
      last = substr(old, length(old), 1) + 0
      new = substr(old, 1, length(old) - 1) ((last + 6) % 10)
      $0 = substr($0, 1, RSTART - 1) new substr($0, RSTART + RLENGTH)
      printf "deed %d: %s → %s\n", s, old, new > "/dev/stderr"
      done = 1
    }
  }
  { print }
  END { if (done != 1) { print "✕ No deed from seq " want " on has a latency_ms to change." > "/dev/stderr"; exit 1 } }
' "$file" > "$tmp"
mv "$tmp" "$file"
trap - EXIT
