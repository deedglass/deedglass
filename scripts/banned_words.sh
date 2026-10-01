#!/usr/bin/env bash
# Banned-words check (DEEDGLASS_BRAND.md §3, CLAUDE.md rule 7). Scans tracked and untracked-but-not-ignored text files
# (docs, code strings, comments, SQL, JSON, workflows) and, with --commits, commit messages.
# Phrases come from scripts/banned_words.txt. A hit fails unless .banned-words-allow lists it as
#   <path>::<phrase, lowercase>::<reason>
# (exact quoted prior-art titles and explanatory negations only, each with a reason).
# Commit messages have no allowlist.
#
# Usage: scripts/banned_words.sh [--commits <git rev-range, e.g. HEAD or origin/main..HEAD>]
# Env:   DEEDGLASS_REPO_PUBLIC=1 drops the @until-public phrases (after the repo is made public).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
LIST="scripts/banned_words.txt"
ALLOW=".banned-words-allow"
COMMITS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --commits) COMMITS="${2:?--commits needs a rev range}"; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "banned-words: unknown argument $1" >&2; exit 2 ;;
  esac
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# 1. Phrases.
grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$LIST" | while IFS= read -r line; do
  case "$line" in
    "@until-public "*) [ "${DEEDGLASS_REPO_PUBLIC:-0}" = "1" ] || printf '%s\n' "${line#@until-public }" ;;
    *) printf '%s\n' "$line" ;;
  esac
done > "$tmp/patterns"

# 2. Allowlist keys (path::phrase), each entry needs a reason.
: > "$tmp/allow"
bad_allow=0
if [ -f "$ALLOW" ]; then
  while IFS= read -r line; do
    case "$line" in ''|'#'*) continue ;; esac
    path="${line%%::*}"; rest="${line#*::}"; phrase="${rest%%::*}"; reason="${rest#*::}"
    if [ "$path" = "$line" ] || [ "$phrase" = "$rest" ] || [ -z "$(printf '%s' "$reason" | tr -d '[:space:]')" ]; then
      echo "banned-words: malformed allow entry (need path::phrase::reason): $line" >&2
      bad_allow=1
      continue
    fi
    printf '%s::%s\n' "$path" "$(printf '%s' "$phrase" | tr '[:upper:]' '[:lower:]')" >> "$tmp/allow"
  done < "$ALLOW"
fi

# 3. Files: everything git would track, minus third-party text and the list itself (and its
#    byte-for-byte copy embedded by the policy compiler, internal/policy/bannedwords.txt).
git ls-files -co --exclude-standard \
  | grep -v -E '^(LICENSE|go\.sum|scripts/banned_words\.txt|internal/policy/bannedwords\.txt|\.banned-words-allow|testdata/(jcs|rfc|wycheproof)/|testdata/SHA256SUMS)' \
  | while IFS= read -r f; do [ -f "$f" ] && printf '%s\n' "$f"; done > "$tmp/files"
nfiles="$(wc -l < "$tmp/files" | tr -d ' ')"

# Matcher (perl, identical on macOS and Linux; grep -w differs between BSD and GNU): a phrase matches
# case-insensitively when not preceded or followed by [A-Za-z0-9_]; spaces in a phrase match any run
# of whitespace; the longest phrase wins at a position. Binary files are skipped.
# Input: a list of files (one per line) or, with label "-", text on stdin. Output: path:line:match.
scan() {
  perl -e '
    my ($patfile, $list) = @ARGV;
    open(my $pf, "<", $patfile) or die "patterns: $!";
    my @p = grep { /\S/ } map { chomp; $_ } <$pf>;
    my $alt = join "|", map { (my $q = quotemeta $_) =~ s/\\ /\\s+/g; $q } sort { length($b) <=> length($a) } @p;
    my $re = qr/(?<![A-Za-z0-9_])($alt)(?![A-Za-z0-9_])/i;
    sub scanfh { my ($fh, $name) = @_; my $n = 0;
      while (my $line = <$fh>) { $n++; while ($line =~ /$re/g) { (my $m = $1) =~ s/\s+/ /g; print "$name:$n:$m\n" } } }
    if ($list eq "-") { scanfh(*STDIN, "-"); exit 0 }
    open(my $lf, "<", $list) or die "list: $!";
    while (my $f = <$lf>) { chomp $f; next if -B $f; open(my $fh, "<", $f) or next; scanfh($fh, $f); close $fh }
  ' "$tmp/patterns" "$1"
}
scan "$tmp/files" > "$tmp/hits"

fail=0
allowed=0
: > "$tmp/used"
while IFS= read -r hit; do
  path="${hit%%:*}"; rest="${hit#*:}"; lineno="${rest%%:*}"; match="${rest#*:}"
  key="$path::$(printf '%s' "$match" | tr '[:upper:]' '[:lower:]')"
  if grep -q -x -F -e "$key" "$tmp/allow"; then
    allowed=$((allowed + 1))
    printf '%s\n' "$key" >> "$tmp/used"
  else
    printf '%s:%s: banned phrase "%s": %s\n' "$path" "$lineno" "$match" "$(sed -n "${lineno}p" "$path" | cut -c1-160)"
    fail=1
  fi
done < "$tmp/hits"

# Stale allow entries are reported, not fatal.
while IFS= read -r key; do
  grep -q -x -F -e "$key" "$tmp/used" || echo "banned-words: note: unused allow entry $key" >&2
done < "$tmp/allow"

# 4. Commit messages (no allowlist).
ncommits=0
if [ -n "$COMMITS" ]; then
  for c in $(git rev-list "$COMMITS"); do
    ncommits=$((ncommits + 1))
    git log -1 --format=%B "$c" | scan - | while IFS= read -r m; do
      printf 'commit %s: banned phrase "%s"\n' "$(git rev-parse --short "$c")" "${m#-:*:}"
    done > "$tmp/c_hits"
    if [ -s "$tmp/c_hits" ]; then cat "$tmp/c_hits"; fail=1; fi
  done
fi

if [ "$fail" -ne 0 ] || [ "$bad_allow" -ne 0 ]; then
  echo "banned-words: FAIL (see CLAUDE.md rule 7; reword, or add an explanatory entry to $ALLOW with a reason)" >&2
  exit 1
fi
echo "banned-words: OK ($nfiles files, $ncommits commit messages, $allowed allowed hits)"
