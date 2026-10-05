#!/usr/bin/env bash
# scripts/completions.sh (S35): writes completions/deedglass.{bash,zsh,fish} from the binary's own
# command table (deedglass completion <shell>). GoReleaser runs it before building the archives.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p completions
for sh in bash zsh fish; do
  go run ./cmd/deedglass completion "$sh" > "completions/deedglass.$sh"
done
echo "✓ completions/deedglass.{bash,zsh,fish}"
