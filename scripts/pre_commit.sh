#!/usr/bin/env bash
# Pre-commit hook for Deedglass product repository
# Enforces:
# 1. Zero unformatted Go files (gofmt -l)
# 2. Zero banned words (scripts/banned_words.sh)
# 3. Clean compiler vet (go vet ./...)
set -euo pipefail

TARGET="${BASH_SOURCE[0]}"
while [ -L "$TARGET" ]; do
  DIR="$(cd -P "$(dirname "$TARGET")" >/dev/null 2>&1 && pwd)"
  TARGET="$(readlink "$TARGET")"
  [[ $TARGET != /* ]] && TARGET="$DIR/$TARGET"
done
ROOT="$(cd -P "$(dirname "$TARGET")/.." >/dev/null 2>&1 && pwd)"
cd "$ROOT"

echo "== [PRE-COMMIT] Running Deedglass commit verification gates in $ROOT..."

# 1. gofmt check
echo "-- Checking Go formatting (gofmt -l .)..."
OUT="$(gofmt -l .)"
if [ -n "$OUT" ]; then
    echo "ERROR: gofmt needed on the following files:"
    echo "$OUT"
    echo "Run 'gofmt -w .' or 'make fmt' to fix formatting before committing."
    exit 1
fi

# 2. banned words check
if [ -f "scripts/banned_words.sh" ]; then
    echo "-- Checking for banned words..."
    bash scripts/banned_words.sh
fi

# 3. go vet check
echo "-- Running go vet ./..."
go vet ./...

echo "== [PRE-COMMIT] All pre-commit checks passed cleanly."
