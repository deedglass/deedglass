#!/usr/bin/env bash
# scripts/release-notes.sh <version> (S35): the GitHub release body, from CHANGELOG.md's section for
# <version>, the limits in docs/LIMITATIONS.md and the install and check commands. Calm and exact;
# no exclamation marks. GoReleaser takes it with --release-notes.
set -euo pipefail
cd "$(dirname "$0")/.."
v="${1:?usage: scripts/release-notes.sh <version>}"
v="${v#v}"
section="$(awk -v v="$v" '
  $0 ~ "^## \\[" v "\\]" {on=1; next}
  on && /^## \[/ {exit}
  on && /^### What it doesn/ {skip=1}
  on && /^### / && !/^### What it doesn/ {skip=0}
  on && !skip {print}' CHANGELOG.md | sed '/^\[.*\]: /d')"
[ -n "$(printf '%s' "$section" | tr -d '[:space:]')" ] || { echo "✕ CHANGELOG.md has no ## [$v] section." >&2; exit 1; }
limits="$(sed -n '/^## Known limits in v1/,/^## /p' docs/LIMITATIONS.md | sed '1d;$d' | head -12)"
cat <<NOTES
# Deedglass $v

## What's new
$(printf '%s\n' "$section" | sed 's/^### Added$//' | sed '/^$/N;/^\n$/D')

## What it doesn't do yet
$limits

The full list: https://github.com/deedglass/deedglass/blob/v$v/docs/LIMITATIONS.md

## Install
- npx: \`npx -y deedglass@$v version\`
- The install script (macOS, Linux): \`curl -fsSL https://github.com/deedglass/deedglass/releases/download/v$v/install.sh | sh -s -- --version $v\`
- A binary: download the archive for your platform below, check it, then put \`deedglass\` on your PATH.
- Docker: there's no public image yet (one is coming); build it from the tag with \`make web-build docker\`.
- Homebrew (once the tap exists): \`brew install deedglass/tap/deedglass\`

## Check this release
Each archive is listed in \`checksums.txt\` with its sha256:

\`\`\`sh
shasum -a 256 --ignore-missing -c checksums.txt
\`\`\`

\`checksums.txt\` is also signed by this repository's release workflow in the open Sigstore log:

\`\`\`sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \\
  --certificate-identity-regexp '^https://github.com/deedglass/deedglass/\.github/workflows/release\.yml@refs/tags/v' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
\`\`\`

Each archive has an SPDX SBOM next to it (\`*.sbom.json\`). See https://docs.deedglass.com/install for the details.
NOTES
