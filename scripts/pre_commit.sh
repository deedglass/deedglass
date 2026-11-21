#!/usr/bin/env bash
# Pre-commit hook for Deedglass product repository
# Enforces:
# 1. Zero unformatted Go files (gofmt -l)
# 2. Zero banned words (scripts/banned_words.sh)
# 3. Clean compiler vet (go vet ./...)
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

echo "== [PRE-COMMIT] Running Deedglass commit verification gates..."

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
