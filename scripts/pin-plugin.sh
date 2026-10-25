#!/usr/bin/env bash
# scripts/pin-plugin.sh <X.Y.Z[-rc.N]>: pin the Claude Code plugin to one release. Anthropic's plugin
# directory refuses an unpinned npx command, so integrations/claude-code/.mcp.json runs
# `npx -y deedglass@<version> mcp serve`, and .claude-plugin/plugin.json carries the same version.
# A release step (docs/RELEASING.md); scripts/check-versions.sh and make listings-check fail when
# the two drift from the release.
set -euo pipefail
cd "$(dirname "$0")/.."
v="${1:-}"
if ! [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
  echo "✕ \"$v\" isn't a version. Use X.Y.Z or X.Y.Z-rc.N, for example 0.8.0." >&2
  exit 2
fi
node - "$v" <<'JS'
const fs = require('fs');
const v = process.argv[2];
const mcp = 'integrations/claude-code/.mcp.json';
const m = JSON.parse(fs.readFileSync(mcp, 'utf8'));
const args = m.mcpServers.deedglass.args;
const i = args.findIndex((a) => a === 'deedglass' || a.startsWith('deedglass@'));
if (i < 0) throw new Error(`${mcp}: no deedglass package in args`);
args[i] = `deedglass@${v}`;
fs.writeFileSync(mcp, `{\n  "mcpServers": {\n    "deedglass": ${JSON.stringify(m.mcpServers.deedglass)}\n  }\n}\n`);
const pj = 'integrations/claude-code/.claude-plugin/plugin.json';
const raw = fs.readFileSync(pj, 'utf8');
if (!/"version": "[^"]*"/.test(raw)) throw new Error(`${pj}: no version`);
fs.writeFileSync(pj, raw.replace(/"version": "[^"]*"/, `"version": "${v}"`));
console.log(`✓ Claude Code plugin pinned to deedglass@${v}`);
JS
