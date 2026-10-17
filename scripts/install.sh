#!/bin/sh
# install.sh: installs the deedglass binary from a GitHub release of github.com/deedglass/deedglass.
#
#   curl -fsSL https://raw.githubusercontent.com/deedglass/deedglass/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/deedglass/deedglass/main/scripts/install.sh | sh -s -- --version 0.1.0 --dir /usr/local/bin
#
# It downloads two files from the release, the archive for this machine and checksums.txt, checks the
# archive's sha256 against checksums.txt, and copies the one binary into the install directory
# (default ~/.local/bin). It needs curl or wget, tar, and sha256sum or shasum. It runs nothing it
# downloads, asks for no password and changes no other file; it never edits your shell profile.
#
# Options: --version X.Y.Z (default: the latest release), --dir DIR. The same as environment variables:
# DEEDGLASS_VERSION, DEEDGLASS_INSTALL_DIR. DEEDGLASS_RELEASES_URL replaces
# https://github.com/deedglass/deedglass/releases (for tests and mirrors).
set -eu

releases="${DEEDGLASS_RELEASES_URL:-https://github.com/deedglass/deedglass/releases}"
version="${DEEDGLASS_VERSION:-}"
dir="${DEEDGLASS_INSTALL_DIR:-${HOME:-}/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf '✕ %s\n' "$1" >&2; [ -z "${2:-}" ] || printf '  next  %s\n' "$2" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a value" "Pass --version 0.1.0, for example."; version="$2"; shift 2 ;;
    --version=*) version="${1#--version=}"; shift ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value" "Pass --dir ~/.local/bin, for example."; dir="$2"; shift 2 ;;
    --dir=*) dir="${1#--dir=}"; shift ;;
    -h|--help) say "usage: install.sh [--version X.Y.Z] [--dir DIR]   (default: the latest release, into ~/.local/bin)"; exit 0 ;;
    *) die "unknown option $1" "Run with --help to see the options." ;;
  esac
done
[ -n "$dir" ] || die "no install directory" "Set HOME, or pass --dir."
version="${version#v}"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "this script installs on macOS and Linux only" "On Windows, run npx -y deedglass, or download the .zip from $releases/latest." ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "no release build for $(uname -m)" "Build from source: go install github.com/deedglass/deedglass/cmd/deedglass@latest" ;;
esac

# HTTPS only, redirects included; plain http only for a test server on this machine.
proto='=https'
case "$releases" in http://127.0.0.1[:/]*|http://localhost[:/]*) proto='=http,https' ;; esac
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto "$proto" --proto-redir "$proto" --retry 2 -o "$2" "$1"; }
  latest() { curl -fsSLI --proto "$proto" --proto-redir "$proto" -o /dev/null -w '%{url_effective}' "$releases/latest"; }
elif command -v wget >/dev/null 2>&1; then
  case "$releases" in https://*|http://127.0.0.1[:/]*|http://localhost[:/]*) ;; *) die "DEEDGLASS_RELEASES_URL must be https" ;; esac
  fetch() { wget -q -O "$2" "$1"; }
  latest() { wget -q -S --spider "$releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
  die "neither curl nor wget is installed" "Install one of them, or use npx -y deedglass."
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  die "neither sha256sum nor shasum is installed" "Install coreutils, or use npx -y deedglass."
fi

if [ -z "$version" ]; then
  url="$(latest)" || die "couldn't reach $releases/latest" "Check your connection, or pass --version."
  version="${url##*/tag/v}"
  case "$version" in
    ''|*/*|*' '*) die "couldn't read the latest version from $releases/latest" "Pass --version 0.1.0, for example." ;;
  esac
fi
case "$version" in
  [0-9]*) ;;
  *) die "\"$version\" isn't a version" "Pass --version 0.1.0, for example." ;;
esac

archive="deedglass_${version}_${os}_${arch}.tar.gz"
base="$releases/download/v$version"
tmp="$(mktemp -d 2>/dev/null || mktemp -d -t deedglass)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Installing deedglass $version for $os/$arch into $dir"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "couldn't download $base/checksums.txt" "Check that release v$version exists: $releases"
fetch "$base/$archive" "$tmp/$archive" || die "couldn't download $base/$archive" "Check that release v$version has a build for $os/$arch: $releases"

want="$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "checksums.txt doesn't list $archive" "Nothing was installed. Report it: https://github.com/deedglass/deedglass/issues"
got="$(sha256 "$tmp/$archive")"
[ "$got" = "$want" ] || die "$archive doesn't match checksums.txt (sha256 $got, expected $want)" "Nothing was installed. Try again; if it keeps failing, report it: https://github.com/deedglass/deedglass/issues"
say "✓ sha256 matches checksums.txt"

tar -xzf "$tmp/$archive" -C "$tmp" deedglass || die "couldn't unpack deedglass from $archive" "Nothing was installed."
mkdir -p "$dir" || die "couldn't create $dir" "Pass --dir with a directory you can write to."
cp "$tmp/deedglass" "$tmp/deedglass.new" && chmod 0755 "$tmp/deedglass.new"
mv -f "$tmp/deedglass.new" "$dir/deedglass" 2>/dev/null || cp "$tmp/deedglass.new" "$dir/deedglass" || die "couldn't write $dir/deedglass" "Pass --dir with a directory you can write to."
say "✓ installed $dir/deedglass"

case ":${PATH:-}:" in
  *":$dir:"*) say "  next  deedglass version" ;;
  *) say "  $dir isn't on your PATH. Add it, for example: export PATH=\"$dir:\$PATH\""
     say "  next  $dir/deedglass version" ;;
esac
