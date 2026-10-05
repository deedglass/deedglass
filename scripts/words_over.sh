#!/usr/bin/env bash
# words_over.sh (S37): the banned-words list (scripts/banned_words.txt, the same matcher as
# scripts/banned_words.sh) over directories outside this repo: the site, the docs site's sources,
# ~/txv/ops/launch and ~/txv/ops/gtm. No allowlist. Exit 1 on any hit.
#
#   scripts/words_over.sh <dir|file>...      (a missing path is reported and skipped)
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIST="$ROOT/scripts/banned_words.txt"
[ $# -gt 0 ] || { echo "Usage: scripts/words_over.sh <dir|file>..." >&2; exit 2; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$LIST" | while IFS= read -r line; do
  case "$line" in
    "@until-public "*) [ "${DEEDGLASS_REPO_PUBLIC:-0}" = "1" ] || printf '%s\n' "${line#@until-public }" ;;
    *) printf '%s\n' "$line" ;;
  esac
done > "$tmp/patterns"
: > "$tmp/files"
for p in "$@"; do
  if [ ! -e "$p" ]; then echo "○ $p doesn't exist; skipped" >&2; continue; fi
  find "$p" -type f \( -name node_modules -prune -o -name dist -prune -o -print \) 2>/dev/null |
    grep -v -E '/(node_modules|dist|\.astro|\.wrangler|\.git)/|\.(png|jpg|jpeg|gif|webp|ico|pdf|woff2?|ttf|mp4|zip|gz|patch)$' >> "$tmp/files" || true
done
perl -e '
  my ($patfile, $list) = @ARGV;
  open(my $pf, "<", $patfile) or die; my @p = grep { /\S/ } map { chomp; $_ } <$pf>;
  my $alt = join "|", map { (my $q = quotemeta $_) =~ s/\\ /\\s+/g; $q } sort { length($b) <=> length($a) } @p;
  my $re = qr/(?<![A-Za-z0-9_])($alt)(?![A-Za-z0-9_])/i;
  open(my $lf, "<", $list) or die;
  while (my $f = <$lf>) { chomp $f; next if -B $f; open(my $fh, "<", $f) or next; my $n = 0;
    while (my $line = <$fh>) { $n++; while ($line =~ /$re/g) { (my $m = $1) =~ s/\s+/ /g; print "$f:$n:$m\n" } } close $fh }
' "$tmp/patterns" "$tmp/files" > "$tmp/hits"
n="$(wc -l < "$tmp/files" | tr -d ' ')"
if [ -s "$tmp/hits" ]; then
  while IFS= read -r h; do printf '✕ %s · banned phrase "%s"\n' "${h%:*}" "${h##*:}"; done < "$tmp/hits"
  echo "✕ $(wc -l < "$tmp/hits" | tr -d ' ') banned phrases in $n files. Reword them (DEEDGLASS_BRAND.md §3)." >&2
  exit 1
fi
echo "✓ $n files · 0 banned phrases"
