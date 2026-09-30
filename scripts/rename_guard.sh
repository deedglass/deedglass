#!/usr/bin/env bash
# Rename guard (ADR-012, CLAUDE.md rule 5). Fails on any retired identifier left in the repo:
#   testamur, deltamark, and "txv" as a token (not preceded by a letter or digit, and followed by
#   _ . / -, a word boundary or the end of the line), all case-insensitive.
# Scans tracked and untracked-but-not-ignored text files. Commit messages are history and are not
# scanned. Allowed without an entry: the on-disk workspace paths ~/txv/, $HOME/txv/ and
# /Users/<name>/txv/ (the repo path stays; contract §1). Anything else needs a line in .rename-allow:
#   <path>::<token, lowercase>::<reason>
#
# Usage: scripts/rename_guard.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
ALLOW=".rename-allow"
case "${1:-}" in
  -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
  "") ;;
  *) echo "rename-guard: unknown argument $1" >&2; exit 2 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# 1. Allowlist keys (path::token), each entry needs a reason.
: > "$tmp/allow"
bad_allow=0
if [ -f "$ALLOW" ]; then
  while IFS= read -r line; do
    case "$line" in ''|'#'*) continue ;; esac
    path="${line%%::*}"; rest="${line#*::}"; token="${rest%%::*}"; reason="${rest#*::}"
    if [ "$path" = "$line" ] || [ "$token" = "$rest" ] || [ -z "$(printf '%s' "$reason" | tr -d '[:space:]')" ]; then
      echo "rename-guard: malformed allow entry (need path::token::reason): $line" >&2
      bad_allow=1
      continue
    fi
    printf '%s::%s\n' "$path" "$(printf '%s' "$token" | tr '[:upper:]' '[:lower:]')" >> "$tmp/allow"
  done < "$ALLOW"
fi

# 2. Files: everything git would track, minus this script and its allowlist.
git ls-files -co --exclude-standard \
  | grep -v -x -E 'scripts/rename_guard\.sh|\.rename-allow' \
  | while IFS= read -r f; do [ -f "$f" ] && printf '%s\n' "$f"; done > "$tmp/files"
nfiles="$(wc -l < "$tmp/files" | tr -d ' ')"

# 3. Matcher (perl, identical on macOS and Linux). Workspace paths are blanked before matching.
# Output: path:line:token (token lowercased).
perl -e '
  my $re = qr/(testamur|deltamark|(?<![A-Za-z0-9])txv(?=[_.\/-]|\b|$))/i;
  my $path = qr{(?:~|\$HOME|/Users/[^/\s]+)/txv/};
  open(my $lf, "<", $ARGV[0]) or die "list: $!";
  while (my $f = <$lf>) {
    chomp $f; next if -B $f; open(my $fh, "<", $f) or next; my $n = 0;
    while (my $line = <$fh>) {
      $n++; $line =~ s/$path/ /g;
      while ($line =~ /$re/g) { print "$f:$n:" . lc($1) . "\n" }
    }
    close $fh;
  }
' "$tmp/files" > "$tmp/hits"

fail=0
allowed=0
: > "$tmp/used"
while IFS= read -r hit; do
  path="${hit%%:*}"; rest="${hit#*:}"; lineno="${rest%%:*}"; token="${rest#*:}"
  key="$path::$token"
  if grep -q -x -F -e "$key" "$tmp/allow"; then
    allowed=$((allowed + 1))
    printf '%s\n' "$key" >> "$tmp/used"
  else
    printf '%s:%s: retired identifier "%s". Rename it to deedglass (docs/NAMING.md).\n' "$path" "$lineno" "$token"
    fail=1
  fi
done < "$tmp/hits"

# Stale allow entries are reported, not fatal.
while IFS= read -r key; do
  grep -q -x -F -e "$key" "$tmp/used" || echo "rename-guard: note: unused allow entry $key" >&2
done < "$tmp/allow"

if [ "$fail" -ne 0 ] || [ "$bad_allow" -ne 0 ]; then
  echo "rename-guard: FAIL (rename the identifier, or add an entry with a reason to $ALLOW)" >&2
  exit 1
fi
echo "rename-guard: OK ($nfiles files, $allowed allowed hits)"
