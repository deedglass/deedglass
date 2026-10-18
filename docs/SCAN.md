# deedglass scan

`deedglass scan` tells you, in a few seconds and without changing anything, what your agent setup
can change, and grades it A to F. It runs on your machine, reads your files and, if you ask, one
database's catalog in a read-only transaction. It sends nothing anywhere else.

It also reads Claude Code's permission rules and says what an agent can run in a shell: for example
`an agent can run prisma db push --force-reset here without a prompt`. See
[What an agent can run in a shell](#what-an-agent-can-run-in-a-shell).

```sh
deedglass scan                                   # this directory, plus your user-level MCP configs
deedglass scan --dir ~/code/my-agent --postgres-dsn-env DATABASE_URL
npx -y deedglass scan                            # the same CLI from npm
```

It writes `deedglass-scan.md` and `deedglass-scan.html` (change with `--format`, or `--no-write` to
print only) and ends with one line: `What your grade means: deedglass.com/score`

## The score

The first line is the Agent Blast-Radius Score for the setup the scan ran on:

```
Blast radius: C · 5 write paths with no check · fix this first: The role can run SECURITY DEFINER functions
  A means a small blast radius, not a safe agent.
```

The deductions follow it, every time: the score never appears without them.

Formula v1 (`formula_version: 1` in `--json`):

```
points = 100
       − 35                             if the database role is a superuser or has BYPASSRLS
       − 20 × min(2, high findings)
       −  8 × min(3, medium findings)
       −  4 × min(5, unchecked write paths)
points = max(points, 0)
grade  = A ≥ 90 · B 80–89 · C 65–79 · D 50–64 · F < 50
```

- **An unchecked write path** is a tool that can write, delete or move money (from the catalog), or
  a table the role can DELETE or TRUNCATE, that isn't routed through `deedglass mcp wrap` with an
  active policy. A table counts as routed only when every discovered server that reaches its
  database is routed. When the scan can't tell, the path counts, and its deduction says
  `not shown to be routed`.
- Low and info findings never deduct.
- The shell findings (FEAT-05, [below](#what-an-agent-can-run-in-a-shell)) count like any other
  finding, so the formula and `formula_version` stay 1: `shell_destructive_unprompted` is high
  (−20), `env_database_url_readable` is medium (−8), and `shell_destructive_no_rule` is medium (−8)
  when `defaultMode` is `auto` in your user settings and low (0) otherwise. They share the high and
  medium caps above, and `.deedglass/score.json` keeps its shape.
- Items past a cap are listed with 0 points and `(over the cap)`, so the list explains the whole grade.
- A table's deduction names its enabled triggers: a trigger is how one reported row becomes 301.
- When a table the role can change has an enabled trigger, or its changes cascade to other tables,
  the text report adds one line above the footer that points to the playground's "said 1, changed
  301" scene: `A trigger on public.orders (orders_ship_open_batch) can turn one reported row into
  many. See it happen in 20 s: https://deedglass.com/play`.

`A means a small blast radius, not a safe agent.` The grade measures what the setup could change,
not what your agent will do.

## What it reads

| Source | Where | What it keeps | What it never keeps |
|---|---|---|---|
| MCP client configs | under `--dir`: `.cursor/mcp.json`, a root `.mcp.json` (`mcpServers` for Claude Code, `servers` for VS Code; both read), `.vscode/mcp.json`, any `claude_desktop_config.json` or `mcp_config.json`. With `--user-configs` (the default; `--ci` turns it off): Claude Desktop, `~/.cursor/mcp.json`, Windsurf's `~/.config/devin/mcp_config.json` and the older `~/.codeium/windsurf/mcp_config.json` | the path (`~` for your home), the client, each server's name, transport, command, the package and version from its arguments (`npx -y @scope/pkg@1.2.3`, `uvx pkg`, `docker run … image:tag`), the names of its env keys, and whether a database URL appears in its arguments | env values; argument values |
| Dependencies | `package.json`, `pyproject.toml`, `requirements*.txt`, `Gemfile` | the file, the package and its version spec when the catalog knows it | anything else in those files |
| Scripts | `package.json` `scripts` | the name of a script whose command line runs a destructive database command from the catalog | the script's command line: it is matched in memory and never printed |
| Claude Code settings | under `--dir`: `.claude/settings.json` and `.claude/settings.local.json`. With `--user-configs`: `~/.claude/settings.json` | the path, the scope, `defaultMode` and how many allow, ask and deny rules it has; a rule is quoted only when it decides a finding and holds no `=`, `@`, quote, URL or long token | every other rule: one saved from a prompt can hold a whole command line |
| Database URL variables | `.env`, `.env.*` | the variable names that look like database URLs | the values: each line is cut at the first `=` and the right side is dropped first |
| Tools that can write | the scan's catalog: known MCP servers and SDKs, each tool's side effect, a source and the date it was checked | the tool and its side effect | anything from running a server: the scan never starts one |

It skips `.git`, `node_modules`, `vendor`, `.venv`, `dist`, `build` and the like, never follows a
symlink out of `--dir`, skips files over 1 MiB, and stops after `--max-files` (20,000).

## The archived Postgres MCP server

<a id="archived_postgres_mcp"></a>
Any use of `@modelcontextprotocol/server-postgres`, at any version, in a client config or a
dependency file is a finding, one per place it appears:

> This Postgres MCP server was archived on 2025-05-28. Its read-only mode runs queries in a
> read-only transaction that a stacked COMMIT; can end (Datadog Security Labs). If its database role
> can write, the agent can write.

It is **high** when you gave `--postgres-dsn-env` and that role can INSERT, UPDATE, DELETE or
TRUNCATE any table (or is a superuser or bypasses RLS), **medium** when the database wasn't checked,
and **low** when the role can only SELECT. The scan says which role it checked. The fixes: give the
server a role that can only SELECT, or route it through Deedglass with
`deedglass mcp wrap --connector <name> -- <the server's command>`. The check is string matching over
files; the scan never runs a stacked statement.

Sources: the archived repository (github.com/modelcontextprotocol/servers-archived, src/postgres)
and Datadog Security Labs' write-up of the SQL injection in that server.

### Other findings

| Id | Severity | When |
|---|---|---|
| `shell_destructive_unprompted` | high | a destructive database command from a dependency would run without a prompt ([below](#shell_destructive_unprompted)) |
| `env_database_url_readable` | medium | a `.env` file holds a database URL variable that no Read deny rule covers ([below](#env_database_url_readable)) |
| `shell_destructive_no_rule` | medium or low | no ask or deny rule covers a spelling of a destructive database command ([below](#shell_destructive_no_rule)) |
| <a id="database_url_in_client_args"></a>`database_url_in_client_args` | medium | a database URL sits in a client config's arguments |
| <a id="role_superuser"></a>`role_superuser`, <a id="role_bypassrls"></a>`role_bypassrls` | high | the checked role is a superuser, or bypasses row-level security |
| <a id="role_replication"></a>`role_replication`, <a id="role_createrole"></a>`role_createrole` | medium | the checked role can replicate, or create roles |
| <a id="role_createdb"></a>`role_createdb` | low | the checked role can create databases |
| <a id="security_definer_executable"></a>`security_definer_executable` | medium | the role can run SECURITY DEFINER functions (at most 10 listed) |
| <a id="no_primary_key_on_writable_table"></a>`no_primary_key_on_writable_table` | low | a table the role can change has no primary key: undo and L2 can't anchor its rows |
| <a id="wal_level_not_logical"></a>`wal_level_not_logical` | info | L2 witnessing needs `wal_level=logical` |

## What an agent can run in a shell

Many public reports of an agent wiping a database came through the shell, not through a database
tool: a schema push with a reset (claude-code#36183), a migration downgrade to the first revision
(claude-code#26913), a migration diff pointed at production (claude-code#80868). The scan finds the tools that
have such commands in your dependency files and reads Claude Code's permission rules, then works out,
for each way an agent might type a destructive command, whether Claude Code would run it without
asking you. It never runs a command.

| Tool | Found in | Commands the scan checks | Source (checked 2026-10-18) |
|---|---|---|---|
| Prisma | `package.json`: `prisma` | `prisma db push --force-reset`, `prisma migrate reset --force`, `prisma migrate diff --shadow-database-url` (Prisma 6 and earlier; Prisma 7 removed the flag) | prisma.io/docs: `db push`, `migrate reset`, the shadow database, `migrate diff` |
| Drizzle Kit | `package.json`: `drizzle-kit` | `drizzle-kit push --force` | orm.drizzle.team/docs/drizzle-kit-push |
| Alembic | Python dependencies: `alembic` | `alembic downgrade base` | alembic.sqlalchemy.org tutorial, "Downgrading" |
| Rails | `Gemfile`: `rails` or `activerecord` | `rails db:drop`, `rails db:reset`, `rails db:schema:load` (also as `bin/rails`, `rake`, `bin/rake`) | guides.rubyonrails.org/command_line.html |
| Knex | `package.json`: `knex` | `knex migrate:rollback --all` | knexjs.org/guide/migrations.html |
| TypeORM | `package.json`: `typeorm` | `typeorm schema:drop`, `typeorm schema:sync` (also `typeorm-ts-node-commonjs`, `typeorm-ts-node-esm`) | TypeORM's "Using CLI" page |
| Sequelize | `package.json`: `sequelize-cli` | `sequelize db:drop`, `sequelize db:migrate:undo:all` | sequelize/cli README, sequelize.org migrations |

Each command is checked as an agent might type it: on its own and through `npx`, `pnpm`,
`pnpm exec`, `yarn` and `bunx` for Node tools; `uv run`, `poetry run`, `python -m` and `python3 -m`
for Python; `bundle exec` for Ruby; and as `npm run <script>` (and the `pnpm`, `yarn` and `bun`
forms) when a `package.json` script runs it.

The rules follow Claude Code's permissions page: deny, then ask, then allow, the first match wins,
across every settings file. Deny and ask rules hold in every mode. A `*` stands for any text, a
trailing ` *` or `:*` also matches the bare command, and a rule without `*` matches one exact
command. A `.env` file is covered by a `Read` deny rule written in gitignore style, where a bare name
such as `Read(.env)` matches at any depth. `Read(./.env)` is read as the `.env` in the project
directory only, as the permissions page describes it, so the scan never counts cover it can't show.
The permission mode comes from `defaultMode`: local settings, then project, then user; `auto` and
`bypassPermissions` in a project or local file don't take effect, as Claude Code's permission-modes
page says. `disableBypassPermissionsMode` or `disableAutoMode` set to `"disable"` in any file turns
that mode off. A key whose value has the wrong type is left out, and the file's line in the Found
list says which.

Sources, checked 2026-10-18: Claude Code's
[permissions page](https://code.claude.com/docs/en/permissions) (rule order, `*` matching, compound
commands, `Read` patterns, the warning that a rule isn't a security boundary),
[permission-modes page](https://code.claude.com/docs/en/permission-modes) (`defaultMode` values,
which mode a session starts in, ask rules in auto mode, the broad allow rules auto mode drops) and
[settings page](https://code.claude.com/docs/en/settings) (the three settings files, lists merged
across them, and the published
[JSON schema](https://json.schemastore.org/claude-code-settings.json)).

<a id="shell_destructive_unprompted"></a>
**`shell_destructive_unprompted`** · high. An allow rule matches one spelling of a destructive
command and no ask or deny rule does, or `defaultMode` is `bypassPermissions` in your user settings.
The finding names the first such spelling, the rule and the file, what the command does, and up to
three other spellings that also run without a prompt:

> An agent can run `npm run db:diff` here without a prompt. The allow rule `Bash(npm run *)` in
> .claude/settings.local.json matches it, and no ask or deny rule does. The db:diff script runs
> `prisma migrate diff --shadow-database-url`, which resets the database it is given as the shadow
> database. A rule covers `npx prisma migrate diff --shadow-database-url`, not `npm run db:diff`: a
> rule matches the text of a command, not the program.

When the matching allow rule is a broad one, such as `Bash` or `Bash(npm run *)`, the finding adds
that auto mode drops it and a classifier reviews the command instead.

<a id="shell_destructive_no_rule"></a>
**`shell_destructive_no_rule`** · medium or low. No ask or deny rule covers some spelling of a
destructive command, and no allow rule runs it, so the permission mode decides. It is **medium**
when `defaultMode` is `auto` in your user settings: a classifier decides instead of you. It is
**low** otherwise and doesn't deduct: Claude Code asks first in Manual mode, but auto mode is its
built-in default on recent versions, and the scan can't see which version you run. With
`defaultMode` `dontAsk`, which refuses whatever no rule allows, it isn't reported. With
`disableAutoMode` it stays low and says so.

This is the shape of claude-code#80868: the user had ask rules for `npx prisma db:*` and
`npx prisma migrate:*` and ran in auto mode. Those rules name one spelling. `prisma migrate reset
--force`, `pnpm prisma migrate reset --force` and a `package.json` script that runs it don't match
them, so in auto mode a classifier decides those without asking. The issue itself reports a compound
command with a command substitution that the ask rule didn't catch, which the scan can't model: it
checks spellings, not Claude Code's parser.

The fix for both findings is one ask rule per command, with a leading `*`, such as
`Bash(*prisma migrate reset*)`. Claude Code's permissions page says a `*` can stand anywhere in a
rule, and an ask rule prompts even in auto mode, so the rule matches the command behind `npx`,
`pnpm`, `uv run` or any other runner. A rule for a script is written the same way: `Bash(* db:diff*)`. A test
adds every rule the findings suggest and scans again: no shell finding is left.

<a id="env_database_url_readable"></a>
**`env_database_url_readable`** · medium. A `.env` file holds a database URL variable (the names the
scan already lists) and no `Read` deny rule covers it. Claude Code reads project files without a
prompt, so an agent can read the URL and point any command at that database. Files ending in
`.example`, `.sample`, `.template` or `.dist` are left out. The fix is `Read(.env)` and
`Read(.env.*)` in `permissions.deny`. That stops Claude Code's file tools and the file commands it
recognises, such as `cat` or `head` naming the file; a program that opens the file itself, such as
`prisma`, still reads it. The scan reads variable names only, never values, so it can't tell a local
database from a remote one.

What this part doesn't do:

- It reads Claude Code's settings only. Cursor, Codex and other agents have their own rules.
- It doesn't read managed settings, a `--settings` file or command-line flags such as
  `--dangerously-skip-permissions`, and every finding says which files it read.
- It checks spellings, not Claude Code's own parser. Claude Code's permissions page says ask and
  deny rules apply to each part of a compound command; claude-code#80868 reports Claude Code 2.1.218
  running a compound command with a command substitution that an ask rule should have caught.
- A rule is a second check on the text of a command, not a sandbox: a program reached another way,
  such as `sh -c`, is a different spelling. Claude Code's sandbox enforces file and network limits
  whatever the command text.
- It assumes a session starts in the directory that holds `.claude/`, or in `--dir` when there is
  none.

## Checking a database

`--postgres-dsn-env VAR` names the environment variable that holds the URL; the scan never takes a
URL on the command line. It reads the variable once and never prints, logs or stores it; a
connection error shows only its SQLSTATE, like `28P01 invalid_password`.

The session sets `default_transaction_read_only=on`, a 2 s statement timeout, a 500 ms lock timeout
and `application_name=deedglass-scan`, then runs one `BEGIN READ ONLY` transaction with six catalog
reads and rolls back:

1. who and where: the role, the database, the server version and `wal_level`;
2. the role's attributes: superuser, BYPASSRLS, replication, create role, create database;
3. every table and what the role may do to it (INSERT, UPDATE, DELETE, TRUNCATE), its estimated
   rows, row-level security, replica identity and primary key;
4. the triggers, and the tables their functions name after UPDATE, INSERT INTO or DELETE FROM;
5. the foreign keys that change another table on delete or update;
6. the SECURITY DEFINER functions the role can run.

Postgres's own log of a scan shows `BEGIN READ ONLY`, the six SELECTs and `ROLLBACK`, nothing
else, and a write sent inside the scan's transaction is refused by Postgres with `25006`. Both are
tests.

### Reading the table

| Column | Meaning |
|---|---|
| Table | `schema.table`, ranked: the ones that can do the most damage first |
| Can change | INSERT, UPDATE, DELETE, TRUNCATE for this role |
| Est. rows | Postgres's estimate (`not analysed` when it has none) |
| Cascades to | tables a delete or update here also changes, through foreign keys |
| Triggers | enabled triggers: one statement can change rows you didn't name |
| Undo-ready | a primary key and a replica identity that carries it: Deedglass can undo writes here |
| L2-ready | undo-ready and `wal_level=logical`: Deedglass can witness writes here in Postgres's change log |

The rank is `(40·D + 25·U + 10·I + 15·T + 10·C + 10·M) × (0.5 + 0.5·min(1, log10(1 + rows) / 6))`,
where D is DELETE or TRUNCATE, U UPDATE, I INSERT, T an enabled trigger, C a cascade and M a name
that sounds like money or people (orders, payments, customers, …), each 0 or 1. A table never
analysed uses 0.75 for the second factor.

## The three policies

The scan quotes three rules from Deedglass's starter policies, with only the table name changed,
each a whole policy you can push: `block-destructive-sql` (DROP and TRUNCATE never run),
`writes-need-approval` narrowed to your top table (writes wait for a person), and a budget
(`postgres-safe`'s per-minute rate, or `stripe-refunds`' daily refund budget when a money tool was
found).

## Outputs

- `deedglass-scan.md`: the report in Markdown.
- `deedglass-scan.html`: the same, self-contained: no script, nothing loaded from anywhere.
- `deedglass-scan.json` and `--json`: the `scan_report` object, version 1 (`object`, `version`,
  `scanned_at`, `deedglass_version`, `dir`, `offline`, `score`, `summary`, `configs`,
  `dependencies`, `env_names`, `claude_settings`, `tools`, `findings`, `postgres`, `policies`,
  `footer`). Fields may be added; none is renamed within version 1. `claude_settings` lists each
  settings file read: `path`, `scope`, `default_mode` and the `allow`, `ask` and `deny` counts.
- `deedglass-scan.sarif`: SARIF 2.1.0 for code scanning (below).

## The badge

`--badge` writes `.deedglass/score.json`, a shields.io endpoint file
(`{"schemaVersion":1,"label":"agent blast radius","message":"C","color":"3F4453","cacheSeconds":3600}`),
and prints the Markdown line to paste into your README. The colours are teal for A and B, neutral
for C and amber for D and F; never red, which means deny and doesn't match. Commit the file
yourself. Since S47, `deedglass init` writes `.deedglass/*` and `!.deedglass/score.json` to
`.gitignore`, so every key file stays ignored and the badge file doesn't. On an older `.gitignore`
that ignores the whole `.deedglass/` directory, the command prints the two-line change and edits
nothing. The scan never runs git, never opens a pull request, issue or comment anywhere, and no
Deedglass server is involved: shields.io reads your own file (it caches for at least 300 seconds).

The README line links the badge to `https://deedglass.com/score`, the page that explains the grade.
`--badge-share` (which implies `--badge`) links it to the owner's own `/s` share page instead and
prints a warning: anyone who reads the README can open that page. That link holds the grade, the
points and the finding counts by severity only (`"f":[]`, `"d":[]`): no finding id and no deduction
id, since those come from the configs and the database and a README is public. It keeps the result
of that run; run `--badge-share` again when the grade changes.

## The share link

`--share` prints `https://deedglass.com/s#v1.…`. Everything after `#` stays in the browser; the
page renders it there. The link holds only the grade, the points, the finding counts, the finding
ids and the deduction ids with counts: never a path, file, table, schema, role, database, server,
package version, variable name or host. Without `--share`, no link is ever printed.

## SARIF, --ci and --fail-under

- `--format sarif` writes `deedglass-scan.sarif`: one rule per finding id (`helpUri`
  `https://docs.deedglass.com/scan#<id>`), one result per finding location under `--dir` (user
  configs are left out), levels high → `error`, medium → `warning`, low and info → `note`, and the
  score in `runs[0].properties.deedglass_score`.
- `--ci` doesn't read user configs, prints no colour or mark, and never asks anything.
- `--fail-under C` exits 1 when the grade is below C, after writing every file. `F` isn't a value.

Exit codes: 0 the report was written · 1 below `--fail-under`, or the Postgres part failed (the file
part was written) · 2 a usage error, or the DSN variable is unset.

## What it doesn't do

It reads your configuration and catalog. It doesn't run your agent, test your tools, run any
command it checks or connect anywhere you didn't name. It doesn't check databases other than Postgres, and it grades only the
setup on the machine it runs on: it has no mode that takes someone else's repository. A good grade
means a small blast radius, not a safe agent.

## What the grade means, and the full report

`deedglass.com/score` explains the grade: what it measures, how points come off and what it doesn't
measure. `deedglass scan --full` renders the full report, with a fix for every finding, on your machine; it needs a licence
([LICENSING.md](LICENSING.md)).
