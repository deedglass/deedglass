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
# It never answers allow, runs nothing it reads, uses no network, and never uses the one exit code
# Claude Code reads as a block (2). When it can't check, because deedglass isn't on PATH, is an older
# deedglass without guard shell, or fails:
#   ask   it exits 0 with no decision and a systemMessage that says so, so the command goes on to
#         Claude Code's own permission rules
#   deny  it says so on stderr and exits 1, which Claude Code shows without blocking the command
# hooks.json also sets "onFailure": "continue", so even a hook that can't start (no sh on Windows
# without Git Bash) or times out never blocks a command.
case "${DEEDGLASS_SHELL_GUARD:-off}" in
ask) set -- ;;
deny) set -- --deny ;;
*)
	cat >/dev/null
	exit 0
	;;
esac

unchecked() {
	cat >/dev/null
	if [ "$DEEDGLASS_SHELL_GUARD" = deny ]; then
		echo "The Deedglass shell guard is on (DEEDGLASS_SHELL_GUARD=deny) but $1, so it checked nothing. Install deedglass (docs/INSTALL.md) or unset DEEDGLASS_SHELL_GUARD." >&2
		exit 1
	fi
	printf '{"systemMessage":"The Deedglass shell guard is on (DEEDGLASS_SHELL_GUARD=ask) but %s, so it checked nothing and your permission rules decide. Install deedglass (docs/INSTALL.md) or unset DEEDGLASS_SHELL_GUARD."}\n' "$1"
	exit 0
}

command -v deedglass >/dev/null 2>&1 || unchecked "deedglass isn't on PATH"
out=$(deedglass guard shell "$@")
rc=$?
[ "$rc" -eq 0 ] || unchecked "deedglass guard shell exited $rc (an older deedglass without it?)"
[ -z "$out" ] || printf '%s\n' "$out"
exit 0
