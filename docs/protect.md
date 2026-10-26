# Protect every agent in one command

`deedglass protect` adds the [shell guard](SHELL_GUARD.md) to every coding agent it finds on your
machine: Claude Code, Cursor, Codex CLI, Gemini CLI and GitHub Copilot CLI. After it, a command that
would destroy a database, a volume or infrastructure waits for your yes first, whichever agent runs
it:

```sh
npx deedglass protect
```

```text
deedglass protect  3 of 5 agents found

  Claude Code     → adds the guard (PreToolUse)
                    ~/.claude/settings.json
  Cursor          → adds the guard (beforeShellExecution)
                    ~/.cursor/hooks.json
  Codex CLI       ✓ already has the guard
                    ~/.codex/hooks.json
  Gemini CLI      • not found
  GitHub Copilot  • not found

  hook  /opt/homebrew/bin/deedglass guard shell

Change 2 files? Each is backed up first, and deedglass protect --undo puts it back. [y/N]
```

It is a second check, not a sandbox: the guard reads each shell command as written and compares it
with a dated, sourced catalog (`deedglass guard shell --list`). It needs no server, no account and
no network.

## What it changes

It looks for each agent's config folder in your home directory and writes the hook config that
`deedglass guard shell --print-config --client <name>` prints, into:

| Agent | Folder it looks for | File it writes | Hook event |
|---|---|---|---|
| Claude Code | `~/.claude` | `~/.claude/settings.json` | `PreToolUse` on `Bash` |
| Cursor | `~/.cursor` | `~/.cursor/hooks.json` | `beforeShellExecution` |
| Codex CLI | `~/.codex` | `~/.codex/hooks.json` | `PreToolUse` on `^Bash$` |
| Gemini CLI | `~/.gemini` | `~/.gemini/settings.json` | `BeforeTool` on `run_shell_command` |
| GitHub Copilot CLI | `~/.copilot` | `~/.copilot/hooks/deedglass.json` | `preToolUse` |

`--project` also writes into the current directory's folders when they exist: `.claude/settings.local.json`
(personal, not committed), `.cursor/hooks.json`, `.codex/hooks.json`, `.gemini/settings.json` and
`.github/hooks/deedglass.json`. Those other files are usually committed, so the hook then runs for
everyone who works in the repository. `--client claude-code,cursor` picks agents.

Codex CLI and Gemini CLI can't ask, so with them the guard denies and the agent reads why. Codex
runs a new hook only after you trust it with `/hooks`. Each agent's limits are in
[INSTALL_CLIENTS.md](INSTALL_CLIENTS.md#the-shell-guard-in-each-coding-agent).

## Safe to run

- **It shows the plan first.** It names each file and writes only after `[y/N]`, or with `--yes`.
  `--dry-run` prints the plan and writes nothing.
- **It only adds.** It merges into the JSON already there and keeps every setting and hook you wrote,
  in your order and your indent. Your other hooks still run.
- **It runs once.** A file that already runs `deedglass guard shell` (yours, or from an earlier run)
  gets nothing new. Run it again and nothing changes.
- **It leaves alone what it can't read.** A file with comments, a syntax error or an unexpected
  shape is skipped, and the plan says why.
- **It backs up first.** Before it changes a file it copies it to
  `~/.deedglass/protect/<time>/`, with a `manifest.json` that lists every file.

## Undo

```sh
deedglass protect --undo
```

It puts back every file protect changed, newest run first: byte for byte from the backup, and a
file protect created is removed, with any folder it made. If you edited a file after protect wrote
it, undo keeps your edit and takes out only the guard. `--undo --dry-run` says what it would do.

## How the hook finds deedglass

The hook has to start `deedglass` long after `protect` ends, from an agent that may not share your
shell's `PATH` (Cursor started from the Dock, for example). So it writes:

- the `deedglass` on your `PATH`, by its absolute path, when it is the binary you ran (a Homebrew
  link stays valid across upgrades);
- `npx -y deedglass@<version>` after an `npx deedglass protect` run, since npx's cache folder can
  be cleared;
- otherwise the absolute path of the binary you ran.

`--hook-command` sets it yourself, for example `--hook-command deedglass`.

## The badge

After a run that adds the guard, protect prints two optional lines and asks nothing:

```text
  badge  [![Agents protected by Deedglass](https://img.shields.io/badge/agents-protected%20by%20Deedglass-0f766e)](https://deedglass.com/check)
  share  Optional: paste it in your README, or send a teammate npx deedglass protect.
```

The first is a [shields.io](https://shields.io) static badge for your README that links to
[deedglass.com/check](https://deedglass.com/check); protect writes it nowhere. The badge line stays
on one line so it copies whole. `--json` carries it as `badge` (empty when nothing was written),
and `NO_COLOR` turns off colour on these lines as on every other.

## The Claude Code plugin

The Deedglass plugin for Claude Code carries the same guard, and since v0.12.0 it is on (`ask`) by
default. `DEEDGLASS_SHELL_GUARD=off` turns it off and `deny` makes it deny
([SHELL_GUARD.md](SHELL_GUARD.md#with-the-deedglass-plugin)). With the plugin and `protect` both
installed, a matching command is checked twice and asks once per check.

## JSON

`--json` prints `{"object": "protect", "dry_run", "applied", "command", "backup", "changes",
"badge", "agents": [...]}`, one entry per agent with `client`, `title`, `scope`, `file`, `event`, `status`
(`add`, `present`, `skip`, `absent`), `reason` and `notes`. Without `--yes` it plans only.
`--undo --json` prints `{"object": "protect_undo", "dry_run", "files": [...]}`, each with `status`
`restored`, `removed`, `left` or `failed`.

## Tests

`go test ./internal/protect ./cmd/deedglass -run 'Protect|EachAgent|AllAgents|Undo|Skips|HookCommand'`
runs each agent's format against a temporary HOME (the goldens in `internal/protect/testdata`),
idempotence, the skipped files, undo after an edit, symlinked settings, and the full round trip:
`protect --yes` and then `--undo` leave HOME byte-identical.
