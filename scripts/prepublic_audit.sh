#!/usr/bin/env bash
# prepublic_audit.sh (S37 §3.2): read-only checks before the repo goes public. It reads every commit
# (git log -p --all), every blob size and the working tree, and reports, by category:
#
#   secret     key and token shapes (dg_live_, dg_test_…, sk_live_, rk_live_, xox?-, ghp_, github_pat_,
#              AKIA, -----BEGIN, sk-ant-, re_…)                                        → blocker
#   env        a .env file ever committed                                               → blocker
#   big        a blob over 5 MB in history                                              → blocker
#   path       absolute local paths (/Users/, ~/txv/ops)                                → triage
#   plan       internal plan references (prompts-v7, 85_ALLOY…, the internal codename)  → triage
#   email      addresses other than @deedglass.com, noreply and reserved test domains,
#              in content or as a commit author or committer                            → triage
#   words      banned words in commit messages (scripts/banned_words.sh --commits)      → blocker
#
# It never prints a secret: a secret finding names the commit, the file and the shape only.
# Exit 0 when there is no blocker (triage findings are listed for the founder), 1 otherwise.
# If a live secret is found: stop, tell the founder; rotating it and rewriting history are his calls.
#
#   scripts/prepublic_audit.sh [--repo <dir>] [--max 5]      (--max: examples printed per category)
set -uo pipefail
export LC_ALL=C
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAX=5
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="$(cd "$2" && pwd)"; shift 2 ;;
    --max) MAX="$2"; shift 2 ;;
    *) echo "prepublic_audit: unknown argument $1" >&2; exit 2 ;;
  esac
done
cd "$REPO"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

SECRET='dg_live_[A-Za-z0-9_]{8,}|dg_test_[a-z0-9]{8}_[A-Za-z0-9]{32}|sk_live_[A-Za-z0-9]{8,}|rk_live_[A-Za-z0-9]{8,}|xox[abp]-[A-Za-z0-9-]{8,}|ghp_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|sk-ant-[A-Za-z0-9_-]{20,}|re_[A-Za-z0-9]{20,}'
PATHS='/Users/[A-Za-z]|~/txv/ops'
PLAN='prompts-v7|85_ALLOY|\balloy\b'
EMAIL='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
EMAIL_OK='@deedglass\.com$|noreply|no-reply|@example\.(com|org|net|test)$|@[a-z0-9-]+\.test$|@[a-z0-9.-]+\.example$|@localhost$|@users\.noreply\.github\.com$|@anthropic\.com$'

# 1. History: one pass over every patch, remembering the commit and file of each added line.
git log -p --all --no-color --format='commit %H' 2>/dev/null | awk -v S="$SECRET" -v P="$PATHS" -v L="$PLAN" '
  /^commit [0-9a-f]{40}$/ { c = substr($2, 1, 10); next }
  /^\+\+\+ b\// { f = substr($0, 7); next }
  /^\+/ {
    line = substr($0, 2)
    if (match(line, S)) print "secretraw\t" c "\t" f "\t" substr(line, RSTART, RLENGTH)
    if (match(line, P)) print "path\t" c "\t" f "\t" substr(line, RSTART, 24)
    if (tolower(line) ~ L) { match(tolower(line), L); print "plan\t" c "\t" f "\t" substr(line, RSTART, 24) }
  }' > "$tmp/hist"
# Emails in added lines (filtered after, so the allow list stays one regex).
git log -p --all --no-color --format='commit %H' 2>/dev/null | awk '
  /^commit [0-9a-f]{40}$/ { c = substr($2, 1, 10); next } /^\+\+\+ b\// { f = substr($0, 7); next }
  /^\+/ { print c "\t" f "\t" substr($0, 2) }' |
  perl -ne 'chomp; my ($c,$f,$l)=split /\t/,$_,3; while ($l =~ /([A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,})/g) { print "email\t$c\t$f\t$1\n" }' |
  grep -v -E "$EMAIL_OK" >> "$tmp/hist" || true
# Authors and committers.
git log --all --format='%h %ae%n%h %ce' 2>/dev/null | sort -u -k2,2 | while read -r h e; do
  printf '%s\n' "$e" | grep -q -E "$EMAIL_OK" || printf 'email\t%s\t(commit metadata)\t%s\n' "$h" "$e"
