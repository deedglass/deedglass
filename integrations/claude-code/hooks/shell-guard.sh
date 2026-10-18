#!/bin/sh
# The Deedglass shell guard as a Claude Code PreToolUse hook (docs/SHELL_GUARD.md).
#
# Off by default: unless DEEDGLASS_SHELL_GUARD is "ask" or "deny" (set it under "env" in
# .claude/settings.json or ~/.claude/settings.json), this reads its input and exits 0 with no output,
# so Claude Code's own permission rules decide as if the hook weren't there.
#
#   ask   deedglass guard shell: a command that would destroy database data needs your yes first
#   deny  deedglass guard shell --deny: such a command doesn't run, and Claude reads why
#
# It never answers allow, runs nothing it reads, and uses no network. If deedglass isn't on PATH,
# it says so and exits 1, which Claude Code shows without blocking the command.
case "${DEEDGLASS_SHELL_GUARD:-off}" in
ask) set -- ;;
deny) set -- --deny ;;
*)
	cat >/dev/null
	exit 0
	;;
esac
if ! command -v deedglass >/dev/null 2>&1; then
	cat >/dev/null
	echo "The Deedglass shell guard is on (DEEDGLASS_SHELL_GUARD=${DEEDGLASS_SHELL_GUARD}) but deedglass isn't on PATH, so it checked nothing. Install it (docs/INSTALL.md) or unset DEEDGLASS_SHELL_GUARD." >&2
	exit 1
fi
exec deedglass guard shell "$@"
