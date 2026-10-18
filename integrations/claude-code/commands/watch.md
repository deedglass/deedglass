---
description: Watch what Postgres commits while Claude Code works, shell commands included. Prints the deedglass observe run line that restarts this session under watch; you run it, with your own key.
argument-hint: [connector-name]
---

The user wants to see the rows Postgres commits on the watched tables of their dev database while
you (Claude Code) work, including changes you make through the shell (`psql`, scripts, ORM
commands), and to get a way back. Arguments: `$ARGUMENTS` (the name of their Deedglass Postgres
connector).

You can't watch yourself: the session has to be opened outside this conversation, with the
person's admin key. Your job is to print one line and explain it. Don't run any `deedglass observe`
command yourself, don't read any `.deedglass/` file, and never ask for a key or a database URL.

1. If no connector name was given, ask for it (`deedglass connector list` in their own terminal
   shows it). Use `dev` only if they say that's the name.
2. Check what it needs, in plain words: a running Deedglass server; a Postgres connector set up
   with `deedglass connector postgres init` on a dev database with `wal_level=logical`; and
   `witness.observe.enabled: true` in that connector's config
   (https://docs.deedglass.com/guides/watch-a-coding-agent/). If they haven't run
   `npx deedglass init` yet, say that first, and suggest running it outside this project:
   it saves the admin key in `.deedglass/admin.key`, and a file in this folder or a parent is one
   you could read. The same goes for a key in a shell profile, `.env` or Claude Code settings.
3. Print these lines with their connector name, for them to run in their own terminal, from this
   project's folder, after they quit this session (`/exit`):

   ```sh
   mkdir -p ~/deedglass-undo
   npx -y deedglass observe run --connector <name> --label "claude code" --undo-sql ~/deedglass-undo -- claude --continue
   ```

   `claude --continue` picks up the most recent conversation in that folder, so they come back to
   this one, now watched. If `type claude` shows an alias, they put its full path after `--`.
4. Say what they'll see when they quit Claude Code again: the sealed session with a line per table,
   `Deedglass caught this: …` when one transaction changed many rows (`caught_rows`, 100 by
   default) or truncated a table, the undo SQL saved to `~/deedglass-undo/undo-<session id>.sql`
   (free; Deedglass never runs it) with the `psql` line to run it, and the note `Changes from any
   writer during this window appear here. Leave out anything that wasn't the agent before you
   undo.`
5. Say what it isn't: a second check, not a sandbox. Nothing is blocked or asked first. It records
   row changes on watched tables only: a `DROP TABLE`, `DROP SCHEMA` or an ORM's reset leaves
   nothing in the session (it can read 0 transactions right after one) and stops the decoder,
   which the run says. A TRUNCATE and sequences can't be put back from the record. The undo SQL
   holds row values: keep it out of version control and out of this folder.
6. Say that inside the watched session, this plugin's Deedglass tools find no key in their
   environment (`observe run` removes every `DEEDGLASS_*` variable), so they refuse calls until
   they quit, unless they find the agent key `init` wrote in `.deedglass/` above the folder.

Don't edit any file or config yourself. If they ask you to turn on observe mode, show the config
change and let them make it.