done >> "$tmp/hist"
# .env files ever added.
git log --all --diff-filter=A --name-only --format='commit %h' 2>/dev/null | awk '/^commit /{c=$2; next} /(^|\/)\.env(\.[a-z]+)?$/ && $0 !~ /\.env\.example$/ { print "env\t" c "\t" $0 "\tcommitted" }' >> "$tmp/hist"
# Blobs over 5 MB anywhere in history.
git rev-list --objects --all 2>/dev/null | git cat-file --batch-check='%(objecttype) %(objectname) %(objectsize) %(rest)' 2>/dev/null |
  awk '$1 == "blob" && $3 > 5242880 { printf "big\t%s\t%s\t%.1f MB\n", substr($2,1,10), $4, $3/1048576 }' >> "$tmp/hist"

# 2. The working tree (tracked and untracked-but-not-ignored files), for what history doesn't hold yet.
git ls-files -co --exclude-standard 2>/dev/null | while IFS= read -r f; do [ -f "$f" ] && printf '%s\n' "$f"; done > "$tmp/files"
while IFS= read -r f; do
  [ "$f" = "scripts/prepublic_audit.sh" ] && continue
  grep -I -o -E "$SECRET" "$f" 2>/dev/null | while IFS= read -r v; do printf 'secretraw\tworking tree\t%s\t%s\n' "$f" "$v"; done
done < "$tmp/files" >> "$tmp/hist"

# 3. Banned words in commit messages.
words=0
if [ -x scripts/banned_words.sh ] && ! scripts/banned_words.sh --commits "$(git rev-list --max-parents=0 HEAD | tail -1)..HEAD" > "$tmp/words" 2>&1; then
  grep '^commit ' "$tmp/words" | while IFS= read -r l; do printf 'words\t%s\tcommit message\t%s\n' "$(echo "$l" | cut -d' ' -f2 | tr -d :)" "$(echo "$l" | sed 's/.*banned phrase //')"; done >> "$tmp/hist"
fi

# Secret triage: scripts/prepublic_audit.allow names documented examples, sentinels and published
# test vectors. A value never leaves this script: a finding prints its first 8 characters only.
ALLOWF="$REPO/scripts/prepublic_audit.allow"
[ -f "$ALLOWF" ] || ALLOWF=/dev/null
perl -e '
  my $af = shift; my (@v, @p);
  open(my $a, "<", $af) or die; while (<$a>) { chomp; next if /^\s*(#|$)/; my ($k, $rx) = /^(value|path):(.*?)::/ or next; push @{ $k eq "value" ? \@v : \@p }, qr/$rx/ }
  while (my $l = <STDIN>) { chomp $l; my ($cat, $c, $f, $w) = split /\t/, $l, 4;
    if ($cat ne "secretraw") { print "$l\n"; next }
    next if grep { $w =~ $_ } @v; next if grep { $f =~ $_ } @p;
    print "secret\t$c\t$f\t" . substr($w, 0, 8) . "…\n" }
' "$ALLOWF" < "$tmp/hist" > "$tmp/hist2" && mv "$tmp/hist2" "$tmp/hist"
# The scanner's own patterns and the audit's documentation never count.
grep -v -E $'\t(scripts/prepublic_audit\\.sh|docs/PUBLIC_REPO_SWITCH\\.md)\t' "$tmp/hist" | sort -u > "$tmp/all"

blockers=0
for cat in secret env big words path plan email; do
  n="$(awk -F'\t' -v c="$cat" '$1 == c' "$tmp/all" | wc -l | tr -d ' ')"
  [ "$n" -eq 0 ] && { printf '✓ %-6s 0\n' "$cat"; continue; }
  case "$cat" in secret|env|big|words) sym='✕'; blockers=$((blockers + n)) ;; *) sym='○' ;; esac
  files="$(awk -F'\t' -v c="$cat" '$1 == c { print $3 }' "$tmp/all" | sort -u | wc -l | tr -d ' ')"
  printf '%s %-6s %s finding(s) in %s file(s)\n' "$sym" "$cat" "$n" "$files"
  awk -F'\t' -v c="$cat" '$1 == c' "$tmp/all" | awk -F'\t' '!seen[$3]++' | head -n "$MAX" | while IFS=$'\t' read -r _ c f what; do
    printf '    %s · %s · %s\n' "$c" "$f" "$what"
  done
done
if [ "$blockers" -gt 0 ]; then
  echo "✕ $blockers blocker finding(s). Stop: tell the founder. A live secret must be rotated before the switch; rewriting history is his call."
  exit 1
fi
echo "✓ No blockers. Triage the ○ findings in docs/PUBLIC_REPO_SWITCH.md before the switch."
